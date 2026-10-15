package web

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/vdi"
	"github.com/tuna-os/corral/pkg/vdi/vditest"
)

// The VDI broker over HTTP (#227): identity comes from the trusted headers
// only, and a user cannot claim as, inspect, start, or release another
// user's desktop. Admins come from CORRAL_ADMINS.

var (
	vdiDesk1 = vdi.MemberRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "desk-1"}
	vdiDesk2 = vdi.MemberRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "desk-2"}
)

type vdiFixture struct {
	*TestFixture
	cluster *vditest.Cluster
}

func newVDIFixture(t *testing.T) *vdiFixture {
	t.Helper()
	t.Setenv("CORRAL_VDI_BROKER", "1")
	t.Setenv("CORRAL_VDI_NAMESPACES", "vdi")
	t.Setenv("CORRAL_ADMINS", "root@example.com")
	c := vditest.New(time.Now)
	c.AddPool(vdi.PoolRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "desk"}, vdi.PoolModePersistent, 2)
	orig := vdiCluster
	vdiCluster = c
	f := NewTestFixture()
	t.Cleanup(func() {
		f.Close()
		vdiCluster, vdiBroker = orig, nil
	})
	return &vdiFixture{TestFixture: f, cluster: c}
}

// do sends a request as login ("" = no identity headers) and decodes the
// JSON response into out when out is non-nil.
func (f *vdiFixture) do(t *testing.T, method, path, login string, header http.Header, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, f.Server.URL+path, strings.NewReader(`{"identity":"bob@example.com","user":"bob@example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if login != "" {
		req.Header.Set("Tailscale-User-Login", login)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, body, err)
		}
	}
	return resp.StatusCode
}

func (f *vdiFixture) claim(t *testing.T, login string) vdi.Desktop {
	t.Helper()
	var d vdi.Desktop
	if code := f.do(t, "POST", "/api/vdi/pools/vm/vdi/desk/claim", login, nil, &d); code != http.StatusOK {
		t.Fatalf("claim as %s: HTTP %d", login, code)
	}
	return d
}

func (f *vdiFixture) release(t *testing.T, login string, ref vdi.MemberRef) int {
	t.Helper()
	var accepted map[string]string
	code := f.do(t, "POST", "/api/vdi/desktops/"+ref.ID()+"/release", login, nil, &accepted)
	if code == http.StatusAccepted {
		task, ok := tasks.get(accepted["task"])
		if !ok || accepted["desktop"] != ref.ID() {
			t.Fatalf("release response %v names no task", accepted)
		}
		task.wait()
		if s := task.snapshot(); s["status"] != "done" {
			t.Fatalf("release task: %v", s)
		}
	}
	return code
}

func TestVDI_ClaimUsesTheCallerIdentityNotTheBody(t *testing.T) {
	f := newVDIFixture(t)
	// The request body names bob; the trusted header names alice.
	d := f.claim(t, "alice@example.com")
	if d.Owner != "alice@example.com" || f.cluster.Holder(vdiDesk1) != "alice@example.com" {
		t.Fatalf("claim went to %q (cluster holder %q), want alice", d.Owner, f.cluster.Holder(vdiDesk1))
	}
	if d.ID != vdiDesk1.ID() || d.PoolID != "vm/vdi/desk" || d.SessionID == "" || !d.Running {
		t.Fatalf("desktop = %+v", d)
	}
	var mine []vdi.Desktop
	if code := f.do(t, "GET", "/api/vdi/desktops", "bob@example.com", nil, &mine); code != http.StatusOK || len(mine) != 0 {
		t.Fatalf("bob's desktops = %d %+v; want none", code, mine)
	}
}

func TestVDI_NonAdminMaySelfServeButNotAdministerVMs(t *testing.T) {
	f := newVDIFixture(t)
	f.claim(t, "alice@example.com")
	// The self-service exemption does not open other mutations to alice.
	if code := f.do(t, "POST", "/api/vms/vdi/desk-2/stop", "alice@example.com", nil, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin VM stop: HTTP %d, want 403", code)
	}
}

func TestVDI_UserCannotTouchAnotherUsersDesktop(t *testing.T) {
	f := newVDIFixture(t)
	f.claim(t, "alice@example.com")
	path := "/api/vdi/desktops/" + vdiDesk1.ID()
	if code := f.do(t, "GET", path, "bob@example.com", nil, nil); code != http.StatusForbidden {
		t.Errorf("bob inspected alice's desktop: HTTP %d", code)
	}
	if code := f.do(t, "POST", path+"/start", "bob@example.com", nil, nil); code != http.StatusForbidden {
		t.Errorf("bob started alice's desktop: HTTP %d", code)
	}
	if code := f.release(t, "bob@example.com", vdiDesk1); code != http.StatusForbidden {
		t.Errorf("bob released alice's desktop: HTTP %d", code)
	}
	if code := f.do(t, "GET", "/api/vdi/desktops?all=1", "bob@example.com", nil, nil); code != http.StatusForbidden {
		t.Errorf("bob listed every desktop: HTTP %d", code)
	}
	if f.cluster.Holder(vdiDesk1) != "alice@example.com" || !f.cluster.Running(vdiDesk1) {
		t.Fatal("alice lost her desktop")
	}
	// bob's own claim gets the other member, never alice's.
	if d := f.claim(t, "bob@example.com"); d.ID != vdiDesk2.ID() {
		t.Fatalf("bob claimed %s", d.ID)
	}
	if code := f.do(t, "POST", "/api/vdi/pools/vm/vdi/desk/claim", "carol@example.com", nil, nil); code != http.StatusConflict {
		t.Fatalf("claim from an exhausted pool: HTTP %d, want 409", code)
	}
}

func TestVDI_SpoofedServiceIdentityIsNotTrusted(t *testing.T) {
	f := newVDIFixture(t)
	f.claim(t, "alice@example.com")
	// peerServiceAuth strips a client-supplied service identity, so this is
	// plain bob, not an admin service.
	spoof := http.Header{"Corral-Service-Identity": {"corral-peer"}}
	if code := f.release(t, "bob@example.com", vdiDesk1); code != http.StatusForbidden {
		t.Fatalf("bob: HTTP %d", code)
	}
	if code := f.do(t, "POST", "/api/vdi/desktops/"+vdiDesk1.ID()+"/release", "bob@example.com", spoof, nil); code != http.StatusForbidden {
		t.Fatalf("bob with a spoofed service identity released alice's desktop: HTTP %d", code)
	}
	if f.cluster.Holder(vdiDesk1) != "alice@example.com" {
		t.Fatal("alice lost her desktop")
	}
}

func TestVDI_PeerServiceIdentityCannotHoldDesktops(t *testing.T) {
	f := newVDIFixture(t)
	t.Setenv("CORRAL_PEER_TOKEN", "s3cret")
	peer := http.Header{"Authorization": {"Bearer s3cret"}}
	if code := f.do(t, "POST", "/api/vdi/pools/vm/vdi/desk/claim", "alice@example.com", peer, nil); code != http.StatusForbidden {
		t.Fatalf("peer claim: HTTP %d, want 403", code)
	}
}

func TestVDI_AnonymousCallerIsRejected(t *testing.T) {
	f := newVDIFixture(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/vdi/pools"},
		{"POST", "/api/vdi/pools/vm/vdi/desk/claim"},
		{"GET", "/api/vdi/desktops"},
		{"POST", "/api/vdi/desktops/vm/vdi/desk-1/release"},
	} {
		if code := f.do(t, tc.method, tc.path, "", nil, nil); code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s: HTTP %d, want 401", tc.method, tc.path, code)
		}
	}
	if f.cluster.Holder(vdiDesk1) != "" {
		t.Fatal("an anonymous caller claimed a desktop")
	}
}

func TestVDI_OwnerAndAdminRelease(t *testing.T) {
	f := newVDIFixture(t)
	f.claim(t, "alice@example.com")
	if code := f.release(t, "alice@example.com", vdiDesk1); code != http.StatusAccepted {
		t.Fatalf("owner release: HTTP %d", code)
	}
	if f.cluster.Holder(vdiDesk1) != "" || f.cluster.Running(vdiDesk1) {
		t.Fatal("release left the claim or the VM running")
	}
	f.claim(t, "bob@example.com")
	var all []vdi.Desktop
	if code := f.do(t, "GET", "/api/vdi/desktops?all=1", "root@example.com", nil, &all); code != http.StatusOK || len(all) != 1 {
		t.Fatalf("admin list: HTTP %d %+v", code, all)
	}
	if code := f.release(t, "root@example.com", vdiDesk1); code != http.StatusAccepted {
		t.Fatalf("admin force release: HTTP %d", code)
	}
	if f.cluster.Holder(vdiDesk1) != "" {
		t.Fatal("force release kept bob's claim")
	}
	var log []TaskEntry
	f.do(t, "GET", "/api/tasklog", "root@example.com", nil, &log)
	var audited bool
	for _, e := range log {
		if e.Action == "vdi force-release" && e.User == "root@example.com" && strings.Contains(e.Target, "held by bob@example.com") {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("force release missing from the task log: %+v", log)
	}
}

func TestVDI_StartRetryAndInspect(t *testing.T) {
	f := newVDIFixture(t)
	f.cluster.FailStarts(vdiDesk1, 1)
	d := f.claim(t, "alice@example.com")
	if d.StartError == "" || d.Running {
		t.Fatalf("claim with a failed start = %+v", d)
	}
	path := "/api/vdi/desktops/" + vdiDesk1.ID()
	if code := f.do(t, "POST", path+"/start", "alice@example.com", nil, &d); code != http.StatusOK || !d.Running {
		t.Fatalf("start retry: HTTP %d %+v", code, d)
	}
	if code := f.do(t, "GET", path, "alice@example.com", nil, &d); code != http.StatusOK || d.Owner != "alice@example.com" {
		t.Fatalf("inspect: HTTP %d %+v", code, d)
	}
	if code := f.do(t, "GET", "/api/vdi/desktops/vm/vdi/desk-2", "alice@example.com", nil, nil); code != http.StatusNotFound {
		t.Fatalf("inspect of an unclaimed member: HTTP %d", code)
	}
	var pools []vdi.PoolStatus
	if code := f.do(t, "GET", "/api/vdi/pools", "alice@example.com", nil, &pools); code != http.StatusOK || len(pools) != 1 || pools[0].Claimed != 1 {
		t.Fatalf("pools: HTTP %d %+v", code, pools)
	}
}

func TestVDI_OffByDefault(t *testing.T) {
	t.Setenv("CORRAL_VDI_BROKER", "")
	f := NewTestFixture()
	defer f.Close()
	resp, err := http.Get(f.Server.URL + "/api/vdi/pools")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("broker off: HTTP %d, want 404", resp.StatusCode)
	}
}

func TestSelfServiceMutation(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/api/vdi/pools/vm/vdi/desk/claim", true},
		{"POST", "/api/vdi/desktops/vm/vdi/desk-1/release", true},
		{"DELETE", "/api/vdi/desktops/vm/vdi/desk-1", false},
		{"POST", "/api/vdi/../vms/vdi/desk-1/stop", false},
		{"POST", "/api/vms/vdi/desk-1/stop", false},
		{"POST", "/api/vdix/claim", false},
	} {
		r := httptest.NewRequest(tc.method, "/", nil)
		r.URL.Path = tc.path
		if got := selfServiceMutation(r); got != tc.want {
			t.Errorf("selfServiceMutation(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// waitDesktop polls the desktop until cond holds.
func (f *vdiFixture) waitDesktop(t *testing.T, login string, cond func(vdi.Desktop) bool) vdi.Desktop {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var d vdi.Desktop
		f.do(t, "GET", "/api/vdi/desktops/"+vdiDesk1.ID(), login, nil, &d)
		if cond(d) {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("desktop never reached the expected state: %+v", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVDI_ConsoleSessionIsTracked(t *testing.T) {
	f := newVDIFixture(t)
	serverSide, _ := net.Pipe()
	orig := consoleDialer
	consoleDialer = &fakeConsoleDialer{conn: serverSide}
	t.Cleanup(func() { consoleDialer = orig })
	f.claim(t, "alice@example.com")

	cfg, err := websocket.NewConfig(wsURL(f.Server.URL)+"/api/vnc/vdi/desk-1", f.Server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header.Set("Tailscale-User-Login", "alice@example.com")
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	f.waitDesktop(t, "alice@example.com", func(d vdi.Desktop) bool { return d.Connected })
	_ = ws.Close()
	d := f.waitDesktop(t, "alice@example.com", func(d vdi.Desktop) bool { return !d.Connected })
	if d.DisconnectedAt == nil || d.ReclaimReason != vdi.ReclaimDisconnected || d.ReclaimAt == nil || !d.ReclaimAt.Equal(d.DisconnectedAt.Add(15*time.Minute)) {
		t.Fatalf("after disconnect: %+v", d)
	}
}

func TestVDI_ReclaimPolicyFromEnvironment(t *testing.T) {
	t.Setenv("CORRAL_VDI_BROKER", "1")
	t.Setenv("CORRAL_VDI_DISCONNECT_GRACE", "soon")
	if _, err := newVDIBroker(); err == nil || !strings.Contains(err.Error(), "CORRAL_VDI_DISCONNECT_GRACE") {
		t.Fatalf("bad grace: %v", err)
	}
	t.Setenv("CORRAL_VDI_DISCONNECT_GRACE", "0")
	t.Setenv("CORRAL_VDI_MAX_SESSION", "-1h")
	if _, err := newVDIBroker(); err == nil || !strings.Contains(err.Error(), "CORRAL_VDI_MAX_SESSION") {
		t.Fatalf("negative max session: %v", err)
	}
	t.Setenv("CORRAL_VDI_MAX_SESSION", "8h")
	if b, err := newVDIBroker(); err != nil || b == nil {
		t.Fatalf("valid policy: %v", err)
	}
	t.Setenv("CORRAL_VDI_BROKER", "")
	if b, err := newVDIBroker(); err != nil || b != nil {
		t.Fatalf("broker off: %v, %v", b, err)
	}
}

func TestVDI_ReclaimIsAuditedInTheTaskLog(t *testing.T) {
	resetActivity()
	auditVDI(vdi.AuditEvent{Action: "reclaim", Actor: vdi.ReclaimActor, Target: vdiDesk1.ID(), Holder: "alice@example.com", Reason: vdi.ReclaimDisconnected})
	activity.mu.Lock()
	defer activity.mu.Unlock()
	if len(activity.entries) != 1 || activity.entries[0].Action != "vdi reclaim (disconnect-grace)" || activity.entries[0].User != vdi.ReclaimActor {
		t.Fatalf("task log = %+v", activity.entries)
	}
}

// stubRDP makes the RDP probe of desk-1 answer (open) or refuse.
func (f *vdiFixture) stubRDP(t *testing.T, open bool) {
	t.Helper()
	f.Runner.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"},
		`{"items":[{"metadata":{"name":"desk-1","namespace":"vdi"},"status":{"interfaces":[{"ipAddress":"10.0.0.5"}]}}]}`, nil)
	orig := rdpDial
	rdpDial = func(addr string) (net.Conn, error) {
		if !open || addr != "10.0.0.5:3389" {
			return nil, &net.OpError{Op: "dial", Err: io.EOF}
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	t.Cleanup(func() { rdpDial = orig })
}

func TestVDI_ConnectResolvesTheProtocol(t *testing.T) {
	f := newVDIFixture(t)
	f.claim(t, "alice@example.com")
	path := "/api/vdi/desktops/" + vdiDesk1.ID() + "/connect"

	f.stubRDP(t, false)
	var got vdi.ConnectTarget
	if code := f.do(t, "GET", path, "alice@example.com", nil, &got); code != http.StatusOK {
		t.Fatalf("connect: HTTP %d", code)
	}
	if got.Protocol != vdi.ProtocolVNC || got.Path != "/api/vnc/vdi/desk-1" || len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "10.0.0.5:3389") {
		t.Fatalf("RDP closed: %+v", got)
	}

	f.stubRDP(t, true)
	got = vdi.ConnectTarget{}
	if code := f.do(t, "GET", path, "alice@example.com", nil, &got); code != http.StatusOK || got.Protocol != vdi.ProtocolRDP ||
		got.Path != "/api/rdp/vdi/desk-1?rdcleanpath=1" || got.Direct != "10.0.0.5:3389" || got.Desktop.Owner != "alice@example.com" {
		t.Fatalf("RDP open: HTTP %d %+v", code, got)
	}
}

func TestVDI_ConnectAuthorizationAndStaleClaims(t *testing.T) {
	f := newVDIFixture(t)
	f.stubRDP(t, false)
	f.claim(t, "alice@example.com")
	path := "/api/vdi/desktops/" + vdiDesk1.ID() + "/connect"
	if code := f.do(t, "GET", path, "bob@example.com", nil, nil); code != http.StatusForbidden {
		t.Errorf("bob: HTTP %d, want 403", code)
	}
	if code := f.do(t, "GET", path, "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("anonymous: HTTP %d, want 401", code)
	}
	if code := f.do(t, "GET", path, "root@example.com", nil, nil); code != http.StatusOK {
		t.Errorf("admin: HTTP %d, want 200", code)
	}
	f.cluster.SetRunning(vdiDesk1, false)
	if code := f.do(t, "GET", path, "alice@example.com", nil, nil); code != http.StatusConflict {
		t.Errorf("stopped: HTTP %d, want 409", code)
	}
	f.release(t, "alice@example.com", vdiDesk1)
	if code := f.do(t, "GET", path, "alice@example.com", nil, nil); code != http.StatusNotFound {
		t.Errorf("released: HTTP %d, want 404", code)
	}
}

func TestVDI_ConsoleOfAClaimedDesktopIsTheHolders(t *testing.T) {
	f := newVDIFixture(t)
	orig := consoleDialer
	dialer := &pipeDialer{ended: make(chan struct{}, 8)}
	consoleDialer = dialer
	t.Cleanup(func() { consoleDialer = orig })
	f.claim(t, "alice@example.com")

	dial := func(login, path string) error {
		cfg, err := websocket.NewConfig(wsURL(f.Server.URL)+path, f.Server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if login != "" {
			cfg.Header.Set("Tailscale-User-Login", login)
		}
		ws, err := websocket.DialConfig(cfg)
		if err == nil {
			_ = ws.Close()
			<-dialer.ended // the bridge has ended
		}
		return err
	}
	if err := dial("bob@example.com", "/api/vnc/vdi/desk-1"); err == nil {
		t.Error("bob opened alice's console")
	}
	if err := dial("", "/api/rdp/vdi/desk-1"); err == nil {
		t.Error("an anonymous caller opened alice's console")
	}
	if err := dial("alice@example.com", "/api/vnc/vdi/desk-1"); err != nil {
		t.Errorf("alice: %v", err)
	}
	if err := dial("root@example.com", "/api/vnc/vdi/desk-1"); err != nil {
		t.Errorf("admin: %v", err)
	}
	if err := dial("bob@example.com", "/api/vnc/vdi/desk-2"); err != nil {
		t.Errorf("an unclaimed member must stay open: %v", err)
	}
}

// pipeDialer hands every console a fresh in-memory connection and signals
// ended when a bridge closes it.
type pipeDialer struct{ ended chan struct{} }

func (d *pipeDialer) Dial(_, _ string, _ kubevirt.Console) (net.Conn, error) {
	a, b := net.Pipe()
	go func() {
		_, _ = io.Copy(io.Discard, b)
		d.ended <- struct{}{}
	}()
	return a, nil
}
