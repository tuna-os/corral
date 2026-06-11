package types

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
	Namespace      string `json:"namespace,omitempty"`
	LiveMigratable bool   `json:"liveMigratable"` // VMI LiveMigratable condition
	AgentConnected bool   `json:"agentConnected"` // qemu-guest-agent reachable
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
	Backend   string            `json:"backend"`
	Namespace string            `json:"namespace,omitempty"`
	Password  string            `json:"password,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
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
	PVC               string
	Namespace         string
	Node              string
	CloudInitPassword string
	CloudInitExtra    string
	SSHPublicKey      string
	TailscaleAuthKey  string // injected into cloud-init so the VM joins the tailnet
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
