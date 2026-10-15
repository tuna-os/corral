package backend

import (
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

// Every registered backend can inventory its context; fleet.List depends on
// it and has no other path.
func TestEveryBackendIsALister(t *testing.T) {
	for _, b := range Backends {
		adapter, err := For(types.InstanceRef{Backend: b, Context: "ctx"})
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if _, ok := adapter.(Lister); !ok {
			t.Errorf("%s adapter does not implement Lister", b)
		}
	}
}
