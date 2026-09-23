package qemu

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVsockCID_DerivedStable(t *testing.T) {
	a := VsockCID("alpha", 0)
	b := VsockCID("alpha", 0)
	if a != b {
		t.Errorf("derived CID not stable: %d != %d", a, b)
	}
	if a < 3 {
		t.Errorf("CID %d is reserved", a)
	}
	c := VsockCID("beta", 0)
	if a == c {
		// Different names should ideally differ, but hash may collide rarely.
		// Check range not equal for this pair is expected.
		t.Logf("warning: alpha and beta mapped to same CID %d", a)
	}
}

func TestVsockCID_Explicit(t *testing.T) {
	if got := VsockCID("x", 42); got != 42 {
		t.Errorf("explicit CID wrong: %d", got)
	}
	if got := VsockCID("x", 1); got != 3 {
		t.Errorf("CID 1 should clamp to 3, got %d", got)
	}
}

func TestQemuArgs_WithVsockAndTPM(t *testing.T) {
	// Fake vsock args similar to what VsockArgs would produce.
	cid := VsockCID("testvm", 0)
	vsockArgs := []string{"-device", "vhost-vsock-pci,guest-cid=42", "-smbios", "type=11,value=foo"}
	tpmArgs := []string{"-chardev", "socket,id=chrtpm,path=/tmp/swtpm.sock", "-tpmdev", "emulator,id=tpm0,chardev=chrtpm", "-device", "tpm-crb,tpmdev=tpm0"}
	args := qemuArgs(generateUnitOpts{
		Name:        "testvm",
		Mem:         "4G",
		CPU:         2,
		DiskPath:    "/tmp/disk.qcow2",
		TailscaleIP: "127.0.0.1",
		VncDisplay:  1,
		SSHPort:     2222,
		QMPSocket:   "/tmp/qmp.sock",
		SerialLog:   "/tmp/serial.log",
		VsockCID:    cid,
		VsockArgs:   vsockArgs,
		TPMArgs:     tpmArgs,
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{"vhost-vsock-pci", "tpm-crb", "qmp.sock", "serial0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("qemuArgs missing %q in %s", want, joined)
		}
	}
}

func TestMetadataVsockTPMRoundTrip(t *testing.T) {
	dir := t.TempDir()
	SetStateDirs(dir, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	vmDir := filepath.Join(dir, "roundtrip")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate Create metadata write with vsock/tpm.
	meta := map[string]any{
		"name":      "roundtrip",
		"cpu":       2,
		"memory":    "4G",
		"disk_size": "20G",
		"vsock":     true,
		"vsock_cid": uint32(1234),
		"tpm":       true,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(vmDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readMetadata("roundtrip")
	if err != nil {
		t.Fatalf("readMetadata: %v", err)
	}
	if !got.Vsock || got.VsockCID != 1234 || !got.TPM {
		t.Errorf("roundtrip failed: %+v", got)
	}
}

func TestBootFailedOnSerial(t *testing.T) {
	dir := t.TempDir()
	SetStateDirs(dir, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	vmDir := filepath.Join(dir, "diagvm")
	_ = os.MkdirAll(vmDir, 0o755)
	_ = os.WriteFile(filepath.Join(vmDir, "serial.log"), []byte("Booting...\nDracut Emergency Shell\n"), 0o644)
	_ = os.WriteFile(filepath.Join(vmDir, "metadata.json"), []byte(`{"name":"diagvm"}`), 0o644)
	if ok, sig := BootFailedOnSerial("diagvm"); !ok || sig != "Dracut Emergency Shell" {
		t.Errorf("BootFailedOnSerial = %v %q, want true Dracut", ok, sig)
	}
	_ = os.WriteFile(filepath.Join(vmDir, "serial.log"), []byte("Booting... OK\n"), 0o644)
	if ok, _ := BootFailedOnSerial("diagvm"); ok {
		t.Error("expected no boot failure on clean log")
	}
}

func TestDiagnosticBundleCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	SetStateDirs(filepath.Join(home, "vms"), t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	vmDir := filepath.Join(home, "vms", "bundlevm")
	_ = os.MkdirAll(vmDir, 0o755)
	_ = os.WriteFile(filepath.Join(vmDir, "serial.log"), []byte("hello serial\n"), 0o644)
	_ = os.WriteFile(filepath.Join(vmDir, "metadata.json"), []byte(`{"name":"bundlevm","vsock":true,"vsock_cid":4321}`), 0o644)
	out := filepath.Join(dir, "out")
	if err := DiagnosticBundle("bundlevm", out); err != nil {
		t.Fatalf("DiagnosticBundle: %v", err)
	}
	for _, name := range []string{"serial.log", "metadata.json", "diagnostics.txt"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("bundle missing %s: %v", name, err)
		}
	}
}
