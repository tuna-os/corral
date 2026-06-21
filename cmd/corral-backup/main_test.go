package main

import (
	"testing"

	"github.com/hanthor/corral/pkg/shell"
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
