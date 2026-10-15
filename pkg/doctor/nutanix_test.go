package doctor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/nutanix"
)

// fakeNutanixPC points a nutanix client at a fake Prism Central answering
// the probe page, exercising the doctor through the client cache.
func fakeNutanixPC(t *testing.T, status int) *nutanix.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"authentication failed"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data": [], "metadata": {"totalAvailableResults": 0}}`))
	}))
	t.Cleanup(srv.Close)
	client, err := nutanix.New(nutanix.Config{Host: srv.URL, Username: "admin", Secret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := srv.Client()
	httpClient.Timeout = nutanix.Timeout
	return client.WithHTTPClient(httpClient)
}

func writeNutanixContext(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Contexts = []config.ContextConfig{{
		Name:    "pc",
		Backend: "nutanix",
		Context: "pc.example.com",
		Nutanix: &config.NutanixContextConfig{Username: "admin"},
	}}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv(nutanix.PasswordEnv, "secret")
}

func TestNutanixChecksAgainstFakePC(t *testing.T) {
	writeNutanixContext(t)
	nutanix.SetClientForContext("pc", fakeNutanixPC(t, http.StatusOK))
	t.Cleanup(nutanix.ResetClients)

	checks := nutanixChecks("pc")
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	reachable, ok := byName["Prism Central reachable"]
	if !ok || !reachable.OK {
		t.Fatalf("reachable check = %+v (all: %+v)", reachable, checks)
	}
	note, ok := byName["Migration targets"]
	if !ok || note.Severity != "warning" {
		t.Errorf("migration note = %+v, want a warning", note)
	}
}

func TestNutanixChecksRefuseBadCredentials(t *testing.T) {
	writeNutanixContext(t)
	nutanix.SetClientForContext("pc", fakeNutanixPC(t, http.StatusUnauthorized))
	t.Cleanup(nutanix.ResetClients)

	checks := nutanixChecks("pc")
	if len(checks) != 1 || checks[0].Name != "Credentials accepted" {
		t.Fatalf("checks = %+v, want the credentials refusal", checks)
	}
	if !strings.Contains(checks[0].Detail, nutanix.PasswordEnv) {
		t.Errorf("detail = %q, want the env remedy", checks[0].Detail)
	}
}

func TestNutanixChecksRefuseUnconfigured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	checks := nutanixChecks("pc")
	if len(checks) != 1 || checks[0].Name != "Endpoint configuration" {
		t.Fatalf("checks = %+v, want the configuration refusal", checks)
	}
}
