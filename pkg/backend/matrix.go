// Package backend is the parity matrix: for every operation Corral ships, what
// each backend does about it — and where it does nothing, whether that is
// because the backend cannot, or only because Corral has not.
//
// It exists because the honest state of the codebase was "KubeVirt is
// first-class and the rest are best effort", and that state was invisible.
// Capabilities were a hardcoded table in types.CapabilitiesForBackend, the
// richer operations were reached by `if backend == "kubevirt"` branches in
// cmd/ and pkg/web, and nothing anywhere said which absences were deliberate.
// A gap you cannot enumerate is a gap nobody closes.
//
// So the matrix is data, in one place, and it is enforced: conformance tests
// check it against the declared capabilities, against pkg/snapshot's adapter
// registry, and against docs/backend-parity.md, so the table in the docs cannot
// drift from the code and a capability cannot be advertised that nothing
// implements.
//
// The rule this encodes, per the parity decision: if a backend can do a thing
// we ship, we want to support it. Not every backend feature — parity across
// backends for the features Corral has.
package backend

import "sort"

// Support is what Corral does about one operation on one backend.
type Support string

const (
	// Shipped — implemented and reachable from at least one surface.
	Shipped Support = "shipped"
	// Possible — the backend can do this and Corral does not yet. Note names
	// the native mechanism, so the gap is a work item rather than a shrug.
	Possible Support = "possible"
	// Unsupported — the backend genuinely cannot, or the operation is
	// meaningless there. Note says why, so nobody re-litigates it quarterly.
	Unsupported Support = "unsupported"
)

// Backends are the compute backends, in the order tables should show them.
var Backends = []string{"kubevirt", "qemu", "incus", "libvirt", "proxmox", "virtualbox", "vsphere", "xen", "nutanix"}

// Operation is one capability-bearing thing Corral does to an instance.
type Operation struct {
	// ID is stable and used by tests and docs.
	ID string
	// Title is how the docs name it.
	Title string
	// Capability is the types.InstanceCapabilities field this operation gates
	// on, or "" for operations that have no capability flag yet.
	Capability string
}

// Operations are every instance operation Corral ships, in reading order.
var Operations = []Operation{
	{ID: "list", Title: "List / inventory"},
	{ID: "create", Title: "Create"},
	{ID: "start", Title: "Start", Capability: "Start"},
	{ID: "stop", Title: "Stop", Capability: "Stop"},
	{ID: "restart", Title: "Restart"},
	{ID: "pause", Title: "Pause / resume"},
	{ID: "delete", Title: "Delete", Capability: "Delete"},
	{ID: "ssh", Title: "SSH", Capability: "SSH"},
	{ID: "tty", Title: "Serial / shell console", Capability: "TTY"},
	{ID: "vnc", Title: "Graphical console (VNC)", Capability: "VNC"},
	{ID: "rdp", Title: "RDP", Capability: "RDP"},
	{ID: "metrics", Title: "Live CPU / memory", Capability: "Metrics"},
	{ID: "snapshots", Title: "Snapshot / restore", Capability: "Snapshots"},
	{ID: "migrate", Title: "Migrate", Capability: "Migrate"},
	{ID: "clone", Title: "Clone"},
	{ID: "template", Title: "Template mark"},
	{ID: "scale", Title: "CPU / memory edit"},
	{ID: "volumes", Title: "Add / remove disks", Capability: "Volumes"},
	{ID: "expand", Title: "Expand disk"},
	{ID: "gpu", Title: "GPU passthrough", Capability: "GPU"},
	{ID: "export", Title: "Export / backup disk"},
	{ID: "events", Title: "Events"},
	{ID: "tags", Title: "Tags"},
	{ID: "ports", Title: "Published ports"},
	{ID: "containers", Title: "Containers (CT)"},
}

// Entry is one cell: what a backend does about an operation, and why.
type Entry struct {
	Support Support
	// Note is the native mechanism for Possible, the reason for Unsupported,
	// and the implementing path for Shipped. Required in every case — a cell
	// without a note is a cell nobody can act on.
	Note string
}

// Matrix is operation ID → backend → entry. It is the single source of truth
// the docs table and the conformance tests both read.
//
// Every cell was checked against the code, not inferred from intent; where the
// audit found something surprising the note says so.
var Matrix = map[string]map[string]Entry{
	"list": {
		"kubevirt":   {Shipped, "kubectl get vms/vmis, pkg/kubevirt"},
		"qemu":       {Shipped, "systemd user units + metadata, pkg/qemu"},
		"incus":      {Shipped, "incus list --format=json, pkg/incus"},
		"libvirt":    {Shipped, "virsh list --all, pkg/libvirt"},
		"proxmox":    {Shipped, "GET /cluster/resources, pkg/proxmoxbe.List — one call for the whole cluster"},
		"virtualbox": {Shipped, "VBoxManage list vms plus showvminfo --machinereadable per VM, pkg/virtualbox"},
		"vsphere":    {Shipped, "container view over VirtualMachine, pkg/vsphere.List — one call for the whole estate"},
		"xen":        {Shipped, "VM.get_all plus VM.get_record per VM, pkg/xen.List — host names and disk sizes joined in"},
		"nutanix":    {Shipped, "GET /vmm/{ver}/ahv/config/vms, pkg/nutanix.List — OData pages cover the whole estate"},
	},
	"create": {
		"kubevirt":   {Shipped, "pkg/web/create_*.go, cmd/create.go"},
		"qemu":       {Shipped, "qemu-img + generated systemd unit, pkg/qemu.Create"},
		"incus":      {Shipped, "incus launch, pkg/incus.Create"},
		"libvirt":    {Shipped, "virt-install, pkg/libvirt.Create"},
		"proxmox":    {Shipped, "POST /nodes/{node}/qemu or /lxc, pkg/proxmoxbe.Create"},
		"virtualbox": {Possible, "createvm plus storagectl/storageattach/modifyvm exist natively; Corral has no creator wiring them yet"},
		"vsphere":    {Possible, "CloneVM and OVF deploy exist natively; Corral has no creator wiring them yet"},
		"xen":        {Possible, "VM.create exists natively; Corral has no creator wiring it yet"},
		"nutanix":    {Possible, "VM create exists in vmm natively; Corral has no creator wiring it yet"},
	},
	"start": {
		"kubevirt":   {Shipped, "virtctl start"},
		"qemu":       {Shipped, "systemctl --user start"},
		"incus":      {Shipped, "incus start"},
		"libvirt":    {Shipped, "virsh start"},
		"proxmox":    {Shipped, "POST …/status/start"},
		"virtualbox": {Shipped, "startvm --type headless"},
		"vsphere":    {Shipped, "PowerOnVM"},
		"xen":        {Shipped, "VM.start with paused and force clear — a plain boot, never a forced one"},
		"nutanix":    {Shipped, "POST $actions/power-on — a plain boot, never a forced one"},
	},
	"stop": {
		"kubevirt":   {Shipped, "virtctl stop"},
		"qemu":       {Shipped, "systemctl --user stop"},
		"incus":      {Shipped, "incus stop"},
		"libvirt":    {Shipped, "virsh shutdown"},
		"proxmox":    {Shipped, "POST …/status/shutdown; the forced power cut is Kill, chosen by name"},
		"virtualbox": {Shipped, "controlvm acpipowerbutton, falling back to controlvm poweroff when the guest ignores ACPI"},
		"vsphere":    {Shipped, "ShutdownGuest, falling back to PowerOff when the guest ignores the request"},
		"xen":        {Shipped, "VM.clean_shutdown, falling back to VM.hard_shutdown when the guest ignores the request"},
		"nutanix":    {Shipped, "POST $actions/shutdown, falling back to $actions/power-off when the guest ignores the request"},
	},
	"restart": {
		"kubevirt":   {Shipped, "virtctl restart"},
		"qemu":       {Shipped, "stop then start, cmd/ops.go"},
		"incus":      {Shipped, "incus restart — the backend's own, so the guest's shutdown ordering survives"},
		"libvirt":    {Shipped, "virsh reboot — an ACPI request; a guest that ignores it stays up, which is honest"},
		"proxmox":    {Shipped, "POST …/status/reboot — a real reboot, not stop-then-start"},
		"virtualbox": {Shipped, "controlvm reset — the backend's own reset, not stop-then-start"},
		"vsphere":    {Shipped, "ResetVM — the backend's own reset, not stop-then-start"},
		"xen":        {Shipped, "VM.clean_reboot with VM.hard_reboot fallback — a real reboot, not stop-then-start"},
		"nutanix":    {Shipped, "POST $actions/reboot with $actions/power-cycle fallback — a real reboot, not stop-then-start"},
	},
	"pause": {
		"kubevirt":   {Shipped, "virtctl pause / unpause"},
		"qemu":       {Shipped, "QMP stop/cont on the unit's QMP socket, pkg/qemu.Pause"},
		"incus":      {Shipped, "incus pause / incus start, pkg/incus.Pause"},
		"libvirt":    {Shipped, "virsh suspend / virsh resume, pkg/libvirt.Pause"},
		"proxmox":    {Shipped, "POST …/status/suspend and /resume"},
		"virtualbox": {Shipped, "controlvm pause / resume, the guest frozen to memory"},
		"vsphere":    {Shipped, "SuspendVM with PowerOn resume, the guest frozen to memory"},
		"xen":        {Shipped, "VM.suspend with VM.resume, the guest frozen to memory"},
		"nutanix":    {Shipped, "POST $actions/suspend with $actions/resume, the guest frozen to memory"},
	},
	"delete": {
		"kubevirt":   {Shipped, "kubectl delete vm"},
		"qemu":       {Shipped, "unit removal + disk delete"},
		"incus":      {Shipped, "incus delete --force"},
		"libvirt":    {Shipped, "virsh undefine --remove-all-storage"},
		"proxmox":    {Shipped, "DELETE …?purge=1, so backup jobs do not outlive the guest"},
		"virtualbox": {Shipped, "unregistervm --delete, which removes the registration and deletes the disk images"},
		"vsphere":    {Shipped, "Destroy, powering off first because vCenter refuses a running guest"},
		"xen":        {Shipped, "VM.destroy of the halted guest plus VDI.destroy per attached disk"},
		"nutanix":    {Shipped, "power-off first, then DELETE with the ETag; pkg/nutanix.Destroy"},
	},
	"ssh": {
		"kubevirt":   {Shipped, "virtctl ssh"},
		"qemu":       {Shipped, "ssh via hostfwd port, pkg/qemu.SSH"},
		"incus":      {Shipped, "incus exec … ssh, pkg/incus.SSH"},
		"libvirt":    {Possible, "the domain's address via the guest agent or DHCP leases, then plain ssh — pkg/libvirt has SSH but the TUI does not offer it because the capability table omits it"},
		"proxmox":    {Shipped, "the guest agent (VM) or interface list (CT) for the address, then plain ssh"},
		"virtualbox": {Possible, "the guest address via Guest Additions guestproperty or a NAT port-forward, then plain ssh — no address discovery is wired yet"},
		"vsphere":    {Possible, "the guest address via VMware Tools, then plain ssh — no address discovery is wired yet"},
		"xen":        {Possible, "the guest address via guest metrics, then plain ssh — no address discovery is wired yet"},
		"nutanix":    {Possible, "the NIC address sits in inventory, then plain ssh — no address discovery is wired yet"},
	},
	"tty": {
		"kubevirt":   {Shipped, "virtctl console, web ttyBridge"},
		"qemu":       {Possible, "the serial socket the generated unit already defines"},
		"incus":      {Shipped, "incus exec through the web ttyBridge (pkg/web/server.go); the TUI has no TTY view for any backend yet"},
		"libvirt":    {Possible, "virsh console"},
		"proxmox":    {Possible, "termproxy tickets are implemented (pkg/proxmoxbe.TermTicket); the web websocket bridge is not wired yet"},
		"virtualbox": {Possible, "the UART is redirectable to a host pipe via modifyvm --uartmode1; Corral has no console bridge for it"},
		"vsphere":    {Possible, "the serial port is redirectable to host or network; Corral has no console bridge for it"},
		"xen":        {Possible, "vt100 console objects serve the guest console natively; Corral wires no bridge to them"},
		"nutanix":    {Unsupported, "the v4 reference proves no per-VM console endpoint; nothing to bridge"},
	},
	"vnc": {
		"kubevirt":   {Shipped, "virtctl vnc --proxy-only, noVNC in the browser"},
		"qemu":       {Shipped, "VNC display on the host, pkg/qemu.VNCAddr"},
		"incus":      {Possible, "incus console --type=vga for Incus VMs; the web vncBridge handles local, libvirt, and cluster namespaces only"},
		"libvirt":    {Shipped, "pkg/libvirt.DialVNC, including over SSH"},
		"proxmox":    {Possible, "vncproxy tickets are implemented (pkg/proxmoxbe.VNCTicket); the web websocket bridge is not wired yet"},
		"virtualbox": {Possible, "VRDE serves the guest display natively; Corral wires no viewer or bridge to it"},
		"vsphere":    {Possible, "MKS/webmks tickets exist (AcquireTicket); Corral wires no viewer or bridge to them"},
		"xen":        {Possible, "rfb console objects hand out display locations; Corral wires no viewer or bridge to them"},
		"nutanix":    {Unsupported, "the v4 reference proves no per-VM display endpoint; nothing to view"},
	},
	"rdp": {
		"kubevirt":   {Shipped, "port-forward + IronRDP, ADR-0002"},
		"qemu":       {Possible, "the same probe and bridge over the hostfwd port"},
		"incus":      {Possible, "same, via the instance address"},
		"libvirt":    {Possible, "same, via the domain address"},
		"proxmox":    {Possible, "same, via the guest address"},
		"virtualbox": {Possible, "VRDE is an RDP server already; Corral has no probe or bridge for it"},
		"vsphere":    {Possible, "same probe and bridge via the guest address"},
		"xen":        {Possible, "same probe and bridge via the guest address"},
		"nutanix":    {Unsupported, "the v4 reference proves no per-VM console endpoint; nothing to bridge"},
	},
	"metrics": {
		"kubevirt":   {Shipped, "kubectl top pod, metrics-server"},
		"qemu":       {Shipped, "the unit's cgroup via systemctl show, pkg/qemu.Metrics — host-side cost, sampled twice for a CPU rate"},
		"incus":      {Shipped, "incus query /1.0/instances/{name}/state, pkg/incus.Metrics"},
		"libvirt":    {Shipped, "virsh domstats --cpu-total --balloon, pkg/libvirt.Metrics"},
		"proxmox":    {Shipped, "GET …/status/current for live usage, /rrddata for the sparkline's real history"},
		"virtualbox": {Possible, "VBoxManage metrics query exposes host-side CPU and memory counters; no sampler is wired yet"},
		"vsphere":    {Possible, "performance manager counters; no sampler is wired yet"},
		"xen":        {Possible, "VM_metrics and host_metrics counters; no sampler is wired yet"},
		"nutanix":    {Possible, "v4 stats and v1 REST counters exist; no sampler is wired yet"},
	},
	"snapshots": {
		"kubevirt":   {Shipped, "VirtualMachineSnapshot, pkg/snapshot.KubeVirt"},
		"qemu":       {Shipped, "qemu-img internal snapshots, offline only, pkg/snapshot.QEMU"},
		"incus":      {Shipped, "incus snapshot, pkg/snapshot.Incus"},
		"libvirt":    {Shipped, "virsh snapshot-create-as --atomic, pkg/snapshot.Libvirt"},
		"proxmox":    {Shipped, "pkg/snapshot.Proxmox; vmstate=1 captures RAM, so a running guest is honestly consistent"},
		"virtualbox": {Shipped, "snapshot take/delete/restore, pkg/snapshot.VirtualBox — a running capture carries RAM but nothing quiesced, so it reports crash consistency; restore refuses a running guest"},
		"vsphere":    {Shipped, "Create/Remove/RevertSnapshot, pkg/snapshot.Vsphere — a running capture carries RAM but nothing quiesced, so it reports crash consistency unless the tools quiesced it"},
		"xen":        {Shipped, "VM.snapshot/revert/destroy, pkg/snapshot.Xen — XenAPI snapshots without quiesce, so a running capture reports crash consistency and a halted one offline"},
		"nutanix":    {Shipped, "recovery-point create/delete/revert, pkg/snapshot.Nutanix — crash-consistent only, never filesystem"},
	},
	"migrate": {
		"kubevirt":   {Shipped, "VirtualMachineInstanceMigration"},
		"qemu":       {Unsupported, "one host; there is nowhere to migrate to"},
		"incus":      {Possible, "incus move, including between remotes"},
		"libvirt":    {Possible, "virsh migrate --live to another URI"},
		"proxmox":    {Shipped, "POST …/migrate with online=1 (restart=1 for LXC), and GET …/migrate as a real precondition check"},
		"virtualbox": {Unsupported, "one host; there is nowhere to migrate to"},
		"vsphere":    {Shipped, "RelocateVM/vMotion, and CanMigrate as a real precondition check"},
		"xen":        {Shipped, "VM.pool_migrate, and CanMigrate as a real precondition check"},
		"nutanix":    {Shipped, "POST $actions/migrate to a host extId; CanMigrate approximates from power state until host listing lands"},
	},
	"clone": {
		"kubevirt":   {Shipped, "VirtualMachineClone"},
		"qemu":       {Possible, "qemu-img convert plus a new unit"},
		"incus":      {Possible, "incus copy"},
		"libvirt":    {Possible, "virt-clone"},
		"proxmox":    {Shipped, "POST …/clone, full by default so a clone does not depend on its parent forever"},
		"virtualbox": {Possible, "clonevm"},
		"vsphere":    {Shipped, "CloneVM, full by default so a clone does not depend on its parent"},
		"xen":        {Shipped, "VM.clone, full by default so a clone does not depend on its parent"},
		"nutanix":    {Shipped, "POST $actions/clone, full by default so a clone does not depend on its parent"},
	},
	"template": {
		"kubevirt":   {Shipped, "corral.dev/template label"},
		"qemu":       {Possible, "the same mark in the local registry"},
		"incus":      {Possible, "incus publish, or the registry mark"},
		"libvirt":    {Possible, "the registry mark"},
		"proxmox":    {Shipped, "POST …/template — native, and one-way: unmarking is refused with the reason"},
		"virtualbox": {Possible, "no native mark; the local registry mark, the same mechanism qemu would use"},
		"vsphere":    {Shipped, "MarkAsTemplate — native, and one-way: unmarking is refused with the reason"},
		"xen":        {Possible, "VM.set_is_a_template marks natively; Corral has no templater wiring it yet"},
		"nutanix":    {Unsupported, "no template-mark endpoint proven in the v4 reference; clone covers the golden-image path"},
	},
	"scale": {
		"kubevirt":   {Shipped, "patch domain cpu/memory, hotplug where migratable"},
		"qemu":       {Possible, "rewrite the unit and restart"},
		"incus":      {Possible, "incus config set limits.cpu / limits.memory, live"},
		"libvirt":    {Possible, "virsh setvcpus / setmem"},
		"proxmox":    {Shipped, "PUT …/config cores/memory; the config's hotplug field says whether it applies live"},
		"virtualbox": {Possible, "modifyvm --cpus/--memory while stopped"},
		"vsphere":    {Shipped, "ReconfigVM cores/memory; the config's hot-add flags say whether it applies live"},
		"xen":        {Possible, "VCPUs and memory set calls exist natively; nothing drives them yet"},
		"nutanix":    {Possible, "PUT config updates cores and memory natively; nothing drives them yet"},
	},
	"volumes": {
		"kubevirt":   {Shipped, "addvolume / removevolume, DataVolumes"},
		"qemu":       {Possible, "qemu-img create plus a unit edit"},
		"incus":      {Possible, "incus storage volume attach"},
		"libvirt":    {Possible, "virsh attach-disk / detach-disk"},
		"proxmox":    {Shipped, "PUT …/config scsiN to attach, delete= to remove"},
		"virtualbox": {Possible, "storageattach/storagectl plus closemedium; nothing drives them yet"},
		"vsphere":    {Possible, "reconfig device attach and detach; nothing drives them yet"},
		"xen":        {Possible, "VBD plug/unplug and VDI create exist natively; nothing drives them yet"},
		"nutanix":    {Possible, "disk device endpoints exist in vmm natively; nothing drives them yet"},
	},
	"expand": {
		"kubevirt":   {Shipped, "PVC resize where the storage class allows"},
		"qemu":       {Possible, "qemu-img resize while stopped"},
		"incus":      {Possible, "incus config device set … size"},
		"libvirt":    {Possible, "virsh blockresize"},
		"proxmox":    {Shipped, "PUT …/resize; PVE refuses to shrink, which is what Corral wants"},
		"virtualbox": {Possible, "modifymedium --resize while stopped"},
		"vsphere":    {Possible, "virtual disk extend via reconfig; nothing drives it yet"},
		"xen":        {Possible, "VDI.resize grows a disk natively; nothing drives it yet"},
		"nutanix":    {Possible, "disk update exists in vmm natively; nothing drives it yet"},
	},
	"gpu": {
		"kubevirt":   {Shipped, "GPU device plugin, pkg/web/gpu.go"},
		"qemu":       {Possible, "vfio-pci in the generated unit"},
		"incus":      {Possible, "incus config device add … gpu"},
		"libvirt":    {Possible, "hostdev in the domain XML"},
		"proxmox":    {Shipped, "PUT …/config hostpciN"},
		"virtualbox": {Possible, "PCI passthrough via modifyvm --pciattach on Linux hosts; experimental and host-limited"},
		"vsphere":    {Possible, "PCI passthrough via reconfig device attach; experimental and host-limited"},
		"xen":        {Possible, "PCI device assignment exists natively; experimental and host-limited"},
		"nutanix":    {Unsupported, "no GPU passthrough endpoint proven in the v4 reference"},
	},
	"export": {
		"kubevirt":   {Shipped, "virtctl vmexport, qcow2 or raw.gz — pkg/export.KubeVirt"},
		"qemu":       {Shipped, "qemu-img convert while stopped — pkg/export.QEMU"},
		"incus":      {Shipped, "incus export; qcow2 pulls the boot disk out of the archive — pkg/export.Incus"},
		"libvirt":    {Shipped, "qemu-img convert of the backing volume — pkg/export.Libvirt"},
		"proxmox":    {Shipped, "vzdump in snapshot mode; the export adapter downloads the backup over the PVE API, extracts the disk via vma, and converts to qcow2 — pkg/proxmoxbe.Export"},
		"virtualbox": {Possible, "export to OVA natively; pkg/export has no VirtualBox adapter yet"},
		"vsphere":    {Possible, "ExportVm serves an OVF/NFC lease natively; pkg/export has no vSphere adapter yet"},
		"xen":        {Possible, "GET /export serves XVA natively; pkg/export has no Xen adapter yet"},
		"nutanix":    {Possible, "recovery points exist in dataprotection but no download path is proven; pkg/export has no Nutanix adapter yet"},
	},
	"events": {
		"kubevirt":   {Shipped, "kubectl get events for the VM and its launcher pod"},
		"qemu":       {Shipped, "journalctl --user for the unit, pkg/qemu.Events"},
		"incus":      {Unsupported, "incus monitor is a subscription to what happens next; Incus keeps no queryable event history, so there is nothing to read at the moment a view asks"},
		"libvirt":    {Unsupported, "virsh event likewise only streams; libvirt keeps no per-domain event log. Both would need Corral to run a collector and store the history itself, which is a feature rather than an adapter"},
		"proxmox":    {Shipped, "GET /nodes/{node}/tasks filtered by vmid — PVE's task history is its event record"},
		"virtualbox": {Unsupported, "VBox keeps no queryable per-VM event history; like libvirt and Incus this would need Corral to run a collector and store the history itself"},
		"vsphere":    {Possible, "the event manager keeps per-VM history; no reader is wired yet"},
		"xen":        {Possible, "the event class records pool activity; Corral wires no reader for it yet"},
		"nutanix":    {Possible, "the prism task list records activity; Corral wires no per-VM reader for it yet"},
	},
	"tags": {
		"kubevirt":   {Shipped, "corral.dev/tag.<name> labels"},
		"qemu":       {Possible, "the local registry, which already persists per-VM state"},
		"incus":      {Possible, "instance config user.corral.tag.<name>"},
		"libvirt":    {Possible, "domain metadata"},
		"proxmox":    {Shipped, "the config's own tags field, read-modify-write so a second tag does not erase the first"},
		"virtualbox": {Possible, "setextradata keys or the registry mark; nothing reads them back yet"},
		"vsphere":    {Possible, "vSphere tags exist natively; nothing drives them yet"},
		"xen":        {Possible, "the VM record carries tags natively; Corral has no tagger writing them yet"},
		"nutanix":    {Shipped, "$actions/associate-categories; Corral tags ride one category key, so governed values may refuse"},
	},
	"ports": {
		"kubevirt":   {Shipped, "port-proxy Service on the tailnet, ApplyProxy"},
		"qemu":       {Shipped, "hostfwd in the generated unit"},
		"incus":      {Possible, "incus config device add … proxy"},
		"libvirt":    {Unsupported, "no port mapping of Corral's own; the domain is on the host network"},
		"proxmox":    {Unsupported, "the guest is bridged; PVE has no per-VM port mapping to drive"},
		"virtualbox": {Possible, "NAT port-forwarding rules via modifyvm --natpf1; Corral does not manage them yet"},
		"vsphere":    {Unsupported, "the guest is bridged; vSphere has no per-VM port mapping to drive"},
		"xen":        {Unsupported, "the guest is bridged; Xen has no per-VM port mapping to drive"},
		"nutanix":    {Unsupported, "no per-VM port mapping in the v4 reference; Corral manages none"},
	},
	"containers": {
		"kubevirt":   {Shipped, "pet-pod CTs, ADR-0005, pkg/ct"},
		"qemu":       {Unsupported, "a hypervisor, not a container runtime"},
		"incus":      {Shipped, "LXC containers listed and controlled as CTs through pkg/incus.Containers; Incus virtual machines are VMs, not CTs"},
		"libvirt":    {Unsupported, "libvirt's LXC driver is not something Corral drives"},
		"proxmox":    {Possible, "pkg/proxmoxbe.Containers lists them and Create makes them; pkg/ct does not yet surface a non-Kubernetes CT"},
		"virtualbox": {Unsupported, "a hypervisor, not a container runtime"},
		"vsphere":    {Unsupported, "a hypervisor, not a container runtime"},
		"xen":        {Unsupported, "a hypervisor, not a container runtime"},
		"nutanix":    {Unsupported, "a hypervisor, not a container runtime"},
	},
}

// Get returns one cell.
func Get(operation, backend string) (Entry, bool) {
	row, ok := Matrix[operation]
	if !ok {
		return Entry{}, false
	}
	entry, ok := row[backend]
	return entry, ok
}

// OperationIDs returns every operation ID in reading order.
func OperationIDs() []string {
	ids := make([]string, 0, len(Operations))
	for _, op := range Operations {
		ids = append(ids, op.ID)
	}
	return ids
}

// Gaps returns the operations a backend could support and does not, in reading
// order — the work list this package exists to make enumerable.
func Gaps(backend string) []string {
	var out []string
	for _, op := range Operations {
		if entry, ok := Get(op.ID, backend); ok && entry.Support == Possible {
			out = append(out, op.ID)
		}
	}
	return out
}

// ShippedBy returns the backends that ship an operation, sorted.
func ShippedBy(operation string) []string {
	var out []string
	for backend, entry := range Matrix[operation] {
		if entry.Support == Shipped {
			out = append(out, backend)
		}
	}
	sort.Strings(out)
	return out
}
