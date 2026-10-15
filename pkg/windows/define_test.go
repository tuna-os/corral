package windows

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// noDistrobuilder is a fake runner on a host without distrobuilder.
type noDistrobuilder struct{ *shell.Fake }

func (n noDistrobuilder) LookPath(name string) (string, error) {
	if name == "distrobuilder" {
		return "", errors.New("not found")
	}
	return n.Fake.LookPath(name)
}

func joined(c shell.Call) string { return c.Name + " " + strings.Join(c.Args, " ") }

func TestLibvirt_DefineCreatesDiskThenDefinesDomain(t *testing.T) {
	fake := withFake(t)
	fake.AddPrefixResponse("/fake/bin/qemu-img create", "", nil)
	fake.AddPrefixResponse("virsh -c qemu+ssh://host/system define ", "", nil)

	res, err := (Libvirt{}).Define(types.InstanceRef{Backend: "libvirt", Context: "qemu+ssh://host/system", Name: "win11"},
		Spec{Name: "win11", InstallerISO: "/isos/win.iso", VirtioISO: "/isos/virtio.iso", Memory: "16GB"})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want qemu-img create then virsh define", calls)
	}
	if got, want := joined(calls[0]), "/fake/bin/qemu-img create -f qcow2 /isos/win11.qcow2 64G"; got != want {
		t.Errorf("disk call = %q, want %q (next to the installer, 64G default)", got, want)
	}
	xmlPath := calls[1].Args[len(calls[1].Args)-1]
	if _, err := os.Stat(xmlPath); !os.IsNotExist(err) {
		t.Errorf("domain XML temp file %s left behind (stat err %v)", xmlPath, err)
	}
	for _, want := range []string{"/isos/win11.qcow2", "/isos/virtio.iso", "<memory unit='MiB'>16384</memory>"} {
		if !strings.Contains(res.Definition, want) {
			t.Errorf("definition missing %q", want)
		}
	}
	if res.AdminPassword != "" || res.AnswerISO != "" {
		t.Errorf("interactive install produced unattended artifacts: %+v", res)
	}
}

func TestLibvirt_DefineDefaultsToSystemURIAndHonoursDiskSize(t *testing.T) {
	fake := withFake(t)
	fake.AddPrefixResponse("/fake/bin/qemu-img create", "", nil)
	fake.AddPrefixResponse("virsh -c qemu:///system define ", "", nil)
	if _, err := (Libvirt{}).Define(types.InstanceRef{Backend: "libvirt", Name: "w"},
		Spec{Name: "w", InstallerISO: "/i/win.iso", VirtioISO: "/i/v.iso", DiskSize: "120G"}); err != nil {
		t.Fatalf("Define: %v", err)
	}
	if got := joined(fake.Calls()[0]); !strings.HasSuffix(got, " 120G") {
		t.Errorf("disk call %q ignores DiskSize", got)
	}
}

func TestLibvirt_DefineStopsWhenDiskCreationFails(t *testing.T) {
	fake := withFake(t)
	fake.AddPrefixResponse("/fake/bin/qemu-img create", "Could not open: Permission denied\n", errors.New("exit 1"))
	_, err := (Libvirt{}).Define(types.InstanceRef{Backend: "libvirt", Name: "w"},
		Spec{Name: "w", InstallerISO: "/i/win.iso", VirtioISO: "/i/v.iso"})
	if err == nil || err.Error() != "creating the system disk: Could not open: Permission denied" {
		t.Fatalf("err = %v, want qemu-img's message", err)
	}
	for _, c := range fake.Calls() {
		if c.Name == "virsh" {
			t.Errorf("defined a domain with no disk: %+v", c)
		}
	}
}

func TestLibvirt_DefineReportsVirshFailure(t *testing.T) {
	fake := withFake(t)
	fake.AddPrefixResponse("/fake/bin/qemu-img create", "", nil)
	fake.AddPrefixResponse("virsh", "", errors.New("exit status 1"))
	_, err := (Libvirt{}).Define(types.InstanceRef{Backend: "libvirt", Name: "w"},
		Spec{Name: "w", InstallerISO: "/i/win.iso", VirtioISO: "/i/v.iso"})
	if err == nil || err.Error() != "virsh define: exit status 1" {
		t.Fatalf("err = %v, want the virsh error when it prints nothing", err)
	}
}

func TestIncus_DefineRepacksThenInitsWithoutBooting(t *testing.T) {
	fake := withFake(t)
	fake.AddPrefixResponse("distrobuilder repack-windows", "", nil)
	fake.AddPrefixResponse("incus ", "", nil)

	res, err := (Incus{}).Define(types.InstanceRef{Backend: "incus", Context: "lab", Name: "win11"},
		Spec{Name: "win11", InstallerISO: "/isos/win.iso", VirtioISO: "/isos/virtio.iso", CPU: 6, Memory: "16GiB", DiskSize: "100GiB"})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}
	want := []string{
		"distrobuilder repack-windows /isos/win.iso /isos/win11-repacked.iso --drivers /isos/virtio.iso",
		"incus init --empty --vm lab:win11 -c limits.cpu=6 -c limits.memory=16GiB -d root,size=100GiB -c security.secureboot=true",
		"incus config device add lab:win11 install disk source=/isos/win11-repacked.iso boot.priority=10",
	}
	calls := fake.Calls()
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %d", calls, len(want))
	}
	for i, c := range calls {
		if got := joined(c); got != want[i] {
			t.Errorf("call %d = %q\n want %q", i, got, want[i])
		}
		if c.Name == "incus" && (c.Args[0] == "launch" || c.Args[0] == "start") {
			t.Errorf("booted the installer during Define: %q", joined(c))
		}
	}
	if !strings.HasPrefix(res.Definition, "init --empty --vm lab:win11") {
		t.Errorf("Definition = %q, want the init args", res.Definition)
	}
}

func TestIncus_DefineDefaults(t *testing.T) {
	fake := withFake(t)
	fake.AddPrefixResponse("distrobuilder", "", nil)
	fake.AddPrefixResponse("incus ", "", nil)
	if _, err := (Incus{}).Define(types.InstanceRef{Backend: "incus", Name: "w"},
		Spec{Name: "w", InstallerISO: "/i/win.iso", VirtioISO: "/i/v.iso"}); err != nil {
		t.Fatal(err)
	}
	if got := joined(fake.Calls()[1]); got != "incus init --empty --vm local:w -c limits.cpu=4 -c limits.memory=8GiB -d root,size=64GiB -c security.secureboot=true" {
		t.Errorf("init = %q, want the local remote and 4 CPU / 8GiB / 64GiB defaults", got)
	}
}

func TestIncus_DefineFailures(t *testing.T) {
	t.Run("needs virtio", func(t *testing.T) {
		withFake(t)
		_, err := (Incus{}).Define(types.InstanceRef{Backend: "incus", Name: "w"}, Spec{Name: "w", InstallerISO: "/i/win.iso"})
		var u *Unsupported
		if !errors.As(err, &u) || !strings.Contains(u.Remedy, "--virtio-iso") {
			t.Fatalf("err = %v, want an Unsupported naming --virtio-iso", err)
		}
	})
	t.Run("needs installer", func(t *testing.T) {
		withFake(t)
		if _, err := (Incus{}).Define(types.InstanceRef{Backend: "incus", Name: "w"}, Spec{Name: "w"}); err == nil {
			t.Fatal("defined a Windows VM with no installation media")
		}
	})
	t.Run("repack fails", func(t *testing.T) {
		fake := withFake(t)
		fake.AddPrefixResponse("distrobuilder", "unsupported Windows version\n", errors.New("exit 1"))
		_, err := (Incus{}).Define(types.InstanceRef{Backend: "incus", Name: "w"},
			Spec{Name: "w", InstallerISO: "/i/win.iso", VirtioISO: "/i/v.iso"})
		if err == nil || err.Error() != "distrobuilder repack-windows: unsupported Windows version" {
			t.Fatalf("err = %v", err)
		}
		if len(fake.Calls()) != 1 {
			t.Errorf("created an instance after the repack failed: %+v", fake.Calls())
		}
	})
	t.Run("attach fails", func(t *testing.T) {
		fake := withFake(t)
		fake.AddPrefixResponse("distrobuilder", "", nil)
		fake.AddPrefixResponse("incus init", "", nil)
		fake.AddPrefixResponse("incus config device add", "Error: pool full\n", errors.New("exit 1"))
		_, err := (Incus{}).Define(types.InstanceRef{Backend: "incus", Name: "w"},
			Spec{Name: "w", InstallerISO: "/i/win.iso", VirtioISO: "/i/v.iso"})
		if err == nil || err.Error() != "attaching the installer: Error: pool full" {
			t.Fatalf("err = %v", err)
		}
	})
}
