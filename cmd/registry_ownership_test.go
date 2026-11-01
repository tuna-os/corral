package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/fleet"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

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
			target := config.ContextConfig{Name: "selected-alias", Backend: tc.backend, Context: "selected-target"}
			createScratch(t, target)
			t.Setenv("CORRAL_KUBE_CONTEXT", "")
			t.Setenv("CORRAL_INCUS_REMOTE", "")
			t.Setenv("CORRAL_LIBVIRT_URI", "")
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
			if err := config.SetDefaultContext(target.Name); err != nil {
				t.Fatal(err)
			}
			rootContext = ""
			if got := createContext(tc.read); got != target.Context {
				t.Fatalf("selected backend default = %q, want %q", got, target.Context)
			}
			t.Setenv(tc.env, "environment-target")
			if got := createContext(tc.read); got != "environment-target" {
				t.Fatalf("environment backend default = %q, want environment-target", got)
			}
			for _, explicit := range []string{target.Name, "raw-target", "local"} {
				rootContext = explicit
				want := explicit
				if explicit == target.Name {
					want = target.Context
				} else if explicit == "local" {
					want = ""
				}
				got := createContext(func() string {
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

// Exercise real create/import callers and reopen the JSON they persist, rather
// than testing a duplicate of the ownership predicate or command arguments.
func TestKubevirtWritersAndDeleteRetireOnlyTheSelectedTarget(t *testing.T) {
	for _, writer := range []string{"create-default", "create-alias", "import-url", "import-file", "windows-tui"} {
		t.Run(writer, func(t *testing.T) {
			fake := placementScratch(t)
			fake.AddPrefixResponse("kubectl", "", nil)
			fake.AddPrefixResponse("virtctl image-upload", "uploaded", nil)
			// Named-context manifest helpers bind DefaultKubectl rather than
			// the package runners installed by placementScratch.
			oldKubectl := shell.DefaultKubectl
			shell.DefaultKubectl = fake
			t.Cleanup(func() { shell.DefaultKubectl = oldKubectl })
			t.Setenv("PATH", t.TempDir())
			t.Setenv("CORRAL_KUBE_CONTEXT", "")
			t.Setenv("CORRAL_INCUS_REMOTE", "")
			t.Setenv("CORRAL_LIBVIRT_URI", "")
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
			target := config.ContextConfig{Name: "production-alias", Backend: "kubevirt", Context: "production-cluster"}
			if err := config.AddContext(target); err != nil {
				t.Fatal(err)
			}
			if err := config.SetDefaultContext(target.Name); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "registry.json")
			registryStore = registry.NewStoreAt(path)
			ref := types.InstanceRef{Backend: "kubevirt", Context: target.Context, Namespace: "team-a", Name: "desk"}
			foreign := []types.InstanceRef{
				{Backend: "kubevirt", Context: "other-cluster", Namespace: ref.Namespace, Name: ref.Name},
				{Backend: "kubevirt", Context: ref.Context, Namespace: "team-b", Name: ref.Name},
				{Peer: "other-peer", Backend: ref.Backend, Context: ref.Context, Namespace: ref.Namespace, Name: ref.Name},
				{Backend: "incus", Context: ref.Context, Namespace: "incus", Name: ref.Name},
			}
			for _, other := range foreign {
				if err := registryStore.SetRef(other, types.RegistryEntry{Username: "foreign", Password: other.String()}); err != nil {
					t.Fatal(err)
				}
			}
			legacy := types.RegistryEntry{Backend: "kubevirt", Namespace: ref.Namespace, Password: "unbound-secret"}
			if err := registryStore.Set(ref.Name, legacy); err != nil {
				t.Fatal(err)
			}
			before := registry.NewStoreAt(path).All()
			oldNS, oldPassword := createNamespace, createCloudInitPassword
			oldImage, oldImport, oldPVC := createImage, createImport, createPVC
			oldImportRunner, oldSelected := importRunner, selectedVM
			t.Cleanup(func() {
				createNamespace, createCloudInitPassword = oldNS, oldPassword
				createImage, createImport, createPVC = oldImage, oldImport, oldPVC
				importRunner, selectedVM = oldImportRunner, oldSelected
			})
			rootContext = ""
			createNamespace, createCloudInitPassword = ref.Namespace, "selected-secret"
			createImage, createImport, createPVC = "", "", ""
			createKubevirt, createIncus, createBootc = true, false, ""
			createContainerDisk = "quay.io/containerdisks/fedora:latest"
			createDedicatedCPUs, createIsolateEmulator, createTags = false, false, nil
			importRunner = fake
			var err error
			switch writer {
			case "create-default":
				err = executeCreate(ref.Name, true)
			case "create-alias":
				rootContext = target.Name
				err = executeCreate(ref.Name, true)
			case "import-url":
				err = runImport(ref.Name, ref.Namespace, "https://example.test/disk.qcow2", "20G", "4G", 2)
			case "import-file":
				source := filepath.Join(t.TempDir(), "disk.qcow2")
				if err := os.WriteFile(source, []byte("disk input"), 0600); err != nil {
					t.Fatal(err)
				}
				err = runImport(ref.Name, ref.Namespace, source, "20G", "4G", 2)
			case "windows-tui":
				iso := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Length", "1024")
				}))
				t.Cleanup(iso.Close)
				err = createWindowsFromTUI(ref.Name, target, iso.URL+"/windows.iso")
			}
			if err != nil {
				t.Fatal(err)
			}
			persisted := registry.NewStoreAt(path).All()
			entry, ok := persisted[ref.String()]
			if !ok || entry.Backend != ref.Backend || entry.Context != ref.Context || entry.Namespace != ref.Namespace {
				t.Fatalf("persisted target = %+v (found %v), want %s", entry, ok, ref.String())
			}
			for key, original := range before {
				if persisted[key] != original {
					t.Fatalf("create changed unrelated credentials at %s", key)
				}
			}
			listedKubevirtVMs(t, fake, types.VM{Name: ref.Name, Namespace: ref.Namespace})
			forceDeletes(t)
			if err := deleteCmd.RunE(deleteCmd, []string{ref.String()}); err != nil {
				t.Fatal(err)
			}
			if after := registry.NewStoreAt(path).All(); !reflect.DeepEqual(after, before) {
				t.Fatalf("delete did not retire exactly its own credentials: got keys %v, want keys %v", registry.NewStoreAt(path).Names(), mapKeys(before))
			}
			listedKubevirtVMs(t, fake)
			if _, err := requireOrPrompt([]string{ref.Name}, "ssh"); err == nil {
				t.Fatal("retained legacy credentials resolved a deleted VM as live")
			} else {
				var notFound fleet.NotFoundError
				if !errors.As(err, &notFound) {
					t.Fatalf("unlisted VM error = %v, want NotFoundError", err)
				}
			}
			if _, ok := registry.NewStoreAt(path).GetRef(ref); ok {
				t.Fatal("unbound legacy credentials were reused for the deleted target")
			}
			data, err := os.ReadFile(path)
			if err != nil || !json.Valid(data) || !strings.Contains(string(data), "unbound-secret") {
				t.Fatalf("legacy user data not retained as valid JSON: %v", err)
			}
		})
	}
}

func mapKeys(entries map[string]types.RegistryEntry) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	return keys
}

func TestDeleteRetiresOnlyExactlyBoundLegacyCredentials(t *testing.T) {
	for _, ownership := range []string{"matching", "foreign-context", "foreign-namespace", "foreign-peer", "unbound"} {
		t.Run(ownership, func(t *testing.T) {
			ref := types.InstanceRef{Backend: "kubevirt", Context: "cluster-a", Namespace: "team-a", Name: "desk"}
			fake := kubevirtVMScratch(t, ref.Name, types.RegistryEntry{Backend: ref.Backend, Context: ref.Context, Namespace: ref.Namespace})
			entry := types.RegistryEntry{Backend: ref.Backend, Context: ref.Context, Namespace: ref.Namespace, Password: "legacy-secret"}
			switch ownership {
			case "foreign-context":
				entry.Context = "cluster-b"
			case "foreign-namespace":
				entry.Namespace = "team-b"
			case "foreign-peer":
				entry.Peer = "other-peer"
			case "unbound":
				entry.Context = ""
			}
			if err := registryStore.Set(ref.Name, entry); err != nil {
				t.Fatal(err)
			}
			forceDeletes(t)
			if err := deleteCmd.RunE(deleteCmd, []string{ref.String()}); err != nil {
				t.Fatal(err)
			}
			got, retained := registryStore.Get(ref.Name)
			if ownership == "matching" {
				if retained {
					t.Fatal("matching bound legacy credentials survived successful delete")
				}
			} else if !retained || got != entry {
				t.Fatal("delete changed credentials whose ownership was not established")
			}
			listedKubevirtVMs(t, fake)
			if _, err := requireOrPrompt([]string{ref.Name}, "ssh"); err == nil {
				t.Fatal("retained registry data proved a deleted VM live")
			}
		})
	}
}

func TestTUIDeleteRetiresCredentialsOnlyAfterBackendSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "backend-failure"
		}
		t.Run(name, func(t *testing.T) {
			fake := remoteVMScratch(t, "desk", types.RegistryEntry{Backend: "incus", Context: "remote-a"})
			vm := types.VM{Name: "desk", Backend: "incus", Context: "remote-a", Namespace: "incus"}
			foreign := vm.Ref()
			foreign.Context = "remote-b"
			if err := registryStore.SetRef(foreign, types.RegistryEntry{Password: "foreign-secret"}); err != nil {
				t.Fatal(err)
			}
			before := registryStore.All()
			if fail {
				fake.AddResponse("incus delete remote-a:desk --force", "denied", errors.New("delete denied"))
			}
			_, kind := (actionTarget{vm: vm}).run("delete")
			after := registryStore.All()
			if fail {
				if kind != "error" || !reflect.DeepEqual(after, before) {
					t.Fatal("failed TUI delete changed saved credentials")
				}
			} else {
				delete(before, vm.Ref().String())
				if kind != "ok" || !reflect.DeepEqual(after, before) {
					t.Fatal("TUI delete did not retire exactly its target credentials")
				}
			}
		})
	}
}
