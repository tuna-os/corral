package create

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestCreateGuestOptionsValidation(t *testing.T) {
	for _, tc := range []struct {
		backend, arch, firmware, wantError string
	}{
		{"qemu", "", "", ""},
		{"qemu", "amd64", "uefi", ""},
		{"qemu", "arm64", "uefi", ""},
		{"qemu", "amd64", "bios", ""},
		{"qemu", "x86_64", "uefi", "unsupported guest architecture"},
		{"qemu", "riscv64", "uefi", "unsupported guest architecture"},
		{"qemu", "amd64", "efi", "unsupported firmware"},
		{"kubevirt", "amd64", "uefi", "not supported on backend kubevirt"},
		{"incus", "arm64", "", "not supported on backend incus"},
		{"libvirt", "amd64", "", "not supported on backend libvirt"},
		{"proxmox", "amd64", "", "not supported on backend proxmox"},
		{"bootc", "amd64", "", "not supported on backend bootc"},
		{"kubevirt", "", "uefi", ""},
	} {
		t.Run(tc.backend+"/"+tc.arch+"/"+tc.firmware, func(t *testing.T) {
			r := request{Options: Options{Arch: tc.arch, Firmware: tc.firmware}}
			err := r.validateGuestOptions(tc.backend)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("validation error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestQEMUFirmwareDefaultsToUEFIForARM64(t *testing.T) {
	for _, tc := range []struct {
		firmware, arch, host string
		want                 bool
	}{
		{"", "", "arm64", true},
		{"", "arm64", "amd64", true},
		{"", "", "amd64", false},
		{"", "amd64", "arm64", false},
		{"uefi", "amd64", "amd64", true},
		{"bios", "arm64", "arm64", false},
	} {
		if got := qemuUsesUEFI(tc.firmware, tc.arch, tc.host); got != tc.want {
			t.Errorf("qemuUsesUEFI(%q, %q, host %q) = %v, want %v", tc.firmware, tc.arch, tc.host, got, tc.want)
		}
	}
}

func TestCreateARM64GuestWithoutFirmwareUsesUEFI(t *testing.T) {
	isolateConfig(t)
	service := NewService(nil, nil)
	var got types.CreateOpts
	service.QEMUCreate = func(opts types.CreateOpts) error {
		got = opts
		return nil
	}
	if err := service.Execute("guest", Options{Arch: "arm64"}); err != nil {
		t.Fatal(err)
	}
	if !got.UEFI {
		t.Fatalf("arm64 guest without --firmware reached QEMU without UEFI: %+v", got)
	}
}

func TestCreateLoopbackOptionsValidation(t *testing.T) {
	r := request{Options: Options{Loopback: true}}
	for _, backend := range []string{"qemu", "kubevirt", "incus", "libvirt", "proxmox", "bootc", "unknown-backend"} {
		t.Run(backend, func(t *testing.T) {
			err := r.validateGuestOptions(backend)
			if backend == "qemu" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "--loopback is supported only on local QEMU create") {
				t.Fatalf("backend %s accepted --loopback: %v", backend, err)
			}
		})
	}
}

func TestCreateCarriesGuestArchitectureAndLoopbackToQEMU(t *testing.T) {
	isolateConfig(t)
	service := NewService(nil, nil)
	called := false
	service.QEMUCreate = func(opts types.CreateOpts) error {
		called = true
		if opts.Arch != types.GuestArch("amd64") || !opts.Loopback || !opts.UEFI {
			t.Fatalf("guest selection changed at QEMU boundary: %+v", opts)
		}
		return nil
	}
	if err := service.Execute("guest", Options{Arch: "amd64", Loopback: true, Firmware: "uefi"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("QEMU create was not reached")
	}
}
