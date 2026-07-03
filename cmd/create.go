package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

var (
	createKubevirt          bool
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
	createStart             bool
	createWaitSSH           bool
	createTimeout           int
	createSSHUser           string
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

Boot a container image as a VM? Install the bootc extension:
  corral plugin install bootc && corral bootc create myvm --image quay.io/centos-bootc/centos-bootc:stream9`,
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

		if existing := resolveBackend(name); existing != "" && !createForce {
			return fmt.Errorf("VM %q already exists (backend: %s). Use --force to overwrite", name, existing)
		}

		if createBootc != "" {
			if createKubevirt {
				return runKubevirtBootcCreate(name)
			}
			if err := runLocalBootcCreate(name); err != nil {
				return err
			}
			return maybeStartAndWait(name)
		}

		if createKubevirt || createImage != "" || createImport != "" {
			return runKubevirtCreate(name)
		}
		if err := runQemuCreate(name); err != nil {
			return err
		}
		return maybeStartAndWait(name)
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.Flags().BoolVarP(&createKubevirt, "kubevirt", "k", false, "Use KubeVirt backend")
	createCmd.Flags().StringVar(&createMem, "mem", "4G", "Memory allocation")
	createCmd.Flags().IntVar(&createCPU, "cpu", 2, "CPU cores")
	createCmd.Flags().StringVar(&createDisk, "disk", "", "Disk size (default: 20G)")
	createCmd.Flags().StringVar(&createISO, "iso", "", "ISO path/URL (QEMU: local file, KubeVirt: CDI imports from URL)")
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
	createCmd.Flags().BoolVar(&createStart, "start", false, "[qemu] Start the VM after creating it")
	createCmd.Flags().BoolVar(&createWaitSSH, "wait-ssh", false, "[qemu] Start the VM and block until SSH answers; nonzero exit on timeout (CI gate)")
	createCmd.Flags().IntVar(&createTimeout, "timeout", 600, "[qemu] Seconds to wait for SSH with --wait-ssh")
	createCmd.Flags().StringVar(&createSSHUser, "ssh-user", "root", "[qemu] User for the --wait-ssh probe (bootc injects the key for root)")
}

func runKubevirtCreate(name string) error {
	ns := createNamespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
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
		Name:              name,
		Namespace:         ns,
		Mem:               createMem,
		CPU:               createCPU,
		Disk:              createDisk,
		ISO:               iso,
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
	}
	if err := kubevirt.CreateVM(opts); err != nil {
		return err
	}
	// Expose SSH/VNC on the tailnet via the Tailscale operator proxy (no
	// in-guest tailscale needed). Best-effort — the VM is already created.
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tailnet expose failed: %v\n", err)
	}

	if registryStore != nil {
		if err := registryStore.Set(name, types.RegistryEntry{
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

func runQemuCreate(name string) error {
	if err := qemu.Create(types.CreateOpts{
		Name:  name,
		Mem:   createMem,
		CPU:   createCPU,
		Disk:  createDisk,
		ISO:   createISO,
		QCOW:  createQCOW,
		Force: createForce,
	}); err != nil {
		return err
	}
	if registryStore != nil {
		registryStore.Set(name, types.RegistryEntry{Backend: "qemu"})
	}
	return nil
}

func runKubevirtBootcCreate(name string) error {
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
		size = "50Gi"
	}
	build, err := kubevirt.BootcBuildDisk(name, ns, createBootc, sshKey, size, createStorageClass, createProvisionScript, os.Stderr)
	if err != nil {
		return err
	}
	vm := kubevirt.GenerateBootcVM(name, ns, build.PVCName, createBootc, sshKey, createMem, createCPU, createNode)
	if err := kubevirt.Apply(vm); err != nil {
		return err
	}
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tailnet expose failed: %v\n", err)
	}
	if registryStore != nil {
		registryStore.Set(name, types.RegistryEntry{Backend: "kubevirt", Namespace: ns})
	}
	fmt.Fprintf(os.Stderr, "VM %q created in ns/%s\n", name, ns)
	fmt.Fprintf(os.Stderr, "  Start:  corral start %s\n", name)
	fmt.Fprintf(os.Stderr, "  SSH:    corral ssh %s\n", name)
	return nil
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

	var provisionArg string
	if createProvisionScript != "" {
		provFile := filepath.Join(vmDir, "provision.sh")
		if err := os.WriteFile(provFile, []byte(createProvisionScript), 0755); err != nil {
			return err
		}
		defer os.Remove(provFile)
		provisionArg = "&& cat /output/provision.sh | chroot /mnt /bin/bash"
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
	// Install straight from the host's root podman storage when the image is
	// already there — that makes locally built images (localhost/... from a
	// plain `sudo podman build` / `just build`) first-class, and skips the
	// in-container re-pull for registry refs too. Requires the store mount.
	sourceArg := ""
	if exec.Command("sudo", "podman", "image", "exists", createBootc).Run() == nil {
		sourceArg = fmt.Sprintf(" --source-imgref containers-storage:%s", createBootc)
	} else if strings.HasPrefix(createBootc, "localhost/") {
		return fmt.Errorf("image %q not found in root podman storage — build it with sudo, or sync it: podman save %s | sudo podman load", createBootc, createBootc)
	}
	// --generic-image installs every bootloader flavor instead of flashing
	// host-specific firmware, so the disk boots under plain QEMU (SeaBIOS
	// or OVMF) — required for portable/CI disks.
	cmd := exec.Command("sudo", "podman", "run", "--privileged", "--pid=host", "--security-opt", "label=disable",
		"-v", "/dev:/dev", "-v", "/var/lib/containers:/var/lib/containers", "-v", vmDir+":/output:Z",
		createBootc, "sh", "-c",
		fmt.Sprintf("bootc install to-disk --filesystem xfs --wipe --generic-image --root-ssh-authorized-keys /output/id_rsa.pub%s %s && udevadm settle && mkdir -p /mnt && mount %sp3 /mnt %s && umount /mnt", sourceArg, loopDev, loopDev, provisionArg))
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
	if err := qemu.Create(types.CreateOpts{
		Name:         name,
		Mem:          createMem,
		CPU:          createCPU,
		Disk:         createDisk,
		Force:        true,
		ExistingDisk: true,
	}); err != nil {
		return err
	}
	if registryStore != nil {
		registryStore.Set(name, types.RegistryEntry{Backend: "qemu"})
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
