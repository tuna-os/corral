// Package lima parses and translates Lima VM files without backend dependencies.
package lima

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Values holds the create fields populated by a Lima file.
type Values struct {
	Mem             string
	CPU             int
	Disk            string
	ISO             string
	ContainerDisk   string
	Image           string
	Import          string
	CloudInit       string
	Bootc           string
	ProvisionScript string
}

// File is the subset of Lima YAML understood by Corral. Other fields are ignored.
type File struct {
	Bootc     string      `yaml:"bootc"`
	Images    []Image     `yaml:"images"`
	CPUs      int         `yaml:"cpus"`
	Memory    string      `yaml:"memory"`
	Disk      string      `yaml:"disk"`
	Provision []Provision `yaml:"provision"`
}

type Image struct {
	Location string `yaml:"location"`
	Arch     string `yaml:"arch"`
}

type Provision struct {
	Mode   string `yaml:"mode"`
	Script string `yaml:"script"`
}

func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var spec File
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &spec, nil
}

// ParseMemory converts Lima-formatted memory strings ("16GiB", "8G", "4096")
// to the corral format ("16G", "8G", "4096M").
func ParseMemory(s string) string {
	s = strings.TrimSuffix(s, "iB")
	s = strings.TrimSuffix(s, "B")
	// If it's just a number, treat as MiB
	if _, err := fmt.Sscanf(s, "%d", new(int)); err == nil && !strings.ContainsAny(s, "GMK") {
		return s + "M"
	}
	return s
}

// ParseDisk converts Lima-formatted disk strings ("60GiB", "40G") to
// corral format ("60G", "40G").
func ParseDisk(s string) string {
	return ParseMemory(s)
}

// ScriptToCloudInit converts a shell provisioning script to a cloud-init
// runcmd entry. The entire script runs as a single sh invocation so that
// multi-line constructs (if/fi, loops, backslash continuations) work.
func ScriptToCloudInit(script string) string {
	script = strings.TrimSpace(script)
	// Strip the shebang line — cloud-init runs via sh -c.
	if strings.HasPrefix(script, "#!") {
		idx := strings.Index(script, "\n")
		if idx >= 0 {
			script = strings.TrimSpace(script[idx+1:])
		}
	}
	if script == "" {
		return ""
	}
	// YAML literal block scalar (|) keeps newlines intact inside cloud-init.
	return "runcmd:\n  - |\n" + indentLines(script, "    ")
}

// indentLines prepends prefix to every line of s.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// Apply translates the file over existing values, preserving create flag precedence.
func (spec *File) Apply(v *Values) {
	if spec.Bootc != "" {
		v.Bootc = spec.Bootc
	}
	var rawScripts []string
	for _, p := range spec.Provision {
		if p.Script != "" {
			rawScripts = append(rawScripts, p.Script)
		}
	}
	if len(rawScripts) > 0 {
		v.ProvisionScript = strings.Join(rawScripts, "\n")
	}

	if spec.CPUs != 0 && v.CPU == 2 {
		v.CPU = spec.CPUs
	}
	if spec.Memory != "" && v.Mem == "4G" {
		v.Mem = ParseMemory(spec.Memory)
	}
	if spec.Disk != "" && v.Disk == "" {
		v.Disk = ParseDisk(spec.Disk)
	}

	// Resolve the first image as the boot source.
	if len(spec.Images) > 0 && v.ContainerDisk == "" && v.Image == "" && v.ISO == "" && v.Import == "" {
		loc := spec.Images[0].Location
		switch {
		case strings.HasSuffix(loc, ".iso") || strings.HasPrefix(loc, "https://") && strings.Contains(loc, ".iso"):
			v.ISO = loc
		case strings.HasSuffix(loc, ".qcow2") || strings.HasSuffix(loc, ".raw") || strings.HasSuffix(loc, ".img"):
			v.Import = loc
		case strings.Contains(loc, "/") && strings.Contains(loc, ":"):
			// OCI image reference (e.g. quay.io/containerdisks/fedora:44)
			v.ContainerDisk = loc
		default:
			// Bare name — try catalog lookup
			v.Image = loc
		}
	}

	// Convert provision scripts to cloud-init.
	if v.CloudInit == "" {
		var parts []string
		for _, p := range spec.Provision {
			if p.Script == "" {
				continue
			}
			ci := ScriptToCloudInit(p.Script)
			if ci != "" {
				parts = append(parts, ci)
			}
		}
		if len(parts) > 0 {
			v.CloudInit = "#cloud-config\n" + strings.Join(parts, "\n")
		}
	}
}
