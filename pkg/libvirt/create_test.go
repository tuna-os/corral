package libvirt

// Tests for Client.Create (virt-install), the libvirt create cell the parity
// matrix ships. Create used to shell out via os/exec directly, bypassing the
// package runner, so no test could assert its argv; it now goes through the
// runner like every other virsh call.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

func TestCreateFromQCOWUsesImport(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("virt-install", "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	err := NewClient("qemu:///system").Create(types.CreateOpts{
		Name: "web", CPU: 4, Mem: "8G", QCOW: "/var/lib/libvirt/images/web.qcow2",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	call := f.LastCall()
	if call == nil || call.Name != "virt-install" {
		t.Fatalf("last call = %+v, want a virt-install invocation", call)
	}
	argv := strings.Join(call.Args, " ")
	for _, want := range []string{
		"--connect qemu:///system", "--name web", "--vcpus 4", "--memory 8192",
		"--import", "--disk path=/var/lib/libvirt/images/web.qcow2", "--noautoconsole",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
}

func TestCreateFromISOUsesCdrom(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("virt-install", "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	// Zero CPU must fall back to 2, and Disk "20G" is a size, not a path.
	err := NewClient("qemu:///system").Create(types.CreateOpts{
		Name: "db", ISO: "/iso/debian.iso", Disk: "20G",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	argv := strings.Join(f.LastCall().Args, " ")
	for _, want := range []string{
		"--vcpus 2", "--cdrom /iso/debian.iso", "--disk size=20",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
}

func TestCreateWithoutSourceIsARefusal(t *testing.T) {
	f := shell.NewFake()
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("qemu:///system").Create(types.CreateOpts{Name: "web"}); err == nil {
		t.Fatal("expected a refusal when neither --iso nor --qcow is given")
	} else if !strings.Contains(err.Error(), "--iso or --qcow") {
		t.Errorf("refusal %q should name the missing source", err)
	}
	if len(f.Calls()) != 0 {
		t.Error("a refused create must not invoke virt-install")
	}
}

func TestCreateSurfacesVirtInstallOutput(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("virt-install", "ERROR: bad network", fmt.Errorf("exit status 1"))
	SetRunner(f)
	defer SetRunner(shell.Real{})

	err := NewClient("qemu:///system").Create(types.CreateOpts{
		Name: "web", QCOW: "/img/web.qcow2",
	})
	if err == nil {
		t.Fatal("expected the virt-install failure")
	}
	if !strings.Contains(err.Error(), "bad network") {
		t.Errorf("error %q should carry the tool's output", err)
	}
}
