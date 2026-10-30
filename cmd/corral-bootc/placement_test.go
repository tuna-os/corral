//go:build bootc

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/bootc"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

func kubevirtContextHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	cfgDir := filepath.Join(home, ".config", "corral")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
		[]byte("contexts:\n- name: k8s\n  backend: kubevirt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runCreate(t *testing.T, args ...string) error {
	t.Helper()
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"create"}, args...))
	return root.Execute()
}

func TestCreateHelpListsPlacementFlags(t *testing.T) {
	usage := createSubcommand(t, newRootCmd()).Flags().FlagUsages()
	for _, flag := range []string{"--dedicated-cpus", "--isolate-emulator-thread", "--tag"} {
		if !strings.Contains(usage, flag) {
			t.Errorf("corral bootc create --help does not list %s", flag)
		}
	}
}

// --dedicated-cpus on a node without the static CPU manager is refused before
// the builder VM exists: the build takes minutes and its VM would never start.
func TestCreateDedicatedCPUsRefusedBeforeTheBuild(t *testing.T) {
	kubevirtContextHome(t)
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"get", "node", "karnataka", "-o", "jsonpath={.metadata.labels.cpumanager}"}, "false", nil)
	kubevirt.SetPackageRunner(fake)
	kubevirt.SetApplyRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetPackageRunner(shell.Real{})
		kubevirt.SetApplyRunner(shell.Real{})
	})

	err := runCreate(t, "perf-a", "--context", "k8s", "--image", "quay.io/fedora/fedora-bootc:41",
		"--ssh-key", "ssh-ed25519 AAAAtest", "--node", "karnataka", "--dedicated-cpus", "--tag", "pair=run42")
	if err == nil || !strings.Contains(err.Error(), "node karnataka does not have label cpumanager=true") {
		t.Fatalf("err = %v, want the CPU manager refusal naming karnataka", err)
	}
	for _, c := range fake.Calls() {
		if c.Name != "kubectl" || len(c.Args) < 2 || c.Args[0] != "get" || c.Args[1] != "node" {
			t.Errorf("refused create still ran %s %v", c.Name, c.Args)
		}
	}
}

func TestCreatePlacementValidation(t *testing.T) {
	kubevirtContextHome(t)
	for args, want := range map[string]string{
		"--tag Pair=x":              "invalid tag",
		"--isolate-emulator-thread": "needs --dedicated-cpus",
	} {
		err := runCreate(t, append([]string{"perf-a", "--context", "k8s", "--image", "fedora-bootc"}, strings.Fields(args)...)...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
}

// An off-cluster build refuses the KubeVirt-only flags before building.
func TestCreatePlacementRefusedOffCluster(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRAL_DEFAULT_BACKEND", "qemu")
	fb := &fakeBuilder{}
	oldBuilder := newBuilder
	newBuilder = func(bool) bootc.Builder { return fb }
	t.Cleanup(func() { newBuilder = oldBuilder })

	err := runCreate(t, "gate", "--image", "quay.io/fedora/fedora-bootc:41",
		"--ssh-key", "ssh-ed25519 AAAAtest", "--tag", "pair=x")
	if err == nil || !strings.Contains(err.Error(), "KubeVirt backend only, not on backend qemu") {
		t.Fatalf("err = %v, want a KubeVirt-only refusal", err)
	}
	if fb.reqSeen.Image != "" {
		t.Error("the build ran despite the refusal")
	}
}

// finishVM applies the placement to the final VM only.
func TestFinishVM_AppliesPlacement(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
	})
	t.Setenv("HOME", t.TempDir())

	placement := kubevirt.Placement{DedicatedCPUs: true, Tags: map[string]string{"pair": "run42"}}
	if err := finishVM("perf-a", "corral-vms", "perf-a-bootc-disk", "ghcr.io/tuna-os/marlin:latest",
		"ssh-ed25519 AAAAtest", "6G", 4, "n1", placement); err != nil {
		t.Fatalf("finishVM: %v", err)
	}
	var vm map[string]any
	for _, c := range fake.Calls() {
		if strings.Contains(c.Stdin, `"kind":"VirtualMachine"`) {
			if err := json.Unmarshal([]byte(c.Stdin), &vm); err != nil {
				t.Fatal(err)
			}
		}
	}
	if vm == nil {
		t.Fatal("no VirtualMachine applied")
	}
	labels := vm["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["corral.dev/tag.pair"] != "run42" {
		t.Errorf("labels = %v", labels)
	}
	cpu := vm["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["domain"].(map[string]any)["cpu"].(map[string]any)
	if cpu["dedicatedCpuPlacement"] != true {
		t.Errorf("cpu = %v", cpu)
	}
}
