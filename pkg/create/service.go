// Package create coordinates VM creation for CLI and TUI callers.
// Backend packages retain the VM creation primitives; this package owns
// source selection, bootc coordination, registry updates, and post-create actions.
package create

import (
	"time"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/lima"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// Options is a single create request, independent of Cobra flags.
type Options struct {
	lima.Values
	Backend, Context  string
	KubevirtExplicit  bool
	Kubevirt          bool
	Incus             bool
	QCOW              string
	Force             bool
	PVC               string
	Namespace         string
	Node              string
	CloudInitPassword string
	InstanceType      string
	Preference        string
	File              string
	StorageClass      string
	Kargs             []string
	Start             bool
	WaitSSH           bool
	Timeout           int
	SSHUser           string
	Ephemeral         bool
	TTL               string
	LAN               bool
	NetworkNAD        string
	BridgeIface       string
	LANService        bool
	Firmware          string
	Vsock             bool
	VsockCID          uint32
	TPM               bool
	DedicatedCPUs     bool
	IsolateEmulator   bool
	Tags              []string
}

// Registry persists creation metadata.
type Registry interface {
	Set(string, types.RegistryEntry) error
}

// Service supplies backend operations. NewService installs production defaults;
// callers can substitute operations to exercise orchestration without a host or cluster.
type Service struct {
	Registry       Registry
	ResolveBackend func(string) string
	Reachable      func() bool
	BackendFor     func(types.InstanceRef) (backend.Adapter, error)
	QEMUCreate     func(types.CreateOpts) error
	QEMUStart      func(string) error
	QEMUWaitSSH    func(string, string, time.Duration) error
	KubevirtCreate func(types.CreateOpts) error
	IncusCreate    func(incus.CreateOpts) error
	LibvirtCreate  func(types.CreateOpts) error
}

// NewService wires existing backend implementations and caller-owned inventory/registry.
func NewService(resolve func(string) string, registry Registry) *Service {
	if resolve == nil {
		resolve = func(string) string { return "" }
	}
	return &Service{Registry: registry, ResolveBackend: resolve,
		Reachable: kubevirt.Reachable, BackendFor: backend.For,
		QEMUStart: qemu.Start, QEMUWaitSSH: qemu.WaitSSH,
		QEMUCreate: qemu.Create, KubevirtCreate: kubevirt.CreateVM,
		IncusCreate: incus.Create, LibvirtCreate: libvirt.NewClient("").Create,
	}
}

type request struct {
	Options
	*Service
}

// Execute validates and dispatches one request. Lima files override the same
// default-valued fields as the CLI historically did; they do not mutate callers.
func (s *Service) Execute(name string, opts Options) error {
	if opts.File != "" {
		spec, err := lima.Load(opts.File)
		if err != nil {
			return err
		}
		spec.Apply(&opts.Values)
		if spec.Bootc == "" {
			opts.Kubevirt = true
		}
	}
	return (request{Options: opts, Service: s}).execute(name)
}
