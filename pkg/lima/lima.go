package lima

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is the Lima YAML format — corral reads Lima files natively.
// Only the subset of fields that map to KubeVirt/corral are parsed; the rest
// (mounts, networks, etc.) are ignored with a warning where applicable.
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

// LoadFile reads and parses a Lima YAML file from path.
func LoadFile(path string) (*File, error) {
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

// ProvisionScriptsToCloudInit converts a slice of Provision items into a combined cloud-init yaml.
func ProvisionScriptsToCloudInit(provisions []Provision) string {
	var parts []string
	for _, p := range provisions {
		if p.Script == "" {
			continue
		}
		ci := ScriptToCloudInit(p.Script)
		if ci != "" {
			parts = append(parts, ci)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "#cloud-config\n" + strings.Join(parts, "\n")
}

// indentLines prepends prefix to every line of s.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
