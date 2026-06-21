package web

import (
	"net/http"
	"testing"
)

// When qcow2 is requested but qemu-img isn't on PATH, the export degrades to a
// clear 501 (Not Implemented) rather than a confusing 500 — and the default
// raw.gz path is unaffected.
func TestHandleExport_Qcow2_DegradesWithoutQemuImg(t *testing.T) {
	t.Setenv("PATH", "") // hide qemu-img from exec.LookPath
	fx := NewTestFixture()
	defer fx.Server.Close()

	// `kubectl get vmi` returns no response from the fake → errors → the handler
	// treats the VM as stopped and proceeds to the export path.
	resp, err := http.Get(fx.Server.URL + "/api/vms/tailvm/testvm/export?format=qcow2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("qcow2 without qemu-img: got %d, want 501", resp.StatusCode)
	}
}
