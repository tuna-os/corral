package kubevirt

import "fmt"

// Windows VM support, shared by the corral-windows plugin (CLI) and the web
// server (GUI create flow). A Windows guest needs q35 + UEFI + TPM + Hyper-V
// enlightenments, the installer ISO as a CD-ROM, and the virtio-win driver
// ISO as a second CD-ROM so Setup can load disk/network drivers.

// VirtioWinImage is KubeVirt's containerdisk build of the virtio-win driver ISO.
const VirtioWinImage = "quay.io/kubevirt/virtio-container-disk:latest"

// GenerateWindowsVM builds the Windows-tuned VirtualMachine manifest. Boot
// order is disk(1) → installer ISO(2): the empty disk falls through to the ISO
// on first boot, and Windows boots from disk directly once installed.
func GenerateWindowsVM(name, ns, mem string, cpu int) map[string]any {
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

// CreateWindowsVM imports the installer ISO, provisions the boot disk, and
// applies the Windows-tuned VM. When rdp is true it also exposes RDP (3389)
// through the corral tailnet proxy. disk/mem default to 64Gi/8Gi, cpu to 4.
func CreateWindowsVM(name, ns, iso, disk, mem string, cpu int, rdp bool) error {
	if ns == "" {
		ns = DefaultNamespace
	}
	if disk == "" {
		disk = "64Gi"
	}
	if mem == "" {
		mem = "8Gi"
	}
	if cpu == 0 {
		cpu = 4
	}
	if iso == "" {
		return fmt.Errorf("a Windows installer ISO URL is required")
	}
	EnsureNamespace(ns)
	sc := PreferredStorageClass()

	if err := Apply(GenerateDataVolume(name+"-iso", ns, iso)); err != nil {
		return fmt.Errorf("creating ISO DataVolume: %w", err)
	}
	if err := Apply(GeneratePVCWithClass(name+"-disk", ns, disk, sc)); err != nil {
		return fmt.Errorf("creating boot disk PVC: %w", err)
	}
	if err := Apply(GenerateWindowsVM(name, ns, mem, cpu)); err != nil {
		return fmt.Errorf("creating VM: %w", err)
	}
	if rdp {
		if err := ApplyProxy(name, ns, []int{3389}); err != nil {
			return fmt.Errorf("exposing RDP: %w", err)
		}
	}
	return nil
}
