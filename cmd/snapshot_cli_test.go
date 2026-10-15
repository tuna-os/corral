package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/types"
)

func snapshotSeam(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	snapshot.SetRunner(fake)
	t.Cleanup(func() { snapshot.SetRunner(shell.Real{}) })
	return fake
}

// `corral snapshot ls` on an Incus VM lists through pkg/snapshot, with each
// snapshot's consistency. It used to stop with "snapshot is only supported
// for KubeVirt VMs" (#491).
func TestSnapshotListOnIncusUsesTheSnapshotContract(t *testing.T) {
	remoteVMScratch(t, "web", types.RegistryEntry{Backend: "incus", Context: "lab"})
	fake := snapshotSeam(t)
	fake.AddResponse("incus query lab:/1.0/instances/web/snapshots?recursion=1",
		`[{"name":"web/before-upgrade","created_at":"2026-10-01T00:00:00Z","config":{"volatile.last_state.power":"STOPPED"}}]`, nil)

	out := captureStdout(t, func() {
		if err := snapshotListCmd.RunE(snapshotListCmd, []string{"web"}); err != nil {
			t.Errorf("snapshot ls on an Incus VM: %v", err)
		}
	})
	if !strings.Contains(out, "before-upgrade") || !strings.Contains(out, "offline") {
		t.Errorf("snapshot ls output = %q, want the snapshot and its consistency", out)
	}
}

// `corral snapshot create` on a libvirt domain runs virsh against the
// domain's own URI.
func TestSnapshotCreateOnLibvirtRunsVirsh(t *testing.T) {
	remoteVMScratch(t, "db", types.RegistryEntry{Backend: "libvirt", Context: "qemu+ssh://hv/system"})
	fake := snapshotSeam(t)
	fake.AddResponse("virsh -c qemu+ssh://hv/system domstate db", "shut off\n", nil)
	fake.AddResponse("virsh -c qemu+ssh://hv/system snapshot-create-as --domain db --name before-upgrade --atomic", "", nil)
	snapshotName = "before-upgrade"
	t.Cleanup(func() { snapshotName = "" })

	out := captureStdout(t, func() {
		if err := snapshotCreateCmd.RunE(snapshotCreateCmd, []string{"db"}); err != nil {
			t.Errorf("snapshot create on a libvirt domain: %v", err)
		}
	})
	if !strings.Contains(out, "before-upgrade (offline)") {
		t.Errorf("snapshot create output = %q, want the name and the offline consistency", out)
	}
}

// Restore on a running libvirt domain is refused with the backend's reason.
func TestSnapshotRestoreOnARunningLibvirtDomainIsRefused(t *testing.T) {
	remoteVMScratch(t, "db", types.RegistryEntry{Backend: "libvirt", Context: "qemu+ssh://hv/system"})
	fake := snapshotSeam(t)
	fake.AddResponse("virsh -c qemu+ssh://hv/system domstate db", "running\n", nil)

	err := snapshotRestoreCmd.RunE(snapshotRestoreCmd, []string{"db", "before-upgrade"})
	var unsupported *snapshot.Unsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("restore of a running domain = %v, want *snapshot.Unsupported", err)
	}
}
