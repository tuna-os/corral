package qemu

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestAccelArgsFor_Matrix(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		for _, host := range []string{"amd64", "arm64"} {
			for _, guest := range []types.GuestArch{"", types.ArchAMD64, types.ArchARM64} {
				for _, kvm := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/guest=%s/kvm=%v", goos, host, guest, kvm), func(t *testing.T) {
						effective := string(guest)
						if effective == "" {
							effective = host
						}
						machine, acceleration, model := "q35", "tcg", "max"
						if effective == "arm64" {
							machine = "virt"
						}
						if effective == host && goos == "darwin" {
							acceleration, model = "hvf", "host"
						} else if effective == host && kvm {
							acceleration, model = "kvm", "host"
						}
						want := []string{"-cpu", model, "-smp", "4", "-machine", machine + ",accel=" + acceleration}
						if got := accelArgsFor(goos, host, guest, kvm, 4); !reflect.DeepEqual(got, want) {
							t.Fatalf("runtime machine args = %v, want %v", got, want)
						}
					})
				}
			}
		}
	}
}

func TestQemuSystemBinary(t *testing.T) {
	for _, tc := range []struct {
		arch types.GuestArch
		want string
	}{
		{types.ArchAMD64, "qemu-system-x86_64"},
		{types.ArchARM64, "qemu-system-aarch64"},
		{"riscv64", ""},
		{"x86_64", ""},
	} {
		if got := qemuSystemBinary(tc.arch); got != tc.want {
			t.Errorf("qemuSystemBinary(%q) = %q, want %q", tc.arch, got, tc.want)
		}
	}
	if got, want := qemuSystemBinary(""), qemuSystemBinary(types.GuestArch(runtime.GOARCH)); got != want {
		t.Errorf("default emulator = %q, want host emulator %q", got, want)
	}
}
