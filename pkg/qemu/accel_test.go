package qemu

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestAccelArgsFor_DeviceAccessRequiresReadWrite(t *testing.T) {
	// Real filesystem permission boundary only, not a claim of KVM hardware.
	path := filepath.Join(t.TempDir(), "device")
	if kvmDeviceAccessible(path) {
		t.Fatal("missing device admitted")
	}
	if err := os.WriteFile(path, []byte("device-access-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if !kvmDeviceAccessible(path) {
		t.Fatal("caller-readable/writable descriptor refused")
	}
	t.Run("present-but-not-writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses this DAC boundary; native qualification runs unprivileged")
		}
		if err := os.Chmod(path, 0400); err != nil {
			t.Fatal(err)
		}
		if kvmDeviceAccessible(path) {
			t.Fatal("mere node presence admitted without caller write access")
		}
	})
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
