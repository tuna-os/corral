package kubevirt

import (
	"fmt"
	"time"
)

// ── Clone ─────────────────────────────────────────────────────────

// Clone creates a VirtualMachineClone from src into a new VM named dst.
func (c *Client) Clone(src, dst string) error {
	obj := map[string]any{
		"apiVersion": "clone.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineClone",
		"metadata": map[string]any{
			"name":      fmt.Sprintf("clone-%s-%d", dst, time.Now().Unix()),
			"namespace": c.Namespace,
			"labels":    map[string]any{VMLabel: dst},
		},
		"spec": map[string]any{
			"source": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": src},
			"target": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": dst},
			// Copy all labels except the template marker — a clone isn't a template.
			"labelFilters": []string{"*", "!corral.dev/template"},
		},
	}
	return Apply(obj)
}
