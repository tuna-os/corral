package kubevirt

import (
	"encoding/json"
	"fmt"

	"github.com/tuna-os/corral/pkg/types"
)

// Details reads one VM, its running instance (if any), and the namespace's
// PVCs, and flattens them into the shape `corral info --json` prints.
func (c *Client) Details(name string) (types.VMDetails, error) {
	r := c.runner()
	vmJSON, err := r.Run("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "json")
	if err != nil {
		return types.VMDetails{}, fmt.Errorf("reading VM %s/%s: %s", c.Namespace, name, commandOutputOrErr(vmJSON, err))
	}
	// A stopped VM has no instance, and a missing PVC list only drops sizes:
	// neither is a failure of the read.
	vmiJSON, err := r.Run("kubectl", "get", "vmi", name, "-n", c.Namespace, "-o", "json")
	if err != nil {
		vmiJSON = nil
	}
	pvcJSON, err := r.Run("kubectl", "get", "pvc", "-n", c.Namespace, "-o", "json")
	if err != nil {
		pvcJSON = nil
	}
	d, err := parseVMDetails(vmJSON, vmiJSON, pvcJSON)
	if err != nil {
		return types.VMDetails{}, err
	}
	d.Context = c.Context
	return d, nil
}

// vmDomain is the part of a KubeVirt domain spec that Details reports. VM
// template and VMI spec share it.
type vmDomain struct {
	CPU struct {
		Sockets               int    `json:"sockets"`
		Cores                 int    `json:"cores"`
		Threads               int    `json:"threads"`
		Model                 string `json:"model"`
		DedicatedCPUPlacement bool   `json:"dedicatedCpuPlacement"`
		IsolateEmulatorThread bool   `json:"isolateEmulatorThread"`
	} `json:"cpu"`
	Memory struct {
		Guest string `json:"guest"`
	} `json:"memory"`
	Resources struct {
		Requests map[string]string `json:"requests"`
	} `json:"resources"`
	Machine struct {
		Type string `json:"type"`
	} `json:"machine"`
	Firmware struct {
		Bootloader struct {
			EFI *struct {
				SecureBoot *bool `json:"secureBoot"`
			} `json:"efi"`
		} `json:"bootloader"`
		KernelBoot *json.RawMessage `json:"kernelBoot"`
	} `json:"firmware"`
	Devices struct {
		Disks []struct {
			Name  string            `json:"name"`
			Disk  map[string]string `json:"disk"`
			CDRom map[string]string `json:"cdrom"`
			LUN   map[string]string `json:"lun"`
		} `json:"disks"`
		Inputs []types.VMInput `json:"inputs"`
	} `json:"devices"`
}

type vmVolume struct {
	Name                  string `json:"name"`
	PersistentVolumeClaim *struct {
		ClaimName string `json:"claimName"`
	} `json:"persistentVolumeClaim"`
	DataVolume *struct {
		Name string `json:"name"`
	} `json:"dataVolume"`
	ContainerDisk *struct {
		Image string `json:"image"`
	} `json:"containerDisk"`
	CloudInitNoCloud   *json.RawMessage `json:"cloudInitNoCloud"`
	CloudInitConfigDrv *json.RawMessage `json:"cloudInitConfigDrive"`
	EmptyDisk          *json.RawMessage `json:"emptyDisk"`
	Secret             *json.RawMessage `json:"secret"`
	ConfigMap          *json.RawMessage `json:"configMap"`
}

type vmSpec struct {
	Domain       vmDomain          `json:"domain"`
	NodeSelector map[string]string `json:"nodeSelector"`
	Volumes      []vmVolume        `json:"volumes"`
}

// parseVMDetails is Details without the cluster: the VM object, the VMI
// object (nil when stopped), and the namespace's PVC list (nil if unread).
// The running instance wins for hardware, because KubeVirt expands an
// instancetype and applies defaults there.
func parseVMDetails(vmJSON, vmiJSON, pvcJSON []byte) (types.VMDetails, error) {
	var vm struct {
		Metadata struct {
			Name        string            `json:"name"`
			Namespace   string            `json:"namespace"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec vmSpec `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			PrintableStatus string `json:"printableStatus"`
			Ready           bool   `json:"ready"`
		} `json:"status"`
	}
	if err := json.Unmarshal(vmJSON, &vm); err != nil {
		return types.VMDetails{}, fmt.Errorf("parsing VM: %w", err)
	}
	spec := vm.Spec.Template.Spec
	node := spec.NodeSelector["kubernetes.io/hostname"]
	var ips []string
	if len(vmiJSON) > 0 {
		var vmi struct {
			Spec   vmSpec `json:"spec"`
			Status struct {
				NodeName   string `json:"nodeName"`
				Interfaces []struct {
					IPAddress   string   `json:"ipAddress"`
					IPAddresses []string `json:"ipAddresses"`
				} `json:"interfaces"`
			} `json:"status"`
		}
		if err := json.Unmarshal(vmiJSON, &vmi); err == nil {
			spec = vmi.Spec
			if vmi.Status.NodeName != "" {
				node = vmi.Status.NodeName
			}
			seen := map[string]bool{}
			for _, iface := range vmi.Status.Interfaces {
				addrs := iface.IPAddresses
				if len(addrs) == 0 && iface.IPAddress != "" {
					addrs = []string{iface.IPAddress}
				}
				for _, a := range addrs {
					if !seen[a] {
						seen[a] = true
						ips = append(ips, a)
					}
				}
			}
		}
	}

	dom := spec.Domain
	sockets, cores, threads := orOne(dom.CPU.Sockets), orOne(dom.CPU.Cores), orOne(dom.CPU.Threads)
	memory := dom.Memory.Guest
	if memory == "" {
		memory = dom.Resources.Requests["memory"]
	}
	firmware := types.VMFirmware{Type: "bios", Machine: dom.Machine.Type}
	switch {
	case dom.Firmware.KernelBoot != nil:
		firmware.Type = "kernelBoot"
	case dom.Firmware.Bootloader.EFI != nil:
		firmware.Type = "uefi"
		// KubeVirt defaults secureBoot to true when efi is set without it.
		sb := dom.Firmware.Bootloader.EFI.SecureBoot
		firmware.SecureBoot = sb == nil || *sb
	}

	d := types.VMDetails{
		Name:      vm.Metadata.Name,
		Namespace: vm.Metadata.Namespace,
		Backend:   "kubevirt",
		Status:    vm.Status.PrintableStatus,
		Ready:     vm.Status.Ready,
		Node:      node,
		CPU: types.VMCPU{
			VCPUs:                 sockets * cores * threads,
			Sockets:               sockets,
			Cores:                 cores,
			Threads:               threads,
			Model:                 dom.CPU.Model,
			DedicatedCPUPlacement: dom.CPU.DedicatedCPUPlacement,
			IsolateEmulatorThread: dom.CPU.IsolateEmulatorThread,
		},
		Memory:    memory,
		Disks:     vmDisks(dom, spec.Volumes, pvcSizes(pvcJSON)),
		Inputs:    dom.Devices.Inputs,
		Firmware:  firmware,
		IPs:       ips,
		TagValues: tagValuesFromLabels(vm.Metadata.Labels),
		Labels:    vm.Metadata.Labels,
		Image:     vm.Metadata.Annotations["corral.bootc/image"],
	}
	return d, nil
}

func orOne(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// vmDisks pairs each disk device with the volume that backs it.
func vmDisks(dom vmDomain, volumes []vmVolume, sizes map[string]string) []types.VMDisk {
	byName := make(map[string]vmVolume, len(volumes))
	for _, v := range volumes {
		byName[v.Name] = v
	}
	var disks []types.VMDisk
	for _, dd := range dom.Devices.Disks {
		disk := types.VMDisk{Name: dd.Name, Type: "disk", Bus: dd.Disk["bus"]}
		switch {
		case dd.CDRom != nil:
			disk.Type, disk.Bus = "cdrom", dd.CDRom["bus"]
		case dd.LUN != nil:
			disk.Type, disk.Bus = "lun", dd.LUN["bus"]
		}
		v := byName[dd.Name]
		switch {
		case v.PersistentVolumeClaim != nil:
			disk.Source, disk.Claim = "persistentVolumeClaim", v.PersistentVolumeClaim.ClaimName
		case v.DataVolume != nil:
			disk.Source, disk.Claim = "dataVolume", v.DataVolume.Name
		case v.ContainerDisk != nil:
			disk.Source, disk.Image = "containerDisk", v.ContainerDisk.Image
		case v.CloudInitNoCloud != nil:
			disk.Source = "cloudInitNoCloud"
		case v.CloudInitConfigDrv != nil:
			disk.Source = "cloudInitConfigDrive"
		case v.EmptyDisk != nil:
			disk.Source = "emptyDisk"
		case v.Secret != nil:
			disk.Source = "secret"
		case v.ConfigMap != nil:
			disk.Source = "configMap"
		}
		if disk.Claim != "" {
			disk.Size = sizes[disk.Claim]
		}
		disks = append(disks, disk)
	}
	return disks
}

// pvcSizes maps each PVC name to its bound capacity, or its request while
// it is still pending.
func pvcSizes(pvcJSON []byte) map[string]string {
	if len(pvcJSON) == 0 {
		return nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Resources struct {
					Requests map[string]string `json:"requests"`
				} `json:"resources"`
			} `json:"spec"`
			Status struct {
				Capacity map[string]string `json:"capacity"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(pvcJSON, &list) != nil {
		return nil
	}
	sizes := make(map[string]string, len(list.Items))
	for _, p := range list.Items {
		size := p.Status.Capacity["storage"]
		if size == "" {
			size = p.Spec.Resources.Requests["storage"]
		}
		sizes[p.Metadata.Name] = size
	}
	return sizes
}
