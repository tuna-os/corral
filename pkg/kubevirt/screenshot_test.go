package kubevirt

import (
	"strings"
	"testing"
)

func TestClient_Screenshot(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"vnc", "screenshot", "testvm", "-n", "tailvm", "-f", "shot.png"}, "", nil)
	if err := c.Screenshot("testvm", "shot.png"); err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
}

func TestClient_Screenshot_ErrorCarriesVirtctlOutput(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"vnc", "screenshot", "testvm", "-n", "tailvm", "-f", "shot.png"},
		"VMI testvm is not running", errSimulated)
	err := c.Screenshot("testvm", "shot.png")
	if err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("Screenshot error = %v, want virtctl's message", err)
	}
}
