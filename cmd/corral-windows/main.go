// corral-windows is the Windows-VM Corral plugin: it creates a KubeVirt VM
// pre-configured the way Windows wants it — q35 + UEFI + TPM + Hyper-V
// enlightenments, the installer ISO imported via CDI, and the virtio-win
// driver ISO attached as a second CD-ROM so Setup can load disk/network
// drivers. Installed via the marketplace (`corral plugin install windows`)
// and invoked as `corral windows`.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/registry"
	"github.com/hanthor/corral/pkg/shell"
	"github.com/hanthor/corral/pkg/types"
	"github.com/spf13/cobra"
)

var runner shell.Runner = shell.Real{}

// VirtioWinImage is KubeVirt's containerdisk build of the virtio-win driver ISO.
const VirtioWinImage = "quay.io/kubevirt/virtio-container-disk:latest"

// generateWindowsVM builds the VirtualMachine manifest. The boot order is
// disk(1) → installer ISO(2): the empty disk falls through to the ISO on
// first boot, and Windows boots from disk directly once installed.
func generateWindowsVM(name, ns, mem string, cpu int) map[string]any {
	disks := []map[string]any{
		{"name": "rootdisk", "bootOrder": 1, "disk": map[string]any{"bus": "virtio"}},
		{"name": "windows-iso", "bootOrder": 2, "cdrom": map[string]any{"bus": "sata"}},
		{"name": "virtio-drivers", "cdrom": map[string]any{"bus": "sata"}},
	}
	volumes := []map[string]any{
		{"name": "rootdisk", "persistentVolumeClaim": map[string]any{"claimName": name + "-disk"}},
		{"name": "windows-iso", "persistentVolumeClaim": map[string]any{"claimName": name + "-iso"}},
		{"name": "virtio-drivers", "containerDisk": map[string]any{"image": VirtioWinImage}},
	}
	return map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels":    map[string]any{"corral.dev/os": "windows"},
		},
		"spec": map[string]any{
			"running": false,
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"kubevirt.io/vm": name},
				},
				"spec": map[string]any{
					"domain": map[string]any{
						"machine": map[string]any{"type": "q35"},
						"firmware": map[string]any{
							"bootloader": map[string]any{
								// secureBoot needs SMM; off keeps it simple and
								// boots every Windows version.
								"efi": map[string]any{"secureBoot": false},
							},
						},
						"features": map[string]any{
							"acpi": map[string]any{},
							"apic": map[string]any{},
							"smm":  map[string]any{"enabled": false},
							// Hyper-V enlightenments: Windows runs poorly without them.
							"hyperv": map[string]any{
								"relaxed": map[string]any{},
								"vapic":   map[string]any{},
								"vpindex": map[string]any{},
								"synic":   map[string]any{},
								"synictimer": map[string]any{
									"direct": map[string]any{},
								},
								"spinlocks":   map[string]any{"spinlocks": 8191},
								"frequencies": map[string]any{},
								"ipi":         map[string]any{},
							},
						},
						"clock": map[string]any{
							"utc": map[string]any{},
							"timer": map[string]any{
								"hpet":   map[string]any{"present": false},
								"pit":    map[string]any{"tickPolicy": "delay"},
								"rtc":    map[string]any{"tickPolicy": "catchup"},
								"hyperv": map[string]any{},
							},
						},
						"cpu": map[string]any{"cores": cpu},
						"devices": map[string]any{
							"tpm":   map[string]any{},
							"disks": disks,
							"interfaces": []map[string]any{
								// e1000e works during Setup before virtio NIC
								// drivers are installed.
								{"name": "default", "masquerade": map[string]any{}, "model": "e1000e"},
							},
							"inputs": []map[string]any{
								{"type": "tablet", "bus": "usb", "name": "tablet"},
							},
						},
						"resources": map[string]any{
							"requests": map[string]any{"memory": mem},
						},
					},
					"networks": []map[string]any{
						{"name": "default", "pod": map[string]any{}},
					},
					"volumes": volumes,
				},
			},
		},
	}
}

// createWindowsVM imports the installer ISO, provisions the boot disk, and
// applies the Windows-tuned VM. rdpPorts non-empty also exposes RDP through
// the corral proxy.
func createWindowsVM(name, ns, iso, disk, mem string, cpu int, rdp bool) error {
	kubevirt.EnsureNamespace(ns)
	sc := kubevirt.PreferredStorageClass()

	if err := kubevirt.Apply(kubevirt.GenerateDataVolume(name+"-iso", ns, iso)); err != nil {
		return fmt.Errorf("creating ISO DataVolume: %w", err)
	}
	fmt.Fprintf(os.Stderr, "installer ISO importing: %s-iso ← %s\n", name, iso)
	if err := kubevirt.Apply(kubevirt.GeneratePVCWithClass(name+"-disk", ns, disk, sc)); err != nil {
		return fmt.Errorf("creating boot disk PVC: %w", err)
	}
	if err := kubevirt.Apply(generateWindowsVM(name, ns, mem, cpu)); err != nil {
		return fmt.Errorf("creating VM: %w", err)
	}
	if rdp {
		if err := kubevirt.ApplyProxy(name, ns, []int{3389}); err != nil {
			return fmt.Errorf("exposing RDP: %w", err)
		}
		fmt.Fprintf(os.Stderr, "RDP exposed via proxy service %s-proxy (port 3389)\n", name)
	}
	if store, err := registry.NewStore(); err == nil {
		store.Set(name, types.RegistryEntry{
			Backend:   "kubevirt",
			Namespace: ns,
			Extra:     map[string]string{"os": "windows"},
		})
	}
	fmt.Fprintf(os.Stderr, `VM %q created (stopped). Next steps:
  1. wait for the ISO import:   corral images
  2. start it:                  corral start %s
  3. open the console:          corral web → Console (or corral viewer %s)
  4. in Windows Setup, click "Load driver" → browse the virtio CD-ROM
     (E:\amd64\<your windows version>) to see the disk.
`, name, name, name)
	return nil
}

// attachDrivers adds the virtio-win driver CD-ROM to an existing VM via a
// JSON patch appending a disk + matching containerDisk volume.
func attachDrivers(vm, ns string) error {
	patch := fmt.Sprintf(`[
      {"op":"add","path":"/spec/template/spec/domain/devices/disks/-","value":{"name":"virtio-drivers","cdrom":{"bus":"sata"}}},
      {"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"virtio-drivers","containerDisk":{"image":%q}}}
    ]`, VirtioWinImage)
	out, err := runner.Run("kubectl", "patch", "vm", vm, "-n", ns, "--type", "json", "-p", patch)
	if err != nil {
		return fmt.Errorf("attaching drivers: %s", strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(os.Stderr, "virtio-win drivers attached to %s (takes effect on next boot)\n", vm)
	return nil
}

func main() {
	var (
		namespace, iso, disk, mem string
		cpu                       int
		rdp                       bool
	)

	create := &cobra.Command{
		Use:   "create <name> --iso <windows-iso-url>",
		Short: "Create a Windows-ready VM (UEFI, TPM, virtio drivers, Hyper-V enlightenments)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if iso == "" {
				return fmt.Errorf("--iso is required (HTTP(S) URL to a Windows installer ISO)")
			}
			return createWindowsVM(args[0], namespace, iso, disk, mem, cpu, rdp)
		},
	}
	create.Flags().StringVar(&iso, "iso", "", "Windows installer ISO URL (imported via CDI)")
	create.Flags().StringVar(&disk, "disk", "64Gi", "Boot disk size")
	create.Flags().StringVar(&mem, "mem", "8Gi", "Memory")
	create.Flags().IntVar(&cpu, "cpu", 4, "vCPU cores")
	create.Flags().BoolVar(&rdp, "rdp", false, "Expose RDP (3389) through the corral proxy")

	drivers := &cobra.Command{
		Use:   "drivers <vm>",
		Short: "Attach the virtio-win driver CD-ROM to an existing VM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return attachDrivers(args[0], namespace)
		},
	}

	root := &cobra.Command{
		Use:   "corral-windows",
		Short: "Corral plugin — first-class Windows VMs on KubeVirt",
	}
	root.PersistentFlags().StringVarP(&namespace, "namespace", "n", kubevirt.DefaultNamespace, "Namespace")
	root.AddCommand(create, drivers)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
