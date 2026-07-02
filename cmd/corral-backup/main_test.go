package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

func TestRootCmd_Subcommands(t *testing.T) {
	root := rootCmd()
	for _, want := range []string{"create", "restore", "list", "schedule", "unschedule", "schedules"} {
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

func TestCronExpr_Shorthands(t *testing.T) {
	tests := map[string]string{
		"30m": "*/30 * * * *",
		"1h":  "0 * * * *",
		"24h": "0 3 * * *",
	}
	for in, want := range tests {
		got, err := cronExpr(in)
		if err != nil || got != want {
			t.Errorf("cronExpr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestCronExpr_RawCronPassthrough(t *testing.T) {
	got, err := cronExpr("15 2 * * 0")
	if err != nil || got != "15 2 * * 0" {
		t.Errorf("cronExpr raw = %q, %v", got, err)
	}
}

func TestCronExpr_Invalid(t *testing.T) {
	if _, err := cronExpr("bogus"); err == nil {
		t.Error("expected an error for an invalid --every value")
	}
}

func TestAddSchedule_AppliesSecretRBACAndCronJob(t *testing.T) {
	orig := runner
	defer func() { runner = orig }()
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	runner = fake
	kubevirt.SetApplyRunner(fake)
	defer kubevirt.SetApplyRunner(shell.Real{})

	dir := t.TempDir()
	confPath := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(confPath, []byte("[r2]\ntype = s3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RCLONE_CONFIG", confPath)

	if err := addSchedule("web", "tailvm", "0 3 * * *", "r2:backups/corral", 5); err != nil {
		t.Fatalf("addSchedule: %v", err)
	}

	applies := 0
	var sawSecret, sawCronJob bool
	for _, c := range fake.Calls() {
		if c.Name == "kubectl" && len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
			if want := `"kind":"Secret"`; contains(c.Stdin, want) {
				sawSecret = true
			}
			if want := `"kind":"CronJob"`; contains(c.Stdin, want) {
				sawCronJob = true
			}
		}
	}
	// Secret + ServiceAccount + Role + RoleBinding + CronJob
	if applies != 5 {
		t.Errorf("applied %d manifests, want 5", applies)
	}
	if !sawSecret {
		t.Error("expected a Secret manifest (rclone config)")
	}
	if !sawCronJob {
		t.Error("expected a CronJob manifest")
	}
}

func TestAddSchedule_MissingRcloneConfig(t *testing.T) {
	t.Setenv("RCLONE_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.conf"))
	if err := addSchedule("web", "tailvm", "0 3 * * *", "r2:backups/corral", 5); err == nil {
		t.Error("expected an error when the local rclone config is missing")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
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
