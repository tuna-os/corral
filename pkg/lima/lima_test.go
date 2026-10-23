package lima

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMemory(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"16GiB", "16G"},
		{"8GB", "8G"},
		{"8G", "8G"},
		{"4096", "4096M"},
		{"2048M", "2048M"},
		{"512MiB", "512M"},
	}
	for _, tc := range tests {
		got := ParseMemory(tc.input)
		if got != tc.want {
			t.Errorf("ParseMemory(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseDisk(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"60GiB", "60G"},
		{"40G", "40G"},
		{"100GB", "100G"},
	}
	for _, tc := range tests {
		got := ParseDisk(tc.input)
		if got != tc.want {
			t.Errorf("ParseDisk(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestScriptToCloudInit(t *testing.T) {
	script := `#!/bin/bash
echo "hello world"
if [ -f /etc/os-release ]; then
    cat /etc/os-release
fi`

	got := ScriptToCloudInit(script)
	if !strings.HasPrefix(got, "runcmd:\n  - |\n") {
		t.Fatalf("expected runcmd prefix, got: %s", got)
	}
	if strings.Contains(got, "#!/bin/bash") {
		t.Errorf("shebang should be stripped, but found in: %s", got)
	}
	if !strings.Contains(got, "    echo \"hello world\"") {
		t.Errorf("script body should be indented, got: %s", got)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lima.yaml")
	content := `
bootc: quay.io/fedora/fedora-bootc:40
cpus: 4
memory: 8GiB
disk: 40GiB
images:
  - location: https://example.com/fedora.iso
    arch: x86_64
provision:
  - mode: system
    script: |
      #!/bin/sh
      touch /etc/corral
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile failed: %v", err)
	}
	if spec.Bootc != "quay.io/fedora/fedora-bootc:40" {
		t.Errorf("expected bootc, got %s", spec.Bootc)
	}
	if spec.CPUs != 4 {
		t.Errorf("expected CPUs 4, got %d", spec.CPUs)
	}
	if spec.Memory != "8GiB" {
		t.Errorf("expected memory 8GiB, got %s", spec.Memory)
	}
	if spec.Disk != "40GiB" {
		t.Errorf("expected disk 40GiB, got %s", spec.Disk)
	}
	if len(spec.Images) != 1 || spec.Images[0].Location != "https://example.com/fedora.iso" {
		t.Errorf("unexpected images: %+v", spec.Images)
	}
	if len(spec.Provision) != 1 {
		t.Errorf("unexpected provision: %+v", spec.Provision)
	}

	ci := ProvisionScriptsToCloudInit(spec.Provision)
	if !strings.HasPrefix(ci, "#cloud-config\nruncmd:\n  - |\n") {
		t.Errorf("unexpected cloud init: %s", ci)
	}
}
