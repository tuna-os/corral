package qemu

import (
	"strings"
	"testing"
)

func TestParseGuestArch(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    GuestArch
		wantErr bool
	}{
		{in: "amd64", want: GuestAMD64},
		{in: "arm64", want: GuestARM64},
		{in: "AMD64", want: GuestAMD64},
		{in: "", wantErr: true},
		{in: "riscv64", wantErr: true},
	} {
		got, err := ParseGuestArch(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("ParseGuestArch(%q): expected error", tc.in)
		}
		if !tc.wantErr && (err != nil || got != tc.want) {
			t.Errorf("ParseGuestArch(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestGuestArchBinding(t *testing.T) {
	if GuestAMD64.Emulator() != "qemu-system-x86_64" || GuestAMD64.Machine() != "q35" {
		t.Errorf("amd64 binds emulator/machine wrong: %s/%s", GuestAMD64.Emulator(), GuestAMD64.Machine())
	}
	if GuestARM64.Emulator() != "qemu-system-aarch64" || GuestARM64.Machine() != "virt" {
		t.Errorf("arm64 binds emulator/machine wrong: %s/%s", GuestARM64.Emulator(), GuestARM64.Machine())
	}
}

func TestResolveGuestSpec_NativeMatrix(t *testing.T) {
	trueProbe := func() bool { return true }
	for _, tc := range []struct {
		name      string
		guest     GuestArch
		hostOS    string
		hostArch  string
		wantAccel string
		wantCPU   string
	}{
		{name: "linux amd64 native", guest: GuestAMD64, hostOS: "linux", hostArch: "amd64", wantAccel: "kvm", wantCPU: "host"},
		{name: "linux arm64 native", guest: GuestARM64, hostOS: "linux", hostArch: "arm64", wantAccel: "kvm", wantCPU: "host"},
		{name: "darwin amd64 native", guest: GuestAMD64, hostOS: "darwin", hostArch: "amd64", wantAccel: "hvf", wantCPU: "host"},
		{name: "darwin arm64 native", guest: GuestARM64, hostOS: "darwin", hostArch: "arm64", wantAccel: "hvf", wantCPU: "host"},
	} {
		spec, err := ResolveGuestSpec(tc.guest, ResolveOpts{
			HostOS: tc.hostOS, HostArch: tc.hostArch, KVMProbe: trueProbe, HVFProbe: trueProbe,
		})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if spec.Accel != tc.wantAccel || spec.CPU != tc.wantCPU || !spec.Native || spec.Emulated {
			t.Errorf("%s: got %+v, want accel=%s cpu=%s native", tc.name, spec, tc.wantAccel, tc.wantCPU)
		}
		if spec.Emulator != tc.guest.Emulator() || spec.Machine != tc.guest.Machine() {
			t.Errorf("%s: emulator/machine not bound to guest: %+v", tc.name, spec)
		}
	}
}

func TestResolveGuestSpec_NoSilentTCG(t *testing.T) {
	falseProbe := func() bool { return false }
	// Native accel unavailable without opt-in must refuse, not silently TCG.
	if _, err := ResolveGuestSpec(GuestAMD64, ResolveOpts{
		HostOS: "linux", HostArch: "amd64", KVMProbe: falseProbe,
	}); err == nil || !strings.Contains(err.Error(), "--allow-emulation") {
		t.Errorf("expected --allow-emulation refusal, got %v", err)
	}
	// With opt-in the spec is marked emulated, never native.
	spec, err := ResolveGuestSpec(GuestAMD64, ResolveOpts{
		HostOS: "linux", HostArch: "amd64", KVMProbe: falseProbe, AllowEmulation: true,
	})
	if err != nil {
		t.Fatalf("AllowEmulation: %v", err)
	}
	if spec.Accel != "tcg" || spec.Native || !spec.Emulated {
		t.Errorf("TCG fallback must be emulated/non-native: %+v", spec)
	}
}

func TestResolveGuestSpec_CrossArchNeedsFlag(t *testing.T) {
	trueProbe := func() bool { return true }
	if _, err := ResolveGuestSpec(GuestAMD64, ResolveOpts{
		HostOS: "darwin", HostArch: "arm64", KVMProbe: trueProbe, HVFProbe: trueProbe,
	}); err == nil || !strings.Contains(err.Error(), "--allow-cross-arch") {
		t.Errorf("expected --allow-cross-arch refusal, got %v", err)
	}
	spec, err := ResolveGuestSpec(GuestAMD64, ResolveOpts{
		HostOS: "darwin", HostArch: "arm64", KVMProbe: trueProbe, HVFProbe: trueProbe, AllowCrossArch: true,
	})
	if err != nil {
		t.Fatalf("AllowCrossArch: %v", err)
	}
	if spec.Accel != "tcg" || spec.Native || !spec.Emulated {
		t.Errorf("cross-arch must be TCG emulated: %+v", spec)
	}
	// Cross-arch keeps the guest's emulator/machine, not the host's.
	if spec.Emulator != "qemu-system-x86_64" || spec.Machine != "q35" {
		t.Errorf("cross-arch x86 guest must keep x86 emulator/machine: %+v", spec)
	}
}

func TestResolveGuestSpec_UnsupportedHost(t *testing.T) {
	if _, err := ResolveGuestSpec(GuestAMD64, ResolveOpts{HostOS: "windows", HostArch: "amd64"}); err == nil {
		t.Error("windows host must be refused")
	}
	if _, err := ResolveGuestSpec("riscv64", ResolveOpts{HostOS: "linux", HostArch: "amd64"}); err == nil {
		t.Error("unknown guest arch must be refused")
	}
}

func TestAccelArgsForSpec(t *testing.T) {
	args := strings.Join(accelArgsForSpec(GuestSpec{Machine: "virt", Accel: "hvf", CPU: "host"}, 4), " ")
	for _, want := range []string{"-cpu host", "-smp 4", "-machine virt,accel=hvf"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %q", want, args)
		}
	}
}
