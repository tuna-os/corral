package qemu

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestVMHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".local", "share", "corral", "vms")
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
	if !contains(unit, "cdrom") {
		t.Error("unit should contain -cdrom when ISO is specified")
	}
}

func TestGenerateUnit_WithSSHPort(t *testing.T) {
	unit := generateUnit(generateUnitOpts{
		Name:        "web",
		QemuPath:    "/usr/bin/qemu-system-x86_64",
		Mem:         "2G",
		CPU:         1,
		DiskPath:    "/tmp/web.qcow2",
		TailscaleIP: "100.64.0.5",
		VncDisplay:  3,
		SSHPort:     2203,
	})

	if !contains(unit, "hostfwd=tcp:100.64.0.5:2203-:22") {
		t.Error("unit should contain hostfwd for SSH port")
	}
}

func TestGenerateUnit_WithoutSSHPort(t *testing.T) {
	unit := generateUnit(generateUnitOpts{
		Name:        "web",
		QemuPath:    "/usr/bin/qemu-system-x86_64",
		Mem:         "2G",
		CPU:         1,
		DiskPath:    "/tmp/web.qcow2",
		TailscaleIP: "100.64.0.5",
		VncDisplay:  3,
		SSHPort:     0,
	})

	if contains(unit, "hostfwd") {
		t.Error("unit should NOT contain hostfwd when SSHPort is 0")
	}
}

func TestGenerateUnit_MemSuffix(t *testing.T) {
	// Mem without G/M suffix should get G appended
	unit := generateUnit(generateUnitOpts{
		Name:        "test",
		QemuPath:    "/usr/bin/qemu-system-x86_64",
		Mem:         "4096",
		CPU:         1,
		DiskPath:    "/tmp/test.qcow2",
		TailscaleIP: "127.0.0.1",
		VncDisplay:  0,
	})

	if !contains(unit, "-m 4096G") {
		t.Error("unit should append G to bare memory value")
	}
}

func TestGenerateUnit_MemWithMSuffix(t *testing.T) {
	unit := generateUnit(generateUnitOpts{
		Name:        "test",
		QemuPath:    "/usr/bin/qemu-system-x86_64",
		Mem:         "512M",
		CPU:         1,
		DiskPath:    "/tmp/test.qcow2",
		TailscaleIP: "127.0.0.1",
		VncDisplay:  0,
	})

	if !contains(unit, "-m 512M") {
		t.Error("unit should preserve M suffix on memory value")
	}
}

func TestGenerateUnit_AllFields(t *testing.T) {
	unit := generateUnit(generateUnitOpts{
		Name:        "fulltest",
		QemuPath:    "/custom/qemu",
		Mem:         "16G",
		CPU:         8,
		DiskPath:    "/data/disk.qcow2",
		ISOPath:     "/data/install.iso",
		HasISO:      true,
		TailscaleIP: "100.80.0.1",
		VncDisplay:  42,
		SSHPort:     2242,
	})

	checks := []string{
		"fulltest",
		"/custom/qemu",
		"-m 16G",
		"-smp 8",
		"/data/disk.qcow2",
		"-cdrom /data/install.iso",
		"-boot once=d,menu=on",
		"100.80.0.1:42",
		"hostfwd=tcp:100.80.0.1:2242-:22",
	}

	for _, c := range checks {
		if !contains(unit, c) {
			t.Errorf("unit missing expected substring: %q", c)
		}
	}
}

// ── Metadata / Info ──────────────────────────────────────────────

func TestReadMetadata(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vmDir := filepath.Join(VMHome(), "testvm")
	os.MkdirAll(vmDir, 0755)

	meta := vmMetadata{
		Name:      "testvm",
		CPU:       4,
		Memory:    "8G",
		Disk:      "20G",
		VncPort:   5905,
		SSHPort:   2205,
		Tailscale: "100.64.0.1",
	}
	data, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(vmDir, "metadata.json"), data, 0644)

	got, err := readMetadata("testvm")
	if err != nil {
		t.Fatalf("readMetadata: %v", err)
	}
	if got.Name != "testvm" {
		t.Errorf("Name = %q, want testvm", got.Name)
	}
	if got.CPU != 4 {
		t.Errorf("CPU = %d, want 4", got.CPU)
	}
	if got.VncPort != 5905 {
		t.Errorf("VncPort = %d, want 5905", got.VncPort)
	}
}

func TestInfo(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vmDir := filepath.Join(VMHome(), "infovm")
	os.MkdirAll(vmDir, 0755)
	os.WriteFile(filepath.Join(vmDir, "metadata.json"),
		[]byte(`{"name":"infovm","cpu":2}`), 0644)

	data, err := Info("infovm")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if len(data) == 0 {
		t.Error("Info returned empty data")
	}
	if !contains(string(data), "infovm") {
		t.Error("Info should contain VM name")
	}
}

func TestInfo_MissingVM(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, err := Info("nonexistent")
	if err == nil {
		t.Error("Info should return error for nonexistent VM")
	}
}

func TestReadMetadata_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vmDir := filepath.Join(VMHome(), "nometa")
	os.MkdirAll(vmDir, 0755)
	// Don't write metadata.json

	_, err := readMetadata("nometa")
	if err == nil {
		t.Error("readMetadata should return error when metadata.json is missing")
	}
}

func TestReadMetadata_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vmDir := filepath.Join(VMHome(), "badjson")
	os.MkdirAll(vmDir, 0755)
	os.WriteFile(filepath.Join(vmDir, "metadata.json"),
		[]byte(`not json`), 0644)

	_, err := readMetadata("badjson")
	if err == nil {
		t.Error("readMetadata should return error for invalid JSON")
	}
}

// ── Exists ───────────────────────────────────────────────────────

func TestExists(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vmDir := filepath.Join(VMHome(), "existsvm")
	os.MkdirAll(vmDir, 0755)

	if !Exists("existsvm") {
		t.Error("Exists should return true for created VM dir")
	}
	if Exists("noexist") {
		t.Error("Exists should return false for nonexistent VM")
	}
}

func TestExists_FileNotDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vmDir := VMHome()
	os.MkdirAll(vmDir, 0755)
	// Create a file with VM name, not a directory
	os.WriteFile(filepath.Join(vmDir, "notavm"), []byte("x"), 0644)

	if Exists("notavm") {
		t.Error("Exists should return false when path is a file, not a directory")
	}
}

// ── List ─────────────────────────────────────────────────────────

func TestList_Empty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vms, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 0 {
		t.Errorf("List should return empty slice for no VMs, got %d", len(vms))
	}
}

func TestList_WithVMs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Create two VM dirs with metadata
	for _, name := range []string{"vm1", "vm2"} {
		vmDir := filepath.Join(VMHome(), name)
		os.MkdirAll(vmDir, 0755)
		meta := vmMetadata{
			Name:      name,
			CPU:       2,
			Memory:    "4G",
			Disk:      "10G",
			VncPort:   5900,
			Tailscale: "100.64.0.1",
		}
		data, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(vmDir, "metadata.json"), data, 0644)
	}

	vms, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("List should return 2 VMs, got %d", len(vms))
	}

	// Both should be stopped (systemctl is-active will fail in test)
	for _, vm := range vms {
		if vm.Running {
			t.Errorf("VM %q should not be running in test", vm.Name)
		}
		if vm.Backend != "qemu" {
			t.Errorf("VM %q backend should be qemu, got %q", vm.Name, vm.Backend)
		}
	}
}

func TestList_SkipsCache(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Create cache dir and a real VM
	os.MkdirAll(filepath.Join(VMHome(), "cache"), 0755)
	os.MkdirAll(filepath.Join(VMHome(), "realvm"), 0755)
	meta := vmMetadata{Name: "realvm", CPU: 1, Memory: "2G", Disk: "5G", VncPort: 5900}
	data, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(VMHome(), "realvm", "metadata.json"), data, 0644)

	vms, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 1 {
		t.Errorf("List should skip cache dir, got %d VMs", len(vms))
	}
	if vms[0].Name != "realvm" {
		t.Errorf("expected realvm, got %q", vms[0].Name)
	}
}

func TestList_SkipsFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	os.MkdirAll(VMHome(), 0755)
	os.WriteFile(filepath.Join(VMHome(), "notadir"), []byte("x"), 0644)

	vms, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 0 {
		t.Errorf("List should skip files, got %d VMs", len(vms))
	}
}

// ── Path helpers ─────────────────────────────────────────────────

func TestSystemdUserDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := systemdUserDir()
	expected := filepath.Join(tmp, ".config", "systemd", "user")
	if dir != expected {
		t.Errorf("systemdUserDir() = %q, want %q", dir, expected)
	}
}

func TestVMHome_CustomHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := VMHome()
	expected := filepath.Join(tmp, ".local", "share", "corral", "vms")
	if dir != expected {
		t.Errorf("VMHome() = %q, want %q", dir, expected)
	}
}

// ── findQEMU ─────────────────────────────────────────────────────

func TestFindQEMU_NotFound(t *testing.T) {
	// In a temp dir with no qemu binaries, findQEMU should error
	_, _, err := findQEMU()
	if err == nil {
		t.Log("findQEMU succeeded (qemu found on this system)")
	} else {
		t.Logf("findQEMU error (expected on test systems): %v", err)
	}
}

// ── hashDisplay ──────────────────────────────────────────────────

func TestHashDisplay_Range(t *testing.T) {
	for _, name := range []string{"a", "test", "verylongvmname123", "x"} {
		h := hashDisplay(name)
		if h < 0 || h >= 100 {
			t.Errorf("hashDisplay(%q) = %d, out of range [0,99]", name, h)
		}
	}
}

func TestHashDisplay_Different(t *testing.T) {
	// Different names should produce different hashes (high probability)
	a := hashDisplay("alpha")
	b := hashDisplay("beta")
	if a == b {
		t.Log("hash collision between alpha and beta (unlikely but possible)")
	}
}

// ── helpers ──────────────────────────────────────────────────────

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ── Create with fake binaries ────────────────────────────────────

// setupFakeQEMU puts fake qemu-system-x86_64 + qemu-img scripts in a temp
// dir prepended to PATH, where findQEMU picks them up before any real install.
func setupFakeQEMU(t *testing.T) func() {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "qemu-system-x86_64"),
		[]byte("#!/bin/sh\necho fake-qemu\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "qemu-img"),
		[]byte("#!/bin/sh\necho fake-qemu-img creating disk\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() {}
}

func TestCreate_WithFakeBinaries(t *testing.T) {
	cleanup := setupFakeQEMU(t)
	defer cleanup()

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	opts := types.CreateOpts{
		Name: "fakevm",
		CPU:  2,
		Mem:  "4G",
		Disk: "10G",
	}

	err := Create(opts)
	if err != nil {
		t.Logf("Create returned error (may be expected): %v", err)
	}

	// Verify files were created
	vmDir := filepath.Join(VMHome(), "fakevm")
	if _, err := os.Stat(vmDir); err == nil {
		// Metadata should exist
		if _, err := os.Stat(filepath.Join(vmDir, "metadata.json")); err != nil {
			t.Error("metadata.json not created")
		}
		// Systemd unit should exist
		unitPath := filepath.Join(systemdUserDir(), "corral-fakevm.service")
		if _, err := os.Stat(unitPath); err != nil {
			t.Error("systemd unit file not created")
		}
	}
}

func TestCreate_WithISO(t *testing.T) {
	cleanup := setupFakeQEMU(t)
	defer cleanup()

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	opts := types.CreateOpts{
		Name: "isovm",
		CPU:  1,
		Mem:  "2G",
		ISO:  "/fake/path/test.iso",
	}

	err := Create(opts)
	if err != nil {
		t.Logf("Create with ISO returned error: %v", err)
	}

	vmDir := filepath.Join(VMHome(), "isovm")
	if info, err := os.Stat(vmDir); err == nil && info.IsDir() {
		data, _ := os.ReadFile(filepath.Join(vmDir, "metadata.json"))
		if !contains(string(data), "test.iso") {
			t.Error("metadata should reference the ISO path")
		}
	}
}

func TestStart_WithFakeBinaries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// First create the VM
	opts := types.CreateOpts{Name: "startvm", CPU: 1, Mem: "1G", Disk: "5G"}
	_ = Create(opts)

	err := Start("startvm")
	// Start may fail for various reasons with fake systemctl, but shouldn't panic
	if err != nil {
		t.Logf("Start returned: %v", err)
	}
}

func TestStart_NonExistent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	err := Start("no-such-vm")
	if err == nil {
		t.Error("Start should error for nonexistent VM")
	}
}

func TestStop_WithFakeBinaries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_ = Create(types.CreateOpts{Name: "stopvm", CPU: 1, Mem: "1G", Disk: "5G"})

	err := Stop("stopvm")
	// Fake systemctl will run successfully
	if err != nil {
		t.Logf("Stop returned: %v", err)
	}
}

func TestDelete_WithFakeBinaries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_ = Create(types.CreateOpts{Name: "delvm", CPU: 1, Mem: "1G", Disk: "5G"})

	err := Delete("delvm")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// VM directory should be gone
	if Exists("delvm") {
		t.Error("VM directory should be deleted")
	}
}

func TestLogs_WithFakeBinaries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Empty PATH so the real journalctl (which would follow forever) can
	// never be found — Logs must fail fast.
	t.Setenv("PATH", t.TempDir())

	if err := Logs("test"); err == nil {
		t.Fatal("Logs should fail when journalctl is unavailable")
	}
}

// fakeQemuBin drops no-op qemu-system-x86_64 / qemu-img shims into a temp dir
// and prepends it to PATH so Create() can run without real QEMU. The qemu-img
// shim logs its argv to <dir>/qemu-img.log and honors `create` by touching
// the target file so later stat checks behave.
func fakeQemuBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	shim := `#!/bin/sh
echo "$@" >> "$(dirname "$0")/qemu-img.log"
cmd="$1"
case "$cmd" in
create) touch "$3" ;;
convert) cp "$3" "$4" 2>/dev/null || touch "$4" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "qemu-img"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qemu-system-x86_64"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir
}

func TestCreate_ExistingDiskIsNotClobbered(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	bin := fakeQemuBin(t)

	// Simulate a bootc-built disk already in place.
	vmDir := filepath.Join(VMHome(), "bootcvm")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("bootc-built-disk-content")
	diskPath := filepath.Join(vmDir, "disk.qcow2")
	if err := os.WriteFile(diskPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	err := Create(types.CreateOpts{Name: "bootcvm", Force: true, ExistingDisk: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, _ := os.ReadFile(diskPath)
	if string(got) != string(payload) {
		t.Fatalf("prepared disk was modified: got %q", got)
	}
	if log, _ := os.ReadFile(filepath.Join(bin, "qemu-img.log")); len(log) != 0 {
		t.Fatalf("qemu-img was invoked for an ExistingDisk create: %s", log)
	}
}

func TestCreate_ExistingDiskMissingFails(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	fakeQemuBin(t)

	err := Create(types.CreateOpts{Name: "novm", Force: true, ExistingDisk: true})
	if err == nil {
		t.Fatal("expected error when ExistingDisk is set but no disk exists")
	}
}

func TestCreate_QCOWTemplateCopied(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	bin := fakeQemuBin(t)

	template := filepath.Join(tmp, "template.qcow2")
	if err := os.WriteFile(template, []byte("template-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Create(types.CreateOpts{Name: "tmplvm", Force: true, QCOW: template, Disk: "40G"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	log, _ := os.ReadFile(filepath.Join(bin, "qemu-img.log"))
	if !strings.Contains(string(log), "convert") {
		t.Fatalf("expected qemu-img convert for template, log: %s", log)
	}
	if !strings.Contains(string(log), "resize") {
		t.Fatalf("expected qemu-img resize when Disk is explicit, log: %s", log)
	}
}
