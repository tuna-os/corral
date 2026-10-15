package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/virtualbox"
)

// List asks each configured context's own backend, bound to that context: an
// Incus remote, a libvirt URI, the local VirtualBox. A failing context is a
// named partial error, and the listing still records which command ran
// against which context (#214).
func TestList_EachContextIsListedByItsOwnBackend(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CORRAL_INCUS_REMOTE", "")
	t.Setenv("CORRAL_LIBVIRT_URI", "")
	t.Setenv("CORRAL_KUBE_CONTEXT", "")
	t.Setenv("KUBECONFIG", filepath.Join(home, "nonexistent"))
	cfg := `contexts:
  - name: lab
    backend: incus
    context: lab
  - name: hv
    backend: libvirt
    context: qemu+ssh://hv/system
  - name: vbox
    backend: virtualbox
`
	if err := os.MkdirAll(filepath.Join(xdg, "corral"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "corral", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := shell.NewFake()
	down := errors.New("unreachable")
	fake.AddPrefixResponse("incus", "", down)
	fake.AddPrefixResponse("virsh", "", down)
	fake.AddPrefixResponse("VBoxManage", "", down)
	incus.SetRunner(fake)
	libvirt.SetRunner(fake)
	virtualbox.SetRunner(fake)
	t.Cleanup(func() {
		incus.SetRunner(shell.Real{})
		libvirt.SetRunner(shell.Real{})
		virtualbox.SetRunner(shell.Real{})
	})

	res := List(context.Background())
	for _, name := range []string{"lab", "hv", "vbox"} {
		if res.Errors[name] == "" {
			t.Errorf("context %s: want a partial error, got none (errors %v)", name, res.Errors)
		}
	}
	for _, want := range []string{
		"incus list lab: --format=json",
		"virsh -c qemu+ssh://hv/system list --all --name",
		"VBoxManage list vms",
	} {
		found := false
		for _, c := range fake.Calls() {
			if strings.Contains(c.Name+" "+strings.Join(c.Args, " "), want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no %q among %v", want, fake.Calls())
		}
	}
}
