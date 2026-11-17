package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/shell"
)

// trustFixture is a test server with no corral config, a known libvirt URI
// and Incus remote, and the libvirt package wired to the fake runner.
func trustFixture(t *testing.T) *TestFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRAL_LIBVIRT_URI", "qemu+ssh://lab.internal/system")
	t.Setenv("CORRAL_INCUS_REMOTE", "lab")
	fx := NewTestFixture()
	libvirt.SetRunner(fx.Runner)
	t.Cleanup(func() {
		libvirt.SetRunner(shell.Real{})
		fx.Close()
	})
	return fx
}

func trustGET(t *testing.T, fx *TestFixture, path string, header http.Header) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fx.Server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func argvMentions(fx *TestFixture, s string) bool {
	for _, c := range fx.Runner.Calls() {
		for _, a := range append([]string{c.Name}, c.Args...) {
			if strings.Contains(a, s) {
				return true
			}
		}
	}
	return false
}

// A GET passes adminGate with no identity, so ?context= must never let the
// caller choose the hypervisor the server connects to.
func TestLibvirtContextMustBeConfigured(t *testing.T) {
	fx := trustFixture(t)
	fx.Runner.AddPrefixResponse("virsh", "Name: testvm\nState: running\n", nil)

	for _, path := range []string{
		"/api/vms/libvirt/testvm?context=" + url.QueryEscape("qemu+ssh://attacker-host/system"),
		"/api/vms/libvirt/testvm/snapshots?context=" + url.QueryEscape("qemu+ext:///system?command=/tmp/attacker-host"),
	} {
		fx.Runner.Reset()
		fx.Runner.AddPrefixResponse("virsh", "Name: testvm\n", nil)
		if code := trustGET(t, fx, path, nil); code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, code)
		}
		if argvMentions(fx, "attacker-host") {
			t.Fatalf("an unconfigured context reached a command line: %+v", fx.Runner.Calls())
		}
	}

	// The configured URI, which is what the inventory hands the UI, works.
	fx.Runner.Reset()
	fx.Runner.AddPrefixResponse("virsh", "Name: testvm\n", nil)
	path := "/api/vms/libvirt/testvm?context=" + url.QueryEscape("qemu+ssh://lab.internal/system")
	if code := trustGET(t, fx, path, nil); code != http.StatusOK {
		t.Fatalf("GET with the configured URI = %d, want 200", code)
	}
	if !argvMentions(fx, "qemu+ssh://lab.internal/system") {
		t.Fatalf("the configured URI did not reach virsh: %+v", fx.Runner.Calls())
	}
}

func TestIncusRemoteMustBeConfigured(t *testing.T) {
	fx := trustFixture(t)
	fx.Runner.AddPrefixResponse("incus", "Name: web1\n", nil)
	if code := trustGET(t, fx, "/api/vms/incus/web1?context=attacker-host", nil); code != http.StatusBadRequest {
		t.Errorf("unconfigured incus remote = %d, want 400", code)
	}
	if argvMentions(fx, "attacker-host") {
		t.Fatalf("an unconfigured remote reached a command line: %+v", fx.Runner.Calls())
	}
	if code := trustGET(t, fx, "/api/vms/incus/web1?context=lab", nil); code != http.StatusOK {
		t.Errorf("configured incus remote = %d, want 200", code)
	}
}

// The {name} segment is a positional argument to virsh, incus and kubectl;
// a leading dash would make it an option.
func TestGuestNameMustNotBeAnOption(t *testing.T) {
	fx := trustFixture(t)
	fx.Runner.AddPrefixResponse("virsh", "", nil)
	for _, path := range []string{"/api/vms/libvirt/--help", "/api/vms/libvirt/%2Dc", "/api/vms/-n/x", "/api/tty/incus/--all"} {
		fx.Runner.Reset()
		if code := trustGET(t, fx, path, nil); code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, code)
		}
		if len(fx.Runner.Calls()) != 0 {
			t.Errorf("GET %s ran %+v", path, fx.Runner.Calls())
		}
	}
}

// A disk export is a copy of the guest's disk: admins only, once an admin
// allowlist exists.
func TestExport_DeniedToReadOnlyUser(t *testing.T) {
	t.Setenv("CORRAL_ADMINS", "alice@github")
	fx := trustFixture(t)
	mallory := http.Header{"Tailscale-User-Login": {"mallory@github"}}
	if code := trustGET(t, fx, "/api/vms/corral-vms/testvm/export", mallory); code != http.StatusForbidden {
		t.Fatalf("read-only user exporting a disk: got %d, want 403", code)
	}
	if argvMentions(fx, "testvm") {
		t.Fatalf("the export ran for a read-only user: %+v", fx.Runner.Calls())
	}
	alice := http.Header{"Tailscale-User-Login": {"alice@github"}}
	if code := trustGET(t, fx, "/api/vms/corral-vms/testvm/export", alice); code == http.StatusForbidden {
		t.Fatal("an admin was refused the export")
	}
}

// A console is full control of the guest: a read-only user gets none.
func TestConsole_DeniedToReadOnlyUser(t *testing.T) {
	t.Setenv("CORRAL_ADMINS", "alice@github")
	fx := trustFixture(t)
	orig := consoleDialer
	dialer := &pipeDialer{ended: make(chan struct{}, 8)}
	consoleDialer = dialer
	t.Cleanup(func() { consoleDialer = orig })

	dial := func(login, path string) error {
		cfg, err := websocket.NewConfig(wsURL(fx.Server.URL)+path, fx.Server.URL)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Header.Set("Tailscale-User-Login", login)
		ws, err := websocket.DialConfig(cfg)
		if err == nil {
			_ = ws.Close()
			<-dialer.ended
		}
		return err
	}
	for _, kind := range []string{"vnc", "rdp"} {
		if err := dial("mallory@github", "/api/"+kind+"/corral-vms/testvm"); err == nil {
			t.Errorf("read-only user opened a %s console", kind)
		}
	}
	if err := dial("alice@github", "/api/vnc/corral-vms/testvm"); err != nil {
		t.Errorf("admin console: %v", err)
	}
}

// In the Tailscale-header mode there is no cookie, so only the browser's
// own markers tell a cross-site form post from the operator's click.
func TestAdminGate_RefusesCrossSiteMutation(t *testing.T) {
	t.Setenv("CORRAL_ADMINS", "") // open mode: identity is not the issue here
	cases := []struct {
		name   string
		header map[string]string
		want   bool // reaches the handler
	}{
		{"foreign Origin", map[string]string{"Origin": "https://evil.example"}, false},
		{"cross-site fetch", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"same-origin fetch", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://corral.example"}, true},
		{"CLI or peer, no browser headers", nil, true},
	}
	for _, tc := range cases {
		called := false
		gate := adminGate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		r := httptest.NewRequest(http.MethodPost, "http://corral.example/api/vms/ns/x/start", nil)
		for k, v := range tc.header {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, r)
		if called != tc.want {
			t.Errorf("%s: reached handler = %v (code %d), want %v", tc.name, called, rec.Code, tc.want)
		}
		if !tc.want && rec.Code != http.StatusForbidden {
			t.Errorf("%s: code = %d, want 403", tc.name, rec.Code)
		}
	}
}

// The export is a GET, so a top-level navigation from another site would
// otherwise carry the identity with it.
func TestAdminGate_RefusesCrossSiteExport(t *testing.T) {
	t.Setenv("CORRAL_ADMINS", "")
	for _, tc := range []struct {
		site string
		want bool
	}{{"cross-site", false}, {"same-site", false}, {"same-origin", true}, {"none", true}, {"", true}} {
		called := false
		gate := adminGate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		r := httptest.NewRequest(http.MethodGet, "http://corral.example/api/vms/ns/x/export", nil)
		if tc.site != "" {
			r.Header.Set("Sec-Fetch-Site", tc.site)
		}
		gate.ServeHTTP(httptest.NewRecorder(), r)
		if called != tc.want {
			t.Errorf("Sec-Fetch-Site %q: reached handler = %v, want %v", tc.site, called, tc.want)
		}
	}
}
