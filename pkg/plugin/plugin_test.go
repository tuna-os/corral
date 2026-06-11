package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryAndResolve(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CORRAL_PLUGIN_DIR", dir)

	if got := Dir(); got != dir {
		t.Fatalf("Dir() = %q, want %q", got, dir)
	}
	if len(Installed()) != 0 {
		t.Fatal("expected no plugins in empty dir")
	}
	if IsInstalled("bootc") {
		t.Fatal("bootc should not be installed")
	}

	// Drop a fake executable plugin and a non-plugin file.
	if err := os.WriteFile(filepath.Join(dir, "corral-bootc"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notaplugin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ps := Installed()
	if len(ps) != 1 || ps[0].Name != "bootc" {
		t.Fatalf("Installed() = %+v, want one plugin 'bootc'", ps)
	}
	if !IsInstalled("bootc") {
		t.Error("bootc should be installed")
	}
	if Resolve("bootc") == "" {
		t.Error("Resolve(bootc) should find the binary")
	}
}

func TestFindEntry(t *testing.T) {
	idx := &Index{Plugins: []Entry{{Name: "bootc"}, {Name: "other"}}}
	if idx.Find("bootc") == nil {
		t.Error("Find(bootc) should return an entry")
	}
	if idx.Find("missing") != nil {
		t.Error("Find(missing) should be nil")
	}
}
