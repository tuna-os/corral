package nutanix

// Export: an OVA built by Prism Central.
//
// vmm v4.1 added content/ovas: POST an Ova whose source is the VM
// (vmm.v4.content.OvaVmSource {vmExtId, diskFileFormat}), wait for the task,
// read the new OVA's extId from the task's entitiesAffected, stream
// content/ovas/{extId}/file, and delete the OVA so the export leaves nothing
// behind in the catalog. Shapes follow the official SDK
// (nutanix/ntnx-api-golang-clients vmm-go-client v4.1.1 api/ovas_api.go and
// models/vmm/v4/content). vmm v4.0 has no ovas endpoint, so a context on
// v4.0 is refused with the remedy rather than sent to a 404.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ExportTimeout bounds the OVA build on the cluster. Building copies every
// disk, so it is sized for disks, not for API calls.
const ExportTimeout = 2 * time.Hour

// ExportOVA writes the VM as an OVA with QCOW2 disks to dest. It reports
// whether the VM was powered on when the export started: Prism Central does
// not say how it reads a running VM's disks, so the caller must treat that
// export as crash-consistent at best.
func (c *Client) ExportOVA(ctx context.Context, name, dest string) (running bool, err error) {
	if c.cfg.APIVersion == "v4.0" {
		return false, fmt.Errorf("nutanix: OVA export needs vmm v4.1 or later and this context uses %s; set apiVersion: v4.1 (or later) on the context", c.cfg.APIVersion)
	}
	vm, err := c.Resolve(name)
	if err != nil {
		return false, err
	}
	running = !strings.EqualFold(vm.PowerState, "OFF")

	body := map[string]any{
		"name": fmt.Sprintf("corral-export-%s-%d", vm.Name, time.Now().Unix()),
		"source": map[string]any{
			"$objectType":    "vmm.v4.content.OvaVmSource",
			"vmExtId":        vm.ExtID,
			"diskFileFormat": "QCOW2",
		},
	}
	var created actionResponse
	if err := c.post("/vmm/"+c.cfg.APIVersion+"/content/ovas", body, &created); err != nil {
		return running, err
	}
	task := Task{ExtID: created.Data.ExtID}
	if !task.Valid() {
		return running, fmt.Errorf("nutanix: OVA export of %q returned no task extId", name)
	}
	if err := c.WaitTask(task, ExportTimeout); err != nil {
		return running, fmt.Errorf("nutanix: building the OVA of %q: %w", name, err)
	}
	st, err := c.TaskStatus(task)
	if err != nil {
		return running, err
	}
	ova := ""
	for _, e := range st.EntitiesAffected {
		if strings.Contains(strings.ToLower(e.Rel), "ova") {
			ova = e.ExtID
		}
	}
	if ova == "" {
		return running, fmt.Errorf("nutanix: the OVA task of %q names no OVA in entitiesAffected", name)
	}
	path := "/vmm/" + c.cfg.APIVersion + "/content/ovas/" + ova
	defer func() {
		// Leave nothing in the catalog, even when the download failed.
		if etag, err := c.getETag(path); err == nil {
			_, _ = c.deleteTask(path, etag)
		}
	}()
	return running, c.download(ctx, path+"/file", dest)
}

// download streams a file endpoint to dest. It is bound to ctx rather than
// to Timeout, which bounds a whole request including its body.
func (c *Client) download(ctx context.Context, path, dest string) error {
	req, err := c.newRequest(http.MethodGet, path, nil, map[string]string{"Accept": "application/octet-stream"})
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: c.http.Transport}).Do(req.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("nutanix: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Method: http.MethodGet, Path: path}
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("nutanix: downloading %s: %w", path, err)
	}
	return out.Close()
}
