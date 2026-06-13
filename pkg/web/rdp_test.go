package web

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
)

// vmiJSON builds a one-VMI kubectl response with the given IP.
func vmiJSON(ns, name, ip string) string {
	return fmt.Sprintf(`{"items":[{"metadata":{"name":%q,"namespace":%q},
		"status":{"nodeName":"node1","interfaces":[{"ipAddress":%q}]}}]}`, name, ns, ip)
}

func getRDP(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	return body
}

func TestRDPCheck_Open(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	// A local listener stands in for the VM's RDP port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	fx.Runner.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"},
		vmiJSON("tailvm", "winvm", "10.1.2.3"), nil)

	orig := rdpDial
	rdpDial = func(addr string) (net.Conn, error) {
		if addr != "10.1.2.3:3389" {
			t.Errorf("dialed %q, want the VMI IP on 3389", addr)
		}
		return net.Dial("tcp", l.Addr().String())
	}
	defer func() { rdpDial = orig }()

	body := getRDP(t, fx.Server.URL+"/api/vms/tailvm/winvm/rdp")
	if body["open"] != true {
		t.Errorf("open = %v, want true (body: %v)", body["open"], body)
	}
	if body["ip"] != "10.1.2.3" {
		t.Errorf("ip = %v, want 10.1.2.3", body["ip"])
	}
}

func TestRDPCheck_Closed(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	fx.Runner.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"},
		vmiJSON("tailvm", "linuxvm", "10.1.2.4"), nil)

	orig := rdpDial
	rdpDial = func(addr string) (net.Conn, error) {
		return nil, fmt.Errorf("connection refused")
	}
	defer func() { rdpDial = orig }()

	body := getRDP(t, fx.Server.URL+"/api/vms/tailvm/linuxvm/rdp")
	if body["open"] != false {
		t.Errorf("open = %v, want false", body["open"])
	}
}

func TestRDPCheck_NotRunning(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	// No VMIs at all — the VM is stopped.
	fx.Runner.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"},
		`{"items":[]}`, nil)

	body := getRDP(t, fx.Server.URL+"/api/vms/tailvm/stopped/rdp")
	if body["open"] != false {
		t.Errorf("open = %v, want false", body["open"])
	}
	if body["reason"] == nil {
		t.Error("expected a reason for the closed report")
	}
}
