package types

// Backend is the seam between corral's two compute backends (qemu, kubevirt):
// the operations both genuinely implement today. It's deliberately smaller
// than either backend's full capability set — kubevirt.Client has migrate/
// snapshot/scale/GPU operations qemu has no counterpart for, and qemu has no
// restart/pause/unpause plumbing kubevirt does. Those stay reachable through
// the concrete types directly; this interface only covers what both sides
// can promise.
type Backend interface {
	ListVMs() ([]VM, error)
	VMExists(name string) bool
	StartVM(name string) error
	StopVM(name string) error
	DeleteVM(name string) error
	VMInfo(name string) ([]byte, error)
	SSH(name, username, identityFile, command string, port int, password string) error
	Viewer(name string) error
	Logs(name string) error
}

// VM represents a virtual machine from either backend.
type VM struct {
	Name    string `json:"name"`
	Backend string `json:"backend"` // "qemu" or "kubevirt"
	Status  string `json:"status"`  // "Running", "Stopped", "Starting", "↓ 42.5%"
	Ready   bool   `json:"ready"`
	Running bool   `json:"running"`

	CPU  int    `json:"cpu"`
	Mem  string `json:"mem"`
	Disk string `json:"disk,omitempty"`
	Node string `json:"node,omitempty"`
	VNC  string `json:"vnc,omitempty"` // port or "on"/"off"/"pending"
	IP   string `json:"ip,omitempty"`
	ISO  string `json:"iso,omitempty"` // ISO download progress

	// KubeVirt-specific
	Namespace      string   `json:"namespace,omitempty"`
	LiveMigratable bool     `json:"liveMigratable"` // VMI LiveMigratable condition
	AgentConnected bool     `json:"agentConnected"` // qemu-guest-agent reachable
	IsTemplate     bool     `json:"isTemplate"`     // labeled corral.dev/template=true
	Bootc          bool     `json:"bootc"`          // kernel-boot VM (built by the bootc plugin)
	Tags           []string `json:"tags,omitempty"` // from corral.dev/tag.<name> labels
}

// Capabilities reports what optional operations the cluster supports, so the
// UI can enable/disable controls instead of failing on click.
type Capabilities struct {
	StorageClass string `json:"storageClass"` // preferred SC for new disks ("" = cluster default)
	CanExpand    bool   `json:"canExpand"`    // default SC has allowVolumeExpansion
	CanSnapshot  bool   `json:"canSnapshot"`  // a VolumeSnapshotClass exists
}

// RegistryEntry persists backend choice per VM.
type RegistryEntry struct {
	Backend   string `json:"backend"`
	Namespace string `json:"namespace,omitempty"`
	Password  string `json:"password,omitempty"`
}

// CreateOpts holds all VM creation options.
type CreateOpts struct {
	Name              string
	Backend           string // "qemu" or "kubevirt"
	Mem               string
	CPU               int
	Disk              string
	ISO               string
	QCOW              string
	Force             bool
	ContainerDisk     string
	ImportURL         string // qcow2/raw disk image URL → CDI import as the boot disk
	PVC               string
	Namespace         string
	Node              string
	CloudInitPassword string
	CloudInitExtra    string
	SSHPublicKey      string
	InstanceType      string // KubeVirt cluster instancetype (sets CPU/mem); overrides CPU/Mem
	Preference        string // KubeVirt cluster preference (devices/firmware defaults)
	StorageClass      string // overrides PreferredStorageClass() for this VM's disks; "" = default
}

// PortMap maps protocol names to port numbers.
var PortMap = map[string]int{
	"ssh":   22,
	"rdp":   3389,
	"vnc":   5900,
	"http":  80,
	"https": 443,
}

// DefaultPorts are the ports offered in the edit menu.
var DefaultPorts = []int{22, 3389, 5900, 80, 443}
