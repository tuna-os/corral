package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/plugin"
)

// A marketplace can publish a new build of a plugin under the version it
// already has. `corral plugin update` must install that build: the version
// alone said "up to date", and a fix never reached an installed plugin.
func TestPluginUpdate_InstallsANewBuildOfTheSameVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CORRAL_PLUGIN_DIR", dir)
	t.Setenv("CORRAL_MARKETPLACE_URL", "")

	// The handlers run on the server's goroutines, so the build they serve is
	// read and replaced under a lock.
	var mu sync.Mutex
	artifact := []byte("#!/bin/sh\necho first build\n")
	build := func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return artifact
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/artifact", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(build()) })
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		sum := sha256.Sum256(build())
		_, _ = w.Write([]byte(`{"schemaVersion":"` + plugin.MarketplaceAPIV2 + `","name":"m","publisher":{"name":"p"},"plugins":[` +
			`{"name":"bootc","description":"d","version":"0.2.0","license":"Apache-2.0","publisher":{"name":"p"},` +
			`"supportedBackends":["all"],"platforms":{"` + runtime.GOOS + "/" + runtime.GOARCH + `":{"url":"` + srv.URL + `/artifact",` +
			`"sha256":"` + hex.EncodeToString(sum[:]) + `"}}}]}`))
	})
	t.Setenv("CORRAL_MARKETPLACE_URLS", srv.URL+"/index.json")

	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{"bootc"}); err != nil {
		t.Fatalf("install bootc: %v", err)
	}
	installed := filepath.Join(dir, "corral-bootc")
	first, err := os.ReadFile(installed)
	if err != nil || string(first) != string(build()) {
		t.Fatalf("installed file = %q, %v; want the first build", first, err)
	}

	// The same build again is not an update.
	if err := pluginUpdateCmd.RunE(pluginUpdateCmd, nil); err != nil {
		t.Fatalf("update with no new build: %v", err)
	}
	before, err := plugin.ReadState("bootc")
	if err != nil {
		t.Fatal(err)
	}

	second := []byte("#!/bin/sh\necho second build with a fix\n")
	mu.Lock()
	artifact = second
	mu.Unlock()
	if err := pluginUpdateCmd.RunE(pluginUpdateCmd, nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := os.ReadFile(installed)
	if err != nil || string(got) != string(second) {
		t.Errorf("installed file after update = %q, %v; want the second build", got, err)
	}
	after, err := plugin.ReadState("bootc")
	if err != nil {
		t.Fatal(err)
	}
	if after.SHA256 == before.SHA256 || after.Version != "0.2.0" {
		t.Errorf("state after update = version %s sha256 %s; want version 0.2.0 and a new digest (was %s)", after.Version, after.SHA256, before.SHA256)
	}
}
