package create

import (
	"fmt"
	"os"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

func (r request) runKubevirtCreate(name string) error {
	backend.ActivateContext(types.InstanceRef{Backend: "kubevirt", Context: r.createContext(config.KubeContext)})
	ns := r.Namespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	placement, err := r.createPlacement()
	if err != nil {
		return err
	}

	containerDisk := r.ContainerDisk
	importURL := r.Import
	iso := r.ISO
	if r.Image != "" {
		img := catalog.Find(r.Image)
		if img == nil {
			return fmt.Errorf("unknown image %q — see `corral images`", r.Image)
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
		Mem:       r.Mem,
		CPU:       r.CPU,
		Disk:      r.Disk,
		ISO:       iso,
		// --firmware was honoured only by the QEMU paths, so a KubeVirt ISO
		// install always got SeaBIOS and a UEFI-only ISO (Utah, Dakota) found
		// nothing to boot. Opt-in here: changing the KubeVirt default would
		// re-firmware every existing catalog image.
		UEFI:              r.Firmware == "uefi",
		ContainerDisk:     containerDisk,
		ImportURL:         importURL,
		PVC:               r.PVC,
		Node:              r.Node,
		CloudInitPassword: r.CloudInitPassword,
		CloudInitExtra:    r.CloudInit,
		InstanceType:      r.InstanceType,
		Preference:        r.Preference,
		SSHPublicKey:      kubevirt.LoadSSHPublicKey(),
		StorageClass:      r.StorageClass,
		Ephemeral:         r.Ephemeral,
		TTL:               r.TTL,

		DedicatedCPUs:         placement.DedicatedCPUs,
		IsolateEmulatorThread: placement.IsolateEmulatorThread,
		Tags:                  placement.Tags,
	}
	if placement.DedicatedCPUs {
		if err := kubevirt.CheckDedicatedCPUs(r.Node); err != nil {
			return err
		}
	}
	if err := r.KubevirtCreate(opts); err != nil {
		return err
	}
	// Expose SSH/VNC on the tailnet via the Tailscale operator proxy (no
	// in-guest tailscale needed). Best-effort — the VM is already created.
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tailnet expose failed: %v\n", err)
	}

	if r.LAN || r.NetworkNAD != "" {
		if err := r.attachLANBridge(ns, name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	if r.LANService {
		if err := kubevirt.ApplyLANService(name, ns, []int{22}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: LAN service not created: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  LAN:    corral lanservice %s   # check the assigned external IP\n", name)
		}
	}

	if r.Registry != nil {
		if err := r.Registry.SetRef(types.InstanceRef{Backend: "kubevirt", Context: r.createContext(config.KubeContext), Namespace: ns, Name: name}, types.RegistryEntry{
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
func (r request) attachLANBridge(ns, name string) error {
	nad, err := kubevirt.ResolveNAD(r.NetworkNAD, kubevirt.ListNADs())
	if err != nil {
		return fmt.Errorf("LAN bridge not attached: %w", err)
	}
	if err := kubevirt.NewClient(ns).AddNIC(name, nad, r.BridgeIface); err != nil {
		return fmt.Errorf("LAN bridge not attached: %w", err)
	}
	iface := r.BridgeIface
	if iface == "" {
		iface = "net1"
	}
	fmt.Fprintf(os.Stderr, "  LAN:    bridged via %s (guest iface %s)\n", nad, iface)
	return nil
}
