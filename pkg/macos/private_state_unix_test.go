//go:build linux || darwin

package macos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateStateRefusesLinksAndRestrictsModes(t *testing.T) {
	directory := authTestDirectory(t)
	private := filepath.Join(directory, "private")
	if err := os.Mkdir(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(private); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(private)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("directory was not restricted")
	}
	config := filepath.Join(private, "config.json")
	if err := os.WriteFile(config, []byte("native"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := privateConfig(config); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(config)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("config was not restricted")
	}
	linked := filepath.Join(private, "linked")
	if err := os.Symlink(config, linked); err != nil {
		t.Fatal(err)
	}
	if err := privateConfig(linked); err == nil {
		t.Fatal("accepted symlink config")
	}
	hard := filepath.Join(private, "hard")
	if err := os.Link(config, hard); err != nil {
		t.Fatal(err)
	}
	if err := privateConfig(config); err == nil {
		t.Fatal("accepted hardlinked config")
	}
	parent := filepath.Join(directory, "linked-parent")
	if err := os.Symlink(private, parent); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(filepath.Join(parent, "child")); err == nil {
		t.Fatal("accepted symlink parent")
	}
}

func TestPrivateStateRefusesLinkedPreparationLock(t *testing.T) {
	for _, hard := range []bool{false, true} {
		directory := authTestDirectory(t)
		target := filepath.Join(directory, "target")
		if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
			t.Fatal(err)
		}
		lock := filepath.Join(directory, ".prepare.lock")
		var err error
		if hard {
			err = os.Link(target, lock)
		} else {
			err = os.Symlink(target, lock)
		}
		if err != nil {
			t.Fatal(err)
		}
		if release, err := lockPrivateState(context.Background(), directory); err == nil {
			release()
			t.Fatal("accepted linked lock")
		}
		info, err := os.Stat(target)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatal("unsafe lock altered target")
		}
	}
}

func TestPrivatePreparationIsSerializedAndCancellable(t *testing.T) {
	directory := authTestDirectory(t)
	release, err := lockPrivateState(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if unlock, err := lockPrivateState(ctx, directory); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("competing prepare did not wait: %v", err)
	}
	release()
	unlock, err := lockPrivateState(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
