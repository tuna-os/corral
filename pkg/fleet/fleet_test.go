package fleet

// Table-driven tests for Resolve — the pure selector logic that maps a
// user-supplied ID/name onto exactly one VM. The subtle cases (ID colliding
// with another VM's name, duplicate names across contexts) are the ones that
// matter and were previously only spot-covered.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

func TestResolveRequiresScopedIDForDuplicateNames(t *testing.T) {
	vms := []types.VM{{Name: "dev", Backend: "qemu", Namespace: "local"}, {Name: "dev", Backend: "kubevirt", Context: "lab", Namespace: "vms"}}
	for i := range vms {
		vms[i].SetIdentity()
	}
	if _, err := Resolve(vms, "dev"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity, got %v", err)
	}
	got, err := Resolve(vms, vms[1].ID)
	if err != nil || got.Context != "lab" {
		t.Fatalf("scoped resolve: %+v %v", got, err)
	}
}

func TestResolve_ExactID(t *testing.T) {
	vms := []types.VM{
		{ID: "qemu.local.dev", Name: "dev"},
		{ID: "kubevirt.lab.web", Name: "web"},
	}
	got, err := Resolve(vms, "qemu.local.dev")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "dev" {
		t.Errorf("got %+v, want the dev VM", got)
	}
}

func TestResolve_UniqueName(t *testing.T) {
	vms := []types.VM{
		{ID: "qemu.local.dev", Name: "dev"},
		{ID: "kubevirt.lab.web", Name: "web"},
	}
	got, err := Resolve(vms, "web")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != "kubevirt.lab.web" {
		t.Errorf("got %+v, want the web VM", got)
	}
}

func TestResolve_EmptyInventory(t *testing.T) {
	_, err := Resolve(nil, "anything")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("empty inventory: err = %v, want does-not-exist", err)
	}
}

func TestResolve_UnknownSelector(t *testing.T) {
	vms := []types.VM{{ID: "qemu.local.dev", Name: "dev"}}
	_, err := Resolve(vms, "nope")
	if err == nil || !strings.Contains(err.Error(), `"nope" does not exist`) {
		t.Fatalf("err = %v, want does-not-exist naming the selector", err)
	}
}

func TestResolve_DuplicateNameIsAmbiguous(t *testing.T) {
	vms := []types.VM{
		{ID: "qemu.local.dev", Name: "dev"},
		{ID: "kubevirt.lab.dev", Name: "dev"},
	}
	_, err := Resolve(vms, "dev")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v, want ambiguous", err)
	}
}

func TestResolve_IDCollidingWithAnotherNameIsAmbiguous(t *testing.T) {
	// The selector matches VM 1's ID *and* VM 2's name — a bare selector is
	// only valid when it resolves to exactly one instance.
	vms := []types.VM{
		{ID: "web", Name: "webserver"},
		{ID: "qemu.local.other", Name: "web"},
	}
	_, err := Resolve(vms, "web")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v, want ambiguous when an ID collides with a name", err)
	}
}

func TestResolve_PrefersFullyScopedID(t *testing.T) {
	// Same string is a *different* VM's name: the scoped ID wins for the
	// caller who quotes the full identifier... but the contract says the
	// selector must be unique, so this stays ambiguous. Assert the returned
	// behavior is deterministic (ambiguity), documenting the contract.
	vms := []types.VM{
		{ID: "qemu.local.web", Name: "web"},
		{ID: "web", Name: "other"},
	}
	_, err := Resolve(vms, "web")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v, want ambiguous", err)
	}
}

func TestResolve_MultipleMatchesSameVM(t *testing.T) {
	// Selector equals the VM's own ID and its own name: still exactly one
	// VM — must NOT be ambiguous.
	vms := []types.VM{{ID: "dev", Name: "dev"}}
	got, err := Resolve(vms, "dev")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != "dev" {
		t.Errorf("got %+v, want the dev VM", got)
	}
}

// hermeticListEnv isolates config resolution from the host: HOME and
// XDG_CONFIG_HOME both point at throwaway dirs (ConfigDir prefers
// XDG_CONFIG_HOME, so HOME alone does not isolate), and a seeded config
// selects kubevirt as the default backend. The kubevirt target is therefore
// always listed deterministically — independent of whatever config or
// kubeconfig the host running the test happens to have — so callers must
// install fakeEmptyCluster to keep the List path free of real execs.
func hermeticListEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CORRAL_INCUS_REMOTE", "")
	t.Setenv("CORRAL_LIBVIRT_URI", "")
	t.Setenv("CORRAL_KUBE_CONTEXT", "")
	t.Setenv("KUBECONFIG", filepath.Join(home, "nonexistent"))
	cfg := "default:\n    backend: kubevirt\n"
	if err := os.MkdirAll(filepath.Join(xdg, "corral"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "corral", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeEmptyCluster intercepts every kubectl call the List path makes and
// answers with an empty inventory. It installs via
// kubevirt.SetDefaultRunner — the same seam every backend test already
// trusts — so List performs zero real execs even when a real (or stub)
// kubectl sits on PATH.
func fakeEmptyCluster(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	empty := `{"items":[]}`
	fake.AddResponseKV("kubectl", []string{"get", "vms", "-A", "-o", "json"}, empty, nil)
	fake.AddResponseKV("kubectl", []string{"get", "pods", "-A", "-l", "kubevirt.io=virt-launcher", "-o", "json"}, empty, nil)
	fake.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, empty, nil)
	fake.AddResponseKV("kubectl", []string{"get", "nodes", "-o", "json"}, empty, nil)
	kubevirt.SetDefaultRunner(fake)
	t.Cleanup(func() { kubevirt.SetDefaultRunner(nil) })
	return fake
}

func TestList_EmptyLocalQemu(t *testing.T) {
	hermeticListEnv(t)
	fake := fakeEmptyCluster(t)

	res := List(context.Background())
	if len(res.Errors) != 0 {
		t.Errorf("expected no errors for empty local qemu, got: %v", res.Errors)
	}
	if len(res.VMs) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(res.VMs))
	}
	if len(fake.Calls()) == 0 {
		t.Error("expected the kubevirt target to be listed via the fake runner; no kubectl calls were intercepted (is kubevirt still a target?)")
	}
}

// TestList_EmptyLocalQemu_StubKubectlOnPath is the regression variant: a
// kubectl that always fails, shadowed earlier on PATH, must still stay green
// because the fake intercepts before any real exec happens.
func TestList_EmptyLocalQemu_StubKubectlOnPath(t *testing.T) {
	hermeticListEnv(t)
	fake := fakeEmptyCluster(t)

	stubDir := t.TempDir()
	stub := "#!/bin/sh\necho 'stub kubectl must never run' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(stubDir, "kubectl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := List(context.Background())
	if len(res.Errors) != 0 {
		t.Errorf("expected no errors with stub kubectl on PATH, got: %v", res.Errors)
	}
	if len(res.VMs) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(res.VMs))
	}
	if len(fake.Calls()) == 0 {
		t.Error("expected kubectl calls to be intercepted by the fake, got none")
	}
}

func TestList_ContextCanceled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORRAL_INCUS_REMOTE", "")
	t.Setenv("CORRAL_LIBVIRT_URI", "")
	t.Setenv("CORRAL_KUBE_CONTEXT", "")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "nonexistent"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	res := List(ctx)
	if len(res.Errors) == 0 {
		t.Fatal("expected errors when context is canceled, got none")
	}
	if !strings.Contains(res.Errors["local"], "context canceled") {
		t.Errorf("expected context canceled error, got: %v", res.Errors)
	}
}

func TestList_UnsupportedBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRAL_INCUS_REMOTE", "")
	t.Setenv("CORRAL_LIBVIRT_URI", "")
	t.Setenv("CORRAL_KUBE_CONTEXT", "")
	t.Setenv("KUBECONFIG", filepath.Join(home, "nonexistent"))

	// Create config with unsupported backend manually
	configDir := filepath.Join(home, ".config", "corral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `
contexts:
  - name: weird-target
    backend: unsupported-xyz
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	res := List(context.Background())
	if res.Errors == nil || !strings.Contains(res.Errors["weird-target"], "unsupported backend") {
		t.Errorf("expected unsupported backend error for weird-target, got: %v", res.Errors)
	}
}
