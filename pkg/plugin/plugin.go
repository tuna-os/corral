// Package plugin gives Corral a krew-style extension system: plugins are
// standalone executables named `corral-<name>`, discovered in the plugin dir
// (and PATH). `corral <name> ...` dispatches to them. A curated marketplace
// (marketplace.go) lets users browse and install them. This keeps the core
// binary lean — niche features like bootc ship as plugins.
package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Plugin describes an installed extension.
type Plugin struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Installed   bool   `json:"installed"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
}

// Dir is where Corral installs plugins. Override with CORRAL_PLUGIN_DIR.
func Dir() string {
	if d := os.Getenv("CORRAL_PLUGIN_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "corral", "plugins")
}

// Installed lists plugins found in the plugin dir (executables named corral-*).
func Installed() []Plugin {
	var ps []Plugin
	entries, _ := os.ReadDir(Dir())
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "corral-") {
			continue
		}
		ps = append(ps, Plugin{
			Name:      strings.TrimPrefix(e.Name(), "corral-"),
			Path:      filepath.Join(Dir(), e.Name()),
			Installed: true,
		})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	return ps
}

// Resolve returns the executable path for a plugin name, preferring the plugin
// dir, then $PATH. Empty if not found.
func Resolve(name string) string {
	p := filepath.Join(Dir(), "corral-"+name)
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p
	}
	if p, err := exec.LookPath("corral-" + name); err == nil {
		return p
	}
	return ""
}

// IsInstalled reports whether a plugin named `name` is available.
func IsInstalled(name string) bool { return Resolve(name) != "" }

// Dispatch execs a plugin with the given args, wiring std streams through.
// CORRAL_PLUGIN=<name> is exported so plugins know how they were invoked.
func Dispatch(name string, args []string) error {
	bin := Resolve(name)
	if bin == "" {
		return fmt.Errorf("unknown command or plugin %q — try `corral plugin search %s`", name, name)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "CORRAL_PLUGIN="+name)
	return cmd.Run()
}
