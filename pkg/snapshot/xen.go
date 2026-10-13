package snapshot

// ── XenServer / XCP-ng ──────────────────────────────────────────────
//
// VM.snapshot behind the shared contract. XenAPI takes no quiesce flag on
// this path — quiesce only exists on the removed snapshot_with_quiesce — so
// a running capture is crash-consistent by construction, never by accident:
// the Filesystem label stays gated behind proof the API cannot supply, and a
// halted guest is honestly offline-consistent.

import (
	"fmt"

	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/xen"
)

// XenClientFor resolves a client for a reference. It is a package variable
// so the context configuration (host, user, TLS trust) can be supplied by
// whatever owns it, and so tests can point it at an httptest server.
var XenClientFor = func(ref types.InstanceRef) (*xen.Client, error) {
	client, err := xen.ClientForContext(ref.Context)
	if err != nil {
		return nil, fmt.Errorf("%w — add the pool with "+
			"`corral context add <name> --backend xen --context <host>`", err)
	}
	return client, nil
}

type Xen struct{}

func (x Xen) client(ref types.InstanceRef) (*xen.Client, error) {
	return XenClientFor(ref)
}

func (x Xen) Create(ref types.InstanceRef, name string) (Snapshot, error) {
	client, err := x.client(ref)
	if err != nil {
		return Snapshot{}, err
	}
	vmRef, err := client.VMRef(ref.Name)
	if err != nil {
		return Snapshot{}, err
	}
	rec, err := client.VMRecord(vmRef)
	if err != nil {
		return Snapshot{}, err
	}
	running := rec.PowerState == "Running"
	if _, err := client.Snapshot(ref.Name, name); err != nil {
		return Snapshot{}, err
	}
	// Read back rather than assume: the caller is told what the pool
	// actually recorded.
	snaps, err := client.Snapshots(ref.Name)
	if err != nil {
		return Snapshot{}, err
	}
	for _, s := range snaps {
		if name == "" || s.Name == name {
			return Snapshot{Name: s.Name, Source: ref.Name, Ready: true,
				Consistency: x.consistency(running)}, nil
		}
	}
	return Snapshot{Name: name, Source: ref.Name, Ready: true,
		Consistency: x.consistency(running)}, nil
}

func (x Xen) List(ref types.InstanceRef) ([]Snapshot, error) {
	client, err := x.client(ref)
	if err != nil {
		return nil, err
	}
	snaps, err := client.Snapshots(ref.Name)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, Snapshot{Name: s.Name, Source: ref.Name, Ready: true,
			Consistency: x.snapshotConsistency(client, s.Ref)})
	}
	sortNewestFirst(out)
	return out, nil
}

// snapshotConsistency reads the snapshot's own record: a snapshot taken
// while running stayed running, so it is crash-consistent; anything else is
// offline. A record that cannot be read degrades to crash — the weaker claim
// is the safe one.
func (x Xen) snapshotConsistency(client *xen.Client, ref string) Consistency {
	rec, err := client.VMRecord(ref)
	if err != nil {
		return Crash
	}
	return x.consistency(rec.PowerState == "Running")
}

func (x Xen) Restore(ref types.InstanceRef, name string) error {
	client, err := x.client(ref)
	if err != nil {
		return err
	}
	return client.Revert(name)
}

func (x Xen) Delete(ref types.InstanceRef, name string) error {
	client, err := x.client(ref)
	if err != nil {
		return err
	}
	return client.DestroySnapshot(ref.Name, name)
}

// consistency reports what the capture actually caught: XenAPI offers no
// quiesce on this path, so a running guest is crash-consistent and a halted
// one offline — crash unless proven, and nothing here can prove otherwise.
func (x Xen) consistency(running bool) Consistency {
	if !running {
		return Offline
	}
	return Crash
}
