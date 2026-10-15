package snapshot

// ── Nutanix Prism Central (AHV) ─────────────────────────────────────
//
// Recovery points behind the shared contract. v4 takes no quiesce flag on
// this path — the NX-0 spike proved no guest-quiesce action — so every
// capture is crash-consistent by construction, never by accident: the
// Filesystem label stays gated behind proof the API cannot supply. Unlike
// Xen (halted guests honestly offline), a recovery point is disk-only in
// every power state, so there is no offline branch here — crash, always.
import (
	"fmt"
	"strings"

	"github.com/tuna-os/corral/pkg/nutanix"
	"github.com/tuna-os/corral/pkg/types"
)

// NutanixClientFor resolves a client for a reference. It is a package variable
// so the context configuration (VIP, user, TLS trust) can be supplied by
// whatever owns it, and so tests can point it at an httptest server.
var NutanixClientFor = func(ref types.InstanceRef) (*nutanix.Client, error) {
	client, err := nutanix.ClientForContext(ref.Context)
	if err != nil {
		return nil, fmt.Errorf("%w — add the cluster with "+
			"`corral context add <name> --backend nutanix --context <vip>`", err)
	}
	return client, nil
}

// Nutanix implements Adapter over vmm VM recovery points.
type Nutanix struct{}

func (n Nutanix) client(ref types.InstanceRef) (*nutanix.Client, error) {
	return NutanixClientFor(ref)
}

func (n Nutanix) wait(client *nutanix.Client, task nutanix.Task) error {
	return client.WaitTask(task, nutanix.DefaultTimeout)
}

func (n Nutanix) Create(ref types.InstanceRef, name string) (Snapshot, error) {
	client, err := n.client(ref)
	if err != nil {
		return Snapshot{}, err
	}
	if name == "" {
		name = ref.Name + "-snap"
	}
	task, err := client.CreateSnapshot(ref.Name, name)
	if err != nil {
		return Snapshot{}, err
	}
	if err := n.wait(client, task); err != nil {
		return Snapshot{}, err
	}
	// Read back rather than assume: the caller is told what Prism Central
	// actually recorded.
	points, err := client.ListSnapshots(ref.Name)
	if err != nil {
		return Snapshot{}, err
	}
	for _, p := range points {
		if p.Name == name {
			return Snapshot{Name: p.Name, Source: ref.Name, Created: p.CreatedAt,
				Ready: true, Consistency: Crash}, nil
		}
	}
	return Snapshot{Name: name, Source: ref.Name, Ready: true, Consistency: Crash}, nil
}

func (n Nutanix) List(ref types.InstanceRef) ([]Snapshot, error) {
	client, err := n.client(ref)
	if err != nil {
		return nil, err
	}
	points, err := client.ListSnapshots(ref.Name)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(points))
	for _, p := range points {
		out = append(out, Snapshot{Name: p.Name, Source: ref.Name, Created: p.CreatedAt,
			Ready: true, Consistency: Crash})
	}
	return out, nil
}

func (n Nutanix) Restore(ref types.InstanceRef, name string) error {
	client, err := n.client(ref)
	if err != nil {
		return err
	}
	task, err := client.RevertSnapshot(ref.Name, name)
	if err != nil {
		return err
	}
	return n.wait(client, task)
}

func (n Nutanix) Delete(ref types.InstanceRef, name string) error {
	client, err := n.client(ref)
	if err != nil {
		return err
	}
	task, err := client.DeleteSnapshot(ref.Name, name)
	if err != nil {
		// Deleting one that is already gone is not an error — retention
		// races with a human clearing up.
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return n.wait(client, task)
}

// isNotFound reports the client's own unknown-snapshot refusal. A 404 from
// Prism Central is a different signal (the endpoint moved) and stays an
// error.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no snapshot named")
}
