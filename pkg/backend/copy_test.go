package backend

import (
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestCopyIsAnIndependentKubeVirtOnlyCapability(t *testing.T) {
	for name := range factories {
		adapter, err := For(types.InstanceRef{Backend: name, Context: "test", Namespace: "guests", Name: "web"})
		if err != nil {
			t.Fatalf("construct %s adapter: %v", name, err)
		}
		_, copies := adapter.(Copier)
		if copies != (name == "kubevirt") {
			t.Errorf("%s Copier = %v; only KubeVirt may advertise this implementation", name, copies)
		}
	}
}
