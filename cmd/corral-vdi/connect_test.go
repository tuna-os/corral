package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/vdi"
)

var desk = vdi.MemberRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "desk-1"}

func TestConnectViaBroker(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if strings.Contains(r.URL.Path, "desk-2") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"the desktop belongs to another user: vm/vdi/desk-2"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(vdi.ConnectTarget{
			ConnectOption: vdi.ConnectOption{Protocol: vdi.ProtocolRDP, Path: "/api/rdp/vdi/desk-1?rdcleanpath=1"},
			Desktop:       vdi.Desktop{ID: desk.ID(), Owner: "alice"},
			Fallbacks:     []vdi.ConnectOption{{Protocol: vdi.ProtocolVNC}},
		})
	}))
	defer srv.Close()

	got, err := connectViaBroker(context.Background(), srv.Client(), srv.URL+"/", desk)
	if err != nil || got.Protocol != vdi.ProtocolRDP || gotPath != "/api/vdi/desktops/vm/vdi/desk-1/connect" {
		t.Fatalf("connect = %+v, %v (path %s)", got, err, gotPath)
	}
	plan := planConnect(got, desk, srv.URL+"/")
	link := srv.URL + "/#sel=desktops:vm/vdi/desk-1"
	if !strings.Contains(plan.text, link) || !strings.Contains(plan.text, "fallback: vnc") || plan.argv[1] != link {
		t.Fatalf("plan = %+v", plan)
	}

	other := vdi.MemberRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "desk-2"}
	if _, err := connectViaBroker(context.Background(), srv.Client(), srv.URL, other); err == nil || !strings.Contains(err.Error(), "another user") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("forbidden connect: %v", err)
	}
}

func TestPlanConnect_NativeClients(t *testing.T) {
	ct := vdi.MemberRef{Kind: vdi.PoolKindCT, Namespace: "vdi", Name: "lab-1"}
	cases := []struct {
		target vdi.ConnectTarget
		ref    vdi.MemberRef
		want   string
		argv0  string
	}{
		{vdi.ConnectTarget{ConnectOption: vdi.ConnectOption{Protocol: vdi.ProtocolRDP, Direct: "10.0.0.5:3389"}}, desk, "xfreerdp /v:10.0.0.5:3389", "xfreerdp"},
		{vdi.ConnectTarget{ConnectOption: vdi.ConnectOption{Protocol: vdi.ProtocolVNC}, Skipped: []string{"rdp: closed"}}, desk, "virtctl vnc desk-1 -n vdi", "virtctl"},
		{vdi.ConnectTarget{ConnectOption: vdi.ConnectOption{Protocol: vdi.ProtocolTerminal}}, desk, "virtctl console desk-1 -n vdi", "virtctl"},
		{vdi.ConnectTarget{ConnectOption: vdi.ConnectOption{Protocol: vdi.ProtocolTerminal}}, ct, "kubectl exec -it lab-1 -n vdi", ""},
	}
	for _, tc := range cases {
		p := planConnect(tc.target, tc.ref, "")
		if !strings.Contains(p.text, tc.want) {
			t.Errorf("%s: plan %q lacks %q", tc.target.Protocol, p.text, tc.want)
		}
		if tc.argv0 == "" {
			if !p.ctShell {
				t.Errorf("CT terminal plan = %+v", p)
			}
		} else if p.argv[0] != tc.argv0 {
			t.Errorf("%s: argv = %v", tc.target.Protocol, p.argv)
		}
	}
	if p := planConnect(cases[1].target, desk, ""); !strings.Contains(p.text, "skipped: rdp: closed") {
		t.Errorf("skipped reasons missing: %q", p.text)
	}
}

func withClusterFake(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	vdi.SetRunner(fake)
	t.Cleanup(func() { vdi.SetRunner(shell.Real{}) })
	fake.AddResponseKV("kubectl", []string{"get", "lease", "corral-vdi-desk-1", "-n", "vdi", "--ignore-not-found", "-o", "json"},
		`{"metadata":{"name":"corral-vdi-desk-1","resourceVersion":"3","labels":{"corral.dev/vdi-pool":"desk","corral.dev/vdi-member":"desk-1"}},`+
			`"spec":{"holderIdentity":"alice","acquireTime":"2099-01-01T00:00:00Z","renewTime":"2099-01-01T00:00:00Z","leaseDurationSeconds":3600}}`, nil)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "desk-1", "-n", "vdi", "-o", "json"}, `{"status":{"printableStatus":"Running"}}`, nil)
	return fake
}

func TestConnectViaCluster(t *testing.T) {
	fake := withClusterFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vmi", "desk-1", "-n", "vdi", "-o", "jsonpath={.status.interfaces[0].ipAddress}"}, "10.0.0.5", nil)
	orig := vdi.DialRDP
	vdi.DialRDP = func(string) (net.Conn, error) { return nil, errors.New("refused") }
	t.Cleanup(func() { vdi.DialRDP = orig })

	got, err := connectViaCluster(desk, "")
	if err != nil || got.Protocol != vdi.ProtocolVNC || len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "from this machine") {
		t.Fatalf("admin connect = %+v, %v", got, err)
	}
	if got, err := connectViaCluster(desk, "alice"); err != nil || got.Desktop.Owner != "alice" {
		t.Fatalf("owner connect = %+v, %v", got, err)
	}
	if _, err := connectViaCluster(desk, "bob"); !errors.Is(err, vdi.ErrForbidden) {
		t.Fatalf("bob connect: %v", err)
	}

	vdi.DialRDP = func(string) (net.Conn, error) { a, b := net.Pipe(); _ = b.Close(); return a, nil }
	if got, err := connectViaCluster(desk, ""); err != nil || got.Protocol != vdi.ProtocolRDP || got.Direct != "10.0.0.5:3389" {
		t.Fatalf("RDP up = %+v, %v", got, err)
	}
}

func TestConnectCmd_Flags(t *testing.T) {
	c := connectCmd()
	for _, f := range []string{"kind", "broker", "user", "open"} {
		if c.Flags().Lookup(f) == nil {
			t.Errorf("connect lacks --%s", f)
		}
	}
}
