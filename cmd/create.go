package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/plugin"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

var (
	createKubevirt          bool
	createIncus             bool
	createMem               string
	createCPU               int
	createDisk              string
	createISO               string
	createQCOW              string
	createForce             bool
	createContainerDisk     string
	createImage             string
	createImport            string
	createPVC               string
	createNamespace         string
	createNode              string
	createCloudInitPassword string
	createCloudInit         string
	createInstanceType      string
	createPreference        string
	createFile              string
	createStorageClass      string
	createBootc             string
	createProvisionScript   string
	createKargs             []string
	createStart             bool
	createWaitSSH           bool
	createTimeout           int
	createSSHUser           string
	createEphemeral         bool
	createTTL               string
	createLAN               bool
	createNetworkNAD        string
	createBridgeIface       string
	createLANService        bool
	createFirmware          string
	createArch              string
	createLoopback          bool
	createVsock             bool
	createVsockCID          uint32
	createTPM               bool
	createDedicatedCPUs     bool
	createIsolateEmulator   bool
	createTags              []string
)

// limaFile is the Lima YAML format — corral reads Lima files natively.
// Only the subset of fields that map to KubeVirt are parsed; the rest
// (mounts, networks, etc.) are ignored with a warning where applicable.
type limaFile struct {
	Bootc     string          `yaml:"bootc"`
	Images    []limaImage     `yaml:"images"`
	CPUs      int             `yaml:"cpus"`
	Memory    string          `yaml:"memory"`
	Disk      string          `yaml:"disk"`
	Provision []limaProvision `yaml:"provision"`
}

type limaImage struct {
	Location string `yaml:"location"`
	Arch     string `yaml:"arch"`
}

type limaProvision struct {
	Mode   string `yaml:"mode"`
	Script string `yaml:"script"`
}

func loadLimaFile(path string) (*limaFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var spec limaFile
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &spec, nil
}

// parseLimaMemory converts Lima-formatted memory strings ("16GiB", "8G", "4096")
// to the corral format ("16G", "8G", "4096M").
func parseLimaMemory(s string) string {
	s = strings.TrimSuffix(s, "iB")
	s = strings.TrimSuffix(s, "B")
	// If it's just a number, treat as MiB
	if _, err := fmt.Sscanf(s, "%d", new(int)); err == nil && !strings.ContainsAny(s, "GMK") {
		return s + "M"
	}
	return s
}

// parseLimaDisk converts Lima-formatted disk strings ("60GiB", "40G") to
// corral format ("60G", "40G").
func parseLimaDisk(s string) string {
	return parseLimaMemory(s)
}

// limaScriptToCloudInit converts a shell provisioning script to a cloud-init
// runcmd entry. The entire script runs as a single sh invocation so that
// multi-line constructs (if/fi, loops, backslash continuations) work.
func limaScriptToCloudInit(script string) string {
	script = strings.TrimSpace(script)
	// Strip the shebang line — cloud-init runs via sh -c.
	if strings.HasPrefix(script, "#!") {
		idx := strings.Index(script, "\n")
		if idx >= 0 {
			script = strings.TrimSpace(script[idx+1:])
		}
	}
	if script == "" {
		return ""
	}
	// YAML literal block scalar (|) keeps newlines intact inside cloud-init.
	return "runcmd:\n  - |\n" + indentLines(script, "    ")
}

// indentLines prepends prefix to every line of s.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// applyLimaFile applies Lima YAML fields to the corral create flags.  Returns
// the effective VM name (from positional arg, unchanged by Lima — Lima files
// don't have a name field).
func applyLimaFile(spec *limaFile) {
	if spec.Bootc != "" {
		createBootc = spec.Bootc
	}
	var rawScripts []string
	for _, p := range spec.Provision {
		if p.Script != "" {
			rawScripts = append(rawScripts, p.Script)
		}
	}
	if len(rawScripts) > 0 {
		createProvisionScript = strings.Join(rawScripts, "\n")
	}

	if spec.CPUs != 0 && createCPU == 2 {
		createCPU = spec.CPUs
	}
	if spec.Memory != "" && createMem == "4G" {
		createMem = parseLimaMemory(spec.Memory)
	}
	if spec.Disk != "" && createDisk == "" {
		createDisk = parseLimaDisk(spec.Disk)
	}

	// Resolve the first image as the boot source.
	if len(spec.Images) > 0 && createContainerDisk == "" && createImage == "" && createISO == "" && createImport == "" {
		loc := spec.Images[0].Location
		switch {
		case strings.HasSuffix(loc, ".iso") || strings.HasPrefix(loc, "https://") && strings.Contains(loc, ".iso"):
			createISO = loc
		case strings.HasSuffix(loc, ".qcow2") || strings.HasSuffix(loc, ".raw") || strings.HasSuffix(loc, ".img"):
			createImport = loc
		case strings.Contains(loc, "/") && strings.Contains(loc, ":"):
			// OCI image reference (e.g. quay.io/containerdisks/fedora:44)
			createContainerDisk = loc
		default:
			// Bare name — try catalog lookup
			createImage = loc
		}
	}

	// Convert provision scripts to cloud-init.
	if createCloudInit == "" {
		var parts []string
		for _, p := range spec.Provision {
			if p.Script == "" {
				continue
			}
			ci := limaScriptToCloudInit(p.Script)
			if ci != "" {
				parts = append(parts, ci)
			}
		}
		if len(parts) > 0 {
			createCloudInit = "#cloud-config\n" + strings.Join(parts, "\n")
		}
	}
}

var createCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new VM",
	Long: `Create a new virtual machine.

By default, creates a local QEMU/KVM VM. Use --kubevirt for a
Kubernetes KubeVirt VM. The backend choice is persisted so
subsequent commands (start, stop, viewer...) auto-detect it.

QEMU examples:
  corral create myvm --iso https://example.com/ubuntu.iso
  corral create myvm --qcow ./template.qcow2 --disk 40G

KubeVirt examples:
  corral create myvm --kubevirt --iso https://example.com/bluefin.iso
  corral create myvm --kubevirt --container-disk quay.io/containerdisks/ubuntu:24.04

By default a KubeVirt VM only gets a NATed pod-network interface — it can
reach the internet but not LAN-only devices (a smartwatch, a NAS, a router
admin panel). Two ways to fix that (see docs/kubevirt-proxmox-setup.md
§6 for cluster-side setup):
  --lan bridges a second NIC onto a Multus NetworkAttachmentDefinition —
    a real secondary interface, needs Multus installed:
      corral create myvm --kubevirt --image fedora --lan
      corral create myvm --kubevirt --image fedora --network-nad default/lan-bridge
  --lan-service exposes via a LoadBalancer Service instead — no Multus, no
    new interface; needs a controller that fulfills LoadBalancer Services
    (Cilium's own L2 Announcement/BGP, or MetalLB):
      corral create myvm --kubevirt --image fedora --lan-service

Boot a container image as a VM? Install the bootc extension:
  corral plugin install bootc && corral bootc create myvm --image quay.io/centos-bootc/centos-bootc:stream9

Matched VMs for performance comparisons: create each VM with the same
--node, --cpu, --mem, and --disk, add --dedicated-cpus, and group the
pair with --tag (stored as corral.dev/tag.<key> labels):
  corral create gnome --kubevirt --image fedora --node n1 --cpu 4 --mem 6G --dedicated-cpus --tag pair=run42`,
	Example: `  corral create myvm --kubevirt --image fedora
  corral create myvm --iso ./install.iso --disk 40G
  corral create scratch --kubevirt --image bluefin --ephemeral --ttl 2h
  corral create gate --bootc ghcr.io/tuna-os/yellowfin:gnome --wait-ssh --timeout 900
  corral create gate --bootc ghcr.io/tuna-os/yellowfin:gnome --karg tunaos.countme=0 --wait-ssh
  corral create builder --kubevirt --image fedora --lan   # direct LAN access via a Multus bridge NAD
  corral create builder --kubevirt --image fedora --lan-service   # LAN access via LoadBalancer Service (Cilium/MetalLB)
  corral create perf-a --kubevirt --image fedora --node n1 --dedicated-cpus --tag pair=run42 --tag desktop=gnome`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		// --file reads a Lima YAML file — same format limactl uses.
		if createFile != "" {
			spec, err := loadLimaFile(createFile)
			if err != nil {
				return err
			}
			applyLimaFile(spec)
			if spec.Bootc == "" {
				createKubevirt = true
			}
		}

		return executeCreate(name, cmd.Flags().Changed("kubevirt"))
	},
}

// executeCreate runs the backend-selection and dispatch that turns the
// create* package vars into a VM. It is shared by the `corral create` CLI
// command and the TUI create form (cmd/tui_create.go) so both stay in lockstep
// — one place decides which backend and create path a given set of options maps
// to. kubevirtExplicit reports whether the caller explicitly chose KubeVirt
// (the CLI's --kubevirt flag, or the TUI's backend picker); it governs bootc's
// otherwise-automatic backend selection.
func executeCreate(name string, kubevirtExplicit bool) error {
	if err := validateCreateGuestOptions("qemu"); err != nil {
		return err
	}
	if len(createKargs) > 0 && createBootc == "" {
		return fmt.Errorf("--karg needs --bootc: only a bootc install puts kernel arguments into the boot entry")
	}
	if err := kubevirt.ValidateKargs(createKargs); err != nil {
		return err
	}
	if _, err := createPlacement(); err != nil {
		return err
	}
	if existing := resolveBackend(name); existing != "" && !createForce {
		return fmt.Errorf("VM %q already exists (backend: %s). Use --force to overwrite", name, existing)
	}

	if createBootc != "" {
		// The bootc builders choose their image architecture independently.
		// Do not pretend an emulator flag also selects a build architecture.
		if err := validateCreateGuestOptions("bootc"); err != nil {
			return err
		}
		// Backend auto-selection: honor an explicit KubeVirt choice, otherwise
		// use KubeVirt when a cluster is reachable and fall back to local
		// QEMU. One command, right backend, no flag juggling.
		useKubevirt := createKubevirt
		if !kubevirtExplicit {
			useKubevirt = kubevirt.Reachable()
		}
		if useKubevirt {
			return runKubevirtBootcCreate(name)
		}
		if err := refuseKubevirtOnlyFlags("qemu"); err != nil {
			return err
		}
		if err := runLocalBootcCreate(name); err != nil {
			return err
		}
		return maybeStartAndWait(name)
	}

	backend := config.DefaultBackend()
	if rootBackend != "" {
		backend = rootBackend
	} else if rootContext != "" {
		if target, ok := config.FindContext(rootContext); ok {
			backend = target.Backend
		}
	}
	if createIncus {
		backend = "incus"
	}
	if createKubevirt {
		backend = "kubevirt"
	}
	// KubeVirt-only source flags remain an explicit signal for backwards compatibility.
	if backend == "qemu" && !createIncus && (createImage != "" || createImport != "" || createContainerDisk != "" || createPVC != "") {
		backend = "kubevirt"
	}
	if err := validateCreateGuestOptions(backend); err != nil {
		return err
	}
	switch backend {
	case "kubevirt":
		return runKubevirtCreate(name)
	}
	// Every other backend: the KubeVirt-only placement flags are refused,
	// not dropped.
	if err := refuseKubevirtOnlyFlags(backend); err != nil {
		return err
	}
	switch backend {
	case "incus":
		return runIncusCreate(name)
	case "libvirt":
		if err := libvirt.NewClient(createContext("libvirt")).Create(types.CreateOpts{Name: name, CPU: createCPU, Mem: createMem, Disk: createDisk, ISO: createISO, QCOW: createQCOW}); err != nil {
			return err
		}
		if registryStore != nil {
			return registryStore.SetRef(types.InstanceRef{Backend: "libvirt", Context: createContext("libvirt"), Namespace: "libvirt", Name: name}, types.RegistryEntry{})
		}
		return nil
	case "qemu":
		if err := runQemuCreate(name); err != nil {
			return err
		}
		return maybeStartAndWait(name)
	}
	return runAdapterCreate(name, backend, createContext(backend))
}

// Guest architecture is currently implemented only for direct local QEMU create.
// Refuse unsupported combinations before dispatch instead of dropping the flag.
func validateCreateGuestOptions(backend string) error {
	if err := types.GuestArch(createArch).Validate(); err != nil {
		return err
	}
	switch createFirmware {
	case "", "uefi", "bios":
	default:
		return fmt.Errorf("unsupported firmware %q: use uefi or bios", createFirmware)
	}
	if createArch != "" && backend != "qemu" {
		return fmt.Errorf("--arch is not supported on backend %s; use local QEMU create with a prepared disk or ISO", backend)
	}
	if createLoopback && backend != "qemu" {
		return fmt.Errorf("--loopback is supported only on local QEMU create")
	}
	return nil
}

// createPlacement parses --dedicated-cpus, --isolate-emulator-thread, and
// --tag. They are KubeVirt-only: executeCreate refuses them elsewhere.
func createPlacement() (kubevirt.Placement, error) {
	tags, err := kubevirt.ParseTags(createTags)
	if err != nil {
		return kubevirt.Placement{}, err
	}
	p := kubevirt.Placement{
		DedicatedCPUs:         createDedicatedCPUs,
		IsolateEmulatorThread: createIsolateEmulator,
		Tags:                  tags,
	}
	if err := p.Validate(); err != nil {
		return kubevirt.Placement{}, err
	}
	if p.DedicatedCPUs && createInstanceType != "" {
		return kubevirt.Placement{}, fmt.Errorf("--dedicated-cpus cannot combine with --instancetype: the instancetype owns the CPU spec, so pick one that sets dedicatedCPUPlacement instead")
	}
	return p, nil
}

// refuseKubevirtOnlyFlags refuses the placement flags on a backend that
// cannot honour them, rather than creating a VM without them.
func refuseKubevirtOnlyFlags(backendName string) error {
	var set []string
	if createDedicatedCPUs {
		set = append(set, "--dedicated-cpus")
	}
	if createIsolateEmulator {
		set = append(set, "--isolate-emulator-thread")
	}
	if len(createTags) > 0 {
		set = append(set, "--tag")
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("%s: supported on the KubeVirt backend only, not on backend %s", strings.Join(set, ", "), backendName)
}

// createContext returns the backend target, not the Corral configuration alias.
// Package-level create helpers read these same accessors.
func createContext(backend string) string {
	if target, ok := config.FindContext(rootContext); rootContext != "" && ok {
		return target.Context
	}
	if rootContext != "" {
		return rootContext
	}
	switch backend {
	case "kubevirt":
		return config.KubeContext()
	case "incus":
		return config.IncusRemote()
	case "libvirt":
		return config.LibvirtURI()
	}
	if def := config.DefaultContext(); def.Backend == backend {
		return def.Context
	}
	return ""
}

// runAdapterCreate creates on a backend the dedicated paths above do not
// cover, through its pkg/backend Creator. A backend without one is refused by
// name: falling through to local QEMU made a VM on the workstation, not where
// the operator asked, and reported success (#493).
func runAdapterCreate(name, backendName, context string) error {
	adapter, err := backend.For(types.InstanceRef{Backend: backendName, Context: context, Name: name})
	if err != nil {
		return fmt.Errorf("create is not supported on backend %q from the CLI: %w", backendName, err)
	}
	creator, ok := adapter.(backend.Creator)
	if !ok {
		return fmt.Errorf("create is not supported on backend %s from the CLI (see docs/backend-support.md)", backendName)
	}
	if createImage != "" {
		return fmt.Errorf("--image names a KubeVirt or Incus image; it is not supported on backend %s", backendName)
	}
	if createWaitSSH {
		return fmt.Errorf("--wait-ssh is supported for QEMU and KubeVirt VMs, not backend %s", backendName)
	}
	if err := creator.Create(types.CreateOpts{
		Name: name, Backend: backendName,
		CPU: createCPU, Mem: createMem, Disk: createDisk,
		ISO: createISO, QCOW: createQCOW, ContainerDisk: createContainerDisk,
		ImportURL: createImport, PVC: createPVC,
		UEFI: createFirmware == "uefi",
	}); err != nil {
		return err
	}
	if registryStore != nil {
		if err := registryStore.SetRef(types.InstanceRef{Backend: backendName, Context: context, Name: name}, types.RegistryEntry{}); err != nil {
			return err
		}
	}
	if !createStart {
		return nil
	}
	power, ok := adapter.(backend.Power)
	if !ok {
		return fmt.Errorf("created %s, but backend %s cannot start it", name, backendName)
	}
	return power.Start(name)
}

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.Flags().BoolVarP(&createKubevirt, "kubevirt", "k", false, "Use KubeVirt backend")
	createCmd.Flags().BoolVar(&createIncus, "incus", false, "Use Incus backend")
	createCmd.Flags().StringVar(&createFirmware, "firmware", "", "Firmware boot type: uefi (default) or bios")
	createCmd.Flags().StringVar(&createArch, "arch", "", "[qemu] Guest architecture: amd64 or arm64 (default: host architecture)")
	createCmd.Flags().BoolVar(&createLoopback, "loopback", false, "[qemu] Bind VNC and forwarded SSH only to 127.0.0.1")
	createCmd.Flags().StringVar(&createMem, "mem", "4G", "Memory allocation")
	createCmd.Flags().IntVar(&createCPU, "cpu", 2, "CPU cores")
	createCmd.Flags().StringVar(&createDisk, "disk", "", "Disk size (default: 20G)")
	createCmd.Flags().StringVar(&createISO, "iso", "", "ISO path/URL (QEMU: local file, KubeVirt: CDI imports from URL, Proxmox: PVE volume id such as local:iso/x.iso)")
	createCmd.Flags().StringVar(&createQCOW, "qcow", "", "[qemu] QCOW2 template")
	createCmd.Flags().BoolVar(&createForce, "force", false, "Overwrite existing VM")
	createCmd.Flags().StringVar(&createContainerDisk, "container-disk", "", "[kubevirt] Container disk image")
	createCmd.Flags().StringVar(&createImage, "image", "", "[kubevirt] OS image from the catalog (see `corral images`)")
	createCmd.Flags().StringVar(&createImport, "import", "", "[kubevirt] Import a qcow2/raw disk image URL via CDI")
	createCmd.Flags().StringVar(&createPVC, "pvc", "", "[kubevirt] Existing PVC to use")
	createCmd.Flags().StringVarP(&createNamespace, "namespace", "n", kubevirt.DefaultNamespace, "[kubevirt] Namespace")
	createCmd.Flags().StringVar(&createNode, "node", "", "[kubevirt] Schedule on specific node")
	createCmd.Flags().StringVar(&createCloudInitPassword, "cloud-init-password", "", "[kubevirt] Cloud-init password")
	createCmd.Flags().StringVar(&createCloudInit, "cloud-init", "", "[kubevirt] Extra cloud-init user-data YAML")
	createCmd.Flags().StringVar(&createInstanceType, "instancetype", "", "[kubevirt] Cluster instancetype for sizing (overrides --cpu/--mem)")
	createCmd.Flags().StringVar(&createPreference, "preference", "", "[kubevirt] Cluster preference (guest device/firmware defaults)")
	createCmd.Flags().StringVarP(&createFile, "file", "f", "", "Lima YAML file (corral reads Lima format natively)")
	createCmd.Flags().StringVarP(&createStorageClass, "storage-class", "s", "", "[kubevirt] StorageClass for new disks (default: cluster preference)")
	createCmd.Flags().StringVar(&createBootc, "bootc", "", "Bootc container image to run")
	createCmd.Flags().StringArrayVar(&createKargs, "karg", nil, "[bootc] Kernel argument to install into the boot entry, there from the first boot (repeatable)")
	createCmd.Flags().BoolVar(&createStart, "start", false, "Start the VM after creating it")
	createCmd.Flags().BoolVar(&createWaitSSH, "wait-ssh", false, "Start the VM and block until SSH answers; nonzero exit on timeout (CI boot gate; works on QEMU and KubeVirt bootc VMs)")
	createCmd.Flags().IntVar(&createTimeout, "timeout", 600, "Seconds to wait for SSH with --wait-ssh")
	createCmd.Flags().StringVar(&createSSHUser, "ssh-user", "root", "User for the --wait-ssh probe (bootc injects the key for root)")
	createCmd.Flags().BoolVar(&createEphemeral, "ephemeral", false, "[kubevirt] Mark for `corral gc`: stopped (PVCs kept) once --ttl expires, deleted after a grace period")
	createCmd.Flags().StringVar(&createTTL, "ttl", "", "[kubevirt] Lifetime before `corral gc` stops this VM, e.g. \"4h\" (default 4h; requires --ephemeral)")
	createCmd.Flags().BoolVar(&createLAN, "lan", false, "[kubevirt] Bridge a secondary NIC onto the LAN (needs a Multus NetworkAttachmentDefinition)")
	createCmd.Flags().StringVar(&createNetworkNAD, "network-nad", "", "[kubevirt] NetworkAttachmentDefinition to bridge onto (\"ns/name\"); implies --lan")
	createCmd.Flags().StringVar(&createBridgeIface, "bridge-iface", "", "[kubevirt] Guest interface name for the LAN bridge (default: net1)")
	createCmd.Flags().BoolVar(&createLANService, "lan-service", false, "[kubevirt] Expose via a LoadBalancer Service instead of a secondary NIC (no Multus needed — works with Cilium L2/BGP or MetalLB)")
	createCmd.Flags().BoolVar(&createVsock, "vsock", false, "[qemu] Enable AF_VSOCK via vhost-vsock-pci + SMBIOS credentials (fallback SSH for live ISOs, like tuna-os/tunaos iso-e2e.sh)")
	createCmd.Flags().Uint32Var(&createVsockCID, "vsock-cid", 0, "[qemu] Guest CID for vsock (3..0xFFFFFFFF, 0=auto from name; implies --vsock)")
	createCmd.Flags().BoolVar(&createTPM, "tpm", false, "[qemu] Enable emulated TPM 2.0 (swtpm + tpm-crb) for LUKS/measured-boot testing")
	createCmd.Flags().BoolVar(&createDedicatedCPUs, "dedicated-cpus", false, "[kubevirt] Pin each vCPU to its own host CPU (dedicatedCpuPlacement, guaranteed QoS); needs a node labeled cpumanager=true")
	createCmd.Flags().BoolVar(&createIsolateEmulator, "isolate-emulator-thread", false, "[kubevirt] Give the QEMU emulator thread one more dedicated host CPU (needs --dedicated-cpus)")
	createCmd.Flags().StringArrayVar(&createTags, "tag", nil, "[kubevirt] Tag the VM as key=value (key alone means key=true), stored as label corral.dev/tag.<key> (repeatable)")
}

func runIncusCreate(name string) error {
	backend.ActivateContext(types.InstanceRef{Backend: "incus", Context: createContext("incus")})
	if createISO != "" || createQCOW != "" || createImport != "" || createPVC != "" || createContainerDisk != "" {
		return fmt.Errorf("the Incus backend creates from an Incus image; use --image (for example images:ubuntu/24.04)")
	}
	image := createImage
	if image == "" {
		image = "images:ubuntu/24.04"
	}
	if err := incus.Create(incus.CreateOpts{Name: name, Image: image, VM: true, CPU: createCPU, Memory: createMem}); err != nil {
		return err
	}
	if registryStore != nil {
		return registryStore.SetRef(types.InstanceRef{Backend: "incus", Context: createContext("incus"), Namespace: "incus", Name: name}, types.RegistryEntry{})
	}
	return nil
}

func runKubevirtCreate(name string) error {
	backend.ActivateContext(types.InstanceRef{Backend: "kubevirt", Context: createContext("kubevirt")})
	ns := createNamespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	placement, err := createPlacement()
	if err != nil {
		return err
	}

	containerDisk := createContainerDisk
	importURL := createImport
	iso := createISO
	if createImage != "" {
		img := catalog.Find(createImage)
		if img == nil {
			return fmt.Errorf("unknown image %q — see `corral images`", createImage)
		}
		// Catalog entries boot three ways: containerdisks directly, official
		// cloud images via CDI import, installer ISOs via the ISO path.
		switch img.Kind() {
		case "containerDisk":
			containerDisk = img.ContainerDisk
		case "import":
			importURL = img.URL
		case "iso":
			iso = img.ISO
		}
	}

	opts := types.CreateOpts{
		Name:      name,
		Namespace: ns,
		Mem:       createMem,
		CPU:       createCPU,
		Disk:      createDisk,
		ISO:       iso,
		// --firmware was honoured only by the QEMU paths, so a KubeVirt ISO
		// install always got SeaBIOS and a UEFI-only ISO (Utah, Dakota) found
		// nothing to boot. Opt-in here: changing the KubeVirt default would
		// re-firmware every existing catalog image.
		UEFI:              createFirmware == "uefi",
		ContainerDisk:     containerDisk,
		ImportURL:         importURL,
		PVC:               createPVC,
		Node:              createNode,
		CloudInitPassword: createCloudInitPassword,
		CloudInitExtra:    createCloudInit,
		InstanceType:      createInstanceType,
		Preference:        createPreference,
		SSHPublicKey:      kubevirt.LoadSSHPublicKey(),
		StorageClass:      createStorageClass,
		Ephemeral:         createEphemeral,
		TTL:               createTTL,

		DedicatedCPUs:         placement.DedicatedCPUs,
		IsolateEmulatorThread: placement.IsolateEmulatorThread,
		Tags:                  placement.Tags,
	}
	if placement.DedicatedCPUs {
		if err := kubevirt.CheckDedicatedCPUs(createNode); err != nil {
			return err
		}
	}
	if err := kubevirt.CreateVM(opts); err != nil {
		return err
	}
	// Expose SSH/VNC on the tailnet via the Tailscale operator proxy (no
	// in-guest tailscale needed). Best-effort — the VM is already created.
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tailnet expose failed: %v\n", err)
	}

	if createLAN || createNetworkNAD != "" {
		if err := attachLANBridge(ns, name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	if createLANService {
		if err := kubevirt.ApplyLANService(name, ns, []int{22}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: LAN service not created: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  LAN:    corral lanservice %s   # check the assigned external IP\n", name)
		}
	}

	if registryStore != nil {
		if err := registryStore.SetRef(types.InstanceRef{Backend: "kubevirt", Context: createContext("kubevirt"), Namespace: ns, Name: name}, types.RegistryEntry{
			Backend:   "kubevirt",
			Namespace: ns,
			Password:  kubevirt.LastPassword,
		}); err != nil {
			return fmt.Errorf("saving registry: %w", err)
		}
	}

	fmt.Fprintf(os.Stderr, "VM %q created in ns/%s\n", name, ns)
	fmt.Fprintf(os.Stderr, "  Start:  corral start %s\n", name)
	fmt.Fprintf(os.Stderr, "  SSH:    corral ssh %s\n", name)
	return nil
}

// attachLANBridge resolves which Multus NAD to use (--network-nad, or the
// cluster's only one) and hotplugs a bridge-bound secondary NIC onto the
// just-created VM — see kubevirt.ResolveNAD/AddNIC and issue #82.
func attachLANBridge(ns, name string) error {
	nad, err := kubevirt.ResolveNAD(createNetworkNAD, kubevirt.ListNADs())
	if err != nil {
		return fmt.Errorf("LAN bridge not attached: %w", err)
	}
	if err := kubevirt.NewClient(ns).AddNIC(name, nad, createBridgeIface); err != nil {
		return fmt.Errorf("LAN bridge not attached: %w", err)
	}
	iface := createBridgeIface
	if iface == "" {
		iface = "net1"
	}
	fmt.Fprintf(os.Stderr, "  LAN:    bridged via %s (guest iface %s)\n", nad, iface)
	return nil
}

func runQemuCreate(name string) error {
	if err := validateCreateGuestOptions("qemu"); err != nil {
		return err
	}
	useUEFI := createFirmware == "uefi"
	vsock := createVsock || createVsockCID != 0
	if createVsockCID != 0 {
		vsock = true
	}
	if err := qemu.Create(types.CreateOpts{
		Name:     name,
		Arch:     types.GuestArch(createArch),
		Loopback: createLoopback,
		Mem:      createMem,
		CPU:      createCPU,
		Disk:     createDisk,
		ISO:      createISO,
		QCOW:     createQCOW,
		Force:    createForce,
		UEFI:     useUEFI,
		Vsock:    vsock,
		VsockCID: createVsockCID,
		TPM:      createTPM,
	}); err != nil {
		return err
	}
	if registryStore != nil {
		return registryStore.SetRef(types.InstanceRef{Backend: "qemu", Namespace: "local", Name: name}, types.RegistryEntry{})
	}
	return nil
}

func runKubevirtBootcCreate(name string) error {
	backend.ActivateContext(types.InstanceRef{Backend: "kubevirt", Context: createContext("kubevirt")})
	ns := createNamespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	sshKey := kubevirt.LoadSSHPublicKey()
	if sshKey == "" {
		return fmt.Errorf("no SSH public key found (needed for bootc VM)")
	}
	size := createDisk
	if size == "" {
		size = "80Gi"
	}
	placement, err := createPlacement()
	if err != nil {
		return err
	}
	// Refuse before the build: it takes minutes, and its VM would never
	// schedule.
	if placement.DedicatedCPUs {
		if err := kubevirt.CheckDedicatedCPUs(createNode); err != nil {
			return err
		}
	}

	// The bootc build pipeline is an optional plugin (kept out of the lean core
	// binary — see pkg/kubevirt/bootc_core.go). When it isn't compiled in,
	// delegate the build+VM creation to the `corral-bootc` plugin so
	// `corral create --bootc ... --wait-ssh` still works as one command. The
	// plugin creates the VM stopped; we then handle --start/--wait-ssh below.
	if !kubevirt.BootcAvailable() {
		if !plugin.IsInstalled("bootc") {
			return fmt.Errorf("bootc build needs the bootc plugin: corral plugin install bootc")
		}
		if err := plugin.Dispatch("bootc", bootcPluginArgs(name, ns, size)); err != nil {
			return err
		}
		if registryStore != nil {
			if err := registryStore.SetRef(types.InstanceRef{Backend: "kubevirt", Context: createContext("kubevirt"), Namespace: ns, Name: name}, types.RegistryEntry{}); err != nil {
				return err
			}
		}
		if createWaitSSH {
			fmt.Fprintf(os.Stderr, "  Waiting for SSH (timeout %ds)...\n", createTimeout)
			return kubevirt.NewClient(ns).WaitSSH(name, createSSHUser, "", time.Duration(createTimeout)*time.Second)
		}
		if createStart {
			return kubevirt.NewClient(ns).StartVM(name)
		}
		return nil
	}

	build, err := kubevirt.BootcBuildDisk(name, ns, createBootc, sshKey, size, createStorageClass, createProvisionScript, createKargs, createNode, os.Stderr)
	if err != nil {
		return err
	}
	vm := kubevirt.GenerateBootcVM(name, ns, build.PVCName, createBootc, sshKey, createMem, createCPU, createNode)
	kubevirt.ApplyPlacement(vm, placement)
	if err := kubevirt.Apply(vm); err != nil {
		return err
	}
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tailnet expose failed: %v\n", err)
	}
	if registryStore != nil {
		if err := registryStore.SetRef(types.InstanceRef{Backend: "kubevirt", Context: createContext("kubevirt"), Namespace: ns, Name: name}, types.RegistryEntry{}); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "VM %q created in ns/%s\n", name, ns)
	fmt.Fprintf(os.Stderr, "  Start:  corral start %s\n", name)
	fmt.Fprintf(os.Stderr, "  SSH:    corral ssh %s\n", name)

	// --wait-ssh turns create into a boot gate on KubeVirt too: start the VM
	// and block until root SSH answers, so the same command works on both
	// backends. --start (without --wait-ssh) just brings it up.
	if createWaitSSH {
		fmt.Fprintf(os.Stderr, "  Waiting for SSH (timeout %ds)...\n", createTimeout)
		return kubevirt.NewClient(ns).WaitSSH(name, createSSHUser, "", time.Duration(createTimeout)*time.Second)
	}
	if createStart {
		return kubevirt.NewClient(ns).StartVM(name)
	}
	return nil
}

// bootcPluginArgs is the `corral bootc` command line that delegates a
// KubeVirt bootc create to the plugin when the build is not compiled in.
func bootcPluginArgs(name, ns, size string) []string {
	args := []string{"create", name, "--image", createBootc, "-n", ns,
		"--cpu", strconv.Itoa(createCPU), "--mem", createMem}
	if size != "" {
		args = append(args, "--disk", size)
	}
	if createStorageClass != "" {
		args = append(args, "--storage-class", createStorageClass)
	}
	if createNode != "" {
		args = append(args, "--node", createNode)
	}
	for _, karg := range createKargs {
		args = append(args, "--karg", karg)
	}
	if createDedicatedCPUs {
		args = append(args, "--dedicated-cpus")
	}
	if createIsolateEmulator {
		args = append(args, "--isolate-emulator-thread")
	}
	for _, tag := range createTags {
		args = append(args, "--tag", tag)
	}
	return args
}

func runLocalBootcCreate(name string) error {
	vmDir := filepath.Join(qemu.VMHome(), name)
	if qemu.Exists(name) && !createForce {
		return fmt.Errorf("VM %q already exists. Use --force to overwrite", name)
	}
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("creating VM directory: %w", err)
	}

	diskSize := createDisk
	if diskSize == "" {
		diskSize = "20G"
	}
	mem := createMem
	if mem == "" {
		mem = "4G"
	}
	cpu := createCPU
	if cpu == 0 {
		cpu = 2
	}

	_ = qemu.RecordCreating(name, types.CreateOpts{
		Name: name,
		CPU:  cpu,
		Mem:  mem,
		Disk: diskSize,
	})

	var created bool
	defer func() {
		if !created {
			_ = os.RemoveAll(vmDir)
		}
	}()

	diskPath := filepath.Join(vmDir, "disk.raw")
	out, err := exec.Command("truncate", "-s", diskSize, diskPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("creating raw disk: %s: %w", string(out), err)
	}

	out, err = exec.Command("sudo", "losetup", "-fP", diskPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup failed: %s: %w", string(out), err)
	}

	out, err = exec.Command("sudo", "losetup", "-a").CombinedOutput()
	if err != nil {
		return fmt.Errorf("listing loop devices: %s: %w", string(out), err)
	}
	var loopDev string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, filepath.Base(diskPath)) {
			loopDev = strings.Split(line, ":")[0]
			break
		}
	}
	if loopDev == "" {
		return fmt.Errorf("could not find loop device for %s", diskPath)
	}
	defer exec.Command("sudo", "losetup", "-d", loopDev).Run()

	if createProvisionScript != "" {
		provFile := filepath.Join(vmDir, "provision.sh")
		if err := os.WriteFile(provFile, []byte(createProvisionScript), 0755); err != nil {
			return err
		}
		defer os.Remove(provFile)
	}

	sshKey := kubevirt.LoadSSHPublicKey()
	if sshKey == "" {
		return fmt.Errorf("no SSH public key found")
	}
	keyFile := filepath.Join(vmDir, "id_rsa.pub")
	if err := os.WriteFile(keyFile, []byte(sshKey), 0644); err != nil {
		return err
	}
	defer os.Remove(keyFile)

	fmt.Printf("Building bootc image locally onto %s...\n", loopDev)
	// Backend-aware install (mirrors pkg/kubevirt/bootc.go's builder). ostree/
	// server images ship bootupctl and install to xfs with --generic-image (so
	// the disk boots the removable EFI path under plain QEMU). composefs images
	// (Universal Blue / TunaOS desktops: systemd-boot without GRUB payloads)
	// require fs-verity (ext4 when available, otherwise btrfs). Installing the
	// ostree/xfs way drops to dracut emergency mode at initrd-switch-root
	// (tuna-os sailfin desktop Gate). They also need the real kernel/initrd
	// re-extracted over bootc's EROFS-zeroed ESP copies, and the SSH key written
	// into the deployment (--root-ssh-authorized-keys is a no-op on composefs).
	cmd := exec.Command("sudo", "podman", "run", "--privileged", "--pid=host", "--security-opt", "label=disable",
		"-v", "/dev:/dev", "-v", vmDir+":/output:Z",
		createBootc, "sh", "-c",
		localBootcScript(loopDev, createProvisionScript, createKargs))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("local bootc build failed: %w", err)
	}

	qcowPath := filepath.Join(vmDir, "disk.qcow2")
	out, err = exec.Command("qemu-img", "convert", "-O", "qcow2", diskPath, qcowPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("converting to qcow2: %s: %w", string(out), err)
	}
	os.Remove(diskPath)

	// ExistingDisk: the qcow2 we just built IS the boot disk — without it
	// qemu.Create would recreate disk.qcow2 empty and the VM would boot
	// into nothing.
	useUEFI := createFirmware != "bios"
	vsock := createVsock || createVsockCID != 0
	if createVsockCID != 0 {
		vsock = true
	}
	if err := qemu.Create(types.CreateOpts{
		Name:         name,
		Mem:          createMem,
		CPU:          createCPU,
		Disk:         createDisk,
		Force:        true,
		ExistingDisk: true,
		UEFI:         useUEFI,
		Vsock:        vsock,
		VsockCID:     createVsockCID,
		TPM:          createTPM,
	}); err != nil {
		return err
	}
	created = true
	if registryStore != nil {
		return registryStore.SetRef(types.InstanceRef{Backend: "qemu", Namespace: "local", Name: name}, types.RegistryEntry{})
	}
	return nil
}

// maybeStartAndWait handles --start / --wait-ssh for QEMU-backed VMs.
// --wait-ssh implies --start and turns creation into a CI gate: the process
// exits nonzero unless the guest answers SSH within --timeout seconds.
func maybeStartAndWait(name string) error {
	if !createStart && !createWaitSSH {
		return nil
	}
	if err := qemu.Start(name); err != nil {
		return err
	}
	if !createWaitSSH {
		return nil
	}
	return qemu.WaitSSH(name, createSSHUser, time.Duration(createTimeout)*time.Second)
}

// localBootcScript is the `sh -c` script the local bootc build runs inside
// the image. Each karg goes to `bootc install to-disk --karg`, single-quoted.
func localBootcScript(disk, provisionScript string, kargs []string) string {
	var provisionArg string
	if provisionScript != "" {
		provisionArg = `&& cat /output/provision.sh | chroot "$MNT" /bin/bash`
	}
	return fmt.Sprintf(`set -e
DISK=%s
if ls /usr/lib/bootupd/updates/EFI/*/grub*.efi >/dev/null 2>&1 \
  || { [ -f /usr/lib/bootupd/updates/EFI.json ] \
    && find /usr/lib/efi/grub2 -type f -name 'grub*.efi' -print -quit 2>/dev/null | grep -q . \
    && find /usr/lib/efi/shim -type f -name 'shim*.efi' -print -quit 2>/dev/null | grep -q .; }; then
  COMPOSEFS=0; FS=xfs; BACKEND=--generic-image
elif [ -f /usr/lib/systemd/boot/efi/systemd-bootx64.efi ] || [ -f /usr/lib/systemd/boot/efi/systemd-bootaa64.efi ]; then
  COMPOSEFS=1; FS=btrfs; BACKEND=--composefs-backend
  if command -v mkfs.ext4 >/dev/null 2>&1; then FS=ext4; fi
else
  COMPOSEFS=0; FS=xfs; BACKEND=--generic-image
fi
echo "corral: install backend composefs=$COMPOSEFS fs=$FS"
bootc install to-disk $BACKEND --filesystem "$FS" --wipe%s --root-ssh-authorized-keys /output/id_rsa.pub "$DISK"
udevadm settle
if [ "$COMPOSEFS" = 1 ]; then
  MNT_ESP=$(mktemp -d)
  for P in 1 2 3; do
    if mount "${DISK}p${P}" "$MNT_ESP" 2>/dev/null; then
      if [ -d "$MNT_ESP/EFI" ]; then break; fi
      umount "$MNT_ESP"
    fi
  done
  KVER=$(ls /usr/lib/modules | head -1)
  D=$(ls -d "$MNT_ESP"/EFI/Linux/bootc_composefs-* 2>/dev/null | head -1)
  if [ -n "$D" ]; then
    cp -f "/usr/lib/modules/$KVER/vmlinuz" "$D/vmlinuz"
    cp -f "/usr/lib/modules/$KVER/initramfs.img" "$D/initrd"
  fi
  sync; umount "$MNT_ESP" 2>/dev/null || true
  rm -rf "$MNT_ESP"
  MNT_ROOT=$(mktemp -d)
  mount "${DISK}p3" "$MNT_ROOT"
  SSHDIR="$MNT_ROOT/state/os/default/var/roothome/.ssh"
  mkdir -p "$SSHDIR"; chmod 700 "$SSHDIR"
  cp /output/id_rsa.pub "$SSHDIR/authorized_keys"; chmod 600 "$SSHDIR/authorized_keys"; chown -R 0:0 "$SSHDIR"
  sync; umount "$MNT_ROOT"
  rm -rf "$MNT_ROOT"
fi
udevadm settle
MNT=$(mktemp -d)
mount "${DISK}p3" "$MNT" %s && umount "$MNT"
rm -rf "$MNT"`, disk, kubevirt.BootcKargFlags(kargs), provisionArg)
}
