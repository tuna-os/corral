package export

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/xen"
)

// xenPool is a recorded-payload XenAPI pool: the XML-RPC envelopes the
// pkg/xen fixtures use, plus the /export download.
type xenPool struct {
	power string
	mu    sync.Mutex
	paths []string
}

func xenOK(v string) string {
	return `<methodResponse><params><param><value><struct>` +
		`<member><name>Status</name><value><string>Success</string></value></member>` +
		`<member><name>Value</name><value>` + v + `</value></member>` +
		`</struct></value></param></params></methodResponse>`
}

func (p *xenPool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.paths = append(p.paths, r.URL.Path+"?"+r.URL.RawQuery)
	p.mu.Unlock()
	if r.URL.Path == "/export" {
		_, _ = w.Write([]byte("xva-bytes"))
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch s := string(body); {
	case strings.Contains(s, "session.login_with_password"):
		_, _ = w.Write([]byte(xenOK(`<string>OpaqueRef:sess</string>`)))
	case strings.Contains(s, "VM.get_by_uuid"):
		_, _ = w.Write([]byte(`<methodResponse><params><param><value><struct>` +
			`<member><name>Status</name><value><string>Failure</string></value></member>` +
			`<member><name>ErrorDescription</name><value><array><data><value><string>UUID_INVALID</string></value></data></array></value></member>` +
			`</struct></value></param></params></methodResponse>`))
	case strings.Contains(s, "VM.get_by_name_label"):
		_, _ = w.Write([]byte(xenOK(`<array><data><value><string>OpaqueRef:vm</string></value></data></array>`)))
	case strings.Contains(s, "VM.get_record"):
		_, _ = w.Write([]byte(xenOK(`<struct><member><name>name_label</name><value><string>web</string></value></member>` +
			`<member><name>power_state</name><value><string>` + p.power + `</string></value></member></struct>`)))
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func xenFixture(t *testing.T, power string) *xenPool {
	t.Helper()
	pool := &xenPool{power: power}
	srv := httptest.NewServer(pool)
	t.Cleanup(srv.Close)
	client, err := xen.New(xen.Config{Host: srv.URL, Username: "root", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	xen.SetClientForContext("pool", client)
	t.Cleanup(xen.ResetClients)
	return pool
}

func TestXen_ExportsAnXVA(t *testing.T) {
	pool := xenFixture(t, "Halted")
	dest := filepath.Join(t.TempDir(), "web.xva")
	ref := types.InstanceRef{Backend: "xen", Context: "pool", Name: "web"}
	result, err := (Xen{}).Export(context.Background(), Request{Ref: ref, Dest: dest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != XVA || result.Consistency != snapshot.Offline || result.Bytes != int64(len("xva-bytes")) {
		t.Errorf("result = %+v", result)
	}
	if data, _ := os.ReadFile(dest); string(data) != "xva-bytes" {
		t.Errorf("dest = %q", data)
	}
	last := pool.paths[len(pool.paths)-1]
	if !strings.HasPrefix(last, "/export?") || !strings.Contains(last, "ref=OpaqueRef%3Avm") || !strings.Contains(last, "session_id=OpaqueRef%3Asess") {
		t.Errorf("export request = %q", last)
	}
}

func TestXen_RefusesARunningVM(t *testing.T) {
	pool := xenFixture(t, "Running")
	ref := types.InstanceRef{Backend: "xen", Context: "pool", Name: "web"}
	_, err := (Xen{}).Export(context.Background(), Request{Ref: ref, Dest: filepath.Join(t.TempDir(), "web.xva")}, nil)
	var unsup *Unsupported
	if !errors.As(err, &unsup) || !strings.Contains(err.Error(), "corral stop web") {
		t.Fatalf("err = %v, want a typed refusal with the remedy", err)
	}
	for _, p := range pool.paths {
		if strings.HasPrefix(p, "/export") {
			t.Fatalf("export was requested for a running VM: %v", pool.paths)
		}
	}
}

func TestXen_OffersOnlyXVA(t *testing.T) {
	if got := Formats("xen"); len(got) != 1 || got[0] != XVA {
		t.Errorf("formats = %v, want [xva]", got)
	}
}
