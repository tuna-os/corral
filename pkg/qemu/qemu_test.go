package qemu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVMHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".local", "share", "tailvm", "vms")
	if got := VMHome(); got != expected {
		t.Errorf("VMHome() = %s, want %s", got, expected)
	}
}

func TestHashDisplay(t *testing.T) {
	a := hashDisplay("bluefin")
	b := hashDisplay("bluefin")
	if a != b {
		t.Error("hash should be deterministic")
	}
	if a < 0 || a >= 100 {
		t.Errorf("hash out of range: %d", a)
	}
}

func TestGenerateUnit(t *testing.T) {
	unit := generateUnit(generateUnitOpts{
		Name:        "testvm",
		QemuPath:    "/usr/bin/qemu-system-x86_64",
		Mem:         "4G",
		CPU:         2,
		DiskPath:    "/tmp/test.qcow2",
		TailscaleIP: "100.64.0.1",
		VncDisplay:  5,
	})

	if len(unit) == 0 {
		t.Error("unit should not be empty")
	}
	if len(unit) < 10 {
		t.Fatal("unit too short")
	}
}

func TestGenerateUnit_WithISO(t *testing.T) {
	unit := generateUnit(generateUnitOpts{
		Name:        "testvm",
		QemuPath:    "/usr/bin/qemu-system-x86_64",
		Mem:         "8G",
		CPU:         4,
		DiskPath:    "/tmp/test.qcow2",
		ISOPath:     "/tmp/test.iso",
		HasISO:      true,
		TailscaleIP: "100.64.0.1",
		VncDisplay:  10,
	})

	if len(unit) == 0 {
		t.Error("unit should not be empty")
	}
	// Should contain -cdrom
	cdromFound := false
	for _, line := range []string{unit} {
		for i := 0; i < len(line)-5; i++ {
			if line[i:i+5] == "cdrom" {
				cdromFound = true
				break
			}
		}
	}
	if !cdromFound {
		t.Error("unit should contain -cdrom when ISO is specified")
	}
}
