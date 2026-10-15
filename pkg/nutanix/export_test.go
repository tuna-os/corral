package nutanix

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportOVABuildsDownloadsAndDeletes(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	c.cfg.APIVersion = "v4.1"
	dest := filepath.Join(t.TempDir(), "web.ova")
	running, err := c.ExportOVA(context.Background(), "web-prod", dest)
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Error("web-prod is ON, so the export must report a running source")
	}
	if data, _ := os.ReadFile(dest); string(data) != "ova-bytes" {
		t.Errorf("dest = %q", data)
	}

	var create, del *recordedRequest
	for i, r := range f.requests {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/content/ovas"):
			create = &f.requests[i]
		case r.Method == http.MethodDelete:
			del = &f.requests[i]
		}
	}
	if create == nil || create.Path != "/api/vmm/v4.1/content/ovas" {
		t.Fatalf("create = %+v", create)
	}
	var body struct {
		Name   string            `json:"name"`
		Source map[string]string `json:"source"`
	}
	if err := json.Unmarshal([]byte(create.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source["$objectType"] != "vmm.v4.content.OvaVmSource" ||
		body.Source["vmExtId"] != "11111111-1111-1111-1111-111111111111" ||
		body.Source["diskFileFormat"] != "QCOW2" || !strings.HasPrefix(body.Name, "corral-export-web-prod-") {
		t.Errorf("create body = %s", create.Body)
	}
	if del == nil || del.Path != "/api/vmm/v4.1/content/ovas/ova-1" || del.IfMatch != "etag-ova-1" {
		t.Errorf("the OVA was not deleted with its ETag: %+v", del)
	}
}

func TestExportOVARefusesV40(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	_, err := c.ExportOVA(context.Background(), "web-prod", filepath.Join(t.TempDir(), "x.ova"))
	if err == nil || !strings.Contains(err.Error(), "apiVersion: v4.1") {
		t.Fatalf("err = %v, want the v4.1 remedy", err)
	}
	if len(f.requests) != 0 {
		t.Errorf("a v4.0 context still called Prism Central: %+v", f.requests)
	}
}
