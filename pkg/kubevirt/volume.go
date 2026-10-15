package kubevirt

import (
	"fmt"
	"time"
)

// ── Volumes ───────────────────────────────────────────────────────

// AddVolume creates a new PVC and hotplugs it onto the running VM. Returns the
// PVC name. The PVC is labeled for cleanup on VM delete.
func (c *Client) AddVolume(name, size string) (string, error) {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return "", err
	}
	if size == "" {
		size = "10Gi"
	}
	pvcName := fmt.Sprintf("%s-hp-%d", name, time.Now().Unix())
	pvc := GeneratePVCWithClass(pvcName, c.Namespace, size, PreferredStorageClass())
	pvc["metadata"].(map[string]any)["labels"] = map[string]any{"corral.dev/vm": name}
	if err := Apply(pvc); err != nil {
		return "", fmt.Errorf("creating disk PVC: %w", err)
	}
	_, err = c.runner().Run(virtctl, "addvolume", name, "--volume-name="+pvcName, "-n", c.Namespace)
	if err != nil {
		return "", fmt.Errorf("addvolume: %w", err)
	}
	return pvcName, nil
}

// RemoveVolume detaches a hotplugged volume from the VM.
func (c *Client) RemoveVolume(name, vol string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}
	_, err = c.runner().Run(virtctl, "removevolume", name, "--volume-name="+vol, "-n", c.Namespace)
	return err
}

// ExpandDisk grows a PVC to the given size (requires an expandable StorageClass).
func (c *Client) ExpandDisk(pvc, size string) error {
	patch := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":%q}}}}`, size)
	_, err := c.runner().Run("kubectl", "patch", "pvc", pvc, "-n", c.Namespace,
		"--type", "merge", "-p", patch)
	return err
}
