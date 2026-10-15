package kubevirt

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── CPU / memory scaling ──────────────────────────────────────────

type vmDomainInfo struct {
	sockets, cores, threads, maxSockets int
	guestMib, maxGuestMib               int
	running                             bool
}

func (c *Client) vmDomain(name string) (vmDomainInfo, error) {
	out, err := c.runner().Run("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "json")
	if err != nil {
		return vmDomainInfo{}, fmt.Errorf("reading VM %s: %w", name, err)
	}
	var vm struct {
		Spec struct {
			Running  *bool `json:"running"`
			Template struct {
				Spec struct {
					Domain struct {
						CPU struct {
							Cores      int `json:"cores"`
							Sockets    int `json:"sockets"`
							Threads    int `json:"threads"`
							MaxSockets int `json:"maxSockets"`
						} `json:"cpu"`
						Memory struct {
							Guest    string `json:"guest"`
							MaxGuest string `json:"maxGuest"`
						} `json:"memory"`
					} `json:"domain"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &vm); err != nil {
		return vmDomainInfo{}, err
	}
	d := vm.Spec.Template.Spec.Domain
	return vmDomainInfo{
		sockets:     d.CPU.Sockets,
		cores:       d.CPU.Cores,
		threads:     d.CPU.Threads,
		maxSockets:  d.CPU.MaxSockets,
		guestMib:    quantityToMib(d.Memory.Guest),
		maxGuestMib: quantityToMib(d.Memory.MaxGuest),
		running:     vm.Spec.Running != nil && *vm.Spec.Running,
	}, nil
}

// ScaleCPU sets the VM's vCPU count (live when possible, else offline).
func (c *Client) ScaleCPU(name string, vcpus int) error {
	if vcpus < 1 {
		return fmt.Errorf("cpu must be >= 1")
	}
	return c.Scale(name, vcpus, "")
}

// ScaleMemory sets the VM's guest memory, e.g. "8G" (live when possible, else offline).
func (c *Client) ScaleMemory(name, mem string) error {
	return c.Scale(name, 0, mem)
}

// Scale changes CPU and/or memory in one operation. When the VM is running and
// genuinely live-migratable (same-vendor target node, masquerade net) and the
// new values fit the maxSockets/maxGuest headroom, both are hotplugged live
// with no downtime. Otherwise both are applied in a single stop→patch→start
// cycle (one reboot, not two). vcpus==0 / mem=="" mean "leave unchanged".
func (c *Client) Scale(name string, vcpus int, mem string) error {
	if vcpus == 0 && mem == "" {
		return fmt.Errorf("nothing to change")
	}
	d, err := c.vmDomain(name)
	if err != nil {
		return err
	}
	mib := 0
	if mem != "" {
		if mib = quantityToMib(mem); mib < 1 {
			return fmt.Errorf("invalid memory %q", mem)
		}
	}
	socketsBased := d.cores <= 1 && d.threads <= 1
	cpuFits := vcpus == 0 || (socketsBased && d.maxSockets >= vcpus)
	memFits := mem == "" || mib <= d.maxGuestMib

	if d.running && cpuFits && memFits && c.canLiveMigrate(name) {
		if vcpus > 0 {
			if err := c.patchVMMerge(name,
				fmt.Sprintf(`{"spec":{"template":{"spec":{"domain":{"cpu":{"sockets":%d}}}}}}`, vcpus)); err != nil {
				return err
			}
		}
		if mem != "" {
			if err := c.patchVMMerge(name,
				fmt.Sprintf(`{"spec":{"template":{"spec":{"domain":{"memory":{"guest":"%dMi"}}}}}}`, mib)); err != nil {
				return err
			}
		}
		return nil
	}

	domain := map[string]any{}
	if vcpus > 0 {
		domain["cpu"] = cpuSpec(vcpus)
	}
	if mem != "" {
		domain["memory"] = memSpec(mib)
	}
	return c.offlinePatch(name, d.running, domain)
}

// offlinePatch applies a domain-level merge patch, stopping/starting the VM if
// it is running so the change takes effect immediately.
func (c *Client) offlinePatch(name string, running bool, domain map[string]any) error {
	body, err := json.Marshal(map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"domain": domain}}},
	})
	if err != nil {
		return err
	}
	if running {
		if err := c.StopVM(name); err != nil {
			return err
		}
		c.waitStopped(name)
	}
	if err := c.patchVMMerge(name, string(body)); err != nil {
		return err
	}
	if running {
		return c.StartVM(name)
	}
	return nil
}

func (c *Client) waitStopped(name string) {
	for i := 0; i < 60; i++ {
		if _, err := c.runner().Run("kubectl", "get", "vmi", name, "-n", c.Namespace); err != nil {
			return // VMI gone
		}
		time.Sleep(time.Second)
	}
}
