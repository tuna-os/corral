package kubevirt

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/tuna-os/corral/pkg/shell"
)

// ── Export / backup ───────────────────────────────────────────────

// primaryPVC returns the claim name of the VM's first persistent (PVC-backed)
// volume — the disk worth backing up. Empty for fully ephemeral VMs.
func (c *Client) primaryPVC(name string) (string, error) {
	out, err := c.runner().Run("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("reading VM %s: %w", name, err)
	}
	var vm struct {
		Spec struct {
			Template struct {
				Spec struct {
					Volumes []struct {
						PersistentVolumeClaim *struct {
							ClaimName string `json:"claimName"`
						} `json:"persistentVolumeClaim"`
					} `json:"volumes"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &vm); err != nil {
		return "", err
	}
	for _, v := range vm.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
			return v.PersistentVolumeClaim.ClaimName, nil
		}
	}
	return "", nil
}

// Export backs up a VM's disk to outputPath as a gzip image via the KubeVirt
// export API (virtctl vmexport). The VM should be stopped — its RWO PVC can't
// be read while a running VM holds it. volume defaults to the primary PVC;
// outputPath defaults to "<name>.img.gz". virtctl creates the
// VirtualMachineExport, downloads, and cleans it up.
func (c *Client) Export(name, volume, outputPath string) (string, error) {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return "", err
	}
	if volume == "" {
		if volume, err = c.primaryPVC(name); err != nil {
			return "", err
		}
		if volume == "" {
			return "", fmt.Errorf("%s has no persistent disk to export (ephemeral container-disk VMs have nothing to back up)", name)
		}
	}
	return c.exportFormat(virtctl, name, volume, outputPath, "gzip")
}

// ExportRaw downloads the VM's disk uncompressed (no gzip) — the source for an
// on-the-fly qcow2 conversion. Same preconditions as Export.
func (c *Client) ExportRaw(name, volume, outputPath string) (string, error) {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return "", err
	}
	if volume == "" {
		if volume, err = c.primaryPVC(name); err != nil {
			return "", err
		}
		if volume == "" {
			return "", fmt.Errorf("%s has no persistent disk to export (ephemeral container-disk VMs have nothing to back up)", name)
		}
	}
	return c.exportFormat(virtctl, name, volume, outputPath, "raw")
}

func (c *Client) exportFormat(virtctl, name, volume, outputPath, format string) (string, error) {
	if outputPath == "" {
		outputPath = name + ".img.gz"
	}
	expName := name + "-export"
	// Clear any export left over from an interrupted run so --vm can recreate it.
	_ = shell.CommandForContext(c.Context, "kubectl", "delete", "vmexport", expName, "-n", c.Namespace, "--ignore-not-found").Run()
	// --port-forward tunnels straight to the exporter pod (internal links); the
	// export proxy has no external Ingress, so external links never appear.
	args := []string{"vmexport", "download", expName,
		"--namespace=" + c.Namespace, "--vm=" + name, "--volume=" + volume,
		"--output=" + outputPath, "--format=" + format, "--insecure", "--port-forward"}
	cmd := shell.CommandForContext(c.Context, virtctl, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("vmexport download (is the VM stopped?): %w", err)
	}
	return outputPath, nil
}
