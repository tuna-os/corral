package backend

// One adapter per backend: the only place a backend's own signature is
// translated into the contract.
//
// These implement exactly what their backend can do *today*, and nothing more.
// Where a family is absent the gap is real and `docs/backend-parity.md` names the
// native mechanism that would fill it — an adapter that stubs a method to satisfy
// an interface would put the lie back where the conformance tests just removed
// it.
//
// Adapters must be constructible from a bare InstanceRef, because capability
// derivation probes the type rather than a live connection (see probe). Nothing
// in a constructor may dial anything.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/nutanix"
	"github.com/tuna-os/corral/pkg/proxmoxbe"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/virtualbox"
	"github.com/tuna-os/corral/pkg/vsphere"
	"github.com/tuna-os/corral/pkg/xen"
)

func init() {
	Register("kubevirt", func(ref types.InstanceRef) (Adapter, error) {
		ns := ref.Namespace
		if ns == "" {
			ns = kubevirt.DefaultNamespace
		}
		return kubevirtAdapter{client: kubevirt.NewClientForContext(ns, ref.Context)}, nil
	})
	Register("qemu", func(types.InstanceRef) (Adapter, error) { return qemuAdapter{}, nil })
	Register("incus", func(ref types.InstanceRef) (Adapter, error) {
		return incusAdapter{client: incus.NewClient(ref.Context)}, nil
	})
	Register("libvirt", func(ref types.InstanceRef) (Adapter, error) {
		return libvirtAdapter{client: libvirt.NewClient(ref.Context)}, nil
	})
	Register("proxmox", func(ref types.InstanceRef) (Adapter, error) {
		// Deferred: the client needs configuration, and probe() must not fail
		// for a backend that is merely unconfigured — the families a backend
		// implements are a property of its type.
		return proxmoxAdapter{context: ref.Context}, nil
	})
	Register("virtualbox", func(ref types.InstanceRef) (Adapter, error) {
		// No dial: NewClient only records the local context label, so
		// probe() stays offline like every other factory here.
		return virtualboxAdapter{client: virtualbox.NewClient(ref.Context)}, nil
	})
	Register("vsphere", func(ref types.InstanceRef) (Adapter, error) {
		// Deferred: the client needs configuration and a session, and
		// probe() must not fail for an estate that is merely unconfigured —
		// the families a backend implements are a property of its type.
		return vsphereAdapter{context: ref.Context}, nil
	})
	Register("xen", func(ref types.InstanceRef) (Adapter, error) {
		// Deferred: the client needs configuration and a session, and
		// probe() must not fail for a pool that is merely unconfigured —
		// the families a backend implements are a property of its type.
		return xenAdapter{context: ref.Context}, nil
	})
	Register("nutanix", func(ref types.InstanceRef) (Adapter, error) {
		// Deferred: the client needs configuration, and probe() must not
		// fail for a Prism Central that is merely unconfigured — v4
		// authenticates per request, so resolving alone never dials, and
		// the families a backend implements are a property of its type.
		return nutanixAdapter{context: ref.Context}, nil
	})
}

// ── KubeVirt ──────────────────────────────────────────────────────
//
// The reference implementation, and the only backend that satisfies every
// family. That is the fact the parity work exists to change.

type kubevirtAdapter struct{ client *kubevirt.Client }

func (kubevirtAdapter) Backend() string { return "kubevirt" }

// DefaultNamespace makes KubeVirt a Namespacer: a VM lives in a Kubernetes
// namespace inside its cluster.
func (kubevirtAdapter) DefaultNamespace() string { return kubevirt.DefaultNamespace }

func (a kubevirtAdapter) Start(name string) error  { return a.client.StartVM(name) }
func (a kubevirtAdapter) Stop(name string) error   { return a.client.StopVM(name) }
func (a kubevirtAdapter) Delete(name string) error { return a.client.DeleteVM(name) }

func (a kubevirtAdapter) Restart(name string) error { return a.client.RestartVM(name) }

func (a kubevirtAdapter) Pause(name string) error  { return a.client.PauseVM(name) }
func (a kubevirtAdapter) Resume(name string) error { return a.client.UnpauseVM(name) }

func (a kubevirtAdapter) Scale(name string, cores int, mem string) error {
	return a.client.Scale(name, cores, mem)
}

// HotplugsLive follows the VMI's own live-migratable condition, which is what
// KubeVirt ties hotplug to.
func (a kubevirtAdapter) HotplugsLive(name string) bool {
	vms, err := a.client.ListVMs()
	if err != nil {
		return false
	}
	for _, vm := range vms {
		if vm.Name == name {
			return vm.LiveMigratable
		}
	}
	return false
}

func (a kubevirtAdapter) AddDisk(name, size string) error {
	_, err := a.client.AddVolume(name, size)
	return err
}

func (a kubevirtAdapter) RemoveDisk(name, disk string) error {
	return a.client.RemoveVolume(name, disk)
}

func (a kubevirtAdapter) ExpandDisk(name, disk, size string) error {
	// KubeVirt resizes the PVC, not a disk slot; the disk argument is the claim.
	if disk == "" {
		disk = name
	}
	return a.client.ExpandDisk(disk, size)
}

func (a kubevirtAdapter) Migrate(name, target string) error { return a.client.Migrate(name, target) }

func (a kubevirtAdapter) CanMigrate(name string) (bool, string) {
	if a.HotplugsLive(name) {
		return true, ""
	}
	return false, "the VM is not live-migratable (a persistent RWO disk, or masquerade networking is absent)"
}

func (a kubevirtAdapter) Clone(source, target string) error { return a.client.Clone(source, target) }

func (a kubevirtAdapter) MarkTemplate(name string, on bool) error {
	return a.client.MarkTemplate(name, on)
}

func (a kubevirtAdapter) SetTag(name, tag string, on bool) error {
	return a.client.SetTag(name, tag, on)
}

func (a kubevirtAdapter) Metrics(name string) (map[string]string, error) {
	return a.client.Metrics(name), nil
}

func (a kubevirtAdapter) Events(name string) ([]Event, error) {
	events, err := a.client.Events(name)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(events))
	for _, e := range events {
		out = append(out, Event{Time: e.Time, Kind: e.Type, Reason: e.Reason,
			Object: e.Object, Message: e.Message})
	}
	return out, nil
}

func (a kubevirtAdapter) View(name string) error { return a.client.Viewer(name) }
func (a kubevirtAdapter) Logs(name string) error { return a.client.Logs(name) }

func (a kubevirtAdapter) Describe(name string) ([]byte, error) { return a.client.VMInfo(name) }

func (a kubevirtAdapter) SSH(name string, opts SSHOptions) error {
	return a.client.SSH(name, opts.User, opts.Identity, opts.Command, opts.Port, opts.Password, opts.LocalForwards)
}

// ── local QEMU ────────────────────────────────────────────────────
//
// Power plus a real restart. Everything else is a gap the matrix names: the unit
// already opens a QMP socket, which is what pause and metrics would use.

type qemuAdapter struct{}

func (qemuAdapter) Backend() string { return "qemu" }

func (qemuAdapter) Start(name string) error  { return qemu.Start(name) }
func (qemuAdapter) Stop(name string) error   { return qemu.Stop(name) }
func (qemuAdapter) Delete(name string) error { return qemu.Delete(name) }

// Restart is a stop then a start because a local unit has no other reboot: the
// distinction Restarter draws is whether the backend has its own mechanism, and
// systemd restarting the unit *is* qemu's own mechanism.
func (qemuAdapter) Restart(name string) error {
	if err := qemu.Stop(name); err != nil {
		return err
	}
	return qemu.Start(name)
}

func (qemuAdapter) Pause(name string) error  { return qemu.Pause(name) }
func (qemuAdapter) Resume(name string) error { return qemu.Resume(name) }

func (qemuAdapter) Metrics(name string) (map[string]string, error) { return qemu.Metrics(name) }

func (qemuAdapter) View(name string) error                 { return qemu.Viewer(name) }
func (qemuAdapter) Logs(name string) error                 { return qemu.Logs(name) }
func (qemuAdapter) SerialLogs(name string, tail int) error { return qemu.LogsSerial(name, tail) }

func (qemuAdapter) Describe(name string) ([]byte, error) { return qemu.Info(name) }

func (qemuAdapter) SSH(name string, opts SSHOptions) error {
	if opts.Vsock {
		return qemu.SSHViaVsock(name, opts.User, opts.Command)
	}
	return qemu.SSH(name, opts.User, opts.Identity, opts.Command, opts.Port, opts.Password, opts.LocalForwards)
}

// Events: journald already keeps the local backend's history, keyed by the
// unit systemd started. This is the only backend outside KubeVirt and PVE
// where event history exists without Corral recording it itself.
func (qemuAdapter) Events(name string) ([]Event, error) {
	entries, err := qemu.Events(name)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(entries))
	for _, e := range entries {
		out = append(out, Event{
			Time: e.Time, Kind: e.Kind, Reason: "journal",
			Object: "unit/corral-" + name, Message: e.Message,
		})
	}
	return out, nil
}

// ── Incus ─────────────────────────────────────────────────────────

type incusAdapter struct{ client incus.Client }

func (incusAdapter) Backend() string { return "incus" }

func (a incusAdapter) Start(name string) error  { return a.client.Start(name) }
func (a incusAdapter) Stop(name string) error   { return a.client.Stop(name) }
func (a incusAdapter) Delete(name string) error { return a.client.Delete(name) }

func (a incusAdapter) Restart(name string) error { return a.client.Restart(name) }
func (a incusAdapter) Pause(name string) error   { return a.client.Pause(name) }

func (a incusAdapter) Metrics(name string) (map[string]string, error) {
	return a.client.Metrics(name)
}
func (a incusAdapter) Resume(name string) error { return a.client.Resume(name) }

func (a incusAdapter) View(name string) error { return a.client.Viewer(name) }
func (a incusAdapter) Logs(name string) error { return a.client.ConsoleLog(name) }

func (a incusAdapter) Describe(name string) ([]byte, error) { return a.client.Info(name) }

// SSH is `incus exec` into the instance; Incus authenticates the operator,
// so user, identity, and password do not apply.
func (a incusAdapter) SSH(name string, opts SSHOptions) error {
	return a.client.SSH(name, opts.Command)
}

func (a incusAdapter) Address(name string) (string, error) {
	instances, err := a.client.ListInstances()
	if err != nil {
		return "", err
	}
	for _, inst := range instances {
		if inst.Name == name {
			return inst.Address(), nil
		}
	}
	return "", fmt.Errorf("incus: no instance named %q", name)
}

// ── libvirt ───────────────────────────────────────────────────────

type libvirtAdapter struct{ client libvirt.Client }

func (libvirtAdapter) Backend() string { return "libvirt" }

func (a libvirtAdapter) Start(name string) error   { return a.client.Start(name) }
func (a libvirtAdapter) Stop(name string) error    { return a.client.Stop(name) }
func (a libvirtAdapter) Delete(name string) error  { return a.client.Delete(name) }
func (a libvirtAdapter) Restart(name string) error { return a.client.Restart(name) }
func (a libvirtAdapter) Pause(name string) error   { return a.client.Pause(name) }

func (a libvirtAdapter) Metrics(name string) (map[string]string, error) {
	return a.client.Metrics(name)
}
func (a libvirtAdapter) Resume(name string) error { return a.client.Resume(name) }

// View is virt-viewer against the domain's own URI, so a domain on a remote
// hypervisor opens there rather than looking for local QEMU metadata. No
// Logger: libvirt keeps no guest log a client can read back, and `virsh
// console` is an interactive attach, not a log.
func (a libvirtAdapter) View(name string) error { return a.client.Viewer(name) }

func (a libvirtAdapter) Describe(name string) ([]byte, error) { return a.client.Info(name) }

// SSH reports why libvirt cannot shell in yet: Corral discovers no guest
// address for a domain, and the client says so in terms of the fix.
func (a libvirtAdapter) SSH(name string, opts SSHOptions) error {
	return a.client.SSH(name, opts.Command)
}

// ── VirtualBox ────────────────────────────────────────────────────
//
// Local VBoxManage only: Power plus the backend's own reset and pause, clonevm,
// and modifyvm while powered off. No
// Addresser — a NAT guest has no stable address of its own, and a bridged
// one's would need Guest Additions guestproperty reads nothing else uses yet.

type virtualboxAdapter struct{ client virtualbox.Client }

func (virtualboxAdapter) Backend() string { return "virtualbox" }

func (a virtualboxAdapter) Start(name string) error  { return a.client.Start(name) }
func (a virtualboxAdapter) Stop(name string) error   { return a.client.Stop(name) }
func (a virtualboxAdapter) Delete(name string) error { return a.client.Delete(name) }

func (a virtualboxAdapter) Restart(name string) error { return a.client.Reset(name) }

func (a virtualboxAdapter) Pause(name string) error  { return a.client.Pause(name) }
func (a virtualboxAdapter) Resume(name string) error { return a.client.Resume(name) }

func (a virtualboxAdapter) Clone(source, target string) error { return a.client.Clone(source, target) }

func (a virtualboxAdapter) Scale(name string, cores int, mem string) error {
	return a.client.Scale(name, cores, mem)
}

// HotplugsLive is false by construction: modifyvm refuses a running VM.
func (virtualboxAdapter) HotplugsLive(string) bool { return false }

// ── Proxmox ───────────────────────────────────────────────────────
//
// The newest backend, and already second only to KubeVirt. Its methods return a
// Task; the contract's callers want completion, so each one waits — a fleet
// surface that fired and forgot would report success for a migration that failed
// thirty seconds later.

type proxmoxAdapter struct {
	context string
	ctx     context.Context // request context for API-call logs; nil outside a request
}

func (proxmoxAdapter) Backend() string { return "proxmox" }

func (a proxmoxAdapter) client() (*proxmoxbe.Client, error) {
	c, err := proxmoxbe.ClientForContext(a.context)
	if err != nil || a.ctx == nil {
		return c, err
	}
	return c.WithContext(a.ctx), nil
}

// wait runs a task-returning operation to completion.
func (a proxmoxAdapter) wait(op func(*proxmoxbe.Client) (proxmoxbe.Task, error)) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	task, err := op(client)
	if err != nil {
		return err
	}
	return client.WaitTask(task, proxmoxbe.DefaultTimeout)
}

func (a proxmoxAdapter) Start(name string) error {
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) { return c.Start(name) })
}

func (a proxmoxAdapter) Stop(name string) error {
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) { return c.Stop(name) })
}

func (a proxmoxAdapter) Delete(name string) error {
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) { return c.Delete(name) })
}

func (a proxmoxAdapter) Restart(name string) error {
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) { return c.Restart(name) })
}

func (a proxmoxAdapter) Pause(name string) error {
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) { return c.Pause(name) })
}

func (a proxmoxAdapter) Resume(name string) error {
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) { return c.Resume(name) })
}

func (a proxmoxAdapter) Scale(name string, cores int, mem string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.Scale(name, cores, mem)
}

// HotplugsLive asks PVE rather than guessing: the guest's own config says which
// devices are hotpluggable.
func (a proxmoxAdapter) HotplugsLive(name string) bool {
	client, err := a.client()
	if err != nil {
		return false
	}
	cfg, err := client.GuestConfig(name)
	if err != nil {
		return false
	}
	return cfg.HotplugsCPU() && cfg.HotplugsMemory()
}

func (a proxmoxAdapter) AddDisk(name, size string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	storage, err := firstImageStorage(client)
	if err != nil {
		return err
	}
	return client.AddDisk(name, storage, "", size)
}

func (a proxmoxAdapter) RemoveDisk(name, disk string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	if disk == "" {
		return fmt.Errorf("proxmox: which disk? pass a slot such as scsi1")
	}
	return client.RemoveDisk(name, disk)
}

func (a proxmoxAdapter) ExpandDisk(name, disk, size string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	if disk == "" {
		disk = "scsi0"
	}
	return client.ExpandDisk(name, disk, size)
}

func (a proxmoxAdapter) Migrate(name, target string) error {
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) { return c.Migrate(name, target) })
}

func (a proxmoxAdapter) CanMigrate(name string) (bool, string) {
	client, err := a.client()
	if err != nil {
		return false, err.Error()
	}
	pre, err := client.MigratePreconditions(name)
	if err != nil {
		// A container has no precondition endpoint; PVE restarts it on the
		// target, which is a migration Corral can still perform.
		return true, ""
	}
	return pre.CanLiveMigrate()
}

// Create makes a VM through POST /nodes/{node}/qemu and waits for the task, so
// a create that returns nil left a guest behind, not a queued request. PVE
// boots an installer from its own storage: the ISO must already be a volume id
// ("local:iso/debian.iso"). A local path or URL would need an upload the
// create API does not do, so it is refused with that reason rather than
// handed to PVE to fail on.
func (a proxmoxAdapter) Create(opts types.CreateOpts) error {
	if opts.QCOW != "" || opts.ImportURL != "" || opts.ContainerDisk != "" || opts.PVC != "" {
		return fmt.Errorf("proxmox: create boots an installer ISO from PVE storage; " +
			"a qcow2, disk import, container disk or PVC has no PVE create equivalent " +
			"(to bring an existing disk, use `corral move --to proxmox`)")
	}
	if opts.ISO != "" && !isPVEVolume(opts.ISO) {
		return fmt.Errorf("proxmox: the ISO must be a PVE volume id such as local:iso/debian.iso, not %q; "+
			"upload it to a PVE storage first", opts.ISO)
	}
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) {
		return c.Create(proxmoxbe.CreateOpts{
			Name: opts.Name, Node: opts.Node,
			Cores: opts.CPU, Mem: opts.Mem, Disk: opts.Disk,
			ISO: opts.ISO, SSHKeys: opts.SSHPublicKey, Password: opts.CloudInitPassword,
			UEFI: opts.UEFI,
		})
	})
}

// isPVEVolume reports whether s names a volume on PVE storage
// ("storage:content/file") rather than a local path or a URL.
func isPVEVolume(s string) bool {
	if strings.Contains(s, "://") {
		return false
	}
	storage, volume, ok := strings.Cut(s, ":")
	return ok && storage != "" && volume != "" && !strings.ContainsAny(storage, `/\.`)
}

func (a proxmoxAdapter) Clone(source, target string) error {
	return a.wait(func(c *proxmoxbe.Client) (proxmoxbe.Task, error) {
		return c.Clone(source, target, true)
	})
}

func (a proxmoxAdapter) MarkTemplate(name string, on bool) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.MarkTemplate(name, on)
}

func (a proxmoxAdapter) SetTag(name, tag string, on bool) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.SetTag(name, tag, on)
}

func (a proxmoxAdapter) Metrics(name string) (map[string]string, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	return client.Metrics(name)
}

func (a proxmoxAdapter) Events(name string) ([]Event, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	tasks, err := client.Events(name)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(tasks))
	for _, t := range tasks {
		kind := "Normal"
		if t.Failed() {
			kind = "Warning"
		}
		message := t.ExitStatus
		if message == "" {
			message = t.Status
		}
		out = append(out, Event{
			Time:    strconv.FormatInt(t.StartTime, 10),
			Kind:    kind,
			Reason:  t.Type,
			Object:  t.ID,
			Message: message + " (" + t.User + ")",
		})
	}
	return out, nil
}

func (a proxmoxAdapter) Address(name string) (string, error) {
	client, err := a.client()
	if err != nil {
		return "", err
	}
	return client.Address(name)
}

// ── vSphere ─────────────────────────────────────────────────────────
//
// Power, the backend's own reset and suspend, live-or-offline scale,
// vMotion, full clones, and one-way template marks. The govmomi tasks wait
// inside the client methods, so — like the Proxmox adapter — every call here
// means completion, not acceptance. No Addresser: a guest address needs
// VMware Tools reads nothing else uses yet. No consoles: MKS/webmks tickets
// exist, but Corral wires no viewer or bridge to them.

type vsphereAdapter struct{ context string }

func (vsphereAdapter) Backend() string { return "vsphere" }

func (a vsphereAdapter) client() (*vsphere.Client, error) {
	return vsphere.ClientForContext(a.context)
}

// ctx bounds one operation. The client waits its govmomi tasks against this
// deadline the way the Proxmox adapter waits UPIDs against DefaultTimeout.
func (a vsphereAdapter) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), vsphere.DefaultTimeout)
}

func (a vsphereAdapter) Start(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Start(ctx, name)
}

func (a vsphereAdapter) Stop(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Stop(ctx, name)
}

func (a vsphereAdapter) Delete(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Delete(ctx, name)
}

func (a vsphereAdapter) Restart(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Restart(ctx, name)
}

func (a vsphereAdapter) Pause(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Suspend(ctx, name)
}

func (a vsphereAdapter) Resume(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Resume(ctx, name)
}

func (a vsphereAdapter) Scale(name string, cores int, mem string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Scale(ctx, name, cores, mem)
}

// HotplugsLive asks vCenter rather than guessing: the guest's own config
// says whether CPU and memory apply live.
func (a vsphereAdapter) HotplugsLive(name string) bool {
	client, err := a.client()
	if err != nil {
		return false
	}
	ctx, cancel := a.ctx()
	defer cancel()
	live, err := client.HotplugsLive(ctx, name)
	if err != nil {
		return false
	}
	return live
}

func (a vsphereAdapter) Migrate(name, target string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Migrate(ctx, name, target)
}

func (a vsphereAdapter) CanMigrate(name string) (bool, string) {
	client, err := a.client()
	if err != nil {
		return false, err.Error()
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.CanMigrate(ctx, name)
}

func (a vsphereAdapter) Clone(source, target string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.Clone(ctx, source, target)
}

func (a vsphereAdapter) MarkTemplate(name string, on bool) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx()
	defer cancel()
	return client.MarkTemplate(ctx, name, on)
}

// ── XenServer / XCP-ng ──────────────────────────────────────────────
//
// Power, the backend's own reboot and suspend, pool migration, and clones.
// XenAPI calls are synchronous, so — like the Proxmox adapter — every call
// here means completion, not acceptance. No Addresser: a guest address needs
// guest-metrics reads nothing else uses yet. No consoles: console objects
// hand out VNC locations, but Corral wires no viewer or bridge to them.

type xenAdapter struct{ context string }

func (xenAdapter) Backend() string { return "xen" }

func (a xenAdapter) client() (*xen.Client, error) {
	return xen.ClientForContext(a.context)
}

func (a xenAdapter) Start(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.Start(name)
}

func (a xenAdapter) Stop(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.Stop(name)
}

func (a xenAdapter) Delete(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.Destroy(name)
}

func (a xenAdapter) Restart(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.Reboot(name)
}

func (a xenAdapter) Pause(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.Suspend(name)
}

func (a xenAdapter) Resume(name string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.Resume(name)
}

func (a xenAdapter) Migrate(name, target string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	return client.Migrate(name, target)
}

// CanMigrate asks the pool rather than guessing: the guest's own record says
// whether it is running, and the host list says whether a second host exists.
func (a xenAdapter) CanMigrate(name string) (bool, string) {
	client, err := a.client()
	if err != nil {
		return false, err.Error()
	}
	return client.CanMigrate(name)
}

func (a xenAdapter) Clone(source, target string) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	_, err = client.Clone(source, target)
	return err
}

// ── Nutanix Prism Central (AHV) ─────────────────────────────────────
//
// Power, the backend's own reboot, same-cluster migration, and clones. v4
// mutations are async tasks, so — unlike the XenAPI adapter — every call
// here waits its task to completion: a surface that fired and forgot would
// report success for a migration that failed thirty seconds later. No
// Suspender: vmm has no suspend action, so pause stays Unsupported. No
// Tagger: category values need an extId resolution no reference proves. No
// Addresser: the NIC address sits in inventory but no address discovery is
// wired. No consoles: a token endpoint exists but Corral wires no viewer.

type nutanixAdapter struct{ context string }

func (nutanixAdapter) Backend() string { return "nutanix" }

func (a nutanixAdapter) client() (*nutanix.Client, error) {
	return nutanix.ClientForContext(a.context)
}

// complete waits a task-returning client op to completion.
func (a nutanixAdapter) complete(op func(*nutanix.Client) (nutanix.Task, error)) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	task, err := op(client)
	if err != nil {
		return err
	}
	return client.WaitTask(task, nutanix.DefaultTimeout)
}

func (a nutanixAdapter) Start(name string) error {
	return a.complete(func(c *nutanix.Client) (nutanix.Task, error) { return c.Start(name) })
}

func (a nutanixAdapter) Stop(name string) error {
	return a.complete(func(c *nutanix.Client) (nutanix.Task, error) { return c.Stop(name) })
}

func (a nutanixAdapter) Delete(name string) error {
	return a.complete(func(c *nutanix.Client) (nutanix.Task, error) { return c.Destroy(name) })
}

func (a nutanixAdapter) Restart(name string) error {
	return a.complete(func(c *nutanix.Client) (nutanix.Task, error) { return c.Restart(name) })
}

func (a nutanixAdapter) Migrate(name, target string) error {
	return a.complete(func(c *nutanix.Client) (nutanix.Task, error) { return c.Migrate(name, target) })
}

// CanMigrate asks the guest record rather than guessing: the power-state
// approximation lives in the client, and an unconfigured context refuses
// with its reason instead of failing open.
func (a nutanixAdapter) CanMigrate(name string) (bool, string) {
	client, err := a.client()
	if err != nil {
		return false, err.Error()
	}
	return client.CanMigrate(name)
}

func (a nutanixAdapter) Clone(source, target string) error {
	return a.complete(func(c *nutanix.Client) (nutanix.Task, error) { return c.Clone(source, target) })
}

// firstImageStorage picks a storage that accepts disk images, so AddDisk works
// without the caller knowing the cluster's storage layout.
func firstImageStorage(client *proxmoxbe.Client) (string, error) {
	storages, err := client.Storages()
	if err != nil {
		return "", err
	}
	for _, s := range storages {
		if s.Holds("images") {
			return s.Storage, nil
		}
	}
	return "", fmt.Errorf("proxmox: no storage on this cluster accepts disk images")
}

// backendNames is every backend with an adapter, for error messages that need to
// list them.
func backendNames() string {
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
