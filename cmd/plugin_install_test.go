package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/plugin"
)

// marketplaceWith serves a v2 index with one plugin, and the plugin's
// artifact, from one local server.
func marketplaceWith(t *testing.T, name string) *httptest.Server {
	t.Helper()
	artifact := []byte("#!/bin/sh\necho stand-in plugin\n")
	sum := sha256.Sum256(artifact)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/artifact", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(artifact) })
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schemaVersion":"` + plugin.MarketplaceAPIV2 + `","name":"m","publisher":{"name":"p"},"plugins":[` +
			`{"name":"` + name + `","description":"d","version":"0.1.0","license":"Apache-2.0","publisher":{"name":"p"},` +
			`"supportedBackends":["all"],"platforms":{"` + runtime.GOOS + "/" + runtime.GOARCH + `":{"url":"` + srv.URL + `/artifact",` +
			`"sha256":"` + hex.EncodeToString(sum[:]) + `"}}}]}`))
	})
	return srv
}

// `corral plugin install NAME` with a bare name must not install from the
// one source that answered while another source is unread: the unread source
// can be the one the operator means.
func TestPluginInstall_BareNameIsRefusedWhileASourceIsUnread(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CORRAL_PLUGIN_DIR", dir)
	t.Setenv("CORRAL_MARKETPLACE_URL", "")
	unread := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(unread.Close)
	other := marketplaceWith(t, "bootc")
	t.Setenv("CORRAL_MARKETPLACE_URLS", unread.URL+","+other.URL+"/index.json")

	err := pluginInstallCmd.RunE(pluginInstallCmd, []string{"bootc"})
	if err == nil || !strings.Contains(err.Error(), "env-1") {
		t.Errorf("install bootc = %v, want a refusal that names the unread source env-1", err)
	}
	installed := filepath.Join(dir, "corral-bootc")
	if _, statErr := os.Stat(installed); statErr == nil {
		t.Fatalf("%s exists: the build from env-2 was installed under the bare name", installed)
	}

	// The source/name form selects one source and still installs.
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{"env-2/bootc"}); err != nil {
		t.Fatalf("install env-2/bootc: %v", err)
	}
	if _, statErr := os.Stat(installed); statErr != nil {
		t.Errorf("install env-2/bootc did not write %s: %v", installed, statErr)
	}
}
