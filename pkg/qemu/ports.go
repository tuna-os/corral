package qemu

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Loopback is the only bind address the one-line launcher uses for its VNC
// console, SSH forward and web UI. Tailscale binding stays available to the
// regular `corral create` path; the launcher never widens beyond loopback.
const Loopback = "127.0.0.1"

// PortAlloc is one exclusive VNC/SSH assignment.
type PortAlloc struct {
	Display  int
	VNCPort  int
	SSHPort  int
	Loopback bool
}

// portProbe binds to decide whether a candidate port is free. Sealed for
// tests: the default dials the real loopback interface.
var portProbe = func(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", Loopback, port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// SetPortProbe overrides the free-port check (for tests). Nil restores it.
func SetPortProbe(f func(port int) bool) {
	if f == nil {
		portProbe = func(port int) bool {
			ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", Loopback, port))
			if err != nil {
				return false
			}
			_ = ln.Close()
			return true
		}
		return
	}
	portProbe = f
}

// usedPorts collects VNC/SSH ports already recorded in VM metadata so a new
// VM never collides with an existing one. Unreadable entries are skipped —
// the bind probe below is the authority on what is actually free.
func usedPorts() map[int]bool {
	used := map[int]bool{}
	entries, err := os.ReadDir(VMHome())
	if err != nil {
		return used
	}
	for _, e := range entries {
		// cache/ and locks/ are home-level namespaces, never VMs: even
		// a hand-planted metadata.json inside them must not squat ports.
		if !e.IsDir() || e.Name() == "cache" || e.Name() == "locks" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(VMHome(), e.Name(), "metadata.json"))
		if err != nil {
			continue
		}
		var meta struct {
			VncPort int `json:"vnc_port"`
			SSHPort int `json:"ssh_port"`
		}
		if json.Unmarshal(data, &meta) != nil {
			continue
		}
		if meta.VncPort != 0 {
			used[meta.VncPort] = true
		}
		if meta.SSHPort != 0 {
			used[meta.SSHPort] = true
		}
	}
	return used
}

// AllocatePorts assigns a VNC display + VNC/SSH port pair. The name hash is
// the starting point (stable across recreates), but unlike the legacy
// hashDisplay path the result is checked against every recorded VM and
// against the live loopback interface — a collision moves to the next
// display instead of preventing launch. All 100 displays exhausted is an
// error, never a silent reuse.
//
// As a standalone call this is advisory: each probe bind is released before
// the next, so two concurrent callers can be handed one pair. Exclusivity
// holds inside Create, which runs allocation under the VM-home creation
// lock — that is the only path that records the allocation.
func AllocatePorts(name string) (PortAlloc, error) {
	used := usedPorts()
	start := hashDisplay(name)
	for i := 0; i < 100; i++ {
		display := (start + i) % 100
		vncPort := 5900 + display
		sshPort := 2200 + display
		if used[vncPort] || used[sshPort] {
			continue
		}
		if !portProbe(vncPort) || !portProbe(sshPort) {
			continue
		}
		return PortAlloc{Display: display, VNCPort: vncPort, SSHPort: sshPort}, nil
	}
	return PortAlloc{}, fmt.Errorf("no free VNC/SSH port pair for %q (all 100 displays in use)", name)
}

// ConsoleBindAddr is the address QEMU binds its VNC server and SSH hostfwd
// to. Loopback keeps the one-line launcher local-only; the regular path
// keeps the tailscale-or-loopback fallback it already had.
func ConsoleBindAddr(loopback bool, tailscaleIP string) string {
	if loopback {
		return Loopback
	}
	if tailscaleIP != "" {
		return tailscaleIP
	}
	return Loopback
}

// LoopbackVNCAddr returns the loopback VNC endpoint for a VM whose metadata
// records a VNC port, regardless of which address QEMU originally bound.
// Used by the launcher's readiness probe, which always dials loopback.
func LoopbackVNCAddr(name string) (string, error) {
	meta, err := readMetadata(name)
	if err != nil {
		return "", err
	}
	if meta.VncPort == 0 {
		return "", fmt.Errorf("VM %q has no VNC port", name)
	}
	return fmt.Sprintf("%s:%d", Loopback, meta.VncPort), nil
}
