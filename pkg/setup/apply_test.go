package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
)

func TestApplyRefusesAbsentBackend(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.missing["incus"] = true
	SetRunner(f)
	if err := Apply("incus", ApplyOptions{}); err == nil {
		t.Fatal("applied incus with no binary present")
	}
	if _, ok := config.FindContext("incus"); ok {
		t.Fatal("failed apply still saved a context")
	}
}

func TestApplyLibvirtSavesAndReprobeGuards(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.AddResponse("virsh -c qemu:///system connect", "", nil)
	f.AddResponse("virsh -c qemu:///system nodeinfo", "CPU model: x86_64", nil)
	SetRunner(f)
	if err := Apply("libvirt", ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, ok := config.FindContext("libvirt")
	if !ok || got.Context != "qemu:///system" {
		t.Fatalf("saved context = %+v, %v", got, ok)
	}
	// Daemon goes away: re-probe must block a second save, not overwrite.
	f2 := newTestRunner()
	f2.AddResponse("virsh -c qemu:///system connect", "", errors.New("no daemon"))
	f2.AddResponse("virsh -c qemu:///system nodeinfo", "", errors.New("no daemon"))
	SetRunner(f2)
	if err := Apply("libvirt", ApplyOptions{Value: "qemu:///system", Overwrite: true}); err == nil {
		t.Fatal("applied libvirt while the daemon is down")
	}
}

func TestApplyNeverOverwritesWithoutConfirm(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.AddResponse("virsh -c qemu:///system connect", "", nil)
	f.AddResponse("virsh -c qemu:///system nodeinfo", "CPU model: x86_64", nil)
	SetRunner(f)
	if err := Apply("libvirt", ApplyOptions{Name: "lab", Value: "qemu:///system"}); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	err := Apply("libvirt", ApplyOptions{Name: "lab", Value: "qemu:///system"})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Apply = %v, want ErrExists", err)
	}
	if err := Apply("libvirt", ApplyOptions{Name: "lab", Value: "qemu:///system", Overwrite: true}); err != nil {
		t.Fatalf("overwrite Apply: %v", err)
	}
}

func TestApplyProxmoxPrefersTokenFromEnv(t *testing.T) {
	isolate(t)
	t.Setenv("CORRAL_PROXMOX_TOKEN", "root@pam!corral=from-env")
	proxmoxReachable = func(host string) error { return nil }
	SetRunner(newTestRunner())
	if err := Apply("proxmox", ApplyOptions{Name: "pve", Value: "pve.example.com"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, ok := config.FindContext("pve")
	if !ok || got.Context != "pve.example.com" {
		t.Fatalf("saved context = %+v, %v", got, ok)
	}
	if got.Proxmox != nil && got.Proxmox.Token != "" {
		t.Fatal("env token was written to the config file")
	}
	// And the file itself carries no token either.
	raw, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "corral", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "from-env") {
		t.Fatal("config file contains the env token")
	}
	if h := TakeHint(); h != "" {
		t.Fatalf("unexpected post-save hint with token set: %q", h)
	}
}

func TestApplyProxmoxWithoutTokenSavesHostWithHint(t *testing.T) {
	isolate(t)
	proxmoxReachable = func(host string) error { return nil }
	SetRunner(newTestRunner())
	if err := Apply("proxmox", ApplyOptions{Name: "pve", Value: "pve.example.com"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	h := TakeHint()
	if !strings.Contains(h, "CORRAL_PROXMOX_TOKEN_PVE") {
		t.Fatalf("hint = %q, want per-context token variable", h)
	}
}

func TestApplyProxmoxRefusesUnreachable(t *testing.T) {
	isolate(t)
	SetRunner(newTestRunner()) // isolate() leaves proxmoxReachable failing
	if err := Apply("proxmox", ApplyOptions{Name: "pve", Value: "pve.example.com"}); err == nil {
		t.Fatal("applied an unreachable PVE host")
	}
	if _, ok := config.FindContext("pve"); ok {
		t.Fatal("failed apply still saved a context")
	}
}

func TestApplyProxmoxRefusesEmptyHost(t *testing.T) {
	isolate(t)
	SetRunner(newTestRunner())
	if err := Apply("proxmox", ApplyOptions{}); err == nil {
		t.Fatal("applied proxmox with no host — the scan never invents hosts")
	}
}

func TestApplyUnknownBackend(t *testing.T) {
	isolate(t)
	SetRunner(newTestRunner())
	if err := Apply("vsphere", ApplyOptions{}); err == nil {
		t.Fatal("setup onboarded a backend it does not probe")
	}
}
