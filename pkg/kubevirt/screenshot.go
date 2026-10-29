package kubevirt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Screenshot writes the running VMI's VNC framebuffer to outPath as a PNG
// through KubeVirt's vnc/screenshot subresource (virtctl vnc screenshot), the
// same display the web UI's noVNC console shows.
func (c *Client) Screenshot(name, outPath string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(outPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	out, err := c.runner().Run(virtctl, "vnc", "screenshot", name, "-n", c.Namespace, "-f", outPath)
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("virtctl vnc screenshot %s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("virtctl vnc screenshot %s: %w", name, err)
	}
	return nil
}
