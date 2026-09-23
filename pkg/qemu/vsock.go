package qemu

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// VsockCID derives a stable guest CID from the VM name when VsockCID == 0.
// CIDs 0-2 are reserved; we map the hash into 3..65535 and then offset by
// 1000 similar to tuna-os/tunaos scripts/iso-e2e.sh's SSH_PORT+1000 scheme.
func VsockCID(name string, explicit uint32) uint32 {
	if explicit != 0 {
		if explicit < 3 {
			return 3
		}
		return explicit
	}
	// hashDisplay gives 0..99; spread into 3..65535 with stable offset.
	h := hashDisplay(name)
	cid := uint32(1000 + 10*h + 3)
	if cid < 3 {
		cid = 3
	}
	if cid > 0xFFFFFFFE {
		cid = 0xFFFFFFFE
	}
	return cid
}

// VsockKeyPath is where the per-VM vsock SSH keypair lives.
func VsockKeyPath(name string) string {
	return filepath.Join(VMHome(), name, "vsock-ssh-key")
}

// VsockHostAvailable reports whether the host can provide AF_VSOCK transport:
// writable /dev/vhost-vsock, vsock-capable socat, ssh-keygen present.
func VsockHostAvailable() (bool, string) {
	if _, err := os.Stat("/dev/vhost-vsock"); err != nil {
		// Try to load module like iso-e2e.sh does (best-effort); if not there, unavailable.
		return false, "no /dev/vhost-vsock — load vhost_vsock module"
	}
	// Writable check as iso-e2e does.
	if f, err := os.OpenFile("/dev/vhost-vsock", os.O_WRONLY, 0); err != nil {
		return false, "no writable /dev/vhost-vsock"
	} else {
		_ = f.Close()
	}
	if _, err := exec.LookPath("socat"); err != nil {
		return false, "socat not found"
	}
	if out, err := exec.Command("socat", "-V").CombinedOutput(); err != nil || !strings.Contains(strings.ToLower(string(out)), "vsock") {
		if err != nil {
			return false, "socat does not support vsock"
		}
		// Some socat builds print version without vsock string but still support it via help.
		if out2, _ := exec.Command("socat", "-h").CombinedOutput(); !strings.Contains(strings.ToLower(string(out2)), "vsock") {
			return false, "socat has no vsock support"
		}
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return false, "ssh-keygen not found"
	}
	return true, ""
}

// EnsureVsockKey generates a per-VM ed25519 keypair if missing.
func EnsureVsockKey(name string) (string, error) {
	keyPath := VsockKeyPath(name)
	if _, err := os.Stat(keyPath); err == nil {
		return keyPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return "", err
	}
	_ = os.Remove(keyPath)
	_ = os.Remove(keyPath + ".pub")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "corral-vsock-"+name, "-f", keyPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %s: %w", string(out), err)
	}
	return keyPath, nil
}

// VsockArgs returns QEMU args for vsock: -device vhost-vsock-pci plus SMBIOS
// credentials carrying the per-VM pubkey under both names iso-e2e.sh uses.
func VsockArgs(name string, cid uint32) ([]string, error) {
	keyPath := VsockKeyPath(name)
	pubData, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return nil, fmt.Errorf("reading vsock pubkey: %w", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pubData)
	// Strip newlines already; base64 alphabet has no commas so no QEMU escaping needed.
	pubB64 = strings.TrimSpace(pubB64)
	// Two credential names: generic and ephemeral-all (see iso-e2e.sh docs).
	return []string{
		"-device", fmt.Sprintf("vhost-vsock-pci,guest-cid=%d", cid),
		"-smbios", "type=11,value=io.systemd.credential.binary:ssh.authorized_keys.root=" + pubB64,
		"-smbios", "type=11,value=io.systemd.credential.binary:ssh.ephemeral-authorized_keys-all=" + pubB64,
	}, nil
}

// VsockSSHArgs builds ssh args to reach guest via VSOCK. Uses ProxyCommand
// socat VSOCK-CONNECT:cid:22 like iso-e2e's use_vsock_transport().
func VsockSSHArgs(name string, cid uint32, username string) ([]string, error) {
	keyPath := VsockKeyPath(name)
	if _, err := os.Stat(keyPath); err != nil {
		return nil, fmt.Errorf("vsock key not found for %q — create VM with --vsock", name)
	}
	// Hostname never resolved; only names known_hosts and scp dest.
	proxy := fmt.Sprintf("socat - VSOCK-CONNECT:%d:22", cid)
	args := []string{
		"-i", keyPath,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ProxyCommand=" + proxy,
		fmt.Sprintf("%s@e2e-vsock", username),
	}
	return args, nil
}

// VsockEndpoint returns the guest CID for a VM's vsock SSH.
func VsockEndpoint(name string) (uint32, error) {
	meta, err := readMetadata(name)
	if err != nil {
		return 0, err
	}
	if meta.VsockCID == 0 {
		return 0, fmt.Errorf("VM %q has no vsock CID — recreate with --vsock", name)
	}
	return meta.VsockCID, nil
}
