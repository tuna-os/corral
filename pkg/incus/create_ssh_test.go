package incus

// Tests for Client.Create (incus launch) and Client.SSH (incus exec) — the
// two incus cells the parity matrix ships without a covering test. Create
// goes through the package runner like the rest of the lifecycle surface;
// SSH is an interactive exec, so it is pinned with a stub incus binary on
// PATH that records its argv instead of opening a session.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreate_VMWithCPUAndMem(t *testing.T) {
	f := fakeRemote(t, "[]")
	f.AddPrefixResponse("incus launch", "", nil)

	err := NewClient("local").Create(CreateOpts{
		Name: "vm1", Image: "images:debian/12", VM: true, CPU: 4, Memory: "8Gi",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	argv := strings.Join(f.LastCall().Args, " ")
	for _, want := range []string{
		"launch images:debian/12 local:vm1", "--vm",
		"-c limits.cpu=4", "-c limits.memory=8GiB",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
}

func TestCreate_DefaultsToContainerAndUbuntu(t *testing.T) {
	f := fakeRemote(t, "[]")
	f.AddPrefixResponse("incus launch", "", nil)

	if err := NewClient("local").Create(CreateOpts{Name: "ct1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	argv := strings.Join(f.LastCall().Args, " ")
	if !strings.Contains(argv, "launch images:ubuntu/22.04 local:ct1") {
		t.Errorf("argv %q should launch the default image", argv)
	}
	if strings.Contains(argv, "--vm") {
		t.Errorf("argv %q should not pass --vm for a container", argv)
	}
}

func TestCreate_NormalisesMiAndWrapsFailures(t *testing.T) {
	f := fakeRemote(t, "[]")
	f.AddPrefixResponse("incus launch", "Error: bad profile", fmt.Errorf("exit status 1"))

	err := NewClient("local").Create(CreateOpts{Name: "vm1", VM: true, Memory: "512Mi"})
	if err == nil {
		t.Fatal("expected the launch failure")
	}
	if !strings.Contains(err.Error(), "bad profile") {
		t.Errorf("error %q should carry the tool's output", err)
	}
	if argv := strings.Join(f.LastCall().Args, " "); !strings.Contains(argv, "-c limits.memory=512MiB") {
		t.Errorf("argv %q should normalise Mi to MiB", argv)
	}
}

// stubIncus puts a recording incus stub on PATH and returns the file its argv
// lands in. The stub records and exits 0; point ARGV_FAIL at a nonempty value
// to make it exit 1 instead.
func stubIncus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$ARGV_LOG\"\n" +
		"exit ${ARGV_FAIL:-0}\n"
	if err := os.WriteFile(filepath.Join(dir, "incus"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGV_LOG", log)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func recordedArgv(t *testing.T, log string) string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("reading argv log: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

func TestSSH_RunsTheCommandThroughExec(t *testing.T) {
	log := stubIncus(t)

	if err := NewClient("local").SSH("vm1", "uptime"); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	// incus exec takes the target first, then --, then the command: anything
	// else runs on the wrong instance or not at all.
	if got, want := recordedArgv(t, log), "exec local:vm1 -- sh -c uptime"; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestSSH_WithoutACommandOpensAShell(t *testing.T) {
	log := stubIncus(t)

	if err := NewClient("local").SSH("vm1", ""); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	if got, want := recordedArgv(t, log), "exec local:vm1 -- bash"; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestSSH_SurfacesAFailure(t *testing.T) {
	_ = stubIncus(t)
	t.Setenv("ARGV_FAIL", "1")

	if err := NewClient("local").SSH("vm1", "uptime"); err == nil {
		t.Fatal("expected the exec failure")
	}
}
