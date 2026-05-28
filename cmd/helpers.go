package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// registryEntry stores the backend choice per VM name.
type registryEntry struct {
	Backend   string            `json:"backend"`
	Namespace string            `json:"namespace,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

const registryDir = ".local/share/tailvm"
const qemuBaseDir = ".local/share/tailvm/vms"

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

func registryPath() string {
	return filepath.Join(homeDir(), registryDir, "registry.json")
}

func readRegistry() (map[string]registryEntry, error) {
	data, err := os.ReadFile(registryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]registryEntry), nil
		}
		return nil, err
	}
	var reg map[string]registryEntry
	if err := json.Unmarshal(data, &reg); err != nil {
		return make(map[string]registryEntry), nil
	}
	return reg, nil
}

func writeRegistry(reg map[string]registryEntry) error {
	dir := filepath.Join(homeDir(), registryDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(registryPath(), data, 0644)
}

func registryGet(name string) (registryEntry, bool) {
	reg, err := readRegistry()
	if err != nil {
		return registryEntry{}, false
	}
	e, ok := reg[name]
	return e, ok
}

func registrySet(name string, entry registryEntry) error {
	reg, err := readRegistry()
	if err != nil {
		return err
	}
	reg[name] = entry
	return writeRegistry(reg)
}

func registryRemove(name string) error {
	reg, err := readRegistry()
	if err != nil {
		return err
	}
	delete(reg, name)
	return writeRegistry(reg)
}

func resolveBackend(name string) string {
	// Check registry first
	if entry, ok := registryGet(name); ok {
		return entry.Backend
	}
	// Check if KubeVirt VM exists
	if kubectl(nil, "get", "vm", name, "-A", "-o", "name") == nil {
		return "kubevirt"
	}
	// Check if QEMU VM directory exists
	qemuDir := filepath.Join(homeDir(), qemuBaseDir, name)
	if info, err := os.Stat(qemuDir); err == nil && info.IsDir() {
		return "qemu"
	}
	return ""
}

func requireBackend(name string) (string, error) {
	backend := resolveBackend(name)
	if backend == "" {
		return "", fmt.Errorf("VM %q does not exist", name)
	}
	return backend, nil
}

func kubectl(stdin *[]byte, args ...string) error {
	cmd := exec.Command("kubectl", args...)
	if stdin != nil {
		w, _ := cmd.StdinPipe()
		go func() {
			defer w.Close()
			w.Write(*stdin)
		}()
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func kubectlOutput(stdin *[]byte, args ...string) ([]byte, error) {
	cmd := exec.Command("kubectl", args...)
	if stdin != nil {
		w, _ := cmd.StdinPipe()
		go func() {
			defer w.Close()
			w.Write(*stdin)
		}()
	}
	return cmd.Output()
}

func filepathJoin(parts ...string) string {
	return filepath.Join(parts...)
}

func splitFields(s string) []string {
	var result []string
	for _, f := range strings.Fields(s) {
		f = strings.TrimSpace(f)
		if f != "" {
			result = append(result, f)
		}
	}
	return result
}

func uniq(items []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}
