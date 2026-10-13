package qemu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// vfix is a complete coherent VM for command validation: metadata,
// command.json and unit file that all agree. Tests mutate one dimension
// and expect refusal naming it.
type vfix struct {
	t       *testing.T
	dir     string
	unitDir string
	meta    map[string]any
	args    []string
	binary  string
	code    string
	noUnit  bool
}

func hostNativeArch() GuestArch {
	if runtime.GOARCH == "arm64" {
		return GuestARM64
	}
	return GuestAMD64
}

func hostNativeAccel() string {
	if runtime.GOOS == "darwin" {
		return "hvf"
	}
	return "kvm"
}

func newVFix(t *testing.T) *vfix {
	t.Helper()
	home := t.TempDir()
	unitDir := t.TempDir()
	SetStateDirs(home, unitDir)
	SetUserSystemd(func() bool { return false })
	t.Cleanup(func() { SetStateDirs("", ""); SetUserSystemd(nil) })
	dir := filepath.Join(home, "gate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	arch := hostNativeArch()
	accel := hostNativeAccel()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, arch.Emulator())
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code := filepath.Join(t.TempDir(), "code.fd")
	if err := os.WriteFile(code, []byte("firmware"), 0o644); err != nil {
		t.Fatal(err)
	}
	codeSum := sha256.Sum256([]byte("firmware"))
	disk := filepath.Join(dir, "disk.qcow2")
	f := &vfix{
		t: t, dir: dir, unitDir: unitDir, binary: binary, code: code,
		meta: map[string]any{
			"name": "gate", "cpu": 2, "memory": "4G",
			"vnc_port": 5901, "vnc_display": 1, "ssh_port": 2201,
			"tailscale_ip": "127.0.0.1",
			"firmware":     "uefi",
			// The authoritative admitted firmware identity every uefi
			// fixture carries (see validate.go): path plus bytes digest.
			"firmware_code":        code,
			"firmware_code_sha256": hex.EncodeToString(codeSum[:]),
			"guest_arch":           string(arch),
			"machine":              arch.Machine(),
			"accel":                accel,
			"cpu_model":            "host",
			"emulated":             false,
			"loopback":             true,
			"iso":                  "", "has_iso": false,
		},
		args: []string{
			"-name", "gate",
			"-m", "4G",
			"-cpu", "host", "-smp", "2", "-machine", arch.Machine() + ",accel=" + accel,
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
			"-chardev", "file,id=serial0,path=" + filepath.Join(dir, "serial.log") + ",append=on",
			"-serial", "chardev:serial0",
		},
	}
	f.rewrite()
	return f
}

func (f *vfix) rewrite() {
	f.t.Helper()
	metaData, err := json.Marshal(f.meta)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "metadata.json"), metaData, 0o644); err != nil {
		f.t.Fatal(err)
	}
	cmdData, err := json.Marshal(map[string]any{"binary": f.binary, "args": f.args})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "command.json"), cmdData, 0o644); err != nil {
		f.t.Fatal(err)
	}
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	if f.noUnit {
		_ = os.Remove(unitPath)
		return
	}
	unit := fmt.Sprintf("[Service]\nExecStart=%s %s\n", f.binary, strings.Join(f.args, " "))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// setFlag replaces a flag's value in the recorded argv.
func (f *vfix) setFlag(flag, value string) {
	f.t.Helper()
	for i := 0; i+1 < len(f.args); i++ {
		if f.args[i] == flag {
			f.args[i+1] = value
			return
		}
	}
	f.t.Fatalf("flag %s not in argv", flag)
}

func TestValidateCommand_Valid(t *testing.T) {
	f := newVFix(t)
	if err := ValidateCommand("gate"); err != nil {
		t.Errorf("coherent recorded command must validate: %v", err)
	}
	_ = f
}

func TestValidateCommand_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(f *vfix)
		want   string
	}{
		{name: "empty argv", mutate: func(f *vfix) { f.args = nil }, want: "empty QEMU argv"},
		{name: "missing binary", mutate: func(f *vfix) { f.binary = "/nonexistent/qemu" }, want: "not present"},
		{name: "binary is directory", mutate: func(f *vfix) { f.binary = f.dir }, want: "directory"},
		{name: "binary not executable", mutate: func(f *vfix) {
			p := filepath.Join(f.dir, "qemu-system-x86_64")
			if runtime.GOARCH == "arm64" {
				p = filepath.Join(f.dir, "qemu-system-aarch64")
			}
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				f.t.Fatal(err)
			}
			f.binary = p
		}, want: "not executable"},
		{name: "name mismatch", mutate: func(f *vfix) { f.setFlag("-name", "other") }, want: "name"},
		{name: "memory mismatch", mutate: func(f *vfix) { f.setFlag("-m", "8G") }, want: "memory"},
		{name: "smp mismatch", mutate: func(f *vfix) { f.setFlag("-smp", "4") }, want: "smp"},
		{name: "machine mismatch", mutate: func(f *vfix) { f.setFlag("-machine", "pc,accel="+hostNativeAccel()) }, want: "machine"},
		{name: "accel mismatch", mutate: func(f *vfix) {
			f.setFlag("-machine", hostNativeArch().Machine()+",accel=tcg")
		}, want: "accel="},
		{name: "cpu mismatch", mutate: func(f *vfix) { f.setFlag("-cpu", "max") }, want: "cpu"},
		{name: "emulator mismatch", mutate: func(f *vfix) {
			other := "qemu-system-aarch64"
			if runtime.GOARCH == "arm64" {
				other = "qemu-system-x86_64"
			}
			p := filepath.Join(filepath.Dir(f.binary), other)
			if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
				f.t.Fatal(err)
			}
			f.binary = p
		}, want: "emulator"},
		{name: "disk other path", mutate: func(f *vfix) {
			f.setFlag("-drive", "file=/tmp/evil.qcow2,if=virtio,format=qcow2")
		}, want: "disk"},
		{name: "disk format raw", mutate: func(f *vfix) {
			f.setFlag("-drive", "file="+filepath.Join(f.dir, "disk.qcow2")+",if=virtio,format=raw")
		}, want: "disk"},
		{name: "extra drive", mutate: func(f *vfix) {
			f.args = append(f.args, "-drive", "file=/tmp/evil.qcow2,if=virtio,format=qcow2")
		}, want: "extra drive"},
		{name: "vnc non-loopback bind", mutate: func(f *vfix) { f.setFlag("-vnc", "0.0.0.0:1") }, want: "vnc"},
		{name: "vnc display mismatch", mutate: func(f *vfix) { f.setFlag("-vnc", "127.0.0.1:2") }, want: "vnc"},
		{name: "second vnc", mutate: func(f *vfix) {
			f.args = append(f.args, "-vnc", "127.0.0.1:3")
		}, want: "vnc"},
		{name: "ssh port mismatch", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:2299-:22")
		}, want: "ssh forward"},
		{name: "ssh bind mismatch", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0,hostfwd=tcp:0.0.0.0:2201-:22")
		}, want: "ssh forward"},
		{name: "netdev without hostfwd", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0")
		}, want: "ssh forward"},
		{name: "second netdev", mutate: func(f *vfix) {
			f.args = append(f.args, "-netdev", "user,id=net1,hostfwd=tcp:127.0.0.1:2299-:22")
		}, want: "ssh forward"},
		{name: "second qmp", mutate: func(f *vfix) {
			f.args = append(f.args, "-qmp", "unix:/tmp/evil.sock,server,nowait")
		}, want: "-qmp"},
		{name: "uefi missing code drive", mutate: func(f *vfix) {
			out := f.args[:0]
			for i := 0; i < len(f.args); i++ {
				if f.args[i] == "-drive" && i+1 < len(f.args) && strings.Contains(f.args[i+1], "readonly=on") {
					i++
					continue
				}
				out = append(out, f.args[i])
			}
			f.args = out
		}, want: "pflash code"},
		{name: "uefi code file missing", mutate: func(f *vfix) {
			// Replace the pflash CODE drive (the -drive carrying
			// readonly=on), not the first -drive (the disk).
			for i := 0; i+1 < len(f.args); i++ {
				if f.args[i] == "-drive" && strings.Contains(f.args[i+1], "readonly=on") {
					f.args[i+1] = "if=pflash,format=raw,readonly=on,file=/nonexistent/code.fd"
					return
				}
			}
			f.t.Fatal("pflash code drive not in argv")
		}, want: "uefi code"},
		{name: "pflash on bios", mutate: func(f *vfix) { f.meta["firmware"] = "bios" }, want: "pflash drives on a bios"},
		{name: "kernel denied", mutate: func(f *vfix) {
			f.args = append(f.args, "-kernel", "/tmp/evil-kernel")
		}, want: "denied QEMU flag -kernel"},
		{name: "incoming denied", mutate: func(f *vfix) {
			f.args = append(f.args, "-incoming", "tcp:0:4444")
		}, want: "denied QEMU flag -incoming"},
		{name: "snapshot denied", mutate: func(f *vfix) {
			f.args = append(f.args, "-snapshot")
		}, want: "denied QEMU flag -snapshot"},
		{name: "unadmitted cdrom", mutate: func(f *vfix) {
			f.args = append(f.args, "-cdrom", "/tmp/evil.iso")
		}, want: "unadmitted cdrom"},
		{name: "iso mismatch", mutate: func(f *vfix) {
			f.meta["has_iso"] = true
			f.meta["iso"] = "/recorded/boot.iso"
			f.args = append(f.args, "-cdrom", "/tmp/other.iso")
		}, want: "cdrom"},
		{name: "meta machine incoherent", mutate: func(f *vfix) {
			if runtime.GOARCH == "arm64" {
				f.meta["machine"] = "q35"
			} else {
				f.meta["machine"] = "virt"
			}
		}, want: "records machine"},
		{name: "meta emulated incoherent", mutate: func(f *vfix) { f.meta["emulated"] = true }, want: "incoherent"},
		{name: "meta accel unknown", mutate: func(f *vfix) { f.meta["accel"] = "xen" }, want: "neither this host's native"},
		{name: "meta ports incoherent", mutate: func(f *vfix) { f.meta["vnc_port"] = 5902 }, want: "incoherent ports"},
		{name: "meta machine missing", mutate: func(f *vfix) { delete(f.meta, "machine") }, want: "incomplete machine binding"},
		{name: "cross accel kvm refused", mutate: func(f *vfix) {
			if runtime.GOARCH == "arm64" {
				f.meta["guest_arch"] = "amd64"
				f.meta["machine"] = "q35"
			} else {
				f.meta["guest_arch"] = "arm64"
				f.meta["machine"] = "virt"
			}
		}, want: "non-TCG accelerator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVFix(t)
			tc.mutate(f)
			f.rewrite()
			err := ValidateCommand("gate")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want refusal containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateCommand_UnitFile(t *testing.T) {
	// A unit executing a different command refuses.
	f := newVFix(t)
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	other := fmt.Sprintf("[Service]\nExecStart=%s -name gate -m 8G\n", f.binary)
	if err := os.WriteFile(unitPath, []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "different command") {
		t.Errorf("divergent unit must refuse, got %v", err)
	}
	// A unit without ExecStart refuses.
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "no ExecStart") {
		t.Errorf("unit without ExecStart must refuse, got %v", err)
	}
	// A missing unit allows on a direct-only host (command.json governs)
	// and refuses where systemd would run it.
	f.noUnit = true
	f.rewrite()
	if err := ValidateCommand("gate"); err != nil {
		t.Errorf("missing unit on direct host must allow, got %v", err)
	}
	SetUserSystemd(func() bool { return true })
	t.Cleanup(func() { SetUserSystemd(nil) })
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "no systemd unit") {
		t.Errorf("missing unit on systemd host must refuse, got %v", err)
	}
}

// The argv allowlist is complete: anything the generator never emits is
// refused, and admitted shapes are exact. Each case below passed the old
// count-only checks — the refusal is the fix.
func TestValidateCommand_TCPQMPRefused(t *testing.T) {
	f := newVFix(t)
	f.setFlag("-qmp", "tcp:127.0.0.1:4444,server,nowait")
	f.rewrite()
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "-qmp") {
		t.Errorf("TCP QMP must refuse naming -qmp, got %v", err)
	}
}

func TestValidateCommand_SpiceRefused(t *testing.T) {
	f := newVFix(t)
	f.args = append(f.args, "-spice", "port=5900,disable-ticketing")
	f.rewrite()
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "unrecognized") {
		t.Errorf("extra -spice must refuse as unrecognized, got %v", err)
	}
}

func TestValidateCommand_ExtraChardevRefused(t *testing.T) {
	f := newVFix(t)
	f.args = append(f.args, "-chardev", "socket,id=evil,path=/tmp/evil.sock")
	f.rewrite()
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "-chardev") {
		t.Errorf("extra -chardev must refuse naming -chardev, got %v", err)
	}
}

func TestValidateCommand_BlockdevRefused(t *testing.T) {
	f := newVFix(t)
	f.args = append(f.args, "-blockdev", "node-name=evil,driver=file,filename=/tmp/evil")
	f.rewrite()
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "unrecognized") {
		t.Errorf("extra -blockdev must refuse as unrecognized, got %v", err)
	}
}

func TestValidateCommand_DuplicatePflashCodeRefused(t *testing.T) {
	f := newVFix(t)
	f.args = append(f.args, "-drive", "if=pflash,format=raw,readonly=on,file="+f.code)
	f.rewrite()
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "pflash code") {
		t.Errorf("duplicate pflash code drive must refuse, got %v", err)
	}
}

func TestValidateCommand_ReplacedFirmwareBytesRefused(t *testing.T) {
	f := newVFix(t)
	f.rewrite()
	// Same admitted path, different bytes: the digest no longer matches.
	if err := os.WriteFile(f.code, []byte("trojan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "admitted firmware digest") {
		t.Errorf("replaced firmware bytes must refuse naming the digest, got %v", err)
	}
}

func TestValidateCommand_SwappedFirmwarePathRefused(t *testing.T) {
	f := newVFix(t)
	other := filepath.Join(t.TempDir(), "other-code.fd")
	if err := os.WriteFile(other, []byte("firmware"), 0o644); err != nil {
		t.Fatal(err)
	}
	swapped := false
	for i, a := range f.args {
		if strings.Contains(a, "readonly=on") {
			f.args[i] = "if=pflash,format=raw,readonly=on,file=" + other
			swapped = true
		}
	}
	if !swapped {
		t.Fatal("fixture has no pflash code drive to swap")
	}
	f.rewrite()
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "not the admitted firmware") {
		t.Errorf("swapped firmware path must refuse naming the admitted firmware, got %v", err)
	}
}

// The loaded unit is proven to be exactly the admitted file: the manager
// refreshes its load of the known-good file, then the fragment it loads
// plus no drop-ins is verified. An on-disk match alone never suffices on
// a systemd host.

// loadedUnitFixture stubs a live session whose manager answers show with
// the given fragment/drop-ins and reload as configured.
func loadedUnitFixture(t *testing.T, fragment, dropins string, reloadErr error) {
	t.Helper()
	SetUserSystemd(func() bool { return true })
	t.Cleanup(func() { SetUserSystemd(nil) })
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "daemon-reload":
			if reloadErr != nil {
				return []byte("boom\n"), reloadErr
			}
			return []byte{}, nil
		case "show":
			return []byte("FragmentPath=" + fragment + "\nDropInPaths=" + dropins + "\n"), nil
		default:
			return nil, fmt.Errorf("unexpected systemctl %v", args)
		}
	})
}

func TestValidateCommand_LoadedDropinsRefused(t *testing.T) {
	f := newVFix(t)
	f.rewrite()
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	loadedUnitFixture(t, unitPath, "/etc/systemd/user/corral-gate.service.d/override.conf", nil)
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "drop-ins") {
		t.Errorf("loaded drop-ins must refuse naming them, got %v", err)
	}
}

func TestValidateCommand_LoadedFragmentMismatchRefused(t *testing.T) {
	f := newVFix(t)
	f.rewrite()
	loadedUnitFixture(t, "/run/systemd/transient/other.service", "", nil)
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "not the admitted unit") {
		t.Errorf("loaded fragment mismatch must refuse, got %v", err)
	}
}

func TestValidateCommand_LoadedUnitMatchAllows(t *testing.T) {
	f := newVFix(t)
	f.rewrite()
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	loadedUnitFixture(t, unitPath, "", nil)
	if err := ValidateCommand("gate"); err != nil {
		t.Errorf("loaded unit matching the admitted file must validate, got %v", err)
	}
}

func TestValidateCommand_ReloadFailureRefuses(t *testing.T) {
	f := newVFix(t)
	f.rewrite()
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	loadedUnitFixture(t, unitPath, "", fmt.Errorf("exit status 1"))
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "daemon-reload") {
		t.Errorf("reload failure must refuse naming the remedy, got %v", err)
	}
}

// An ISO VM carries exactly its recorded cdrom plus one -boot
// once=d,menu=on: the admitted ISO boot validates.
func TestValidateCommand_ISOBootAdmitted(t *testing.T) {
	f := newVFix(t)
	f.meta["has_iso"] = true
	f.meta["iso"] = "/recorded/boot.iso"
	f.args = append(f.args, "-cdrom", "/recorded/boot.iso", "-boot", "once=d,menu=on")
	f.rewrite()
	if err := ValidateCommand("gate"); err != nil {
		t.Errorf("admitted ISO boot must validate, got %v", err)
	}
}
