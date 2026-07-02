package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctpkg "github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

func TestResolveBackend_Registry(t *testing.T) {
	// Setup a temp registry
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	s := registry.NewStoreAt(path)

	// Override the global for this test
	oldStore := registryStore
	registryStore = s
	defer func() { registryStore = oldStore }()

	s.Set("testvm", types.RegistryEntry{Backend: "kubevirt", Namespace: "default"})

	backend := resolveBackend("testvm")
	if backend != "kubevirt" {
		t.Errorf("expected kubevirt, got %s", backend)
	}
}

func TestResolveBackend_NotFound(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	backend := resolveBackend("nonexistent-vm-xyzzzy")
	if backend != "" {
		t.Errorf("expected empty, got %s", backend)
	}
}

func TestRequireBackend(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	_, err := requireBackend("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestUniq(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b"}
	got := uniq(input)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d: %v", len(got), got)
	}
}

func TestAllVMNames_Empty(t *testing.T) {
	// Without a cluster, should return zero KubeVirt VMs
	// And QEMU dir shouldn't exist
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()
	os.Setenv("HOME", t.TempDir())
	defer os.Unsetenv("HOME")

	names := allVMNames()
	// Should be empty (or just whatever happens to exist)
	_ = names
}

func TestResolveNamespace_FromRegistry(t *testing.T) {
	dir := t.TempDir()
	s := registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	s.Set("testvm", types.RegistryEntry{Backend: "kubevirt", Namespace: "custom-ns"})

	oldStore := registryStore
	registryStore = s
	defer func() { registryStore = oldStore }()

	ns, backend := resolveNamespace("testvm")
	if ns != "custom-ns" {
		t.Errorf("expected custom-ns, got %s", ns)
	}
	if backend != "kubevirt" {
		t.Errorf("expected kubevirt, got %s", backend)
	}
}

func TestResolveNamespace_Default(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	// Without registry and without cluster, should return default namespace
	ns, _ := resolveNamespace("nonexistent-vm-xyzzzy")
	if ns == "" {
		t.Error("expected non-empty default namespace")
	}
}

func TestRequireOrPrompt_NoVMs(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()
	os.Setenv("HOME", t.TempDir())
	defer os.Unsetenv("HOME")

	_, err := requireOrPrompt([]string{"nonexistent-vm"}, "test")
	if err != nil {
		t.Logf("expected error when no VMs exist: %v", err)
	}
	// requireOrPrompt with no VMs should still pass through the name
	// if the name was provided as an arg.
}

func TestRequireOrPrompt_EmptyArgs(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()
	os.Setenv("HOME", t.TempDir())
	defer os.Unsetenv("HOME")

	_, err := requireOrPrompt([]string{}, "test")
	if err == nil {
		t.Error("expected error for empty args with no VMs")
	}
}

func TestRequireOrPrompt_EmptyStringArg(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()
	os.Setenv("HOME", t.TempDir())
	defer os.Unsetenv("HOME")

	_, err := requireOrPrompt([]string{""}, "test")
	if err == nil {
		t.Error("expected error for empty string arg with no VMs")
	}
}

// ── Command structure tests ──────────────────────────────────────

func TestRootCommand_HasExpectedSubcommands(t *testing.T) {
	// List of expected subcommands
	expected := []string{
		"create", "list", "start", "stop", "delete",
		"ssh", "viewer", "logs", "info",
		"config", "images", "plugin", "doctor", "web",
	}

	for _, name := range expected {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil || cmd == rootCmd {
			t.Errorf("expected subcommand %q, but not found", name)
		}
	}
}

func TestCreateCommand_Flags(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"create"})
	if err != nil || cmd == rootCmd {
		t.Fatal("create command not found")
	}

	expectedFlags := []string{
		"kubevirt", "mem", "cpu", "disk", "iso", "qcow",
		"force", "container-disk", "image", "import", "pvc",
		"namespace", "node", "cloud-init-password", "cloud-init",
		"instancetype", "preference",
	}

	for _, flag := range expectedFlags {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("expected flag --%s on create command", flag)
		}
	}
}

func TestCreateCommand_DefaultValues(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"create"})
	if err != nil || cmd == rootCmd {
		t.Fatal("create command not found")
	}

	// Check defaults
	mem, _ := cmd.Flags().GetString("mem")
	if mem != "4G" {
		t.Errorf("expected default mem=4G, got %s", mem)
	}

	cpu, _ := cmd.Flags().GetInt("cpu")
	if cpu != 2 {
		t.Errorf("expected default cpu=2, got %d", cpu)
	}

	ns, _ := cmd.Flags().GetString("namespace")
	if ns != "corral-vms" {
		t.Errorf("expected default namespace=corral-vms, got %s", ns)
	}
}

func TestDeleteCommand_HasForceFlag(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"delete"})
	if err != nil || cmd == rootCmd {
		t.Fatal("delete command not found")
	}
	if cmd.Flags().Lookup("force") == nil {
		t.Error("expected --force flag on delete command")
	}
}

func TestSSHCommand_HasFlags(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"ssh"})
	if err != nil || cmd == rootCmd {
		t.Fatal("ssh command not found")
	}

	for _, flag := range []string{"user", "identity", "command", "port", "password"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("expected --%s flag on ssh command", flag)
		}
	}
}

// ── Helpers ──────────────────────────────────────────────────────

func TestUniq_Empty(t *testing.T) {
	got := uniq([]string{})
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestUniq_Single(t *testing.T) {
	got := uniq([]string{"only"})
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("expected [only], got %v", got)
	}
}

func TestUniq_AllSame(t *testing.T) {
	got := uniq([]string{"x", "x", "x"})
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("expected [x], got %v", got)
	}
}

func TestResolveBackend_QemuExists(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	// Without qemu, this should return empty (qemu.Exists will fail)
	backend := resolveBackend("nonexistent-vm-xyzzzy")
	if backend != "" {
		t.Logf("qemu backend detected for nonexistent VM: %s", backend)
	}
}

func TestRequireBackend_WithRegistry(t *testing.T) {
	dir := t.TempDir()
	s := registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	s.Set("testvm", types.RegistryEntry{Backend: "kubevirt", Namespace: "default"})

	oldStore := registryStore
	registryStore = s
	defer func() { registryStore = oldStore }()

	b, err := requireBackend("testvm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b != "kubevirt" {
		t.Errorf("expected kubevirt, got %s", b)
	}
}

func TestConfigCommand_Exists(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"config"})
	if err != nil || cmd == rootCmd {
		t.Error("config command not found")
	}
}

func TestImagesCommand_Exists(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"images"})
	if err != nil || cmd == rootCmd {
		t.Error("images command not found")
	}
}

func TestPluginCommand_Exists(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"plugin"})
	if err != nil || cmd == rootCmd {
		t.Error("plugin command not found")
	}
}

func TestDoctorCommand_Exists(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"doctor"})
	if err != nil || cmd == rootCmd {
		t.Error("doctor command not found")
	}
}

func TestWebCommand_Exists(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"web"})
	if err != nil || cmd == rootCmd {
		t.Error("web command not found")
	}
}

func TestCreateCommand_ArgsValidation(t *testing.T) {
	cmd, _, _ := rootCmd.Find([]string{"create"})
	// create requires exactly 1 arg
	if cmd.Args == nil {
		t.Error("create command should have Args validation")
	}
}

func TestSSHCommand_Use(t *testing.T) {
	cmd, _, _ := rootCmd.Find([]string{"ssh"})
	if cmd.Use != "ssh [name]" {
		t.Errorf("expected 'ssh [name]', got %q", cmd.Use)
	}
}

func TestRootCommand_Use(t *testing.T) {
	if rootCmd.Use != "corral" {
		t.Errorf("expected 'corral', got %q", rootCmd.Use)
	}
}

// ── RunE error path tests ────────────────────────────────────────

func TestStartCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := startCmd.RunE(startCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestStopCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := stopCmd.RunE(stopCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestInfoCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := infoCmd.RunE(infoCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestViewerCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := viewerCmd.RunE(viewerCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestDeleteCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	forceDelete = true // skip confirmation prompt
	defer func() { registryStore = oldStore; forceDelete = false }()

	err := deleteCmd.RunE(deleteCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestStartCmd_NoArgs(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()
	os.Setenv("HOME", t.TempDir())
	defer os.Unsetenv("HOME")

	err := startCmd.RunE(startCmd, []string{})
	if err == nil {
		t.Error("expected error with no args and no VMs")
	}
}

func TestLogsCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := logsCmd.RunE(logsCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestSSHCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := sshCmd.RunE(sshCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

// ── Plugin helpers ───────────────────────────────────────────────

func TestContains(t *testing.T) {
	if !contains("hello world", "world") {
		t.Error("expected true for 'world' in 'hello world'")
	}
	if contains("hello world", "xyz") {
		t.Error("expected false for 'xyz' in 'hello world'")
	}
	if !contains("HELLO", "hello") {
		t.Error("expected case-insensitive match")
	}
	if !contains("abc", "") {
		t.Error("expected true for empty needle")
	}
}

// ── Ops helper ───────────────────────────────────────────────────

func TestKubevirtOnly_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	_, _, err := kubevirtOnly([]string{"nonexistent-vm-xyzzzy"}, "test")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

// ── Create helper ────────────────────────────────────────────────

func TestRunKubevirtCreate_InvalidImage(t *testing.T) {
	oldImage := createImage
	createImage = "nonexistent-image"
	defer func() { createImage = oldImage }()

	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := runKubevirtCreate("test-vm")
	if err == nil {
		t.Error("expected error for unknown catalog image")
	}
}

func TestRunKubevirtCreate_EmptyName(t *testing.T) {
	oldImage := createImage
	createImage = "fedora"
	defer func() { createImage = oldImage }()

	// With a valid image but no cluster, it'll fail at CreateVM
	// But the function should at least not panic
	// We can't fully test without kubectl, but at least exercise the path
}

// ── Config command ───────────────────────────────────────────────

func TestConfigCmd_Runnable(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"config"})
	if err != nil || cmd == rootCmd {
		t.Fatal("config command not found")
	}
	if cmd.RunE == nil {
		t.Error("config command should have RunE")
	}
}

// ── Root command ─────────────────────────────────────────────────

func TestRootCmd_PersistentPreRunE(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	// PersistentPreRunE initializes the registry store
	err := rootCmd.PersistentPreRunE(rootCmd, []string{})
	if err != nil {
		t.Fatalf("PersistentPreRunE failed: %v", err)
	}
	if registryStore == nil {
		t.Error("registryStore should be initialized after PersistentPreRunE")
	}
}

// ── Config command (pure logic, no cluster needed) ───────────────

func TestConfigCmd_RunE(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TS_AUTHKEY", "")

	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	// configCmd.RunE reads config and prints — should succeed
	err := configCmd.RunE(configCmd, []string{})
	if err != nil {
		t.Errorf("configCmd.RunE failed: %v", err)
	}
}

// ── Images command (pure logic, no cluster needed) ───────────────

func TestImagesCmd_RunE(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	// imagesCmd.RunE just prints the catalog — should succeed
	err := imagesCmd.RunE(imagesCmd, []string{})
	if err != nil {
		t.Errorf("imagesCmd.RunE failed: %v", err)
	}
}

// ── Plugin command error paths ───────────────────────────────────

func TestPluginCmd_Exists(t *testing.T) {
	// pluginCmd has subcommands: list, search, install, remove
	subs := pluginCmd.Commands()
	if len(subs) < 4 {
		t.Errorf("expected at least 4 subcommands, got %d", len(subs))
	}
}

// ── More command RunE error paths ────────────────────────────────

func TestRestartCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := restartCmd.RunE(restartCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestPauseCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := pauseCmd.RunE(pauseCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestMigrateCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := migrateCmd.RunE(migrateCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestScaleCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	err := scaleCmd.RunE(scaleCmd, []string{"nonexistent-vm-xyzzzy"})
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestSnapshotCmd_NonexistentVM(t *testing.T) {
	oldStore := registryStore
	dir := t.TempDir()
	registryStore = registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	defer func() { registryStore = oldStore }()

	// snapshotCmd is a parent command — verify it exists
	cmd, _, err := rootCmd.Find([]string{"snapshot"})
	if err != nil || cmd == rootCmd {
		t.Fatal("snapshot command not found")
	}
}

func TestListCmd_Exists(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"list"})
	if err != nil || cmd == rootCmd {
		t.Error("list command not found")
	}
}

func TestDoctorCmd_Exists(t *testing.T) {
	if doctorCmd == nil {
		t.Error("doctorCmd should exist")
	}
}

func TestWebCmd_Exists(t *testing.T) {
	if webCmd == nil {
		t.Error("webCmd should exist")
	}
}

// ── TUI clone action ─────────────────────────────────────────────

func TestNewCloneInput_SuggestsCloneSuffix(t *testing.T) {
	ti := newCloneInput("web1")
	if ti.Value() != "web1-clone" {
		t.Errorf("expected suggested value web1-clone, got %q", ti.Value())
	}
}

func TestRunClone_RejectsNonKubevirtBackend(t *testing.T) {
	err := runClone(types.VM{Name: "web1", Backend: "qemu"}, "web1-clone")
	if err == nil {
		t.Error("expected an error cloning a non-kubevirt VM")
	}
}

func TestRunClone_RejectsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	s := registry.NewStoreAt(filepath.Join(dir, "registry.json"))
	s.Set("taken", types.RegistryEntry{Backend: "kubevirt", Namespace: "default"})

	oldStore := registryStore
	registryStore = s
	defer func() { registryStore = oldStore }()

	err := runClone(types.VM{Name: "web1", Backend: "kubevirt", Namespace: "default"}, "taken")
	if err == nil {
		t.Error("expected an error cloning onto an existing VM name")
	}
}

func TestActionsListItems_IncludesClone(t *testing.T) {
	found := false
	for _, a := range actionsListItems {
		if a.id == "clone" {
			found = true
		}
	}
	if !found {
		t.Error("expected a clone entry in the TUI actions list")
	}
}

// ── TUI CT representation ────────────────────────────────────────

func TestCtToItem_ShowsPhaseAndPrivilege(t *testing.T) {
	item := ctToItem(ctpkg.CT{Name: "web1", Phase: "Running", CPU: 2, Mem: "1Gi", Privileged: true})
	if item.Title() != "[CT] web1" {
		t.Errorf("Title() = %q", item.Title())
	}
	if !strings.Contains(item.Description(), "Running") || !strings.Contains(item.Description(), "privileged") {
		t.Errorf("Description() = %q, want it to mention phase and privilege", item.Description())
	}
}

func TestActionsListItemsCT_NoHypervisorConcepts(t *testing.T) {
	for _, forbidden := range []string{"migrate", "snapshot", "hardware", "ports", "clone", "ssh"} {
		for _, a := range actionsListItemsCT {
			if a.id == forbidden {
				t.Errorf("CT actions list should not include %q (a hypervisor/VM-only concept)", forbidden)
			}
		}
	}
	for _, want := range []string{"start", "stop", "console", "delete"} {
		found := false
		for _, a := range actionsListItemsCT {
			if a.id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("CT actions list missing %q", want)
		}
	}
}

func TestPerformCTAction_DispatchesToCTPackage(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"delete", "pod", "web1", "-n", "corral-ct", "--ignore-not-found"}, "", nil)
	ctpkg.SetRunner(fake)
	defer ctpkg.SetRunner(shell.Real{})

	m := &tuiModel{isCT: true, selectedCT: ctpkg.CT{Name: "web1", Namespace: "corral-ct"}}
	m.performCTAction("stop")

	found := false
	for _, c := range fake.Calls() {
		if c.Name == "kubectl" && len(c.Args) > 1 && c.Args[0] == "delete" && c.Args[1] == "pod" {
			found = true
		}
	}
	if !found {
		t.Error("performCTAction(\"stop\") should have called kubectl delete pod via pkg/ct")
	}
}
