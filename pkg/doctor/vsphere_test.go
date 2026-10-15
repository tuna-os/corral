package doctor

import (
	"context"
	"testing"

	govmomi "github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/vsphere"
)

func simVsphereClient(t *testing.T) *vsphere.Client {
	t.Helper()
	ctx := context.Background()

	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatalf("simulator create: %v", err)
	}
	t.Cleanup(model.Remove)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)

	gov, err := govmomi.NewClient(ctx, server.URL, true)
	if err != nil {
		t.Fatalf("simulator login: %v", err)
	}
	client, err := vsphere.New(vsphere.Config{Host: "sim", Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client.WithVimClient(gov.Client)
}

func TestVsphereChecksAgainstSimulator(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Contexts = []config.ContextConfig{{
		Name:    "estate",
		Backend: "vsphere",
		Context: "vc.example.com",
		Vsphere: &config.VsphereContextConfig{Username: "administrator@vsphere.local"},
	}}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Setenv(vsphere.PasswordEnv, "secret")

	vsphere.SetClientForContext("estate", simVsphereClient(t))
	t.Cleanup(vsphere.ResetClients)

	checks := vsphereChecks("estate")
	if len(checks) == 0 {
		t.Fatal("no checks returned")
	}
	for _, c := range checks {
		if !c.OK && c.Severity == "required" {
			t.Fatalf("check %q failed: %s", c.Name, c.Detail)
		}
	}
	if checks[0].Name != "vCenter reachable" {
		t.Fatalf("first check = %q, want vCenter reachable", checks[0].Name)
	}
}

func TestVsphereChecksExplainBadConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	checks := vsphereChecks("ghost")
	if len(checks) != 1 || checks[0].Name != "Endpoint configuration" {
		t.Fatalf("checks = %+v, want one Endpoint configuration refusal", checks)
	}
	if !checks[0].OK && checks[0].Severity == "" {
		t.Fatal("refusal must carry a severity")
	}
}

func TestVsphereConnectRefusalKinds(t *testing.T) {
	if got := vsphereConnectRefusal(errFake("ServerFaultCode: InvalidLogin: bad")); got.Name != "Credentials accepted" {
		t.Fatalf("login refusal = %q", got.Name)
	}
	if got := vsphereConnectRefusal(errFake("x509: certificate signed by unknown authority")); got.Name != "TLS trust" {
		t.Fatalf("TLS refusal = %q", got.Name)
	}
	if got := vsphereConnectRefusal(errFake("connection refused")); got.Name != "vCenter reachable" {
		t.Fatalf("dial refusal = %q", got.Name)
	}
}

type fakeErr string

func errFake(s string) error { return fakeErr(s) }

func (e fakeErr) Error() string { return string(e) }
