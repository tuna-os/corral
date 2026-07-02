//go:build bootc

package main

import (
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/shell"
)

// finishVM needs the bootc plugin compiled in (kubevirt.GenerateBootcVM
// returns nil otherwise) — this file only builds with -tags bootc, same as
// CI's test run (see Justfile / .github/workflows/ci.yml).

func TestFinishVM_AppliesVMAndExposesConsole(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "ingressclass", "tailscale"}, "tailscale", nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
	})
	t.Setenv("HOME", t.TempDir())

	err := finishVM("myvm", "tailvm", "myvm-bootc-disk",
		"quay.io/centos-bootc/centos-bootc:stream9", "ssh-ed25519 AAAAtest", "4G", 2, "")
	if err != nil {
		t.Fatalf("finishVM: %v", err)
	}

	applies := 0
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
		}
	}
	// final VM + proxy RBAC + proxy Service + proxy Deployment
	if applies < 4 {
		t.Errorf("applied %d manifests, want >= 4 (VM + proxy trio)", applies)
	}
}

func TestFinishVM_RecordsRegistryEntry(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
	})
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := finishVM("myvm", "tailvm", "myvm-bootc-disk",
		"quay.io/centos-bootc/centos-bootc:stream9", "ssh-ed25519 AAAAtest", "4G", 2, ""); err != nil {
		t.Fatalf("finishVM: %v", err)
	}

	store, err := registry.NewStore()
	if err != nil {
		t.Fatalf("registry.NewStore: %v", err)
	}
	entry, ok := store.Get("myvm")
	if !ok {
		t.Fatal("expected a registry entry for myvm")
	}
	if entry.Backend != "kubevirt" || entry.Namespace != "tailvm" {
		t.Errorf("entry = %+v, want Backend=kubevirt Namespace=tailvm", entry)
	}
}
