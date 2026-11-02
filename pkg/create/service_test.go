package create

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/lima"
	"github.com/tuna-os/corral/pkg/types"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRAL_DEFAULT_BACKEND", "qemu")
}

func TestBackendSelection(t *testing.T) {
	sentinel := errors.New("selected backend")
	for _, tt := range []struct {
		name string
		opts Options
		want string
	}{
		{"default", Options{}, "qemu"},
		{"backend override", Options{Backend: "libvirt"}, "libvirt"},
		{"context", Options{Context: "remote"}, "incus"},
		{"backend beats context", Options{Backend: "libvirt", Context: "remote"}, "libvirt"},
		{"incus flag", Options{Backend: "libvirt", Incus: true}, "incus"},
		{"kubevirt flag wins", Options{Incus: true, Kubevirt: true}, "kubevirt"},
		{"container source", Options{Values: lima.Values{ContainerDisk: "image"}}, "kubevirt"},
		{"import source", Options{Values: lima.Values{Import: "disk"}}, "kubevirt"},
		{"pvc source", Options{PVC: "disk"}, "kubevirt"},
		{"incus source", Options{Incus: true, Values: lima.Values{Image: "images:ubuntu"}}, "incus"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfig(t)
			if err := config.AddContext(config.ContextConfig{Name: "remote", Backend: "incus", Context: "remote"}); err != nil {
				t.Fatal(err)
			}
			svc := NewService(nil, nil)
			got := ""
			svc.QEMUCreate = func(types.CreateOpts) error { got = "qemu"; return sentinel }
			svc.LibvirtCreate = func(types.CreateOpts) error { got = "libvirt"; return sentinel }
			svc.KubevirtCreate = func(types.CreateOpts) error { got = "kubevirt"; return sentinel }
			svc.IncusCreate = func(incus.CreateOpts) error { got = "incus"; return sentinel }
			if err := svc.Execute("vm", tt.opts); !errors.Is(err, sentinel) || got != tt.want {
				t.Fatalf("selected %q, err %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestValidationPrecedesCreation(t *testing.T) {
	for _, tt := range []struct {
		opts Options
		want string
	}{
		{Options{Kargs: []string{"a=1"}}, "--karg needs --bootc"},
		{Options{Values: lima.Values{Bootc: "image"}, Kargs: []string{"two words"}}, "not one kernel argument"},
		{Options{IsolateEmulator: true}, "--dedicated-cpus"},
		{Options{DedicatedCPUs: true, InstanceType: "sized"}, "--instancetype"},
		{Options{Tags: []string{"bad tag=value"}}, "tag"},
		{Options{Backend: "qemu", DedicatedCPUs: true}, "KubeVirt backend only"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			isolateConfig(t)
			svc := NewService(nil, nil)
			svc.QEMUCreate = func(types.CreateOpts) error { t.Fatal("creation before validation"); return nil }
			if err := svc.Execute("vm", tt.opts); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestExistingInstanceAndForce(t *testing.T) {
	isolateConfig(t)
	svc := NewService(func(string) string { return "incus" }, nil)
	called := false
	svc.QEMUCreate = func(types.CreateOpts) error { called = true; return nil }
	if err := svc.Execute("vm", Options{}); err == nil || !strings.Contains(err.Error(), "already exists (backend: incus)") || called {
		t.Fatalf("collision: %v, created=%v", err, called)
	}
	if err := svc.Execute("vm", Options{Force: true}); err != nil || !called {
		t.Fatalf("force: %v, created=%v", err, called)
	}
}

func TestLimaRequestDoesNotLeak(t *testing.T) {
	isolateConfig(t)
	path := filepath.Join(t.TempDir(), "vm.yaml")
	if err := os.WriteFile(path, []byte("cpus: 6\nmemory: 16GiB\nimages:\n  - location: disk.raw\n"), 0600); err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, nil)
	sentinel := errors.New("created")
	svc.KubevirtCreate = func(opts types.CreateOpts) error {
		if opts.Name != "vm" || opts.CPU != 6 || opts.Mem != "16G" || opts.ImportURL != "disk.raw" {
			t.Fatalf("translated options: %+v", opts)
		}
		return sentinel
	}
	opts := Options{File: path, Values: lima.Values{CPU: 2, Mem: "4G"}}
	if err := svc.Execute("vm", opts); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if opts.CPU != 2 || opts.Mem != "4G" || opts.Kubevirt || opts.Import != "" {
		t.Fatalf("request mutated: %+v", opts)
	}
	called := false
	svc.QEMUCreate = func(types.CreateOpts) error { called = true; return nil }
	if err := svc.Execute("next", Options{}); err != nil || !called {
		t.Fatalf("previous request leaked: %v", err)
	}
}

func TestCreateContextUsesBackendDefaultsAndExplicitTargets(t *testing.T) {
	for _, tc := range []struct {
		backend string
		env     string
		read    func() string
	}{
		{"kubevirt", "CORRAL_KUBE_CONTEXT", config.KubeContext},
		{"incus", "CORRAL_INCUS_REMOTE", config.IncusRemote},
		{"libvirt", "CORRAL_LIBVIRT_URI", config.LibvirtURI},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			isolateConfig(t)
			target := config.ContextConfig{Name: "selected-alias", Backend: tc.backend, Context: "selected-target"}
			if err := config.AddContext(target); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CORRAL_KUBE_CONTEXT", "")
			t.Setenv("CORRAL_INCUS_REMOTE", "")
			t.Setenv("CORRAL_LIBVIRT_URI", "")
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
			if err := config.SetDefaultContext(target.Name); err != nil {
				t.Fatal(err)
			}
			r := request{}
			if got := r.createContext(tc.read); got != target.Context {
				t.Fatalf("selected backend default = %q, want %q", got, target.Context)
			}
			t.Setenv(tc.env, "environment-target")
			if got := r.createContext(tc.read); got != "environment-target" {
				t.Fatalf("environment backend default = %q, want environment-target", got)
			}
			for _, explicit := range []string{target.Name, "raw-target", "local"} {
				r.Context = explicit
				want := explicit
				if explicit == target.Name {
					want = target.Context
				} else if explicit == "local" {
					want = ""
				}
				got := r.createContext(func() string {
					t.Fatal("explicit context evaluated the backend default")
					return ""
				})
				if got != want {
					t.Fatalf("explicit context %q = %q, want %q", explicit, got, want)
				}
			}
		})
	}
}
