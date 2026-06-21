package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercises the real qemu-img conversion end to end: a raw image in, a valid
// compressed qcow2 out. Catches flag regressions the fake-runner tests can't.
// Skips when qemu-img isn't installed (CI installs qemu-utils for this).
func TestConvertRawToQcow2_Real(t *testing.T) {
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("qemu-img not installed")
	}
	dir := t.TempDir()
	raw := filepath.Join(dir, "in.raw")
	qcow := filepath.Join(dir, "out.qcow2")

	// A 4 MiB raw disk with a recognizable byte pattern.
	buf := make([]byte, 4<<20)
	for i := range buf {
		buf[i] = byte(i)
	}
	if err := os.WriteFile(raw, buf, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := convertRawToQcow2(qemuImg, raw, qcow); err != nil {
		t.Fatalf("convertRawToQcow2: %v", err)
	}

	// The output exists and is a real qcow2: magic "QFI\xfb" + qemu-img agrees.
	out, err := os.ReadFile(qcow)
	if err != nil {
		t.Fatalf("reading qcow2: %v", err)
	}
	if len(out) < 4 || string(out[:3]) != "QFI" || out[3] != 0xfb {
		t.Fatalf("output is not a qcow2 (bad magic): % x", out[:min(8, len(out))])
	}
	info, err := exec.Command(qemuImg, "info", "--output=json", qcow).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img info: %s", info)
	}
	if !contains(string(info), `"format": "qcow2"`) {
		t.Errorf("qemu-img info doesn't report qcow2: %s", info)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
