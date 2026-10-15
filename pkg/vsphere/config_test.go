package vsphere

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
)

func writeTestConfig(t *testing.T, contexts []config.ContextConfig) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Contexts = contexts
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func vsphereTarget(name, host string) config.ContextConfig {
	return config.ContextConfig{
		Name:    name,
		Backend: "vsphere",
		Context: host,
		Vsphere: &config.VsphereContextConfig{Username: "administrator@vsphere.local"},
	}
}

func TestConfigForContextReadsPasswordFromEnv(t *testing.T) {
	writeTestConfig(t, []config.ContextConfig{vsphereTarget("estate", "vc.example.com")})
	ResetClients()
	defer ResetClients()
	t.Setenv(PasswordEnv, "env-secret")

	cfg, err := ConfigForContext("estate")
	if err != nil {
		t.Fatalf("ConfigForContext: %v", err)
	}
	if cfg.Host != "vc.example.com" || cfg.Username != "administrator@vsphere.local" || cfg.Password != "env-secret" {
		t.Fatalf("bad config: %+v", cfg)
	}
}

func TestConfigForContextPrefersPerContextPassword(t *testing.T) {
	writeTestConfig(t, []config.ContextConfig{
		vsphereTarget("east", "vc-east.example.com"),
		vsphereTarget("west", "vc-west.example.com"),
	})
	ResetClients()
	defer ResetClients()
	t.Setenv(PasswordEnv, "global-secret")
	t.Setenv(PasswordEnv+"_WEST", "west-secret")

	east, err := ConfigForContext("east")
	if err != nil {
		t.Fatalf("east: %v", err)
	}
	if east.Password != "global-secret" {
		t.Fatalf("east password = %q, want global", east.Password)
	}
	west, err := ConfigForContext("west")
	if err != nil {
		t.Fatalf("west: %v", err)
	}
	if west.Password != "west-secret" {
		t.Fatalf("west password = %q, want per-context", west.Password)
	}
}

func TestConfigForContextRefusesMissingSecrets(t *testing.T) {
	writeTestConfig(t, []config.ContextConfig{
		{Name: "nopass", Backend: "vsphere", Context: "vc-nopass.example.com",
			Vsphere: &config.VsphereContextConfig{Username: "u"}},
		{Name: "nouser", Backend: "vsphere", Context: "vc-nouser.example.com",
			Vsphere: &config.VsphereContextConfig{}},
	})

	if _, err := ConfigForContext("nopass"); err == nil || !strings.Contains(err.Error(), PasswordEnv) {
		t.Fatalf("missing password = %v, want %s named", err, PasswordEnv)
	}
	t.Setenv(PasswordEnv, "secret")
	if _, err := ConfigForContext("nouser"); err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("missing username = %v", err)
	}
	if _, err := ConfigForContext("ghost"); err == nil || !strings.Contains(err.Error(), "no configured context") {
		t.Fatalf("unknown context = %v", err)
	}
}

func TestClientsAreCachedAndReplaceable(t *testing.T) {
	writeTestConfig(t, []config.ContextConfig{vsphereTarget("estate", "vc.example.com")})
	ResetClients()
	defer ResetClients()
	t.Setenv(PasswordEnv, "secret")

	first, err := ClientForContext("estate")
	if err != nil {
		t.Fatalf("ClientForContext: %v", err)
	}
	second, err := ClientForContext("estate")
	if err != nil {
		t.Fatalf("ClientForContext: %v", err)
	}
	if first != second {
		t.Fatal("expected the cached client")
	}

	replacement, err := New(Config{Host: "other.example.com", Username: "u", Password: "p"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	SetClientForContext("estate", replacement)
	if got, _ := ClientForContext("estate"); got != replacement {
		t.Fatal("SetClientForContext did not replace the client")
	}
	ResetClients()
	if got, _ := ClientForContext("estate"); got == replacement {
		t.Fatal("ResetClients did not drop the cache")
	}
}
