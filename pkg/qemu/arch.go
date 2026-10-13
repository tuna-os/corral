package qemu

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// GuestArch is an explicit guest CPU architecture. It binds the disk
// architecture, emulator binary, machine, accelerator, CPU model and firmware
// together — a guest is never half x86 and half ARM.
type GuestArch string

const (
	// GuestAMD64 is a 64-bit x86 guest (qemu-system-x86_64, q35).
	GuestAMD64 GuestArch = "amd64"
	// GuestARM64 is a 64-bit ARM guest (qemu-system-aarch64, virt).
	GuestARM64 GuestArch = "arm64"
)

// ParseGuestArch validates a user-supplied architecture string. Empty means
// "not specified" and is not an error here — callers that require an explicit
// guest arch must reject "" themselves so a missing flag never silently
// becomes the host arch.
func ParseGuestArch(s string) (GuestArch, error) {
	switch GuestArch(strings.ToLower(strings.TrimSpace(s))) {
	case GuestAMD64:
		return GuestAMD64, nil
	case GuestARM64:
		return GuestARM64, nil
	case "":
		return "", fmt.Errorf("guest architecture is required (amd64 or arm64)")
	default:
		return "", fmt.Errorf("unsupported guest architecture %q (want amd64 or arm64)", s)
	}
}

// Emulator returns the QEMU system binary for the guest arch.
func (a GuestArch) Emulator() string {
	if a == GuestARM64 {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

// Machine returns the QEMU board for the guest arch: q35 for x86, virt
// for ARM (the board the official QEMU ARM docs use for 64-bit guests).
func (a GuestArch) Machine() string {
	if a == GuestARM64 {
		return "virt"
	}
	return "q35"
}

// GuestSpec is one fully bound guest configuration. Native is true only when
// the guest arch matches the host arch and the accelerator is the host's
// native hypervisor (KVM on Linux, HVF on macOS). Emulated is true for every
// TCG configuration and every cross-arch guest — a TCG guest is never
// reported as native.
type GuestSpec struct {
	GuestArch GuestArch
	Emulator  string
	Machine   string
	Accel     string
	CPU       string
	Native    bool
	Emulated  bool
}

// ResolveOpts controls how ResolveGuestSpec maps a guest arch onto the host.
// HostOS/HostArch default to runtime.GOOS/runtime.GOARCH when empty so tests
// can pin the host without a build matrix.
type ResolveOpts struct {
	HostOS   string
	HostArch string
	// AllowEmulation permits a same-arch TCG fallback when the native
	// hypervisor is unavailable. The returned spec is marked Emulated.
	AllowEmulation bool
	// AllowCrossArch permits a guest arch that differs from the host arch.
	// Cross-arch guests are always TCG and always marked Emulated.
	AllowCrossArch bool
	// KVMProbe reports /dev/kvm presence. Nil uses the real check.
	KVMProbe func() bool
	// HVFProbe reports macOS Hypervisor Framework support. Nil uses the
	// real check (kern.hv_support via sysctl on darwin, false elsewhere).
	HVFProbe func() bool
}

func defaultKVMProbe() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

func defaultHVFProbe() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	out, err := exec.Command("sysctl", "-n", "kern.hv_support").Output()
	if err != nil {
		// Cannot prove HVF support — do not claim native. The caller can
		// still opt into emulation explicitly.
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// NativeAccelForHost names the host's native hypervisor: kvm on Linux,
// hvf on macOS, "" where Corral claims no native scope.
func NativeAccelForHost(hostOS string) string {
	switch hostOS {
	case "linux":
		return "kvm"
	case "darwin":
		return "hvf"
	default:
		return ""
	}
}

// ResolveGuestSpec binds a guest arch to an emulator, machine, accelerator
// and CPU model for the given host. Same-arch guests use the native
// hypervisor; anything else is an explicit, marked emulation scope:
//
//   - native accel unavailable → error unless AllowEmulation
//   - guest arch != host arch → error unless AllowCrossArch (always TCG)
//
// There is no silent TCG-as-native: a spec with Accel "tcg" always has
// Emulated true and Native false.
func ResolveGuestSpec(guest GuestArch, opts ResolveOpts) (GuestSpec, error) {
	if guest != GuestAMD64 && guest != GuestARM64 {
		return GuestSpec{}, fmt.Errorf("unsupported guest architecture %q (want amd64 or arm64)", string(guest))
	}
	hostOS := opts.HostOS
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	hostArch := opts.HostArch
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}
	kvmProbe := opts.KVMProbe
	if kvmProbe == nil {
		kvmProbe = defaultKVMProbe
	}
	hvfProbe := opts.HVFProbe
	if hvfProbe == nil {
		hvfProbe = defaultHVFProbe
	}

	spec := GuestSpec{
		GuestArch: guest,
		Emulator:  guest.Emulator(),
		Machine:   guest.Machine(),
	}

	crossArch := string(guest) != hostArch
	if crossArch && !opts.AllowCrossArch {
		return GuestSpec{}, fmt.Errorf(
			"%s guest on %s host needs --allow-cross-arch (cross-arch guests run under slower TCG emulation, never native)",
			string(guest), hostArch)
	}
	if crossArch {
		spec.Accel = "tcg"
		spec.CPU = "max"
		spec.Emulated = true
		return spec, nil
	}

	var nativeAccel string
	var nativeOK bool
	switch hostOS {
	case "linux":
		nativeAccel = "kvm"
		nativeOK = kvmProbe()
	case "darwin":
		nativeAccel = "hvf"
		nativeOK = hvfProbe()
	default:
		return GuestSpec{}, fmt.Errorf("unsupported host OS %q (Linux/macOS only)", hostOS)
	}
	if nativeOK {
		spec.Accel = nativeAccel
		spec.CPU = "host"
		spec.Native = true
		return spec, nil
	}
	if !opts.AllowEmulation {
		return GuestSpec{}, fmt.Errorf(
			"native %s acceleration unavailable on %s/%s — pass --allow-emulation for slower TCG emulation (never reported as native)",
			nativeAccel, hostOS, hostArch)
	}
	spec.Accel = "tcg"
	spec.CPU = "max"
	spec.Emulated = true
	return spec, nil
}

// accelArgsForSpec renders the -cpu/-smp/-machine triple from a resolved
// spec. It is the arch-aware successor to accelArgs; accelArgs stays for the
// legacy path that has no guest spec.
func accelArgsForSpec(spec GuestSpec, cpu int) []string {
	return []string{"-cpu", spec.CPU, "-smp", fmt.Sprintf("%d", cpu), "-machine", spec.Machine + ",accel=" + spec.Accel}
}

// QemuImgForArch resolves the qemu-img binary for an explicit guest arch.
// The launcher uses it to inspect an admitted image's virtual size before
// creation; creation itself resolves both binaries via findQEMUForArch.
func QemuImgForArch(guestArch string) (string, error) {
	arch, err := ParseGuestArch(guestArch)
	if err != nil {
		return "", err
	}
	_, qemuImg, err := findQEMUForArch(arch)
	if err != nil {
		return "", err
	}
	return qemuImg, nil
}

// findQEMUForArch locates the emulator for an explicit guest arch plus
// qemu-img. PATH wins (covers any install location and lets tests inject
// fakes); the fallback list covers installs not always on PATH, including
// Homebrew on Apple Silicon (/opt/homebrew/bin) which the legacy lookup
// missed.
func findQEMUForArch(arch GuestArch) (qemu, qemuImg string, err error) {
	emulator := arch.Emulator()
	if q, e1 := exec.LookPath(emulator); e1 == nil {
		if qi, e2 := exec.LookPath("qemu-img"); e2 == nil {
			return q, qi, nil
		}
	}
	for _, base := range []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/home/linuxbrew/.linuxbrew/bin",
	} {
		qemuPath := filepath.Join(base, emulator)
		qemuImgPath := filepath.Join(base, "qemu-img")
		if _, e := os.Stat(qemuPath); e == nil {
			if _, e := os.Stat(qemuImgPath); e == nil {
				return qemuPath, qemuImgPath, nil
			}
		}
	}
	return "", "", fmt.Errorf("QEMU for %s not found (looked for %s and qemu-img in PATH, /opt/homebrew/bin, /usr/local/bin, /usr/bin). Install: brew install qemu", string(arch), emulator)
}
