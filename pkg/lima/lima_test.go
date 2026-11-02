package lima

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm.yaml")
	data := `cpus: 6
memory: 16GiB
disk: 60GiB
images:
  - location: https://example.test/disk.qcow2
  - location: ignored.iso
mounts:
  - location: /ignored
provision:
  - mode: system
    script: |
      #!/bin/sh
      if true; then
        echo ready
      fi
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	spec, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	v := Values{CPU: 2, Mem: "4G"}
	spec.Apply(&v)
	if v.CPU != 6 || v.Mem != "16G" || v.Disk != "60G" || v.Import != "https://example.test/disk.qcow2" {
		t.Fatalf("translation: %+v", v)
	}
	want := "#cloud-config\nruncmd:\n  - |\n    if true; then\n      echo ready\n    fi"
	if v.CloudInit != want {
		t.Fatalf("cloud-init = %q, want %q", v.CloudInit, want)
	}
	if !strings.HasPrefix(v.ProvisionScript, "#!/bin/sh\n") {
		t.Fatalf("raw provision script lost: %q", v.ProvisionScript)
	}
}

func TestApplyPreservesOverrides(t *testing.T) {
	spec := File{CPUs: 6, Memory: "16GiB", Disk: "60GiB", Images: []Image{{Location: "file.iso"}}, Bootc: "file-image", Provision: []Provision{{Script: "echo file"}}}
	v := Values{CPU: 8, Mem: "32G", Disk: "100G", Image: "chosen", CloudInit: "custom", Bootc: "flag-image"}
	spec.Apply(&v)
	if v.CPU != 8 || v.Mem != "32G" || v.Disk != "100G" || v.Image != "chosen" || v.ISO != "" || v.CloudInit != "custom" {
		t.Fatalf("overrides lost: %+v", v)
	}
	// bootc and the raw provision script historically always come from the file.
	if v.Bootc != "file-image" || v.ProvisionScript != "echo file" {
		t.Fatalf("file precedence changed: %+v", v)
	}
}

func TestImageTranslation(t *testing.T) {
	for _, tt := range []struct{ location, kind string }{
		{"install.iso", "iso"}, {"https://example.test/install.iso?download=1", "iso"},
		{"disk.qcow2", "import"}, {"disk.raw", "import"}, {"disk.img", "import"},
		{"quay.io/containerdisks/fedora:44", "container"}, {"fedora", "catalog"},
	} {
		t.Run(tt.location, func(t *testing.T) {
			var v Values
			(&File{Images: []Image{{Location: tt.location}}}).Apply(&v)
			got := map[string]string{"iso": v.ISO, "import": v.Import, "container": v.ContainerDisk, "catalog": v.Image}[tt.kind]
			if got != tt.location {
				t.Fatalf("source translation: %+v", v)
			}
		})
	}
}

func TestQuantities(t *testing.T) {
	for input, want := range map[string]string{"16GiB": "16G", "8G": "8G", "4096": "4096M", "512MiB": "512M", "60GB": "60G", "": ""} {
		if got := ParseMemory(input); got != want {
			t.Errorf("memory %q = %q, want %q", input, got, want)
		}
		if got := ParseDisk(input); got != want {
			t.Errorf("disk %q = %q, want %q", input, got, want)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "reading "+path) {
		t.Fatalf("read error: %v", err)
	}
	if err := os.WriteFile(path, []byte("images: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "parsing "+path) {
		t.Fatalf("parse error: %v", err)
	}
}

func TestProvisioningTranslation(t *testing.T) {
	if got := ScriptToCloudInit(" \n\t"); got != "" {
		t.Fatalf("empty script = %q", got)
	}
	spec := File{Provision: []Provision{{Script: ""}, {Script: "echo one"}, {Script: "echo two"}}}
	var v Values
	spec.Apply(&v)
	if v.ProvisionScript != "echo one\necho two" || v.CloudInit != "#cloud-config\nruncmd:\n  - |\n    echo one\nruncmd:\n  - |\n    echo two" {
		t.Fatalf("multiple scripts changed: %+v", v)
	}
}
