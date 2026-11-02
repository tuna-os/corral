package create

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/lima"
	"github.com/tuna-os/corral/pkg/shell"
)

func TestAttachLANBridge_ZeroNADsErrors(t *testing.T) {
	r := request{}
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"get", "net-attach-def", "-A",
		"-o", `jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}`}, "", fmt.Errorf("no Multus"))
	kubevirt.SetPackageRunner(fake)
	defer kubevirt.SetPackageRunner(shell.Real{})

	r.NetworkNAD = ""

	if err := r.attachLANBridge("corral-vms", "myvm"); err == nil {
		t.Error("expected an error when no NAD exists on the cluster, not a silent no-op")
	}
}

func TestAttachLANBridge_ExplicitNADPatchesVM(t *testing.T) {
	r := request{}
	fake := shell.NewFake()
	fake.AddPrefixResponse("kubectl patch vm myvm -n corral-vms --type json -p", "", nil)
	kubevirt.SetPackageRunner(fake)
	kubevirt.SetDefaultRunner(fake)
	defer func() {
		kubevirt.SetPackageRunner(shell.Real{})
		kubevirt.SetDefaultRunner(nil)
	}()

	r.NetworkNAD = "default/lan-bridge"
	r.BridgeIface = "net1"

	if err := r.attachLANBridge("corral-vms", "myvm"); err != nil {
		t.Fatalf("attachLANBridge: %v", err)
	}
	var patched bool
	for _, c := range fake.Calls() {
		if c.Name == "kubectl" && len(c.Args) > 0 && c.Args[0] == "patch" {
			patched = true
		}
	}
	if !patched {
		t.Error("expected attachLANBridge to patch the VM with the new NIC")
	}
}

func TestLocalBootcScript_UsesTempDirMounts(t *testing.T) {
	script := localBootcScript("/dev/loop7", "", nil)

	if !strings.Contains(script, "DISK=/dev/loop7") {
		t.Errorf("script missing DISK=/dev/loop7:\n%s", script)
	}
	if !strings.Contains(script, "MNT_ESP=$(mktemp -d)") {
		t.Errorf("script should use mktemp -d for ESP mount:\n%s", script)
	}
	if !strings.Contains(script, "MNT_ROOT=$(mktemp -d)") {
		t.Errorf("script should use mktemp -d for composefs root mount:\n%s", script)
	}
	if !strings.Contains(script, "MNT=$(mktemp -d)") {
		t.Errorf("script should use mktemp -d for post-install mount check:\n%s", script)
	}

	// Must not contain hardcoded /mnt paths that break on dangling symlinks (#389)
	for _, forbidden := range []string{"/mnt/esp", "/mnt/root", "mkdir -p /mnt", "mount \"${DISK}p3\" /mnt ", "chroot /mnt"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("script contains forbidden hardcoded /mnt path %q (#389):\n%s", forbidden, script)
		}
	}
}

func TestLocalBootcScript_WithProvisionScript(t *testing.T) {
	script := localBootcScript("/dev/loop7", "echo test-provision", nil)

	wantChroot := `cat /output/provision.sh | chroot "$MNT" /bin/bash`
	if !strings.Contains(script, wantChroot) {
		t.Errorf("script missing chroot into temp mount %q:\n%s", wantChroot, script)
	}
	if strings.Contains(script, "chroot /mnt") {
		t.Errorf("script contains forbidden chroot /mnt:\n%s", script)
	}
}

func TestLocalBootcScript_Kargs(t *testing.T) {
	script := localBootcScript("/dev/loop7", "", []string{"tunaos.countme=0", "console=ttyS0,115200n8", "it's=1"})
	want := `--wipe --karg 'tunaos.countme=0' --karg 'console=ttyS0,115200n8' --karg 'it'\''s=1' --root-ssh-authorized-keys /output/id_rsa.pub "$DISK"`
	if !strings.Contains(script, want) {
		t.Errorf("script missing %q:\n%s", want, script)
	}

	plain := localBootcScript("/dev/loop7", "", nil)
	if strings.Contains(plain, "--karg") || !strings.Contains(plain, `--wipe --root-ssh-authorized-keys`) {
		t.Errorf("script without kargs must keep the plain install line:\n%s", plain)
	}
}

func TestBootcPluginArgs_ForwardsKargs(t *testing.T) {
	r := request{}
	r.Bootc, r.StorageClass, r.Node = "ghcr.io/tuna-os/yellowfin:gnome", "fast", "n1"
	r.CPU, r.Mem = 4, "6G"
	r.Kargs = []string{"tunaos.countme=0", "console=ttyS0,115200n8"}
	r.DedicatedCPUs, r.IsolateEmulator = true, true
	r.Tags = []string{"pair=run42", "desktop=gnome"}

	got := strings.Join(r.bootcPluginArgs("gate", "ci", "40Gi"), " ")
	want := "create gate --image ghcr.io/tuna-os/yellowfin:gnome -n ci --cpu 4 --mem 6G --disk 40Gi --storage-class fast --node n1" +
		" --karg tunaos.countme=0 --karg console=ttyS0,115200n8" +
		" --dedicated-cpus --isolate-emulator-thread --tag pair=run42 --tag desktop=gnome"
	if got != want {
		t.Errorf("bootcPluginArgs = %q\nwant              %q", got, want)
	}
}

func TestRunKubevirtCreate_InvalidImage(t *testing.T) {
	r := request{Options: Options{Values: lima.Values{Image: "nonexistent-image"}}}
	if err := r.runKubevirtCreate("test-vm"); err == nil {
		t.Error("expected error for unknown catalog image")
	}
}
