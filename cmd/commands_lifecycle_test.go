package cmd

// Characterization tests for the per-instance lifecycle commands (start, stop,
// delete, info, ssh). They pin the command each backend issues so the routing
// behind these commands can move onto pkg/backend without changing what an
// operator's VM sees (#214, RFC-0004).

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/virtualbox"
)

// kubevirtVMScratch is remoteVMScratch for a KubeVirt VM: the client, package,
// and apply runners all go to the same fake, so no kubectl or virtctl reaches
// a real cluster.
func kubevirtVMScratch(t *testing.T, name string, entry types.RegistryEntry) *shell.Fake {
	t.Helper()
	fake := remoteVMScratch(t, name, entry)
	fake.AddPrefixResponse("kubectl", "", nil)
	fake.AddPrefixResponse("/fake/bin/virtctl", "", nil)
	kubevirt.SetDefaultRunner(fake)
	kubevirt.SetPackageRunner(fake)
	kubevirt.SetApplyRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetDefaultRunner(nil)
		kubevirt.SetPackageRunner(shell.Real{})
		kubevirt.SetApplyRunner(shell.Real{})
	})
	return fake
}

func forceDeletes(t *testing.T) {
	t.Helper()
	old := forceDelete
	forceDelete = true
	t.Cleanup(func() { forceDelete = old })
}

type lifecycleCase struct {
	backend string
	entry   types.RegistryEntry
	start   string
	stop    string
	delete  string
	info    string
}

func lifecycleCases() []lifecycleCase {
	return []lifecycleCase{
		{
			backend: "kubevirt",
			entry:   types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "corral-vms"},
			start:   "virtctl start web -n corral-vms",
			stop:    "virtctl stop web -n corral-vms",
			delete:  "kubectl delete vm web -n corral-vms",
			info:    "kubectl get vm web -n corral-vms -o json",
		},
		{
			backend: "incus",
			entry:   types.RegistryEntry{Backend: "incus", Context: "lab"},
			start:   "incus start lab:web",
			stop:    "incus stop lab:web",
			delete:  "incus delete lab:web --force",
			info:    "incus info lab:web",
		},
		{
			backend: "libvirt",
			entry:   types.RegistryEntry{Backend: "libvirt", Context: "qemu+ssh://hv/system"},
			start:   "virsh -c qemu+ssh://hv/system start web",
			stop:    "virsh -c qemu+ssh://hv/system shutdown web",
			delete:  "virsh -c qemu+ssh://hv/system undefine web --remove-all-storage --nvram",
			info:    "virsh -c qemu+ssh://hv/system dominfo web",
		},
	}
}

func scratchFor(t *testing.T, tc lifecycleCase) *shell.Fake {
	t.Helper()
	if tc.backend == "kubevirt" {
		return kubevirtVMScratch(t, "web", tc.entry)
	}
	return remoteVMScratch(t, "web", tc.entry)
}

func ranCommandLine(fake *shell.Fake, want string) bool {
	for _, c := range fake.Calls() {
		if strings.Contains(commandLineOf(c), want) {
			return true
		}
	}
	return false
}

// commandLineOf drops the directory from a LookPath-resolved binary, so
// virtctl reads as virtctl rather than /fake/bin/virtctl.
func commandLineOf(c shell.Call) string {
	name := c.Name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSpace(name + " " + strings.Join(c.Args, " "))
}

func TestLifecycleCommandsUseTheInstanceBackend(t *testing.T) {
	for _, tc := range lifecycleCases() {
		t.Run(tc.backend+"/start", func(t *testing.T) {
			fake := scratchFor(t, tc)
			if err := startCmd.RunE(startCmd, []string{"web"}); err != nil {
				t.Fatalf("start: %v", err)
			}
			if !ranCommandLine(fake, tc.start) {
				t.Errorf("start did not run %q; calls were %v", tc.start, fake.Calls())
			}
		})
		t.Run(tc.backend+"/stop", func(t *testing.T) {
			fake := scratchFor(t, tc)
			if err := stopCmd.RunE(stopCmd, []string{"web"}); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if !ranCommandLine(fake, tc.stop) {
				t.Errorf("stop did not run %q; calls were %v", tc.stop, fake.Calls())
			}
		})
		t.Run(tc.backend+"/delete", func(t *testing.T) {
			fake := scratchFor(t, tc)
			forceDeletes(t)
			if err := deleteCmd.RunE(deleteCmd, []string{"web"}); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if !ranCommandLine(fake, tc.delete) {
				t.Errorf("delete did not run %q; calls were %v", tc.delete, fake.Calls())
			}
			if _, ok := registryStore.Get("web"); ok {
				t.Error("delete left the VM in the registry")
			}
		})
		t.Run(tc.backend+"/info", func(t *testing.T) {
			fake := scratchFor(t, tc)
			fake.AddResponse(tc.info, "INFO-"+tc.backend, nil)
			out := captureStdout(t, func() {
				if err := infoCmd.RunE(infoCmd, []string{"web"}); err != nil {
					t.Errorf("info: %v", err)
				}
			})
			if !ranCommandLine(fake, tc.info) {
				t.Errorf("info did not run %q; calls were %v", tc.info, fake.Calls())
			}
			if !strings.Contains(out, "INFO-"+tc.backend) {
				t.Errorf("info did not print the backend's answer, got %q", out)
			}
		})
	}
}

// A failed delete keeps the registry entry: the VM may still exist.
func TestDeleteFailureKeepsTheRegistryEntry(t *testing.T) {
	fake := remoteVMScratch(t, "web", types.RegistryEntry{Backend: "incus", Context: "lab"})
	fake.AddResponse("incus delete lab:web --force", "boom", errors.New("exit status 1"))
	forceDeletes(t)
	if err := deleteCmd.RunE(deleteCmd, []string{"web"}); err == nil {
		t.Fatal("delete reported success though incus failed")
	}
	if _, ok := registryStore.Get("web"); !ok {
		t.Error("a failed delete removed the registry entry")
	}
}

// ssh reaches each backend through its own client. The scratch has nothing on
// PATH, so the interactive leg fails — and the failure says which client was
// tried, which is what this pins.
func TestSSHUsesTheInstanceBackend(t *testing.T) {
	t.Run("kubevirt", func(t *testing.T) {
		kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "corral-vms"})
		err := sshCmd.RunE(sshCmd, []string{"web"})
		if err == nil || !strings.Contains(err.Error(), "virtctl") {
			t.Fatalf("ssh on a KubeVirt VM = %v, want it to have tried virtctl", err)
		}
	})
	t.Run("incus", func(t *testing.T) {
		remoteVMScratch(t, "web", types.RegistryEntry{Backend: "incus", Context: "lab"})
		err := sshCmd.RunE(sshCmd, []string{"web"})
		if err == nil || !strings.Contains(err.Error(), "incus") {
			t.Fatalf("ssh on an Incus VM = %v, want it to have tried incus exec", err)
		}
	})
	t.Run("libvirt", func(t *testing.T) {
		remoteVMScratch(t, "web", types.RegistryEntry{Backend: "libvirt", Context: "qemu+ssh://hv/system"})
		err := sshCmd.RunE(sshCmd, []string{"web"})
		if err == nil || !strings.Contains(err.Error(), "no discovered SSH address") {
			t.Fatalf("ssh on a libvirt domain = %v, want libvirt's refusal", err)
		}
	})
}

// A backend without a dedicated CLI path reaches its own adapter. VirtualBox
// VMs used to fall through to local QEMU for start, stop, and delete, which
// looked for a unit that was never there (#214).
func TestLifecycleCommandsOnVirtualBoxUseVBoxManage(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{"start", func() error { return startCmd.RunE(startCmd, []string{"web"}) }, "VBoxManage startvm web --type headless"},
		{"stop", func() error { return stopCmd.RunE(stopCmd, []string{"web"}) }, "VBoxManage controlvm web acpipowerbutton"},
		{"delete", func() error { return deleteCmd.RunE(deleteCmd, []string{"web"}) }, "VBoxManage unregistervm web --delete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := remoteVMScratch(t, "web", types.RegistryEntry{Backend: "virtualbox"})
			fake.AddPrefixResponse("VBoxManage", "", nil)
			virtualbox.SetRunner(fake)
			t.Cleanup(func() { virtualbox.SetRunner(shell.Real{}) })
			forceDeletes(t)
			if err := tc.run(); err != nil {
				t.Fatalf("%s on a VirtualBox VM: %v", tc.name, err)
			}
			if !ranCommandLine(fake, tc.want) {
				t.Errorf("%s did not run %q; calls were %v", tc.name, tc.want, fake.Calls())
			}
		})
	}
}

// info and ssh on a backend with no Inspector or Sheller refuse by backend
// name instead of reading local QEMU metadata for a VM that is not local.
func TestInfoAndSSHOnVirtualBoxAreRefusedByBackend(t *testing.T) {
	remoteVMScratch(t, "web", types.RegistryEntry{Backend: "virtualbox"})
	for action, run := range map[string]func() error{
		"info": func() error { return infoCmd.RunE(infoCmd, []string{"web"}) },
		"ssh":  func() error { return sshCmd.RunE(sshCmd, []string{"web"}) },
	} {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "virtualbox backend does not support "+action) {
			t.Errorf("%s on a VirtualBox VM = %v, want a refusal naming virtualbox", action, err)
		}
	}
}
