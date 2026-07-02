package kubevirt

import "encoding/json"

// VMOptions is a partial update to a VM's boot/options fields — nil fields
// are left unchanged. Every field here maps honestly onto the KubeVirt
// VirtualMachine spec (see SetVMOptions); there's no field for something
// KubeVirt can't actually do.
type VMOptions struct {
	// RunStrategy sets spec.runStrategy ("Always" starts the VM automatically
	// and keeps it running; "Manual" leaves start/stop entirely to the user).
	// Unlike the other fields here, this takes effect immediately — it's a
	// controller-level setting, not part of the VMI template, so it doesn't
	// need a restart to apply.
	RunStrategy *string

	// Firmware sets spec.template.spec.domain.firmware.bootloader:
	// "uefi" → {efi: {secureBoot: false}}, "bios" → {bios: {}}.
	// Requires a restart — firmware is read once at VMI startup.
	Firmware *string

	// MachineType sets spec.template.spec.domain.machine.type (e.g. "q35").
	// Requires a restart.
	MachineType *string

	// BootOrder maps a device name (disk or interface) to its boot order.
	// Devices not present in the map keep their current bootOrder (or none).
	// Requires a restart — boot order is read once at VMI startup.
	BootOrder map[string]int
}

// GetVMManifest fetches the current VM manifest as a generic map, the same
// shape SetVMOptions mutates and re-applies. Used by the Options panel to
// show current values before editing.
func (c *Client) GetVMManifest(name string) (map[string]any, error) {
	out, err := c.VMInfo(name)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// SetVMOptions applies a partial VMOptions update. It reads the full current
// manifest, mutates only the requested fields in place, and re-applies the
// whole thing — a JSON *merge* patch would silently replace (not merge)
// the disks/interfaces arrays wholesale, wiping out every device but the one
// being changed, so a read-modify-apply round trip is used instead.
func (c *Client) SetVMOptions(name string, opts VMOptions) error {
	m, err := c.GetVMManifest(name)
	if err != nil {
		return err
	}

	spec, _ := m["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
		m["spec"] = spec
	}

	if opts.RunStrategy != nil {
		spec["runStrategy"] = *opts.RunStrategy
		delete(spec, "running") // runStrategy and running are mutually exclusive in the KubeVirt API
	}

	template, _ := spec["template"].(map[string]any)
	if template == nil {
		template = map[string]any{}
		spec["template"] = template
	}
	tspec, _ := template["spec"].(map[string]any)
	if tspec == nil {
		tspec = map[string]any{}
		template["spec"] = tspec
	}
	domain, _ := tspec["domain"].(map[string]any)
	if domain == nil {
		domain = map[string]any{}
		tspec["domain"] = domain
	}

	if opts.Firmware != nil {
		firmware, _ := domain["firmware"].(map[string]any)
		if firmware == nil {
			firmware = map[string]any{}
			domain["firmware"] = firmware
		}
		switch *opts.Firmware {
		case "uefi":
			firmware["bootloader"] = map[string]any{"efi": map[string]any{"secureBoot": false}}
		case "bios":
			firmware["bootloader"] = map[string]any{"bios": map[string]any{}}
		}
	}

	if opts.MachineType != nil {
		machine, _ := domain["machine"].(map[string]any)
		if machine == nil {
			machine = map[string]any{}
			domain["machine"] = machine
		}
		machine["type"] = *opts.MachineType
	}

	if len(opts.BootOrder) > 0 {
		devices, _ := domain["devices"].(map[string]any)
		if devices != nil {
			applyBootOrder(devices["disks"], opts.BootOrder)
			applyBootOrder(devices["interfaces"], opts.BootOrder)
		}
	}

	return Apply(m)
}

// applyBootOrder sets/clears "bootOrder" on each device in list (a
// []any of map[string]any, as decoded from JSON) whose "name" is a key in
// order.
func applyBootOrder(list any, order map[string]int) {
	devices, ok := list.([]any)
	if !ok {
		return
	}
	for _, d := range devices {
		dev, ok := d.(map[string]any)
		if !ok {
			continue
		}
		name, _ := dev["name"].(string)
		if n, ok := order[name]; ok {
			dev["bootOrder"] = n
		}
	}
}
