package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestMutate_ConcurrentWritesAllLand exercises the load-mutate-save race
// that motivated the store's lock: many goroutines each add a distinct
// peer via SetPeer at the same time. Before mutate serialized the
// load-modify-save span, each goroutine's Load("") could observe the same
// on-disk state and the last Save to run would silently discard every
// other goroutine's addition. With the lock held across the whole span,
// every peer must survive.
func TestMutate_ConcurrentWritesAllLand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = SetPeer(fmt.Sprintf("peer-%d", i), fmt.Sprintf("https://peer-%d.example/", i))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("SetPeer(peer-%d) = %v", i, err)
		}
	}

	peers := Peers()
	if len(peers) != n {
		t.Fatalf("Peers() has %d entries, want %d (lost update if fewer)", len(peers), n)
	}
	seen := make(map[string]bool, n)
	for _, p := range peers {
		seen[p.Name] = true
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("peer-%d", i)
		if !seen[name] {
			t.Errorf("peer %q missing after concurrent SetPeer calls", name)
		}
	}
}

// TestLoad_ReturnsIndependentValues ensures two calls to Load("") never
// hand back the same *Config, so a caller that mutates its result (as
// every setter in config.go does before calling Save) can never corrupt
// another caller's in-flight copy — even though both reads may be served
// from the same cached bytes.
func TestLoad_ReturnsIndependentValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if a == b {
		t.Fatal("Load(\"\") returned the same *Config pointer twice")
	}

	a.Peers = append(a.Peers, PeerConfig{Name: "mutated-only-on-a"})
	if len(b.Peers) != 0 {
		t.Fatalf("mutating a's Peers affected b: %+v", b.Peers)
	}
}

// TestLoad_DefaultCacheInvalidatesOnHOMEChange guards against the cache
// serving stale bytes for a *different* DefaultPath() after HOME changes
// mid-process, which every other test in this package relies on implicitly
// by setting a fresh HOME per test.
func TestLoad_DefaultCacheInvalidatesOnHOMEChange(t *testing.T) {
	home1 := t.TempDir()
	t.Setenv("HOME", home1)
	if err := SetIncusRemote("remote-in-home1"); err != nil {
		t.Fatalf("SetIncusRemote: %v", err)
	}
	if got := IncusRemote(); got != "remote-in-home1" {
		t.Fatalf("IncusRemote() = %q, want remote-in-home1", got)
	}

	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	if got := IncusRemote(); got != "local" {
		t.Fatalf("IncusRemote() = %q after HOME changed, want default %q (cache leaked across HOME)", got, "local")
	}
}

// TestLoad_SeesExternalEdits guards the cache against serving stale bytes
// after another process (or a hand edit) rewrites or deletes config.yaml:
// a long-running `corral web` must see a concurrent `corral context add`.
func TestLoad_SeesExternalEdits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SetIncusRemote("first"); err != nil {
		t.Fatalf("SetIncusRemote: %v", err)
	}
	if got := IncusRemote(); got != "first" {
		t.Fatalf("IncusRemote() = %q, want first", got)
	}

	// Rewrite in place (same inode, different size).
	if err := os.WriteFile(DefaultPath(), []byte("incus:\n  remote: edited-by-hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := IncusRemote(); got != "edited-by-hand" {
		t.Fatalf("IncusRemote() after in-place edit = %q, want edited-by-hand", got)
	}

	// Replace via rename, as another corral process's Save does. Same
	// size as the previous content, so the inode is what changes.
	tmp := filepath.Join(ConfigDir(), "other.yaml")
	if err := os.WriteFile(tmp, []byte("incus:\n  remote: edited-by-peer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, DefaultPath()); err != nil {
		t.Fatal(err)
	}
	if got := IncusRemote(); got != "edited-by-peer" {
		t.Fatalf("IncusRemote() after rename = %q, want edited-by-peer", got)
	}

	if err := os.Remove(DefaultPath()); err != nil {
		t.Fatal(err)
	}
	if got := IncusRemote(); got != "local" {
		t.Fatalf("IncusRemote() after delete = %q, want default local", got)
	}
}

// TestUpdate_AppliesAndPersists covers the exported read-modify-write entry
// point used outside this package (the web theme handler).
func TestUpdate_AppliesAndPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SetPeer("keep", "https://keep.example"); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(cfg *Config) error {
		cfg.Web.BrandTitle = "Ranch"
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	cfg, err := loadFromDisk(DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.BrandTitle != "Ranch" || len(cfg.Peers) != 1 {
		t.Fatalf("on-disk config = %+v, want BrandTitle Ranch and the existing peer", cfg)
	}

	wantErr := errors.New("boom")
	if err := Update(func(cfg *Config) error {
		cfg.Web.BrandTitle = "discarded"
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Update error = %v, want %v", err, wantErr)
	}
	if cfg, _ := Load(""); cfg.Web.BrandTitle != "Ranch" {
		t.Fatalf("failed Update was saved: BrandTitle = %q", cfg.Web.BrandTitle)
	}
}
