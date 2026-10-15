package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

// fakePeer is a remote Corral web instance: it serves an inventory and
// records every request, so tests can check what the CLI sent where.
type fakePeer struct {
	mu       sync.Mutex
	vms      []types.VM
	status   int
	reqs     []*http.Request
	reply    string
	inventSt int
}

func (f *fakePeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Clone(r.Context()))
	f.mu.Unlock()
	if r.URL.Path == "/api/v1/inventory" {
		if f.inventSt != 0 {
			w.WriteHeader(f.inventSt)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"vms": f.vms})
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	_, _ = io.WriteString(w, f.reply)
}

func (f *fakePeer) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

// withPeer writes a config with one peer named "lab" pointing at f.
func withPeer(t *testing.T, f *fakePeer, token string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	yaml := "peers:\n  - name: lab\n    url: " + srv.URL + "\n"
	if token != "" {
		yaml += "    token: " + token + "\n"
	}
	if err := os.MkdirAll(filepath.Join(dir, "corral"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corral", "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePeerTarget_BareNameIsLocal(t *testing.T) {
	_, ok, err := resolvePeerTarget("web")
	if ok || err != nil {
		t.Errorf("resolvePeerTarget(web) = ok %v, err %v; want a local name", ok, err)
	}
}

func TestResolvePeerTarget_UnknownPeer(t *testing.T) {
	withPeer(t, &fakePeer{}, "")
	_, ok, err := resolvePeerTarget("nowhere/web")
	if !ok || err == nil || !strings.Contains(err.Error(), `unknown Corral peer "nowhere"`) {
		t.Errorf("got ok %v, err %v; want an unknown-peer error", ok, err)
	}
}

func TestResolvePeerTarget_FindsTheInstanceWithToken(t *testing.T) {
	f := &fakePeer{vms: []types.VM{
		{Name: "web", Namespace: "team", Context: "prod"},
		{Name: "db", Namespace: "team"},
	}}
	withPeer(t, f, "s3cret")
	p, ok, err := resolvePeerTarget("lab/web")
	if !ok || err != nil {
		t.Fatalf("resolve: ok %v err %v", ok, err)
	}
	if p.vm.Name != "web" || p.vm.Namespace != "team" || p.peer.Name != "lab" {
		t.Errorf("target = %+v", p)
	}
	if got := f.last().Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("inventory request Authorization = %q, want the peer token", got)
	}
}

func TestResolvePeerTarget_Failures(t *testing.T) {
	cases := []struct {
		name string
		peer *fakePeer
		want string
	}{
		{"missing", &fakePeer{vms: []types.VM{{Name: "db"}}}, `lab has no instance "web"`},
		{"ambiguous", &fakePeer{vms: []types.VM{{Name: "web", Namespace: "a"}, {Name: "web", Namespace: "b"}}}, "ambiguous across namespaces"},
		{"http error", &fakePeer{inventSt: http.StatusForbidden}, "peer lab: HTTP 403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPeer(t, tc.peer, "")
			_, ok, err := resolvePeerTarget("lab/web")
			if !ok || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got ok %v err %v; want %q", ok, err, tc.want)
			}
		})
	}
}

func TestPeerAction_EscapesPathAndCarriesContextAndToken(t *testing.T) {
	f := &fakePeer{vms: []types.VM{{Name: "web 1", Namespace: "team/x", Context: "prod&admin"}}}
	withPeer(t, f, "tok")
	p, _, err := resolvePeerTarget("lab/web 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.action(http.MethodPost, "start"); err != nil {
		t.Fatalf("action: %v", err)
	}
	r := f.last()
	if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/vms/team%2Fx/web%201/start" {
		t.Errorf("request = %s %s, want POST with each segment escaped", r.Method, r.URL.EscapedPath())
	}
	if r.URL.Query().Get("context") != "prod&admin" {
		t.Errorf("context query = %q, want the instance's context intact", r.URL.RawQuery)
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("action sent without the peer token")
	}
}

func TestPeerAction_DeleteHasNoActionSegment(t *testing.T) {
	f := &fakePeer{vms: []types.VM{{Name: "web", Namespace: "team"}}}
	withPeer(t, f, "")
	p, _, err := resolvePeerTarget("lab/web")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.action(http.MethodDelete, ""); err != nil {
		t.Fatal(err)
	}
	if r := f.last(); r.Method != http.MethodDelete || r.URL.Path != "/api/vms/team/web" || r.URL.RawQuery != "" {
		t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
	}
}

func TestPeerAction_SurfacesRemoteError(t *testing.T) {
	f := &fakePeer{vms: []types.VM{{Name: "web", Namespace: "team"}}, status: http.StatusConflict, reply: "  VM is already running\n"}
	withPeer(t, f, "")
	p, _, err := resolvePeerTarget("lab/web")
	if err != nil {
		t.Fatal(err)
	}
	err = p.action(http.MethodPost, "start")
	if err == nil || err.Error() != "peer lab: HTTP 409: VM is already running" {
		t.Errorf("err = %v, want the peer's status and message", err)
	}
}
