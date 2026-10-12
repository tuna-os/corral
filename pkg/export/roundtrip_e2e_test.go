//go:build e2esnapshot

// First real round-trip across the move seam: a tiny qemu disk is exported
// with REAL qemu-img and ingested into libvirt with REAL virsh, proving the
// export artifact is something the destination path consumes — not just bytes
// both sides politely ignore.
//
// Needs qemu-img and a usable libvirt connection; skips gracefully without
// them, and fails loud when CORRAL_REQUIRE_TOOLS promises them (see
// pkg/testenv).

package export

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/bootc"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/testenv"
	"github.com/tuna-os/corral/pkg/types"
)

func TestE2E_QemuToLibvirtRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		testenv.Skip(t, "qemu-img", "not installed")
	}
	if _, err := exec.LookPath("virsh"); err != nil {
		testenv.Skip(t, "virsh", "not installed")
	}
	SetRunner(shell.Real{})
	t.Cleanup(func() { SetRunner(shell.Real{}) })

	uri := ""
	for _, candidate := range []string{"qemu:///session", "qemu:///system"} {
		if exec.Command("virsh", "-c", candidate, "connect").Run() == nil {
			uri = candidate
			break
		}
	}
	if uri == "" {
		testenv.Skip(t, "libvirt", "no usable connection")
	}

	// A stopped local VM with a tiny real disk.
	home, units := t.TempDir(), t.TempDir()
	const name = "corral-roundtrip-e2e"
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "disk.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", src, "16M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %s: %v", out, err)
	}
	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"name":"`+name+`","cpu":1,"memory":"512M"}`), 0o644)
	qemu.SetStateDirs(home, units)
	qemu.SetSystemctl(func(...string) ([]byte, error) { return []byte("inactive\n"), nil })
	t.Cleanup(func() { qemu.SetStateDirs("", ""); qemu.SetSystemctl(nil) })

	// Export: qemu → qcow2 artifact, offline (the source is stopped).
	work := t.TempDir()
	artifact := filepath.Join(work, "moved.qcow2")
	exported, err := (QEMU{}).Export(context.Background(),
		Request{Ref: types.InstanceRef{Backend: "qemu", Name: name}, Dest: artifact}, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported.Format != Qcow2 || exported.Consistency != snapshot.Offline {
		t.Errorf("export = %s/%s, want qcow2/offline", exported.Format, exported.Consistency)
	}
	verifyArtifact(t, artifact, Qcow2)

	// Ingest: the artifact becomes a libvirt domain, stopped. This is the
	// same Import the libvirt move destination runs.
	const domain = "corral-roundtrip-dst"
	t.Cleanup(func() { exec.Command("virsh", "-c", uri, "undefine", domain).Run() })
	if err := (bootc.LibvirtTarget{}).Import(
		types.InstanceRef{Backend: "libvirt", Context: uri, Name: domain},
		artifact, bootc.CreateOpts{CPU: 1, Memory: "512Mi"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// The destination exists and points at a converted disk, not the scratch
	// artifact: Import converts into its own file next to the source.
	out, err := exec.Command("virsh", "-c", uri, "dumpxml", domain).CombinedOutput()
	if err != nil {
		t.Fatalf("dumpxml: %s: %v", out, err)
	}
	if !strings.Contains(string(out), domain) {
		t.Errorf("defined domain does not carry its name:\n%s", out)
	}
	ingested := filepath.Join(work, domain+".qcow2")
	if _, err := os.Stat(ingested); err != nil {
		t.Errorf("ingested disk missing at %s: %v", ingested, err)
	} else {
		verifyArtifact(t, ingested, Qcow2)
	}
	t.Logf("qemu %s → %s → libvirt %s (%d bytes)", name, artifact, domain, exported.Bytes)
}
