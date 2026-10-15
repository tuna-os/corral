//go:build unix

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestMutate_WaitsForOtherProcessLock checks that mutate honours the
// cross-process flock. A separate open file description stands in for a
// second corral process: flock locks belong to the open file description,
// so this conflicts exactly as another process's lock would.
func TestMutate_WaitsForOtherProcessLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(ConfigDir(), ".config.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- SetIncusRemote("after-lock") }()

	select {
	case err := <-done:
		t.Fatalf("SetIncusRemote returned (%v) while another process held the config lock", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetIncusRemote: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetIncusRemote still blocked after the lock was released")
	}
	if got := IncusRemote(); got != "after-lock" {
		t.Fatalf("IncusRemote() = %q, want after-lock", got)
	}
}
