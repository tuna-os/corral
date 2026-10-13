package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/qemu"
)

// reuseEnv isolates qemu state for reuse tests. Every fixture writes a
// matching unit file, and the session probe is pinned off, so no
// expectation depends on the test host's systemd session (the live
// session dimensions are pinned in qemu's validate_test).
func reuseEnv(t *testing.T) (home, units string) {
	t.Helper()
	home = t.TempDir()
	units = t.TempDir()
	qemu.SetStateDirs(home, units)
	qemu.SetUserSystemd(func() bool { return false })
	t.Cleanup(func() { qemu.SetStateDirs("", ""); qemu.SetUserSystemd(nil) })
	return home, units
}

// reuseVM writes a complete coherent VM: metadata, command record and unit
// file that all agree. meta/argv come from reuseMeta/reuseArgv (host-native
// binding); binary is a runnable file named for the guest arch; code is a
// non-empty firmware file. Tests mutate one dimension and expect refusal.
type reuseVM struct {
	home   string
	units  string
	name   string
	meta   map[string]any
	argv   []string
	binary string
}

func reuseGuestArch() string { return runtime.GOARCH }

func reuseMeta(im ManifestImage) map[string]any {
	arch := reuseGuestArch()
	machine := "q35"
	if arch == "arm64" {
		machine = "virt"
	}
	accel := "kvm"
	if runtime.GOOS == "darwin" {
		accel = "hvf"
	}
	return map[string]any{
		"name":          "tuna-v1",
		"cpu":           2,
		"memory":        "4G",
		"guest_arch":    arch,
		"machine":       machine,
		"accel":         accel,
		"cpu_model":     "host",
		"emulated":      false,
		"firmware":      im.Firmware,
		"source_sha256": im.SHA256,
		"source_size":   im.Size,
		"source_format": im.EffectiveFormat(),
		"loopback":      true,
		"tailscale_ip":  "127.0.0.1",
		"vnc_port":      5901,
		"vnc_display":   1,
		"ssh_port":      2201,
		"iso":           "",
		"has_iso":       false,
	}
}

func reuseArgv(home, name, code string) []string {
	arch := reuseGuestArch()
	machine := "q35"
	if arch == "arm64" {
		machine = "virt"
	}
	accel := "kvm"
	if runtime.GOOS == "darwin" {
		accel = "hvf"
	}
	dir := filepath.Join(home, name)
	disk := filepath.Join(dir, "disk.qcow2")
	return []string{
		"-name", name,
		"-m", "4G",
		"-cpu", "host", "-smp", "2", "-machine", machine + ",accel=" + accel,
		"-drive", "file=" + disk + ",if=virtio,format=qcow2",
		"-vnc", "127.0.0.1:1",
		"-vga", "virtio",
		"-display", "none",
		"-drive", "if=pflash,format=raw,readonly=on,file=" + code,
		"-drive", "if=pflash,format=raw,file=" + filepath.Join(dir, "vars.fd"),
		"-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:2201-:22",
		"-device", "virtio-net-pci,netdev=net0",
		"-device", "virtio-rng-pci",
		"-qmp", "unix:" + filepath.Join(dir, "qmp.sock") + ",server,nowait",
	}
}

func writeReuseVM(t *testing.T, home, units, name string, meta map[string]any, argv []string, binary string) {
	t.Helper()
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if argv != nil {
		cmd, err := json.Marshal(map[string]any{"binary": binary, "args": argv})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "command.json"), cmd, 0o644); err != nil {
			t.Fatal(err)
		}
		unit := fmt.Sprintf("[Service]\nExecStart=%s %s\n", binary, strings.Join(argv, " "))
		if err := os.WriteFile(filepath.Join(units, "corral-"+name+".service"), []byte(unit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// validReuseVM crafts a fully coherent existing VM (metadata + argv + unit)
// for the test manifest image. The image arch follows the test host so the
// binding validates on any host.
func validReuseVM(t *testing.T, home, units, name string) (ManifestImage, *reuseVM) {
	t.Helper()
	im := testImage()
	im.Arch = reuseGuestArch()
	emulator := "qemu-system-x86_64"
	if reuseGuestArch() == "arm64" {
		emulator = "qemu-system-aarch64"
	}
	binary := filepath.Join(t.TempDir(), emulator)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code := filepath.Join(t.TempDir(), "code.fd")
	if err := os.WriteFile(code, []byte("firmware"), 0o644); err != nil {
		t.Fatal(err)
	}
	codeSum := sha256.Sum256([]byte("firmware"))
	meta := reuseMeta(im)
	// The authoritative admitted firmware identity every uefi fixture
	// carries (see qemu/validate.go): path plus bytes digest.
	meta["firmware_code"] = code
	meta["firmware_code_sha256"] = hex.EncodeToString(codeSum[:])
	vm := &reuseVM{home: home, units: units, name: name, meta: meta, argv: reuseArgv(home, name, code), binary: binary}
	writeReuseVM(t, home, units, name, vm.meta, vm.argv, vm.binary)
	return im, vm
}

func (vm *reuseVM) rewrite(t *testing.T) {
	t.Helper()
	writeReuseVM(t, vm.home, vm.units, vm.name, vm.meta, vm.argv, vm.binary)
}

func TestCheckExistingVM_MatchReuses(t *testing.T) {
	home, units := reuseEnv(t)
	im, _ := validReuseVM(t, home, units, "tuna-v1")
	if err := checkExistingVM("tuna-v1", im, LaunchOpts{}); err != nil {
		t.Errorf("exact match must reuse: %v", err)
	}
}

func TestCheckExistingVM_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, im *ManifestImage, vm *reuseVM)
		opts   LaunchOpts
		want   string
	}{
		{name: "arch mismatch", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			if im.Arch == "arm64" {
				vm.meta["guest_arch"] = "amd64"
			} else {
				vm.meta["guest_arch"] = "arm64"
			}
		}, want: "but the manifest wants"},
		{name: "firmware mismatch", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.meta["firmware"] = "bios"
			im.Firmware = "uefi"
		}, want: "but the manifest wants"},
		{name: "legacy pre-arch", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			delete(vm.meta, "guest_arch")
		}, want: "predates guest-arch"},
		{name: "legacy pre-provenance", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			delete(vm.meta, "source_sha256")
		}, want: "predates creation-source"},
		{name: "different digest", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.meta["source_sha256"] = strings.Repeat("2", 64)
		}, want: "different image"},
		{name: "different size", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.meta["source_size"] = int64(999)
		}, want: "different image"},
		{name: "different format", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.meta["source_format"] = "raw"
		}, want: "format"},
		{name: "non-loopback flag", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.meta["loopback"] = false
		}, want: "not loopback-bound"},
		{name: "loopback flag with tailscale bind", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.meta["tailscale_ip"] = "100.64.0.1"
		}, want: "not loopback-bound"},
		{name: "emulated without opt-in", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.meta["emulated"] = true
			vm.meta["accel"] = "tcg"
			vm.meta["cpu_model"] = "max"
			for i, a := range vm.argv {
				if a == "-machine" {
					vm.argv[i+1] = strings.Split(vm.argv[i+1], ",")[0] + ",accel=tcg"
				}
				if a == "-cpu" {
					vm.argv[i+1] = "max"
				}
			}
		}, want: "emulated"},
		{name: "cross-arch emulated with only same-arch flag", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			other := "arm64"
			otherMachine := "virt"
			if reuseGuestArch() == "arm64" {
				other = "amd64"
				otherMachine = "q35"
			}
			im.Arch = other
			vm.meta["guest_arch"] = other
			vm.meta["machine"] = otherMachine
			vm.meta["accel"] = "tcg"
			vm.meta["cpu_model"] = "max"
			vm.meta["emulated"] = true
			for i, a := range vm.argv {
				if a == "-machine" {
					vm.argv[i+1] = otherMachine + ",accel=tcg"
				}
				if a == "-cpu" {
					vm.argv[i+1] = "max"
				}
			}
			otherEmu := "qemu-system-aarch64"
			if other == "amd64" {
				otherEmu = "qemu-system-x86_64"
			}
			binary := filepath.Join(t.TempDir(), otherEmu)
			if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
				t.Fatal(err)
			}
			vm.binary = binary
		}, opts: LaunchOpts{AllowEmulation: true}, want: "--allow-cross-arch"},
		{name: "same-arch emulated with only cross flag", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.meta["emulated"] = true
			vm.meta["accel"] = "tcg"
			vm.meta["cpu_model"] = "max"
			for i, a := range vm.argv {
				if a == "-machine" {
					vm.argv[i+1] = strings.Split(vm.argv[i+1], ",")[0] + ",accel=tcg"
				}
				if a == "-cpu" {
					vm.argv[i+1] = "max"
				}
			}
		}, opts: LaunchOpts{AllowCrossArch: true}, want: "--allow-emulation"},
		{name: "missing command record", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.argv = nil
		}, want: "force"},
		{name: "vanished emulator", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.binary = "/nonexistent/qemu-system-x86_64"
		}, want: "force"},
		{name: "empty argv", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.argv = []string{}
		}, want: "force"},
		{name: "argv vnc off loopback", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			for i, a := range vm.argv {
				if a == "-vnc" {
					vm.argv[i+1] = "0.0.0.0:1"
				}
			}
		}, want: "force"},
		{name: "argv wrong disk", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			for i, a := range vm.argv {
				if a == "-drive" && strings.Contains(vm.argv[i+1], "disk.qcow2") {
					vm.argv[i+1] = "file=/tmp/other.qcow2,if=virtio,format=qcow2"
				}
			}
		}, want: "force"},
		{name: "argv denied flag", mutate: func(t *testing.T, im *ManifestImage, vm *reuseVM) {
			vm.argv = append(vm.argv, "-kernel", "/tmp/evil")
		}, want: "force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, units := reuseEnv(t)
			im, vm := validReuseVM(t, home, units, "tuna-v1")
			tc.mutate(t, &im, vm)
			vm.rewrite(t)
			if tc.name == "missing command record" {
				// rewrite skips the command record for nil argv; the unit
				// written alongside must go too (a record-less VM has no
				// coherent unit either).
				_ = os.Remove(filepath.Join(units, "corral-tuna-v1.service"))
			}
			err := checkExistingVM("tuna-v1", im, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want refusal containing %q", err, tc.want)
			}
		})
	}
}

func TestCheckExistingVM_EmulatedWithOptInReuses(t *testing.T) {
	home, units := reuseEnv(t)
	im, vm := validReuseVM(t, home, units, "tuna-v1")
	vm.meta["emulated"] = true
	vm.meta["accel"] = "tcg"
	vm.meta["cpu_model"] = "max"
	for i, a := range vm.argv {
		if a == "-machine" {
			vm.argv[i+1] = strings.Split(vm.argv[i+1], ",")[0] + ",accel=tcg"
		}
		if a == "-cpu" {
			vm.argv[i+1] = "max"
		}
	}
	vm.rewrite(t)
	if err := checkExistingVM("tuna-v1", im, LaunchOpts{AllowEmulation: true}); err != nil {
		t.Errorf("emulated VM with opt-in must reuse: %v", err)
	}
}

func TestCheckExistingVM_CrossArchWithOptInReuses(t *testing.T) {
	home, units := reuseEnv(t)
	im, vm := validReuseVM(t, home, units, "tuna-v1")
	other := "arm64"
	otherMachine := "virt"
	if reuseGuestArch() == "arm64" {
		other = "amd64"
		otherMachine = "q35"
	}
	im.Arch = other
	vm.meta["guest_arch"] = other
	vm.meta["machine"] = otherMachine
	vm.meta["accel"] = "tcg"
	vm.meta["cpu_model"] = "max"
	vm.meta["emulated"] = true
	for i, a := range vm.argv {
		if a == "-machine" {
			vm.argv[i+1] = otherMachine + ",accel=tcg"
		}
		if a == "-cpu" {
			vm.argv[i+1] = "max"
		}
	}
	otherEmu := "qemu-system-aarch64"
	if other == "amd64" {
		otherEmu = "qemu-system-x86_64"
	}
	binary := filepath.Join(t.TempDir(), otherEmu)
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	vm.binary = binary
	vm.rewrite(t)
	if err := checkExistingVM("tuna-v1", im, LaunchOpts{AllowCrossArch: true}); err != nil {
		t.Errorf("cross-arch VM with opt-in must reuse: %v", err)
	}
}

func TestCheckExistingVM_DivergentUnitRefuses(t *testing.T) {
	home, units := reuseEnv(t)
	im, _ := validReuseVM(t, home, units, "tuna-v1")
	// The unit executes a different command than recorded: reuse refuses
	// even though metadata and command.json agree.
	other := "[Service]\nExecStart=/usr/bin/qemu-system-x86_64 -name tuna-v1 -m 8G\n"
	if err := os.WriteFile(filepath.Join(units, "corral-tuna-v1.service"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkExistingVM("tuna-v1", im, LaunchOpts{}); err == nil ||
		!strings.Contains(err.Error(), "different command") {
		t.Errorf("divergent unit must refuse, got %v", err)
	}
}

func TestCheckExistingVM_InvalidMetadata(t *testing.T) {
	home, _ := reuseEnv(t)
	dir := filepath.Join(home, "tuna-v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkExistingVM("tuna-v1", testImage(), LaunchOpts{}); err == nil {
		t.Error("invalid metadata must be refused")
	}
}

func TestEnsureVM_RefusesUnsafeName(t *testing.T) {
	im := testImage()
	if _, err := EnsureVM(LaunchOpts{Name: "../escape"}, "/img", im); err == nil {
		t.Error("traversal name must be refused before any state access")
	}
	if _, err := EnsureVM(LaunchOpts{}, "/img", im); err == nil {
		t.Error("empty name must be refused")
	}
}
