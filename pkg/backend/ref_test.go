package backend

import "testing"

// Only KubeVirt scopes guests by namespace today. If another backend grows
// one, it implements Namespacer and this list changes with it.
func TestNamespacedFollowsTheAdapter(t *testing.T) {
	for _, b := range Backends {
		want := b == "kubevirt"
		if got := Namespaced(b); got != want {
			t.Errorf("Namespaced(%q) = %v, want %v", b, got, want)
		}
	}
	if Namespaced("no-such-backend") {
		t.Error("an unregistered backend cannot scope by namespace")
	}
}

func TestRefKeepsTheNamespaceOnlyWhereItScopes(t *testing.T) {
	kube := Ref("kubevirt", "prod", "corral-vms", "web-1")
	if kube.Backend != "kubevirt" || kube.Context != "prod" || kube.Namespace != "corral-vms" || kube.Name != "web-1" {
		t.Errorf("KubeVirt ref = %+v", kube)
	}
	for _, b := range []string{"qemu", "incus", "libvirt", "proxmox"} {
		ref := Ref(b, "ctx", "corral-vms", "web-1")
		if ref.Namespace != "" || ref.Backend != b || ref.Context != "ctx" || ref.Name != "web-1" {
			t.Errorf("%s ref = %+v, want no namespace", b, ref)
		}
	}
}
