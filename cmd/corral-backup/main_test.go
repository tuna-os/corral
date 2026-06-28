package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

func TestRootCmd_Subcommands(t *testing.T) {
	root := rootCmd()
	for _, want := range []string{"create", "restore", "list"} {
		c, _, err := root.Find([]string{want})
		if err != nil || c == root {
			t.Errorf("missing subcommand %q", want)
		}
	}
}

func TestCreateCmd_RequiresDest(t *testing.T) {
	if createCmd().Flags().Lookup("dest") == nil {
		t.Error("create should have a --dest flag")
	}
	if restoreCmd().Flags().Lookup("size") == nil {
		t.Error("restore should have a --size flag")
	}
}

func TestEnsureRclone(t *testing.T) {
	orig := runner
	defer func() { runner = orig }()

	// Missing rclone → clear error.
	missing := shell.NewFake()
	runner = missing
	if err := ensureRclone(); err == nil {
		t.Error("expected an error when rclone is absent")
	}

	// Present → ok.
	ok := shell.NewFake()
	ok.AddResponseKV("rclone", []string{"version"}, "rclone v1.65", nil)
	runner = ok
	if err := ensureRclone(); err != nil {
		t.Errorf("expected success when rclone present: %v", err)
	}
}

// Exercises the real rclone invocation the backup/restore flows use: copyto
// between two paths (rclone's local backend needs no config), proving the
// command shape works end to end. Skips when rclone isn't installed (CI
// installs it so this runs there).
func TestRclone_RealCopytoRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not installed")
	}
	orig := runner
	runner = shell.Real{}
	defer func() { runner = orig }()

	dir := t.TempDir()
	src := filepath.Join(dir, "src.img.gz")
	dst := filepath.Join(dir, "remote", "backup.img.gz")
	back := filepath.Join(dir, "restored.img.gz")
	payload := []byte("corral-backup-roundtrip")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureRclone(); err != nil {
		t.Fatalf("ensureRclone with rclone present: %v", err)
	}
	// "Upload" then "download" (both real rclone copyto calls).
	if out, err := runner.Run("rclone", "copyto", src, dst); err != nil {
		t.Fatalf("rclone copyto up: %s", out)
	}
	if out, err := runner.Run("rclone", "copyto", dst, back); err != nil {
		t.Fatalf("rclone copyto down: %s", out)
	}
	got, err := os.ReadFile(back)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("round-trip mismatch: got %q err %v", got, err)
	}
}

func TestListCmd_InvokesRcloneLsf(t *testing.T) {
	orig := runner
	defer func() { runner = orig }()
	fake := shell.NewFake()
	fake.AddResponseKV("rclone", []string{"lsf", "r2:backups/corral"}, "web-1.img.gz\n", nil)
	runner = fake

	cmd := listCmd()               // StringVar binding resets flagDest to its default…
	flagDest = "r2:backups/corral" // …so set it after constructing the command.
	if err := cmd.RunE(nil, nil); err != nil {
		t.Fatalf("list: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "lsf" {
		t.Errorf("expected one `rclone lsf` call, got %+v", calls)
	}
}
