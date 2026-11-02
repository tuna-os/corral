package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/create"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/lima"
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
	createVsock             bool
	createVsockCID          uint32
	createTPM               bool
	createDedicatedCPUs     bool
	createIsolateEmulator   bool
	createTags              []string
)

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
		return executeCreate(args[0], cmd.Flags().Changed("kubevirt"))
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.Flags().BoolVarP(&createKubevirt, "kubevirt", "k", false, "Use KubeVirt backend")
	createCmd.Flags().BoolVar(&createIncus, "incus", false, "Use Incus backend")
	createCmd.Flags().StringVar(&createFirmware, "firmware", "", "Firmware boot type: uefi (default) or bios")
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

// executeCreate adapts CLI/TUI flag state to the reusable create service.
func executeCreate(name string, kubevirtExplicit bool) error {
	service := create.NewService(resolveBackend, nil)
	if registryStore != nil {
		service.Registry = registryStore
	}
	return service.Execute(name, createOptions(kubevirtExplicit))
}

func createOptions(kubevirtExplicit bool) create.Options {
	return create.Options{
		KubevirtExplicit: kubevirtExplicit, Backend: rootBackend, Context: rootContext,
		Values: lima.Values{
			Mem:             createMem,
			CPU:             createCPU,
			Disk:            createDisk,
			ISO:             createISO,
			ContainerDisk:   createContainerDisk,
			Image:           createImage,
			Import:          createImport,
			CloudInit:       createCloudInit,
			Bootc:           createBootc,
			ProvisionScript: createProvisionScript,
		},
		Kubevirt:          createKubevirt,
		Incus:             createIncus,
		QCOW:              createQCOW,
		Force:             createForce,
		PVC:               createPVC,
		Namespace:         createNamespace,
		Node:              createNode,
		CloudInitPassword: createCloudInitPassword,
		InstanceType:      createInstanceType,
		Preference:        createPreference,
		File:              createFile,
		StorageClass:      createStorageClass,
		Kargs:             createKargs,
		Start:             createStart,
		WaitSSH:           createWaitSSH,
		Timeout:           createTimeout,
		SSHUser:           createSSHUser,
		Ephemeral:         createEphemeral,
		TTL:               createTTL,
		LAN:               createLAN,
		NetworkNAD:        createNetworkNAD,
		BridgeIface:       createBridgeIface,
		LANService:        createLANService,
		Firmware:          createFirmware,
		Vsock:             createVsock,
		VsockCID:          createVsockCID,
		TPM:               createTPM,
		DedicatedCPUs:     createDedicatedCPUs,
		IsolateEmulator:   createIsolateEmulator,
		Tags:              createTags,
	}
}
