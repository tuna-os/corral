package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/vmtest"
)

// newVMTestCommandForTest is a fresh command with its own options, so each
// test sees the declared defaults and nothing another test set.
func newVMTestCommandForTest(t *testing.T) (*cobra.Command, *vmtestOptions) {
	t.Helper()
	return newVMTestCmd()
}

func TestApplyVMTestFlags_FlagsOverrideTheFile(t *testing.T) {
	spec := &vmtest.Spec{
		Name:    "from-file",
		Bootc:   "example.com/from-file:1",
		CPUs:    2,
		Memory:  "4G",
		Timeout: vmtest.Duration(5 * time.Minute),
	}
	cmd, o := newVMTestCommandForTest(t)
	if err := cmd.Flags().Parse([]string{
		"--bootc", "example.com/from-flag:2",
		"--cpu", "8",
		"--timeout", "20m",
		"--user", "tester",
		"--password", "hunter2",
		"--sudo-user",
		"--package", "jq",
		"--package", "htop",
		"--check", "systemctl is-active sshd",
		"--rm",
	}); err != nil {
		t.Fatalf("parsing flags: %v", err)
	}
	o.apply(cmd, spec, []string{"gate"})

	if spec.Name != "gate" {
		t.Errorf("the positional argument names the VM, got %q", spec.Name)
	}
	if spec.Bootc != "example.com/from-flag:2" {
		t.Errorf("bootc = %q, want the flag's value", spec.Bootc)
	}
	if spec.CPUs != 8 || spec.Timeout.Duration() != 20*time.Minute {
		t.Errorf("cpus/timeout = %d/%s", spec.CPUs, spec.Timeout.Duration())
	}
	// An untouched flag must not overwrite the file.
	if spec.Memory != "4G" {
		t.Errorf("memory = %q, want the file's value — an unset flag must not override", spec.Memory)
	}
	if len(spec.Users) != 1 || spec.Users[0].Name != "tester" || !spec.Users[0].Sudo {
		t.Errorf("users = %+v", spec.Users)
	}
	if strings.Join(spec.Packages, ",") != "jq,htop" {
		t.Errorf("packages = %v", spec.Packages)
	}
	if len(spec.Checks) != 1 {
		t.Errorf("checks = %v", spec.Checks)
	}
	if spec.Keep {
		t.Error("--rm means the VM is not kept")
	}
}

func TestApplyVMTestFlags_KeptByDefault(t *testing.T) {
	// The command exists to hand over a system to test, so the VM stays unless
	// the caller asks for a pure gate.
	cmd, o := newVMTestCommandForTest(t)
	spec := &vmtest.Spec{Bootc: "example.com/os:1"}
	o.apply(cmd, spec, nil)
	if !spec.Keep {
		t.Error("the VM should be kept by default")
	}
}

func TestApplyVMTestFlags_PostBootFileOrInline(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "firstboot.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho from-a-file\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, o := newVMTestCommandForTest(t)
	if err := cmd.Flags().Parse([]string{"--post-boot", script}); err != nil {
		t.Fatal(err)
	}
	spec := &vmtest.Spec{Bootc: "x"}
	o.apply(cmd, spec, nil)
	if len(spec.Provision) != 1 || !strings.Contains(spec.Provision[0].Script, "from-a-file") {
		t.Errorf("a path should be read as a script: %+v", spec.Provision)
	}

	// And a one-liner needs no file.
	cmd, o = newVMTestCommandForTest(t)
	if err := cmd.Flags().Parse([]string{"--post-boot", "systemctl is-active gdm"}); err != nil {
		t.Fatal(err)
	}
	spec = &vmtest.Spec{Bootc: "x"}
	o.apply(cmd, spec, nil)
	if len(spec.Provision) != 1 || spec.Provision[0].Script != "systemctl is-active gdm" {
		t.Errorf("an inline script should be used as-is: %+v", spec.Provision)
	}
}

func TestVMTestSummary_DoesNotPanicOnAFailedRun(t *testing.T) {
	// The summary is printed for every run, including the ones that never got
	// a frame, a hook or an endpoint.
	printVMTestSummary(nil)
	printVMTestSummary(&vmtest.Result{Name: "gate", Status: "failed", Failure: "never became ready\nlast console output:\n  ..."})
}

func TestFirstLineOf(t *testing.T) {
	if got := firstLineOf("one\ntwo"); got != "one" {
		t.Errorf("firstLineOf = %q", got)
	}
	if got := firstLineOf("only"); got != "only" {
		t.Errorf("firstLineOf = %q", got)
	}
}
