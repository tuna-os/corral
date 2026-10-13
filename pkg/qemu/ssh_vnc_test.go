package qemu

// Tests for SSH and VNCAddr — the two qemu cells the parity matrix ships
// without a covering test. VNCAddr is a pure metadata read. SSH ends in a
// real interactive ssh(1), so the success path is pinned with a stub ssh
// binary on PATH that records its argv, over a temp-HOME VM whose metadata
// carries a Tailscale IP and a forwarded port.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeVM(t *testing.T, meta vmMetadata) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(VMHome(), meta.Name)
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
}

func TestVNCAddrJoinsTailnetIPAndPort(t *testing.T) {
	writeVM(t, vmMetadata{Name: "web", VncPort: 5901, Tailscale: "100.64.0.7"})

	addr, err := VNCAddr("web")
	if err != nil {
		t.Fatalf("VNCAddr: %v", err)
	}
	if addr != "100.64.0.7:5901" {
		t.Errorf("addr = %q, want the tailnet IP and the recorded VNC port", addr)
	}
}

func TestVNCAddrFallsBackToLoopback(t *testing.T) {
	// CI runners have no tailnet; the hostfwd is bound to loopback there.
	writeVM(t, vmMetadata{Name: "web", VncPort: 5902})

	addr, err := VNCAddr("web")
	if err != nil {
		t.Fatalf("VNCAddr: %v", err)
	}
	if addr != "127.0.0.1:5902" {
		t.Errorf("addr = %q, want the loopback fallback", addr)
	}
}

func TestVNCAddrWithoutAPortIsAnError(t *testing.T) {
	writeVM(t, vmMetadata{Name: "web", Tailscale: "100.64.0.7"})

	if _, err := VNCAddr("web"); err == nil {
		t.Fatal("expected an error for a VM with no VNC port")
	}
	if _, err := VNCAddr("missing"); err == nil {
		t.Fatal("expected an error for an unknown VM")
	}
}

// stubSSH puts a recording ssh stub on PATH and returns the file its argv
// lands in.
func stubSSH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\necho \"$@\" >> \"$ARGV_LOG\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGV_LOG", log)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func TestSSHUsesTheForwardedPort(t *testing.T) {
	writeVM(t, vmMetadata{Name: "web", SSHPort: 2222, Tailscale: "100.64.0.7"})
	// writeVM reset HOME via t.Setenv; the stub must extend the same PATH.
	log := stubSSH(t)

	if err := SSH("web", "debian", "", "uptime", 0, "", nil); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("reading argv log: %v", err)
	}
	argv := strings.TrimSpace(string(raw))
	for _, want := range []string{"-p 2222", "debian@100.64.0.7", "uptime"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
}

func TestSSHExplicitPortOverridesTheForward(t *testing.T) {
	writeVM(t, vmMetadata{Name: "web", SSHPort: 2222, Tailscale: "100.64.0.7"})
	log := stubSSH(t)

	if err := SSH("web", "debian", "", "", 2022, "", nil); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	raw, _ := os.ReadFile(log)
	if argv := strings.TrimSpace(string(raw)); !strings.Contains(argv, "-p 2022") {
		t.Errorf("argv %q should use the explicit port", argv)
	}
}

func TestSSHWithoutAnAddressIsARefusal(t *testing.T) {
	// No Tailscale IP and no loopback tolerance here (unlike WaitSSH): an
	// interactive session to 127.0.0.1:port would fail confusingly, so SSH
	// refuses up front.
	writeVM(t, vmMetadata{Name: "web", SSHPort: 2222})

	if err := SSH("web", "debian", "", "", 0, "", nil); err == nil {
		t.Fatal("expected a refusal for a VM with no Tailscale IP")
	} else if !strings.Contains(err.Error(), "Tailscale") {
		t.Errorf("refusal %q should name Tailscale", err)
	}
}

func TestSSHWithoutAForwardedPortIsARefusal(t *testing.T) {
	writeVM(t, vmMetadata{Name: "web", Tailscale: "100.64.0.7"})

	if err := SSH("web", "debian", "", "", 0, "", nil); err == nil {
		t.Fatal("expected a refusal for a VM with no forwarded SSH port")
	}
}
