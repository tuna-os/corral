package cmd

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/export"
	"github.com/tuna-os/corral/pkg/types"
)

// Ports, ssh, and export reach the VM's own backend; a backend without the
// operation is refused by name rather than sent to KubeVirt or local QEMU (#214).
func TestTUIPortsSSHExport_UseTheVMsBackend(t *testing.T) {
	inc := types.VM{Name: "web", Backend: "incus", Context: "lab"}
	if err := applyPorts(inc, []int{22}); err == nil || !strings.Contains(err.Error(), "incus backend does not support ports") {
		t.Errorf("applyPorts on an Incus VM = %v, want a refusal naming incus", err)
	}
	if got := exposedPorts(inc); got != nil {
		t.Errorf("exposedPorts on an Incus VM = %v, want none", got)
	}

	lv := types.VM{Name: "db", Backend: "libvirt", Context: "qemu+ssh://hv/system"}
	if err := sshSelected(lv); err == nil || !strings.Contains(err.Error(), "no discovered SSH address") {
		t.Errorf("ssh on a libvirt domain = %v, want libvirt's own refusal, not local QEMU", err)
	}
	vb := types.VM{Name: "box", Backend: "virtualbox"}
	if err := sshSelected(vb); err == nil || !strings.Contains(err.Error(), "virtualbox backend does not support ssh") {
		t.Errorf("ssh on a VirtualBox VM = %v, want a refusal naming virtualbox", err)
	}

	vs := types.VM{Name: "app", Backend: "vsphere", Context: "vc"}
	if _, err := exportVM(vs); err == nil || !strings.Contains(err.Error(), "vsphere") {
		t.Errorf("export on a vSphere VM = %v, want a refusal naming vsphere", err)
	}
}

// KubeVirt ports go through the Publisher: the editor reads the proxy Service
// in the VM's namespace.
func TestTUIPorts_KubeVirtReadsThroughThePublisher(t *testing.T) {
	f := fakeCluster(t)
	exposedPorts(kubeVM("web-prod"))
	if !calledWith(f, "get", "svc", "web-prod-proxy", "corral-vms") {
		t.Errorf("ports were not read from the proxy Service; calls: %v", f.Calls())
	}
}

func TestExportSuffixKeepsTheOldFileNames(t *testing.T) {
	for format, want := range map[string]string{"raw.gz": ".img.gz", "qcow2": ".qcow2", "incus-tar": ".tar.gz"} {
		if got := exportSuffix(export.Format(format)); got != want {
			t.Errorf("exportSuffix(%s) = %q, want %q", format, got, want)
		}
	}
}

// A context filter shows the CTs of that context's backend: Incus LXC
// containers under an Incus context, KubeVirt pet pods under a KubeVirt one.
func TestTUIContextFilter_ShowsTheBackendsCTs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := config.AddContext(config.ContextConfig{Name: "lab", Backend: "incus", Context: "lab"}); err != nil {
		t.Fatal(err)
	}
	m := testModel(kubeVM("web-prod"))
	th := m.th
	m.allItems = append(m.allItems,
		ctToItem(ct.CT{Name: "pod-ct", Backend: "kubevirt"}, th),
		ctToItem(ct.CT{Name: "lxc-ct", Backend: "incus"}, th))
	m.contextName = "lab"
	m.applyContextFilter()
	var names []string
	for _, item := range m.list.Items() {
		if c, ok := item.(ctItem); ok {
			names = append(names, c.ct.Name)
		}
	}
	if len(names) != 1 || names[0] != "lxc-ct" {
		t.Errorf("CTs under the Incus context = %v, want [lxc-ct]", names)
	}
}
