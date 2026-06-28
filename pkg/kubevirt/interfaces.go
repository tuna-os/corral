package kubevirt

import "github.com/tuna-os/corral/pkg/types"

// VMLifecycle is the minimal interface for VM CRUD and lifecycle operations.
// Callers that only need to list, start, stop, or delete VMs should depend
// on this interface rather than the full Client.
type VMLifecycle interface {
	VMExists(name string) bool
	ListVMs() ([]types.VM, error)
	StartVM(name string) error
	StopVM(name string) error
	RestartVM(name string) error
	PauseVM(name string) error
	UnpauseVM(name string) error
	DeleteVM(name string) error
	VMInfo(name string) ([]byte, error)
	Logs(name string) error
	SSH(name, username, identityFile, command string, port int, password string) error
	Viewer(name string) error
}

// VMAdvanced is the interface for advanced KubeVirt operations: live/offline
// scaling, migration, snapshot/clone, disk hotplug, and guest-agent queries.
// Callers building orchestration layers (Proxmox API, advanced TUI) should
// depend on this interface.
type VMAdvanced interface {
	Migrate(name, targetNode string) error
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

// Compile-time check: Client satisfies both capability interfaces.
var (
	_ VMLifecycle = (*Client)(nil)
	_ VMAdvanced  = (*Client)(nil)
)
