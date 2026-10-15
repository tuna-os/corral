package backend

import "github.com/tuna-os/corral/pkg/kubevirt"

// Publisher manages the guest ports Corral publishes for an instance: on
// KubeVirt, a port-proxy Service the Tailscale operator exposes on the
// tailnet. Not a Family: QEMU publishes ports too (hostfwd in its unit), but
// fixes them at create time, so the matrix's "ports" row is about more than
// this live editor.
type Publisher interface {
	// PublishedPorts lists the ports published now; none on error.
	PublishedPorts(name string) []int
	// Publish replaces the published set. An empty set unpublishes.
	Publish(name string, ports []int) error
}

func (a kubevirtAdapter) PublishedPorts(name string) []int {
	return kubevirt.ExposedPorts(name, a.client.Namespace)
}

func (a kubevirtAdapter) Publish(name string, ports []int) error {
	if len(ports) == 0 {
		return kubevirt.DeleteProxy(name, a.client.Namespace)
	}
	return kubevirt.ApplyProxy(name, a.client.Namespace, ports)
}
