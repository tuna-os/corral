package kubevirt

import "github.com/tuna-os/corral/pkg/types"

// VMAdvanced is the interface for advanced KubeVirt operations: live/offline
// scaling, migration, snapshot/clone, disk hotplug, and guest-agent queries.
// Callers building orchestration layers (Proxmox API, advanced TUI) should
// depend on this interface.
type VMAdvanced interface {
	Migrate(name, targetNode string) error
	MigrationState(name string) (MigrationState, error)
	ScaleCPU(name string, vcpus int) error
	ScaleMemory(name, mem string) error
	Scale(name string, vcpus int, mem string) error
	AddVolume(name, size string) (string, error)
	RemoveVolume(name, vol string) error
	ExpandDisk(pvc, size string) error
	Snapshot(vm, snap string) (string, error)
	ListSnapshots(vm string) ([]SnapshotInfo, error)
	RestoreSnapshot(vm, snap string) error
	DeleteSnapshot(snap string) error
	Clone(src, dst string) error
	Export(name, volume, outputPath string) (string, error)
	GuestInfo(name string) (map[string]any, error)
	AddNIC(name, nad, iface string) error
}

// Compile-time check: Client satisfies both the shared Backend seam
// (types.Backend — the operations qemu also implements, see pkg/qemu.Backend)
// and the kubevirt-only advanced capability set.
var (
	_ types.Backend = (*Client)(nil)
	_ VMAdvanced    = (*Client)(nil)
)
