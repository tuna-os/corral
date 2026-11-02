//go:build bootc

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
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
		"quay.io/centos-bootc/centos-bootc:stream9", "ssh-ed25519 AAAAtest", "4G", 2, "", kubevirt.Placement{})
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
		"quay.io/centos-bootc/centos-bootc:stream9", "ssh-ed25519 AAAAtest", "4G", 2, "", kubevirt.Placement{}); err != nil {
		t.Fatalf("finishVM: %v", err)
	}

	store, err := registry.NewStore()
	if err != nil {
		t.Fatalf("registry.NewStore: %v", err)
	}
	entry, ok := store.GetRef(types.InstanceRef{Backend: "kubevirt", Namespace: "tailvm", Name: "myvm"})
	if !ok {
		t.Fatal("expected a registry entry for myvm")
	}
	if entry.Backend != "kubevirt" || entry.Namespace != "tailvm" {
		t.Errorf("entry = %+v, want Backend=kubevirt Namespace=tailvm", entry)
	}
}

func TestFinishVM_RegistryFailureRetainsCreatedVM(t *testing.T) {
	for _, failure := range []string{"open", "write"} {
		t.Run(failure, func(t *testing.T) {
			fake := shell.NewFake()
			fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
			kubevirt.SetApplyRunner(fake)
			kubevirt.SetPackageRunner(fake)
			t.Cleanup(func() {
				kubevirt.SetApplyRunner(shell.Real{})
				kubevirt.SetPackageRunner(shell.Real{})
			})
			home := ""
			if failure == "write" {
				home = filepath.Join(t.TempDir(), "blocked-home")
				if err := os.WriteFile(home, []byte("existing data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)

			err := finishVM("myvm", "tailvm", "myvm-bootc-disk",
				"quay.io/centos-bootc/centos-bootc:stream9", "ssh-ed25519 AAAAtest", "4G", 2, "", kubevirt.Placement{})
			if err == nil || !strings.Contains(err.Error(), "created in ns/tailvm") || !strings.Contains(err.Error(), "VM and disk retained") {
				t.Fatalf("missing created-resource persistence error: %v", err)
			}
			var appliedVM bool
			for _, call := range fake.Calls() {
				if strings.Contains(call.Stdin, `"kind":"VirtualMachine"`) {
					appliedVM = true
				}
				if len(call.Args) > 0 && call.Args[0] == "delete" {
					t.Fatal("registry failure deleted a created resource")
				}
			}
			if !appliedVM {
				t.Fatal("expected VM creation before registry failure")
			}
		})
	}
}
