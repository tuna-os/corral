package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/virtualbox"
)

// remoteVMScratch registers one VM on a non-local backend and isolates
// everything else: no config, no kubeconfig, nothing on PATH, so a command
// that strays onto the local QEMU path fails instead of touching the host.
func remoteVMScratch(t *testing.T, name string, entry types.RegistryEntry) *shell.Fake {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("KUBECONFIG", "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CORRAL_INCUS_REMOTE", "")
	t.Setenv("CORRAL_LIBVIRT_URI", "")
	t.Setenv("CORRAL_KUBE_CONTEXT", "")

	oldStore, oldSelected := registryStore, selectedVM
	registryStore = registry.NewStoreAt(filepath.Join(t.TempDir(), "registry.json"))
	selectedVM = nil
	t.Cleanup(func() { registryStore, selectedVM = oldStore, oldSelected })
	ref := types.InstanceRef{Backend: entry.Backend, Context: entry.Context, Namespace: entry.Namespace, Name: name}
	if ref.Namespace == "" && (ref.Backend == "incus" || ref.Backend == "libvirt" || ref.Backend == "virtualbox") {
		ref.Namespace = ref.Backend
	}
	if err := registryStore.SetRef(ref, entry); err != nil {
		t.Fatal(err)
	}
	if err := config.AddContext(config.ContextConfig{Name: "test-target", Backend: entry.Backend, Context: entry.Context}); err != nil {
		t.Fatal(err)
	}
	if entry.Backend == "kubevirt" {
		if err := config.SetKubeContext(entry.Context); err != nil {
			t.Fatal(err)
		}
	}

	fake := shell.NewFake()
	fake.AddPrefixResponse("incus", "", nil)
	fake.AddPrefixResponse("virsh", "", nil)
	switch entry.Backend {
	case "incus":
		fake.AddResponse("incus list "+entry.Context+": --format=json", `[{"name":"`+name+`","type":"virtual-machine","status":"Stopped"}]`, nil)
	case "libvirt":
		fake.AddResponse("virsh -c "+entry.Context+" list --all --name", name+"\n", nil)
	case "virtualbox":
		fake.AddResponse("VBoxManage list vms", `"`+name+`" {00000000-0000-0000-0000-000000000001}`+"\n", nil)
		virtualbox.SetRunner(fake)
		t.Cleanup(func() { virtualbox.SetRunner(shell.Real{}) })
	}
	incus.SetRunner(fake)
	libvirt.SetRunner(fake)
	t.Cleanup(func() {
		incus.SetRunner(shell.Real{})
		libvirt.SetRunner(shell.Real{})
	})
	return fake
}

func ranCommand(fake *shell.Fake, want string) bool {
	for _, c := range fake.Calls() {
		if strings.Contains(c.Name+" "+strings.Join(c.Args, " "), want) {
			return true
		}
	}
	return false
}

// `corral logs` on an Incus VM reads the instance's console log on its own
// remote. It used to run journalctl for a corral-<name> unit that does not
// exist (#489).
func TestLogsOnIncusReadsTheConsoleLog(t *testing.T) {
	fake := remoteVMScratch(t, "web", types.RegistryEntry{Backend: "incus", Context: "lab"})
	fake.AddResponse("incus console lab:web --show-log", "[    0.000000] Linux version 6.8\n", nil)

	out := captureStdout(t, func() {
		if err := logsCmd.RunE(logsCmd, []string{"web"}); err != nil {
			t.Errorf("logs on an Incus VM: %v", err)
		}
	})
	if !ranCommand(fake, "incus console lab:web --show-log") {
		t.Errorf("logs did not read the Incus console log; calls were %v", fake.Calls())
	}
	if !strings.Contains(out, "Linux version") {
		t.Errorf("logs did not print the console log, got %q", out)
	}
}

// libvirt keeps no log a client can read back, so `corral logs` refuses by
// name instead of reading the local journal.
func TestLogsOnLibvirtIsRefusedByBackend(t *testing.T) {
	remoteVMScratch(t, "db", types.RegistryEntry{Backend: "libvirt", Context: "qemu+ssh://hv/system"})

	err := logsCmd.RunE(logsCmd, []string{"db"})
	if err == nil || !strings.Contains(err.Error(), "libvirt") || !strings.Contains(err.Error(), "logs") {
		t.Fatalf("logs on a libvirt domain = %v, want a refusal naming libvirt", err)
	}
}

// `corral restart` on a libvirt domain is `virsh reboot` against the domain's
// own URI, not a local QEMU stop and start (#489).
func TestRestartOnLibvirtRebootsThroughVirsh(t *testing.T) {
	fake := remoteVMScratch(t, "db", types.RegistryEntry{Backend: "libvirt", Context: "qemu+ssh://hv/system"})

	if err := restartCmd.RunE(restartCmd, []string{"db"}); err != nil {
		t.Fatalf("restart on a libvirt domain: %v", err)
	}
	if !ranCommand(fake, "virsh -c qemu+ssh://hv/system reboot db") {
		t.Errorf("restart did not reboot through virsh; calls were %v", fake.Calls())
	}
}

// `corral restart` on an Incus VM is `incus restart` on its remote.
func TestRestartOnIncusUsesIncusRestart(t *testing.T) {
	fake := remoteVMScratch(t, "web", types.RegistryEntry{Backend: "incus", Context: "lab"})

	if err := restartCmd.RunE(restartCmd, []string{"web"}); err != nil {
		t.Fatalf("restart on an Incus VM: %v", err)
	}
	if !ranCommand(fake, "incus restart lab:web") {
		t.Errorf("restart did not use incus restart; calls were %v", fake.Calls())
	}
}

// `corral viewer` on a libvirt domain launches virt-viewer against the
// domain's URI. With nothing on PATH that launch fails, and the failure names
// virt-viewer — the local QEMU path failed on missing VM metadata instead.
func TestViewerOnLibvirtLaunchesVirtViewer(t *testing.T) {
	remoteVMScratch(t, "db", types.RegistryEntry{Backend: "libvirt", Context: "qemu+ssh://hv/system"})

	err := viewerCmd.RunE(viewerCmd, []string{"db"})
	if err == nil || !strings.Contains(err.Error(), "virt-viewer") {
		t.Fatalf("viewer on a libvirt domain = %v, want it to have tried virt-viewer", err)
	}
}
