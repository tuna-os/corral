package backend

import (
	"context"
	"testing"

	"github.com/tuna-os/corral/pkg/logging"
	"github.com/tuna-os/corral/pkg/types"
)

// An adapter from ForContext runs its commands bound to the request: the
// command seam sees the request ID. One from For does not, so a CLI call
// carries none.
func TestForContext_BindsCommandsToTheRequest(t *testing.T) {
	ctx := logging.WithRequestID(context.Background(), "req-backend")
	for _, backend := range []string{"kubevirt", "incus", "libvirt"} {
		t.Run(backend, func(t *testing.T) {
			fake := newFake()
			seam(t, backend, fake)
			ref := types.InstanceRef{Backend: backend, Context: "c1", Namespace: "ns", Name: "vm1"}

			bound, err := ForContext(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := bound.(Power).Start("vm1"); err != nil {
				t.Fatalf("Start: %v", err)
			}
			plain, err := For(ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := plain.(Power).Start("vm1"); err != nil {
				t.Fatalf("Start: %v", err)
			}

			calls := fake.Calls()
			if len(calls) < 2 {
				t.Fatalf("calls =%s, want one per Start", describeCalls(calls))
			}
			if got := calls[0].RequestID; got != "req-backend" {
				t.Errorf("bound call request ID = %q, want req-backend", got)
			}
			if got := calls[len(calls)-1].RequestID; got != "" {
				t.Errorf("unbound call request ID = %q, want none", got)
			}
		})
	}
}

// Adapters with nothing to bind come back unchanged rather than failing.
func TestForContext_AdapterWithoutCommandsIsUnchanged(t *testing.T) {
	a, err := ForContext(context.Background(), types.InstanceRef{Backend: "qemu", Name: "vm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.(qemuAdapter); !ok {
		t.Errorf("ForContext(qemu) = %T, want qemuAdapter", a)
	}
	if _, err := ForContext(context.Background(), types.InstanceRef{}); err == nil {
		t.Error("ForContext accepted a reference with no backend")
	}
}
