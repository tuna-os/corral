package types

import "testing"

func TestPortMap(t *testing.T) {
	if PortMap["ssh"] != 22 {
		t.Errorf("ssh port should be 22, got %d", PortMap["ssh"])
	}
	if PortMap["vnc"] != 5900 {
		t.Errorf("vnc port should be 5900, got %d", PortMap["vnc"])
	}
}

func TestDefaultPorts(t *testing.T) {
	if len(DefaultPorts) < 3 {
		t.Error("should have at least 3 default ports")
	}
	// Should contain 22, 5900
	has22 := false
	has5900 := false
	for _, p := range DefaultPorts {
		if p == 22 {
			has22 = true
		}
		if p == 5900 {
			has5900 = true
		}
	}
	if !has22 {
		t.Error("default ports should include 22 (SSH)")
	}
	if !has5900 {
		t.Error("default ports should include 5900 (VNC)")
	}
}
