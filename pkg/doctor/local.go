package doctor

import (
	"fmt"
	"os"
	"strings"
)

// localChecks covers the QEMU backend and client-side tooling — everything
// that matters on this machine regardless of any cluster.
func localChecks() []Check {
	var checks []Check

	_, qemuErr := runner.LookPath("qemu-system-x86_64")
	checks = append(checks, Check{
		Name:   "QEMU (local backend)",
		OK:     qemuErr == nil,
		Detail: detailIf(qemuErr == nil, "qemu-system-x86_64 found", "not installed — needed only for local VMs (dnf/apt install qemu-system-x86)"),
	})

	kvm := statDevKVM()
	checks = append(checks, Check{
		Name:   "KVM acceleration",
		OK:     kvm == nil,
		Detail: detailIf(kvm == nil, "/dev/kvm accessible", "no /dev/kvm access — local VMs fall back to slow emulation (add your user to the kvm group, or enable virtualization in BIOS)"),
	})

	// Nested virtualization (#68): only meaningful where KVM works at all,
	// and only a warning — it matters when this host's VMs will themselves
	// run VMs (e.g. a laptop hosting a KubeVirt dev cluster).
	if kvm == nil {
		nested, known := nestedVirtEnabled()
		if known {
			checks = append(checks, Check{
				Name:   "Nested virtualization",
				OK:     nested,
				Detail: detailIf(nested, "kvm module has nested=1 — VMs can host VMs", "disabled — enable with: modprobe -r kvm_intel && modprobe kvm_intel nested=1 (or kvm_amd); only needed if VMs will run VMs"),
			})
		}
	}

	_, tsErr := runner.LookPath("tailscale")
	checks = append(checks, Check{
		Name:   "Tailscale CLI",
		OK:     tsErr == nil,
		Detail: detailIf(tsErr == nil, "found — VMs can join the tailnet", "not installed (optional) — VMs won't auto-join the tailnet"),
	})

	_, vcErr := runner.LookPath("virtctl")
	checks = append(checks, Check{
		Name:   "virtctl CLI",
		OK:     vcErr == nil,
		Detail: detailIf(vcErr == nil, "found — needed for KubeVirt consoles/SSH", "not installed — needed only for the KubeVirt backend (brew install virtctl)"),
	})

	// VSOCK: host device + socat + ssh-keygen, like tuna-os/tunaos iso-e2e setup_vsock
	vsockOK, vsockReason := vsockHostCheck()
	checks = append(checks, Check{
		Name:   "VSOCK host support",
		OK:     vsockOK,
		Detail: detailIf(vsockOK, "AF_VSOCK available (vhost_vsock + vsock-capable socat + ssh-keygen) — corral --vsock fallback works", vsockReason+" — corral --vsock and tunaOS live-ISO fallback will not work"),
	})
	// TPM: swtpm for LUKS/measured-boot testing (tuna-os/tunaos luks-e2e)
	_, tpmErr := runner.LookPath("swtpm")
	checks = append(checks, Check{
		Name:   "swtpm (TPM emulation)",
		OK:     tpmErr == nil,
		Detail: detailIf(tpmErr == nil, "found — corral --tpm for LUKS/measured-boot testing", "not installed (optional) — needed only for corral --tpm / luks-e2e (dnf/apt install swtpm)"),
	})
	// OVMF firmware for UEFI boot (tuna-os/tunaos ISO live boot)
	if ovmfAvailable() {
		checks = append(checks, Check{Name: "OVMF firmware", OK: true, Detail: "UEFI firmware found — corral --firmware uefi / tunaOS ISO boot works"})
	} else {
		checks = append(checks, Check{Name: "OVMF firmware", OK: false, Detail: "UEFI firmware not found (install edk2-ovmf/ovmf) — UEFI guests will not boot"})
	}
	// socat for QMP/VNC bridges (tuna-os/tunaos iso-e2e screenshot path)
	if _, err := runner.LookPath("socat"); err == nil {
		checks = append(checks, Check{Name: "socat", OK: true, Detail: "found — needed for VNC bridge and vsock ProxyCommand"})
	} else {
		checks = append(checks, Check{Name: "socat", OK: false, Detail: "not installed — corral screenshot via VNC + corral --vsock SSH need it (dnf/apt install socat)"})
	}
	// qemu-img for disk handling
	if _, err := runner.LookPath("qemu-img"); err == nil {
		checks = append(checks, Check{Name: "qemu-img", OK: true, Detail: "found — disk create/resize for local VMs"})
	} else {
		checks = append(checks, Check{Name: "qemu-img", OK: false, Detail: "not installed — local VM disk creation needs it (dnf/apt install qemu-img or qemu-utils)"})
	}

	return checks
}

var vsockHostCheck = func() (bool, string) {
	if _, err := os.Stat("/dev/vhost-vsock"); err != nil {
		return false, "no /dev/vhost-vsock (modprobe vhost_vsock)"
	}
	if f, err := os.OpenFile("/dev/vhost-vsock", os.O_WRONLY, 0); err != nil {
		return false, "no writable /dev/vhost-vsock"
	} else {
		_ = f.Close()
	}
	if _, err := runner.LookPath("socat"); err != nil {
		return false, "socat not found"
	}
	// Check vsock in socat -V or -h
	if out, err := runner.Run("socat", "-V"); err == nil && containsCI(string(out), "vsock") {
		// ok
	} else if out2, err2 := runner.Run("socat", "-h"); err2 == nil && containsCI(string(out2), "vsock") {
		// ok
	} else {
		return false, "socat has no VSOCK support"
	}
	if _, err := runner.LookPath("ssh-keygen"); err != nil {
		return false, "ssh-keygen not found"
	}
	return true, ""
}

var ovmfAvailable = func() bool {
	_, _, err := findOVMF()
	return err == nil
}

func containsCI(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		lower := strings.ToLower(s)
		return len(lower) >= len(sub) && strings.Contains(lower, strings.ToLower(sub))
	})()
}

func findOVMF() (string, string, error) {
	// Reuse qemu's firmware discovery by probing env.
	if code := os.Getenv("CORRAL_OVMF_CODE"); code != "" {
		if _, err := os.Stat(code); err == nil {
			return code, os.Getenv("CORRAL_OVMF_VARS"), nil
		}
	}
	candidates := []struct{ code, vars string }{
		{"/usr/share/qemu/edk2-x86_64-code.fd", "/usr/share/qemu/edk2-i386-vars.fd"},
		{"/usr/share/edk2/ovmf/OVMF_CODE.fd", "/usr/share/edk2/ovmf/OVMF_VARS.fd"},
		{"/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd"},
		{"/usr/share/OVMF/OVMF_CODE.fd", "/usr/share/OVMF/OVMF_VARS.fd"},
		{"/usr/share/edk2-ovmf/x64/OVMF_CODE.fd", "/usr/share/edk2-ovmf/x64/OVMF_VARS.fd"},
		{"/usr/share/qemu/ovmf-x86_64-code.bin", "/usr/share/qemu/ovmf-x86_64-vars.bin"},
		{"/home/linuxbrew/.linuxbrew/share/qemu/edk2-x86_64-code.fd", "/home/linuxbrew/.linuxbrew/share/qemu/edk2-i386-vars.fd"},
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.code); err == nil {
			if _, err := os.Stat(c.vars); err == nil {
				return c.code, c.vars, nil
			}
		}
	}
	return "", "", fmt.Errorf("not found")
}

// nestedVirtEnabled reads the kvm_intel/kvm_amd module's nested parameter.
// known=false when neither module directory exists (no KVM, or exotic arch).
// A seam for tests.
var nestedVirtEnabled = func() (nested, known bool) {
	for _, mod := range []string{"kvm_intel", "kvm_amd"} {
		b, err := os.ReadFile("/sys/module/" + mod + "/parameters/nested")
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		return v == "1" || v == "Y" || v == "y", true
	}
	return false, false
}

// statDevKVM is a seam for tests; returns nil when /dev/kvm is usable.
var statDevKVM = func() error {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	_ = f.Close()
	return nil
}
