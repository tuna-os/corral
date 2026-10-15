package backend

import "github.com/tuna-os/corral/pkg/types"

// Namespacer is implemented by a backend whose guests live in a namespace
// inside their context, the way a KubeVirt VM lives in a Kubernetes namespace
// inside a cluster. DefaultNamespace is where a guest goes when none is named.
type Namespacer interface {
	DefaultNamespace() string
}

// Namespaced reports whether a backend scopes guests by namespace.
func Namespaced(backend string) bool {
	adapter, err := probe(backend)
	if err != nil {
		return false
	}
	_, ok := adapter.(Namespacer)
	return ok
}

// Ref builds the canonical reference to a guest in a context. The namespace is
// kept only where the backend scopes guests by one: carried onto a libvirt or
// Incus reference it would make two references to the same guest compare
// unequal, and the registry keys off exactly this.
func Ref(backend, context, namespace, name string) types.InstanceRef {
	ref := types.InstanceRef{Backend: backend, Context: context, Name: name}
	if Namespaced(backend) {
		ref.Namespace = namespace
	}
	return ref
}
