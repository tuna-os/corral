package virtualbox

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

func fakeRunner(t *testing.T) *shell.Fake {
	t.Helper()
	f := shell.NewFake()
	SetRunner(f)
	t.Cleanup(func() { SetRunner(shell.Real{}) })
	return f
}

func TestCloneArgv(t *testing.T) {
	f := fakeRunner(t)
	f.AddResponseKV("VBoxManage", []string{"clonevm", "web", "--name", "web-2", "--register"}, "", nil)
	if err := NewClient("").Clone("web", "web-2"); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if calls := f.Calls(); len(calls) != 1 || argv(calls[0]) != "VBoxManage clonevm web --name web-2 --register" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestScaleStoppedArgv(t *testing.T) {
	f := fakeRunner(t)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "db", "--machinereadable"}, poweredOffInfo, nil)
	f.AddResponseKV("VBoxManage", []string{"modifyvm", "db", "--cpus", "4", "--memory", "4096"}, "", nil)
	if err := NewClient("").Scale("db", 4, "4Gi"); err != nil {
		t.Fatalf("Scale: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 || argv(calls[1]) != "VBoxManage modifyvm db --cpus 4 --memory 4096" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestScaleRunningRefused(t *testing.T) {
	f := fakeRunner(t)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "web", "--machinereadable"}, runningInfo, nil)
	err := NewClient("").Scale("web", 2, "")
	if err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("Scale running = %v, want refusal", err)
	}
	for _, c := range f.Calls() {
		if c.Args[0] == "modifyvm" {
			t.Fatalf("modifyvm ran on a running VM: %v", f.Calls())
		}
	}
}

func TestScaleNothingIsNoop(t *testing.T) {
	f := fakeRunner(t)
	if err := NewClient("").Scale("web", 0, ""); err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("calls = %v, want none", f.Calls())
	}
}

func TestMemoryMiB(t *testing.T) {
	for in, want := range map[string]int{"4Gi": 4096, "2048Mi": 2048, "2G": 2048, "512": 512, "1.5Gi": 1536} {
		if got, err := memoryMiB(in); err != nil || got != want {
			t.Errorf("memoryMiB(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "lots", "0", "-1Gi", "4Ti"} {
		if _, err := memoryMiB(bad); err == nil {
			t.Errorf("memoryMiB(%q) accepted", bad)
		}
	}
}
