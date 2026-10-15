package doctor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/xen"
)

// fakeXenClient points a xen client at a fake pool answering login and one
// host, exercising the doctor through the client cache.
func fakeXenClient(t *testing.T, login string) *xen.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		var resp string
		switch {
		case strings.Contains(body, "session.login_with_password"):
			resp = login
		case strings.Contains(body, "host.get_all_records"):
			resp = `<methodResponse><params><param><value><struct>` +
				`<member><name>Status</name><value><string>Success</string></value></member>` +
				`<member><name>Value</name><value><struct>` +
				`<member><name>OpaqueRef:host-1</name><value><struct>` +
				`<member><name>uuid</name><value><string>host-uuid-1</string></value></member>` +
				`<member><name>name_label</name><value><string>xen-1</string></value></member>` +
				`<member><name>hostname</name><value><string>xen-1.example.com</string></value></member>` +
				`</struct></value></member>` +
				`</struct></value></member>` +
				`</struct></value></param></params></methodResponse>`
		default:
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	client, err := xen.New(xen.Config{Host: srv.URL, Username: "root", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := srv.Client()
	httpClient.Timeout = xen.Timeout
	return client.WithHTTPClient(httpClient)
}

func xenLoginOK() string {
	return `<methodResponse><params><param><value><struct>` +
		`<member><name>Status</name><value><string>Success</string></value></member>` +
		`<member><name>Value</name><value><string>OpaqueRef:session-test</string></value></member>` +
		`</struct></value></param></params></methodResponse>`
}

func xenLoginDenied() string {
	return `<methodResponse><params><param><value><struct>` +
		`<member><name>Status</name><value><string>Failure</string></value></member>` +
		`<member><name>ErrorDescription</name><value><array><data>` +
		`<value><string>SESSION_AUTHENTICATION_FAILED</string></value>` +
		`</data></array></value></member>` +
		`</struct></value></param></params></methodResponse>`
}

func writeXenContext(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Contexts = []config.ContextConfig{{
		Name:    "pool",
		Backend: "xen",
		Context: "xen.example.com",
		Xen:     &config.XenContextConfig{Username: "root"},
	}}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv(xen.PasswordEnv, "secret")
}

func TestXenChecksAgainstFakePool(t *testing.T) {
	writeXenContext(t)
	xen.SetClientForContext("pool", fakeXenClient(t, xenLoginOK()))
	t.Cleanup(xen.ResetClients)

	checks := xenChecks("pool")
	if len(checks) == 0 {
		t.Fatal("no checks returned")
	}
	names := map[string]bool{}
	for _, c := range checks {
		names[c.Name] = c.OK
		if !c.OK && c.Severity == "required" {
			t.Fatalf("required check failed: %s: %s", c.Name, c.Detail)
		}
	}
	if !names["Pool reachable"] || !names["Hosts"] {
		t.Fatalf("checks = %+v", checks)
	}
	// One host: the migration warning must fire.
	warned := false
	for _, c := range checks {
		if c.Name == "Migration target" {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("single-host pool drew no migration warning: %+v", checks)
	}
}

func TestXenChecksRefuseBadCredentials(t *testing.T) {
	writeXenContext(t)
	xen.SetClientForContext("pool", fakeXenClient(t, xenLoginDenied()))
	t.Cleanup(xen.ResetClients)

	checks := xenChecks("pool")
	if len(checks) != 1 || checks[0].Name != "Credentials accepted" {
		t.Fatalf("checks = %+v", checks)
	}
}
