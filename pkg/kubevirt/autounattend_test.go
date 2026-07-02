package kubevirt

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestAutounattendConfigMapName(t *testing.T) {
	if got := AutounattendConfigMapName("win11"); got != "win11-autounattend" {
		t.Errorf("got %q, want win11-autounattend", got)
	}
}

func TestGenerateAutounattendConfigMap_Shape(t *testing.T) {
	cm := GenerateAutounattendConfigMap("win11", "corral-vms", "<xml/>")
	if cm["kind"] != "ConfigMap" {
		t.Errorf("kind = %v, want ConfigMap", cm["kind"])
	}
	meta := cm["metadata"].(map[string]any)
	if meta["name"] != "win11-autounattend" || meta["namespace"] != "corral-vms" {
		t.Errorf("metadata = %v", meta)
	}
	data := cm["data"].(map[string]any)
	if data["autounattend.xml"] != "<xml/>" {
		t.Errorf("data[autounattend.xml] = %v", data["autounattend.xml"])
	}
}

func TestAutounattendXML_WellFormed(t *testing.T) {
	xmlStr := AutounattendXML("win11", "P@ssw0rd123!")
	var v any
	if err := xml.Unmarshal([]byte(xmlStr), &v); err != nil {
		t.Fatalf("generated autounattend.xml is not well-formed: %v", err)
	}
}

func TestAutounattendXML_ContainsKeySettings(t *testing.T) {
	xmlStr := AutounattendXML("mydesktop", "S3cret!Pass9")
	for _, want := range []string{
		"<ComputerName>mydesktop</ComputerName>",
		"S3cret!Pass9",
		"<AcceptEula>true</AcceptEula>",
		"<fDenyTSConnections>false</fDenyTSConnections>", // RDP enabled
		"<WillWipeDisk>true</WillWipeDisk>",
		"Administrator",
		"<Enabled>true</Enabled>", // autologon
	} {
		if !strings.Contains(xmlStr, want) {
			t.Errorf("autounattend.xml missing %q", want)
		}
	}
}

func TestRandomWindowsPassword_MeetsComplexity(t *testing.T) {
	pw := randomWindowsPassword()
	if len(pw) < 8 {
		t.Fatalf("password too short: %q", pw)
	}
	var hasLower, hasUpper, hasDigit, hasSpecial bool
	for _, c := range pw {
		switch {
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= '0' && c <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	if !hasLower || !hasUpper || !hasDigit || !hasSpecial {
		t.Errorf("password %q doesn't meet complexity (lower=%v upper=%v digit=%v special=%v)",
			pw, hasLower, hasUpper, hasDigit, hasSpecial)
	}
}

func TestRandomWindowsPassword_Uniqueness(t *testing.T) {
	if randomWindowsPassword() == randomWindowsPassword() {
		t.Error("expected two consecutive calls to produce different passwords")
	}
}
