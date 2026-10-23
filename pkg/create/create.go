package create

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/lima"
	"github.com/tuna-os/corral/pkg/plugin"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// Options holds all inputs for creating a VM across any backend.
type Options struct {
	Name string

	// Explicit backend choices
	KubevirtExplicit bool
	Kubevirt         bool
	Incus            bool
	Backend          string // If specified, overrides config context/default
	Context          string // Target context name
	Namespace        string // KubeVirt target namespace

	// Sizing & Resource specs
	Mem          string
	CPU          int
	Disk         string
	StorageClass string
	InstanceType string
	Preference   string

	// Source options
	ISO           string
	QCOW          string
	ContainerDisk string
	Image         string
	Import        string
	PVC           string
	Bootc         string

	// Configuration & Provisioning
	CloudInitPassword string
	CloudInit         string
	ProvisionScript   string
	Kargs             []string

	// Lifecycle & Execution options
	Force     bool
	Start     bool
	WaitSSH   bool
	Timeout   int
	SSHUser   string
	Ephemeral bool
	TTL       string

	// Network options
	LAN         bool
	NetworkNAD  string
	BridgeIface string
	LANService  bool

	// Hardware / Guest features
	Firmware string // "uefi" or "bios"
	Vsock    bool
	VsockCID uint32
	TPM      bool
	Node     string
}

// RegistryStore is an interface matching cmd's registryStore for recording VM state.
type RegistryStore interface {
	Set(name string, entry types.RegistryEntry) error
}

// Service orchestrates VM creation across backends.
type Service struct {
	Store RegistryStore

	// Injectable hooks for testing or customization
	ResolveBackendFunc func(name string) string
	KubevirtReachable  func() bool
	Out                *os.File
	Err                *os.File
}

// NewService creates a new create Service with standard defaults.
func NewService(store RegistryStore) *Service {
	// If store is a typed nil pointer (e.g. (*registry.Store)(nil)), normalize to nil
	if isNilStore(store) {
		store = nil
	}
	return &Service{
		Store:              store,
		ResolveBackendFunc: defaultResolveBackend,
		KubevirtReachable:  kubevirt.Reachable,
		Out:                os.Stdout,
		Err:                os.Stderr,
	}
}

func isNilStore(store RegistryStore) bool {
	if store == nil {
		return true
	}
	v := reflect.ValueOf(store)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

func defaultResolveBackend(name string) string {
	// If qemu VM exists locally
	if qemu.Exists(name) {
		return "qemu"
	}
	return ""
}

// ApplyLimaFile mutates Options with values from a parsed Lima File.
func (opts *Options) ApplyLimaFile(spec *lima.File) {
	if spec.Bootc != "" {
		opts.Bootc = spec.Bootc
	}
	var rawScripts []string
	for _, p := range spec.Provision {
		if p.Script != "" {
			rawScripts = append(rawScripts, p.Script)
		}
	}
	if len(rawScripts) > 0 {
		opts.ProvisionScript = strings.Join(rawScripts, "\n")
	}

	if spec.CPUs != 0 && opts.CPU == 2 {
		opts.CPU = spec.CPUs
	}
	if spec.Memory != "" && opts.Mem == "4G" {
		opts.Mem = lima.ParseMemory(spec.Memory)
	}
	if spec.Disk != "" && opts.Disk == "" {
		opts.Disk = lima.ParseDisk(spec.Disk)
	}

	// Resolve the first image as the boot source.
	if len(spec.Images) > 0 && opts.ContainerDisk == "" && opts.Image == "" && opts.ISO == "" && opts.Import == "" {
		loc := spec.Images[0].Location
		switch {
		case strings.HasSuffix(loc, ".iso") || strings.HasPrefix(loc, "https://") && strings.Contains(loc, ".iso"):
			opts.ISO = loc
		case strings.HasSuffix(loc, ".qcow2") || strings.HasSuffix(loc, ".raw") || strings.HasSuffix(loc, ".img"):
			opts.Import = loc
		case strings.Contains(loc, "/") && strings.Contains(loc, ":"):
			// OCI image reference (e.g. quay.io/containerdisks/fedora:44)
			opts.ContainerDisk = loc
		default:
			// Bare name — try catalog lookup
			opts.Image = loc
		}
	}

	// Convert provision scripts to cloud-init.
	if opts.CloudInit == "" {
		ci := lima.ProvisionScriptsToCloudInit(spec.Provision)
		if ci != "" {
			opts.CloudInit = ci
		}
	}
}

// Execute orchestrates backend selection and VM creation according to opts.
func (s *Service) Execute(opts Options) error {
	name := opts.Name
	if len(opts.Kargs) > 0 && opts.Bootc == "" {
		return fmt.Errorf("--karg needs --bootc: only a bootc install puts kernel arguments into the boot entry")
	}
	if err := kubevirt.ValidateKargs(opts.Kargs); err != nil {
		return err
	}

	if s.ResolveBackendFunc != nil {
		if existing := s.ResolveBackendFunc(name); existing != "" && !opts.Force {
			return fmt.Errorf("VM %q already exists (backend: %s). Use --force to overwrite", name, existing)
		}
	}

	if opts.Bootc != "" {
		useKubevirt := opts.Kubevirt
		if !opts.KubevirtExplicit {
			if s.KubevirtReachable != nil {
				useKubevirt = s.KubevirtReachable()
			} else {
				useKubevirt = kubevirt.Reachable()
			}
		}
		if useKubevirt {
			return s.RunKubevirtBootcCreate(opts)
		}
		if err := s.RunLocalBootcCreate(opts); err != nil {
			return err
		}
		return s.MaybeStartAndWait(opts)
	}

	be := config.DefaultBackend()
	if opts.Backend != "" {
		be = opts.Backend
	} else if opts.Context != "" {
		if target, ok := config.FindContext(opts.Context); ok {
			be = target.Backend
		}
	}
	if opts.Incus {
		be = "incus"
	}
	if opts.Kubevirt {
		be = "kubevirt"
	}
	// KubeVirt-only source flags remain an explicit signal for backwards compatibility.
	if be == "qemu" && !opts.Incus && (opts.Image != "" || opts.Import != "" || opts.ContainerDisk != "" || opts.PVC != "") {
		be = "kubevirt"
	}

	switch be {
	case "incus":
		return s.RunIncusCreate(opts)
	case "libvirt":
		if err := libvirt.NewClient("").Create(types.CreateOpts{Name: name, CPU: opts.CPU, Mem: opts.Mem, Disk: opts.Disk, ISO: opts.ISO, QCOW: opts.QCOW}); err != nil {
			return err
		}
		if s.Store != nil {
			return s.Store.Set(name, types.RegistryEntry{Backend: "libvirt"})
		}
		return nil
	case "kubevirt":
		return s.RunKubevirtCreate(opts)
	case "qemu":
		if err := s.RunQemuCreate(opts); err != nil {
			return err
		}
		return s.MaybeStartAndWait(opts)
	}
	return s.RunAdapterCreate(opts, be, s.CreateContext(opts, be))
}

// CreateContext names the context a create on backend lands in.
func (s *Service) CreateContext(opts Options, backendName string) string {
	if opts.Context != "" {
		return opts.Context
	}
	if def := config.DefaultContext(); def.Backend == backendName {
		return def.Name
	}
	return ""
}

// RunAdapterCreate creates on backends covered through pkg/backend Creator.
func (s *Service) RunAdapterCreate(opts Options, backendName, context string) error {
	name := opts.Name
	adapter, err := backend.For(types.InstanceRef{Backend: backendName, Context: context, Name: name})
	if err != nil {
		return fmt.Errorf("create is not supported on backend %q from the CLI: %w", backendName, err)
	}
	creator, ok := adapter.(backend.Creator)
	if !ok {
		return fmt.Errorf("create is not supported on backend %s from the CLI (see docs/backend-support.md)", backendName)
	}
	if opts.Image != "" {
		return fmt.Errorf("--image names a KubeVirt or Incus image; it is not supported on backend %s", backendName)
	}
	if opts.WaitSSH {
		return fmt.Errorf("--wait-ssh is supported for QEMU and KubeVirt VMs, not backend %s", backendName)
	}
	if err := creator.Create(types.CreateOpts{
		Name: name, Backend: backendName,
		CPU: opts.CPU, Mem: opts.Mem, Disk: opts.Disk,
		ISO: opts.ISO, QCOW: opts.QCOW, ContainerDisk: opts.ContainerDisk,
		ImportURL: opts.Import, PVC: opts.PVC,
		UEFI: opts.Firmware == "uefi",
	}); err != nil {
		return err
	}
	if s.Store != nil {
		if err := s.Store.Set(name, types.RegistryEntry{Backend: backendName, Context: context}); err != nil {
			return err
		}
	}
	if !opts.Start {
		return nil
	}
	power, ok := adapter.(backend.Power)
	if !ok {
		return fmt.Errorf("created %s, but backend %s cannot start it", name, backendName)
	}
	return power.Start(name)
}

// RunIncusCreate handles Incus backend creation.
func (s *Service) RunIncusCreate(opts Options) error {
	name := opts.Name
	if opts.ISO != "" || opts.QCOW != "" || opts.Import != "" || opts.PVC != "" || opts.ContainerDisk != "" {
		return fmt.Errorf("the Incus backend creates from an Incus image; use --image (for example images:ubuntu/24.04)")
	}
	image := opts.Image
	if image == "" {
		image = "images:ubuntu/24.04"
	}
	if err := incus.Create(incus.CreateOpts{Name: name, Image: image, VM: true, CPU: opts.CPU, Memory: opts.Mem}); err != nil {
		return err
	}
	if s.Store != nil {
		return s.Store.Set(name, types.RegistryEntry{Backend: "incus"})
	}
	return nil
}

// RunKubevirtCreate handles KubeVirt VM creation.
func (s *Service) RunKubevirtCreate(opts Options) error {
	name := opts.Name
	ns := opts.Namespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}

	containerDisk := opts.ContainerDisk
	importURL := opts.Import
	iso := opts.ISO
	if opts.Image != "" {
		img := catalog.Find(opts.Image)
		if img == nil {
			return fmt.Errorf("unknown image %q — see `corral images`", opts.Image)
		}
		switch img.Kind() {
		case "containerDisk":
			containerDisk = img.ContainerDisk
		case "import":
			importURL = img.URL
		case "iso":
			iso = img.ISO
		}
	}

	createOpts := types.CreateOpts{
		Name:              name,
		Namespace:         ns,
		Mem:               opts.Mem,
		CPU:               opts.CPU,
		Disk:              opts.Disk,
		ISO:               iso,
		UEFI:              opts.Firmware == "uefi",
		ContainerDisk:     containerDisk,
		ImportURL:         importURL,
		PVC:               opts.PVC,
		Node:              opts.Node,
		CloudInitPassword: opts.CloudInitPassword,
		CloudInitExtra:    opts.CloudInit,
		InstanceType:      opts.InstanceType,
		Preference:        opts.Preference,
		SSHPublicKey:      kubevirt.LoadSSHPublicKey(),
		StorageClass:      opts.StorageClass,
		Ephemeral:         opts.Ephemeral,
		TTL:               opts.TTL,
	}
	if err := kubevirt.CreateVM(createOpts); err != nil {
		return err
	}
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		s.logErr("warning: tailnet expose failed: %v\n", err)
	}

	if opts.LAN || opts.NetworkNAD != "" {
		if err := AttachLANBridge(opts, ns, name, s.Err); err != nil {
			s.logErr("warning: %v\n", err)
		}
	}
	if opts.LANService {
		if err := kubevirt.ApplyLANService(name, ns, []int{22}); err != nil {
			s.logErr("warning: LAN service not created: %v\n", err)
		} else {
			s.logErr("  LAN:    corral lanservice %s   # check the assigned external IP\n", name)
		}
	}

	if s.Store != nil {
		if err := s.Store.Set(name, types.RegistryEntry{
			Backend:   "kubevirt",
			Namespace: ns,
			Password:  kubevirt.LastPassword,
		}); err != nil {
			return fmt.Errorf("saving registry: %w", err)
		}
	}

	s.logErr("VM %q created in ns/%s\n", name, ns)
	s.logErr("  Start:  corral start %s\n", name)
	s.logErr("  SSH:    corral ssh %s\n", name)
	return nil
}

// AttachLANBridge resolves which Multus NAD to use and hotplugs a bridge-bound secondary NIC.
func AttachLANBridge(opts Options, ns, name string, errOut *os.File) error {
	nad, err := kubevirt.ResolveNAD(opts.NetworkNAD, kubevirt.ListNADs())
	if err != nil {
		return fmt.Errorf("LAN bridge not attached: %w", err)
	}
	if err := kubevirt.NewClient(ns).AddNIC(name, nad, opts.BridgeIface); err != nil {
		return fmt.Errorf("LAN bridge not attached: %w", err)
	}
	iface := opts.BridgeIface
	if iface == "" {
		iface = "net1"
	}
	if errOut != nil {
		fmt.Fprintf(errOut, "  LAN:    bridged via %s (guest iface %s)\n", nad, iface)
	}
	return nil
}

// RunQemuCreate handles local QEMU VM creation.
func (s *Service) RunQemuCreate(opts Options) error {
	useUEFI := opts.Firmware == "uefi"
	vsock := opts.Vsock || opts.VsockCID != 0
	if opts.VsockCID != 0 {
		vsock = true
	}
	if err := qemu.Create(types.CreateOpts{
		Name:     opts.Name,
		Mem:      opts.Mem,
		CPU:      opts.CPU,
		Disk:     opts.Disk,
		ISO:      opts.ISO,
		QCOW:     opts.QCOW,
		Force:    opts.Force,
		UEFI:     useUEFI,
		Vsock:    vsock,
		VsockCID: opts.VsockCID,
		TPM:      opts.TPM,
	}); err != nil {
		return err
	}
	if s.Store != nil {
		s.Store.Set(opts.Name, types.RegistryEntry{Backend: "qemu"})
	}
	return nil
}

// RunKubevirtBootcCreate handles KubeVirt bootc VM creation.
func (s *Service) RunKubevirtBootcCreate(opts Options) error {
	name := opts.Name
	ns := opts.Namespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	sshKey := kubevirt.LoadSSHPublicKey()
	if sshKey == "" {
		return fmt.Errorf("no SSH public key found (needed for bootc VM)")
	}
	size := opts.Disk
	if size == "" {
		size = "80Gi"
	}

	if !kubevirt.BootcAvailable() {
		if !plugin.IsInstalled("bootc") {
			return fmt.Errorf("bootc build needs the bootc plugin: corral plugin install bootc")
		}
		if err := plugin.Dispatch("bootc", BootcPluginArgs(opts, name, ns, size)); err != nil {
			return err
		}
		if s.Store != nil {
			s.Store.Set(name, types.RegistryEntry{Backend: "kubevirt", Namespace: ns})
		}
		if opts.WaitSSH {
			s.logErr("  Waiting for SSH (timeout %ds)...\n", opts.Timeout)
			return kubevirt.NewClient(ns).WaitSSH(name, opts.SSHUser, "", time.Duration(opts.Timeout)*time.Second)
		}
		if opts.Start {
			return kubevirt.NewClient(ns).StartVM(name)
		}
		return nil
	}

	errOut := s.Err
	if errOut == nil {
		errOut = os.Stderr
	}
	build, err := kubevirt.BootcBuildDisk(name, ns, opts.Bootc, sshKey, size, opts.StorageClass, opts.ProvisionScript, opts.Kargs, opts.Node, errOut)
	if err != nil {
		return err
	}
	vm := kubevirt.GenerateBootcVM(name, ns, build.PVCName, opts.Bootc, sshKey, opts.Mem, opts.CPU, opts.Node)
	if err := kubevirt.Apply(vm); err != nil {
		return err
	}
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		s.logErr("warning: tailnet expose failed: %v\n", err)
	}
	if s.Store != nil {
		s.Store.Set(name, types.RegistryEntry{Backend: "kubevirt", Namespace: ns})
	}
	s.logErr("VM %q created in ns/%s\n", name, ns)
	s.logErr("  Start:  corral start %s\n", name)
	s.logErr("  SSH:    corral ssh %s\n", name)

	if opts.WaitSSH {
		s.logErr("  Waiting for SSH (timeout %ds)...\n", opts.Timeout)
		return kubevirt.NewClient(ns).WaitSSH(name, opts.SSHUser, "", time.Duration(opts.Timeout)*time.Second)
	}
	if opts.Start {
		return kubevirt.NewClient(ns).StartVM(name)
	}
	return nil
}

// BootcPluginArgs builds CLI arguments for dispatching to the corral-bootc plugin.
func BootcPluginArgs(opts Options, name, ns, size string) []string {
	args := []string{"create", name, "--image", opts.Bootc, "-n", ns}
	if size != "" {
		args = append(args, "--disk", size)
	}
	if opts.StorageClass != "" {
		args = append(args, "--storage-class", opts.StorageClass)
	}
	if opts.Node != "" {
		args = append(args, "--node", opts.Node)
	}
	for _, karg := range opts.Kargs {
		args = append(args, "--karg", karg)
	}
	return args
}

// RunLocalBootcCreate builds and creates a bootc VM on local QEMU.
func (s *Service) RunLocalBootcCreate(opts Options) error {
	name := opts.Name
	vmDir := filepath.Join(qemu.VMHome(), name)
	if qemu.Exists(name) && !opts.Force {
		return fmt.Errorf("VM %q already exists. Use --force to overwrite", name)
	}
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("creating VM directory: %w", err)
	}

	diskSize := opts.Disk
	if diskSize == "" {
		diskSize = "20G"
	}
	mem := opts.Mem
	if mem == "" {
		mem = "4G"
	}
	cpu := opts.CPU
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

	if opts.ProvisionScript != "" {
		provFile := filepath.Join(vmDir, "provision.sh")
		if err := os.WriteFile(provFile, []byte(opts.ProvisionScript), 0755); err != nil {
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

	if s.Out != nil {
		fmt.Fprintf(s.Out, "Building bootc image locally onto %s...\n", loopDev)
	}

	cmd := exec.Command("sudo", "podman", "run", "--privileged", "--pid=host", "--security-opt", "label=disable",
		"-v", "/dev:/dev", "-v", vmDir+":/output:Z",
		opts.Bootc, "sh", "-c",
		LocalBootcScript(loopDev, opts.ProvisionScript, opts.Kargs))
	cmd.Stdout = s.Out
	cmd.Stderr = s.Err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("local bootc build failed: %w", err)
	}

	qcowPath := filepath.Join(vmDir, "disk.qcow2")
	out, err = exec.Command("qemu-img", "convert", "-O", "qcow2", diskPath, qcowPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("converting to qcow2: %s: %w", string(out), err)
	}
	os.Remove(diskPath)

	useUEFI := opts.Firmware != "bios"
	vsock := opts.Vsock || opts.VsockCID != 0
	if opts.VsockCID != 0 {
		vsock = true
	}
	if err := qemu.Create(types.CreateOpts{
		Name:         name,
		Mem:          opts.Mem,
		CPU:          opts.CPU,
		Disk:         opts.Disk,
		Force:        true,
		ExistingDisk: true,
		UEFI:         useUEFI,
		Vsock:        vsock,
		VsockCID:     opts.VsockCID,
		TPM:          opts.TPM,
	}); err != nil {
		return err
	}
	created = true
	if s.Store != nil {
		s.Store.Set(name, types.RegistryEntry{Backend: "qemu"})
	}
	return nil
}

// MaybeStartAndWait handles --start / --wait-ssh for QEMU-backed VMs.
func (s *Service) MaybeStartAndWait(opts Options) error {
	name := opts.Name
	if !opts.Start && !opts.WaitSSH {
		return nil
	}
	if err := qemu.Start(name); err != nil {
		return err
	}
	if !opts.WaitSSH {
		return nil
	}
	return qemu.WaitSSH(name, opts.SSHUser, time.Duration(opts.Timeout)*time.Second)
}

// LocalBootcScript is the `sh -c` script the local bootc build runs inside
// the image. Each karg goes to `bootc install to-disk --karg`, single-quoted.
func LocalBootcScript(disk, provisionScript string, kargs []string) string {
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

func (s *Service) logErr(format string, a ...any) {
	if s.Err != nil {
		fmt.Fprintf(s.Err, format, a...)
	}
}
