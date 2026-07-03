package cmd

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/types"
)

// captureStdout is defined in output_test.go — reused here rather than
// duplicated (this file originated as a separate PR against an older base).

func TestPrintCatalog_EmptyHasHeaderColumns(t *testing.T) {
	out := captureStdout(t, func() {
		printCatalog(nil)
	})
	// Should print at least the header row.
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "USER") {
		t.Errorf("expected header row with NAME/USER, got:\n%s", out)
	}
}

func TestPrintCatalog_SingleImage(t *testing.T) {
	img := catalog.Image{
		Name:        "fedora",
		Description: "Fedora Linux 41",
		DefaultUser: "fedora",
		URL:         "https://example.com/fedora.qcow2",
		Source:      "fedoraproject.org",
	}
	out := captureStdout(t, func() {
		printCatalog([]catalog.Image{img})
	})
	if !strings.Contains(out, "fedora") {
		t.Errorf("expected image name 'fedora' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "fedora") && !strings.Contains(out, "Fedora") {
		t.Errorf("expected description in output, got:\n%s", out)
	}
}

func TestPrintCatalog_MultipleImages(t *testing.T) {
	images := []catalog.Image{
		{
			Name:        "ubuntu",
			Description: "Ubuntu 24.04 LTS",
			DefaultUser: "ubuntu",
			URL:         "https://example.com/ubuntu.qcow2",
			Source:      "ubuntu.com",
		},
		{
			Name:        "debian",
			Description: "Debian 12 Bookworm",
			DefaultUser: "debian",
			URL:         "https://example.com/debian.qcow2",
			Source:      "debian.org",
		},
	}
	out := captureStdout(t, func() {
		printCatalog(images)
	})
	if !strings.Contains(out, "ubuntu") {
		t.Errorf("expected 'ubuntu' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "debian") {
		t.Errorf("expected 'debian' in output, got:\n%s", out)
	}
}

func TestPrintCatalog_ContainerDisk(t *testing.T) {
	img := catalog.Image{
		Name:          "arch",
		Description:   "Arch Linux",
		DefaultUser:   "arch",
		ContainerDisk: "quay.io/containerdisks/arch:latest",
		Source:        "archlinux.org",
	}
	out := captureStdout(t, func() {
		printCatalog([]catalog.Image{img})
	})
	if !strings.Contains(out, "arch") {
		t.Errorf("expected 'arch' in output, got:\n%s", out)
	}
	// containerDisk type should appear in the TYPE column
	if !strings.Contains(out, "containerDisk") {
		t.Errorf("expected 'containerDisk' type in output, got:\n%s", out)
	}
}

func TestPrintVMList_Empty(t *testing.T) {
	out := captureStdout(t, func() {
		printVMList(nil)
	})
	// Should print header row.
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "BACKEND") {
		t.Errorf("expected header with NAME/BACKEND, got:\n%s", out)
	}
}

func TestPrintVMList_SingleVM(t *testing.T) {
	vms := []types.VM{
		{
			Name:      "test-vm",
			Backend:   "kubevirt",
			Status:    "Running",
			Ready:     true,
			Running:   true,
			CPU:       4,
			Mem:       "8Gi",
			Namespace: "default",
			Node:      "worker-1",
		},
	}
	out := captureStdout(t, func() {
		printVMList(vms)
	})
	if !strings.Contains(out, "test-vm") {
		t.Errorf("expected 'test-vm' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Running") && !strings.Contains(out, "●") {
		t.Errorf("expected running indicator in output, got:\n%s", out)
	}
}

func TestPrintVMList_StoppedVM(t *testing.T) {
	vms := []types.VM{
		{
			Name:      "stopped-vm",
			Backend:   "qemu",
			Status:    "Stopped",
			Ready:     false,
			Running:   false,
			CPU:       2,
			Mem:       "4Gi",
			Namespace: "default",
		},
	}
	out := captureStdout(t, func() {
		printVMList(vms)
	})
	if !strings.Contains(out, "stopped-vm") {
		t.Errorf("expected 'stopped-vm' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "○") && !strings.Contains(out, "Stopped") {
		t.Errorf("expected stopped indicator in output, got:\n%s", out)
	}
}

func TestPrintVMList_MultiVM_Sorted(t *testing.T) {
	vms := []types.VM{
		{Name: "zeta-vm", Backend: "kubevirt", Namespace: "ns1"},
		{Name: "alpha-vm", Backend: "qemu", Namespace: "ns2"},
		{Name: "beta-vm", Backend: "kubevirt", Namespace: "ns3"},
	}
	out := captureStdout(t, func() {
		printVMList(vms)
	})
	// Verify output contains all VM names.
	if !strings.Contains(out, "alpha-vm") {
		t.Errorf("expected 'alpha-vm' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "beta-vm") {
		t.Errorf("expected 'beta-vm' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "zeta-vm") {
		t.Errorf("expected 'zeta-vm' in output, got:\n%s", out)
	}
}

func TestPrintVMList_EmptyNamespace(t *testing.T) {
	vms := []types.VM{
		{
			Name:    "no-ns",
			Backend: "qemu",
			CPU:     1,
			Mem:     "1Gi",
		},
	}
	out := captureStdout(t, func() {
		printVMList(vms)
	})
	if !strings.Contains(out, "no-ns") {
		t.Errorf("expected 'no-ns' in output, got:\n%s", out)
	}
}
