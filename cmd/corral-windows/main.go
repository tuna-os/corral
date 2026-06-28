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

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/spf13/cobra"
)

var runner shell.Runner = shell.Real{}

// VirtioWinImage is re-exported for tests; the manifest logic lives in
// pkg/kubevirt so the web GUI shares it.
const VirtioWinImage = kubevirt.VirtioWinImage

// generateWindowsVM delegates to the shared package.
func generateWindowsVM(name, ns, mem string, cpu int) map[string]any {
	return kubevirt.GenerateWindowsVM(name, ns, mem, cpu)
}

// createWindowsVM imports the installer ISO, provisions the boot disk, and
// applies the Windows-tuned VM (optionally exposing RDP), then records it in
// the local registry and prints next steps.
func createWindowsVM(name, ns, iso, disk, mem string, cpu int, rdp bool) error {
	fmt.Fprintf(os.Stderr, "installer ISO importing: %s-iso ← %s\n", name, iso)
	if err := kubevirt.CreateWindowsVM(name, ns, iso, disk, mem, cpu, rdp); err != nil {
		return err
	}
	if rdp {
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
