package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

func withImportFakes(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	importRunner = fake
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
		importRunner = shell.Real{}
	})
	t.Setenv("HOME", t.TempDir())
	return fake
}

func TestCheckImportFormat(t *testing.T) {
	for _, ok := range []string{
		"https://example.com/disk.qcow2", "disk.raw", "disk.img",
		"https://example.com/disk.qcow2.gz", "cloud.img.xz",
	} {
		if err := checkImportFormat(ok); err != nil {
			t.Errorf("checkImportFormat(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"disk.vmdk", "appliance.ova", "box.ovf", "win.vhd", "win.vhdx", "DISK.VMDK"} {
		err := checkImportFormat(bad)
		if err == nil {
			t.Errorf("checkImportFormat(%q) should fail", bad)
			continue
		}
		if !strings.Contains(err.Error(), "qemu-img convert") && !strings.Contains(err.Error(), "tar xf") {
			t.Errorf("checkImportFormat(%q) error lacks a conversion hint: %v", bad, err)
		}
	}
}

func TestRunImport_URL(t *testing.T) {
	fake := withImportFakes(t)

	if err := runImport("legacy", "tailvm", "https://example.com/disk.qcow2", "20G", "4G", 2); err != nil {
		t.Fatalf("runImport: %v", err)
	}
	// CDI HTTP import path: boot DataVolume + VM, no virtctl upload.
	applies := 0
	for _, c := range fake.Calls() {
		if c.Name == "virtctl" {
			t.Errorf("URL import must not call virtctl: %v", c.Args)
		}
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
		}
	}
	if applies != 2 {
		t.Errorf("applied %d manifests, want 2 (DataVolume + VM)", applies)
	}
}

func TestRunImport_LocalFile(t *testing.T) {
	fake := withImportFakes(t)
	img := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(img, []byte("fake image"), 0644); err != nil {
		t.Fatal(err)
	}
	fake.AddPrefixResponse("virtctl image-upload dv legacy-disk", "uploaded", nil)

	if err := runImport("legacy", "tailvm", img, "20G", "4G", 2); err != nil {
		t.Fatalf("runImport: %v", err)
	}
	var uploaded bool
	for _, c := range fake.Calls() {
		if c.Name == "virtctl" && len(c.Args) > 0 && c.Args[0] == "image-upload" {
			uploaded = true
			joined := strings.Join(c.Args, " ")
			for _, want := range []string{"dv legacy-disk", "--image-path " + img, "--size 20G"} {
				if !strings.Contains(joined, want) {
					t.Errorf("image-upload missing %q: %s", want, joined)
				}
			}
		}
	}
	if !uploaded {
		t.Error("local import should call virtctl image-upload")
	}
}

func TestRunImport_MissingFile(t *testing.T) {
	withImportFakes(t)

	if err := runImport("legacy", "tailvm", "/nonexistent/disk.qcow2", "20G", "4G", 2); err == nil {
		t.Fatal("import of a missing local file should fail")
	}
}

func TestRunImport_VMDKRejected(t *testing.T) {
	withImportFakes(t)

	err := runImport("legacy", "tailvm", "https://example.com/disk.vmdk", "20G", "4G", 2)
	if err == nil || !strings.Contains(err.Error(), "qemu-img convert") {
		t.Fatalf("VMDK import should fail with a conversion hint, got: %v", err)
	}
}
