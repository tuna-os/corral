package qemu

import (
	"runtime"
	"strings"
	"testing"
)

// ── accelArgsFor platform matrix ─────────────────────────────
// Consumers run on Linux and macOS; the matrix pins
// every combination so a refactor cannot silently move a platform back to
// flags its hypervisor rejects.

func TestAccelArgsFor_Matrix(t *testing.T) {
	cases := []struct {
		goos, arch  string
		hasKVM      bool
		wantCPU     string
		wantMachine string
	}{
		{"linux", "amd64", true, "host", "q35,accel=kvm"},
		{"linux", "amd64", false, "max", "q35,accel=tcg"},
		{"linux", "arm64", true, "host", "q35,accel=kvm"},
		{"linux", "arm64", false, "max", "q35,accel=tcg"},
		{"darwin", "amd64", false, "host", "q35,accel=hvf"},
		{"darwin", "arm64", false, "host", "virt,accel=hvf"},
		// KVM presence is meaningless on macOS: hvf either way.
		{"darwin", "arm64", true, "host", "virt,accel=hvf"},
	}
	for _, c := range cases {
		args := accelArgsFor(c.goos, c.arch, c.hasKVM, 4)
		joined := strings.Join(args, " ")
		for _, want := range []string{"-cpu " + c.wantCPU, "-smp 4", "-machine " + c.wantMachine} {
			if !strings.Contains(joined, want) {
				t.Errorf("accelArgsFor(%q,%q,kvm=%v) = %q, want substring %q",
					c.goos, c.arch, c.hasKVM, joined, want)
			}
		}
	}
}

func TestQemuSystemBinary_MatchesBuildArch(t *testing.T) {
	want := "qemu-system-x86_64"
	if runtime.GOARCH == "arm64" {
		want = "qemu-system-aarch64"
	}
	if got := qemuSystemBinary(); got != want {
		t.Errorf("qemuSystemBinary() = %q on %q, want %q", got, runtime.GOARCH, want)
	}
}
