package snapshot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/xen"
)

// fakeXenPool answers the XenAPI calls the Xen snapshot adapter makes, with
// the VM running so captures must report crash consistency.
func fakeXenPool(t *testing.T) *xen.Client {
	t.Helper()
	handlers := map[string]string{
		"session.login_with_password": xenSuccess(`<string>OpaqueRef:session-test</string>`),
		"VM.get_by_uuid":              xenFailure("UUID_INVALID", "VM", "web-1"),
		"VM.get_by_name_label":        xenSuccess(xenArr(xenVal(xenStr("OpaqueRef:vm-1")))),
		"VM.get_record": xenSuccess(`<struct>` +
			`<member><name>name_label</name><value><string>web-1</string></value></member>` +
			`<member><name>power_state</name><value><string>Running</string></value></member>` +
			`</struct>`),
		"VM.snapshot":      xenSuccess(`<string>OpaqueRef:snap-1</string>`),
		"VM.get_snapshots": xenSuccess(xenArr(xenVal(xenStr("OpaqueRef:snap-1")))),
		"VM.revert":        xenSuccess(`<string></string>`),
		"VM.destroy":       xenSuccess(`<string></string>`),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		method := ""
		if i := strings.Index(body, "<methodName>"); i >= 0 {
			if j := strings.Index(body[i:], "</methodName>"); j >= 0 {
				method = body[i+len("<methodName>") : i+j]
			}
		}
		resp, ok := handlers[method]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("no handler for " + method))
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	// New accepts a full http:// URL as Host, which points the client at
	// the fake pool; production always passes a bare hostname (https).
	client, err := xen.New(xen.Config{Host: srv.URL, Username: "root", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := srv.Client()
	httpClient.Timeout = xen.Timeout
	return client.WithHTTPClient(httpClient)
}

func xenStr(s string) string { return `<string>` + s + `</string>` }

func xenVal(s string) string { return `<value>` + s + `</value>` }

func xenArr(items ...string) string {
	return `<array><data>` + strings.Join(items, "") + `</data></array>`
}

func xenSuccess(valueXML string) string {
	return `<methodResponse><params><param><value><struct>` +
		`<member><name>Status</name><value><string>Success</string></value></member>` +
		`<member><name>Value</name><value>` + valueXML + `</value></member>` +
		`</struct></value></param></params></methodResponse>`
}

func xenFailure(code string, params ...string) string {
	var sb strings.Builder
	sb.WriteString(`<methodResponse><params><param><value><struct>` +
		`<member><name>Status</name><value><string>Failure</string></value></member>` +
		`<member><name>ErrorDescription</name><value><array><data>`)
	sb.WriteString(`<value><string>` + code + `</string></value>`)
	for _, p := range params {
		sb.WriteString(`<value><string>` + p + `</string></value>`)
	}
	sb.WriteString(`</data></array></value></member></struct></value></param></params></methodResponse>`)
	return sb.String()
}

func TestXenSnapshotsEndToEnd(t *testing.T) {
	old := XenClientFor
	client := fakeXenPool(t)
	XenClientFor = func(ref types.InstanceRef) (*xen.Client, error) { return client, nil }
	t.Cleanup(func() { XenClientFor = old })

	ref := types.InstanceRef{Backend: "xen", Context: "pool", Name: "web-1"}
	adapter := Xen{}

	created, err := adapter.Create(ref, "nightly")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "nightly" || created.Source != ref.Name || !created.Ready {
		t.Fatalf("bad snapshot: %+v", created)
	}
	// Running capture with no quiesce on the XenAPI path: crash, by proof.
	if created.Consistency != Crash {
		t.Fatalf("running capture consistency = %q, want crash", created.Consistency)
	}

	listed, err := adapter.List(ref)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Consistency != Crash {
		t.Fatalf("listed = %+v", listed)
	}

	if err := adapter.Restore(ref, "nightly"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := adapter.Delete(ref, "nightly"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
