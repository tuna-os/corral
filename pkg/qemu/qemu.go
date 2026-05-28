package qemu

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hanthor/tailvm-go/pkg/types"
)

// VMHome returns the QEMU VM directory.
func VMHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "tailvm", "vms")
}

// systemdUserDir returns the systemd user unit directory.
func systemdUserDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

// List returns all QEMU VMs.
func List() ([]types.VM, error) {
	dir := VMHome()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // no VMs yet
	}

	var vms []types.VM
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "cache" {
			continue
		}
		metaFile := filepath.Join(dir, e.Name(), "metadata.json")
		data, err := os.ReadFile(metaFile)
		if err != nil {
			continue
		}
		var meta struct {
			Name      string `json:"name"`
			CPU       int    `json:"cpu"`
			Memory    string `json:"memory"`
			Disk      string `json:"disk_size"`
			VncPort   int    `json:"vnc_port"`
			Tailscale string `json:"tailscale_ip"`
		}
		if json.Unmarshal(data, &meta) != nil {
			continue
		}

		svc := "tailvm-" + e.Name()
		out, _ := exec.Command("systemctl", "--user", "is-active", svc).Output()
		running := strings.TrimSpace(string(out)) == "active"

		status := "○ Stopped"
		if running {
			status = "● Running"
		}

		vms = append(vms, types.VM{
			Name:    meta.Name,
			Backend: "qemu",
			Status:  status,
			Ready:   running,
			Running: running,
			CPU:     meta.CPU,
			Mem:     meta.Memory,
			Disk:    meta.Disk,
			VNC:     fmt.Sprintf("%d", meta.VncPort),
			IP:      meta.Tailscale,
		})
	}
	return vms, nil
}

// Exists checks if a QEMU VM exists.
func Exists(name string) bool {
	info, err := os.Stat(filepath.Join(VMHome(), name))
	return err == nil && info.IsDir()
}

// Create creates a new QEMU VM.
func Create(opts types.CreateOpts) error {
	name := opts.Name
	vmDir := filepath.Join(VMHome(), name)

	if Exists(name) && !opts.Force {
		return fmt.Errorf("VM %q already exists. Use --force to overwrite", name)
	}

	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("creating VM directory: %w", err)
	}

	// Find QEMU binaries
	qemuPath, qemuImgPath, err := findQEMU()
	if err != nil {
		return err
	}

	mem := opts.Mem
	if mem == "" {
		mem = "4G"
	}
	cpu := opts.CPU
	if cpu == 0 {
		cpu = 2
	}
	diskSize := opts.Disk
	if diskSize == "" {
		diskSize = "20G"
	}

	// Create disk
	diskPath := filepath.Join(vmDir, "disk.qcow2")
	createDisk := exec.Command(qemuImgPath, "create", "-f", "qcow2", diskPath, diskSize)
	if out, err := createDisk.CombinedOutput(); err != nil {
		return fmt.Errorf("creating disk: %s: %w", string(out), err)
	}

	// Resolve ISO
	var isoPath string
	var hasISO bool
	if opts.ISO != "" {
		isoPath = opts.ISO
		hasISO = true
	}

	// VNC port — use hash of name for stability
	vncDisplay := hashDisplay(name)
	vncPort := 5900 + vncDisplay

	// Tailscale IP
	tailscaleIP, err := tailscaleIPv4()
	if err != nil {
		tailscaleIP = "127.0.0.1"
	}

	// Systemd unit
	unit := generateUnit(generateUnitOpts{
		Name:        name,
		QemuPath:    qemuPath,
		Mem:         mem,
		CPU:         cpu,
		DiskPath:    diskPath,
		ISOPath:     isoPath,
		HasISO:      hasISO,
		TailscaleIP: tailscaleIP,
		VncDisplay:  vncDisplay,
	})
	unitPath := filepath.Join(systemdUserDir(), "tailvm-"+name+".service")
	if err := os.MkdirAll(systemdUserDir(), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("writing unit file: %w", err)
	}

	// Reload systemd
	exec.Command("systemctl", "--user", "daemon-reload").Run()

	// Save metadata
	meta := map[string]any{
		"name":         name,
		"cpu":          cpu,
		"memory":       mem,
		"disk_size":    diskSize,
		"vnc_port":     vncPort,
		"vnc_display":  vncDisplay,
		"tailscale_ip": tailscaleIP,
		"iso":          isoPath,
		"has_iso":      hasISO,
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(vmDir, "metadata.json"), data, 0644)

	fmt.Fprintf(os.Stderr, "VM %q created.\n", name)
	fmt.Fprintf(os.Stderr, "  Start:   tailvm start %s\n", name)
	fmt.Fprintf(os.Stderr, "  VNC:     vnc://%s:%d\n", tailscaleIP, vncPort)
	return nil
}

// Start starts a QEMU VM via systemd.
func Start(name string) error {
	svc := "tailvm-" + name
	unitPath := filepath.Join(systemdUserDir(), svc+".service")
	if _, err := os.Stat(unitPath); err != nil {
		return fmt.Errorf("VM %q does not exist", name)
	}

	out, _ := exec.Command("systemctl", "--user", "is-active", svc).Output()
	if strings.TrimSpace(string(out)) == "active" {
		fmt.Fprintf(os.Stderr, "VM %q is already running.\n", name)
		return nil
	}

	cmd := exec.Command("systemctl", "--user", "start", svc)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("starting VM: %w", err)
	}

	fmt.Fprintf(os.Stderr, "VM %q started.\n", name)

	// Show VNC info
	metaFile := filepath.Join(VMHome(), name, "metadata.json")
	if data, err := os.ReadFile(metaFile); err == nil {
		var meta struct {
			VncPort    int    `json:"vnc_port"`
			Tailscale  string `json:"tailscale_ip"`
		}
		if json.Unmarshal(data, &meta) == nil {
			fmt.Fprintf(os.Stderr, "  VNC: vnc://%s:%d\n", meta.Tailscale, meta.VncPort)
		}
	}
	return nil
}

// Stop stops a QEMU VM.
func Stop(name string) error {
	svc := "tailvm-" + name
	cmd := exec.Command("systemctl", "--user", "stop", svc)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("stopping VM: %w", err)
	}
	fmt.Fprintf(os.Stderr, "VM %q stopped.\n", name)
	return nil
}

// Delete removes a QEMU VM and its files.
func Delete(name string) error {
	svc := "tailvm-" + name
	exec.Command("systemctl", "--user", "stop", svc).Run()
	exec.Command("systemctl", "--user", "disable", svc).Run()

	unitPath := filepath.Join(systemdUserDir(), svc+".service")
	os.Remove(unitPath)
	exec.Command("systemctl", "--user", "daemon-reload").Run()

	vmDir := filepath.Join(VMHome(), name)
	os.RemoveAll(vmDir)

	fmt.Fprintf(os.Stderr, "VM %q deleted.\n", name)
	return nil
}

// Info returns VM metadata.
func Info(name string) ([]byte, error) {
	metaFile := filepath.Join(VMHome(), name, "metadata.json")
	return os.ReadFile(metaFile)
}

// Viewer launches VNC viewer.
func Viewer(name string) error {
	metaFile := filepath.Join(VMHome(), name, "metadata.json")
	data, err := os.ReadFile(metaFile)
	if err != nil {
		return fmt.Errorf("VM %q not found", name)
	}
	var meta struct {
		VncPort   int    `json:"vnc_port"`
		Tailscale string `json:"tailscale_ip"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return fmt.Errorf("invalid metadata")
	}

	vncURL := fmt.Sprintf("vnc://%s:%d", meta.Tailscale, meta.VncPort)

	xdg, _ := exec.LookPath("xdg-open")
	if xdg != "" {
		exec.Command(xdg, vncURL).Start()
		fmt.Fprintf(os.Stderr, "VNC viewer launched: %s\n", vncURL)
		return nil
	}

	// Fallback: flatpak virt-viewer
	flatpak, _ := exec.LookPath("flatpak")
	if flatpak != "" {
		exec.Command(flatpak, "run", "org.virt_manager.virt-viewer", vncURL).Start()
		fmt.Fprintf(os.Stderr, "VNC viewer launched: %s\n", vncURL)
		return nil
	}

	fmt.Fprintf(os.Stderr, "Open VNC manually: %s\n", vncURL)
	return nil
}

// Logs tails the systemd journal for a VM.
func Logs(name string) error {
	svc := "tailvm-" + name
	cmd := exec.Command("journalctl", "--user", "-u", svc, "-n", "50", "-f")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func findQEMU() (qemu, qemuImg string, err error) {
	// Try Homebrew paths first
	for _, base := range []string{
		"/home/linuxbrew/.linuxbrew/bin",
		"/usr/bin",
		"/usr/local/bin",
	} {
		qemuPath := filepath.Join(base, "qemu-system-x86_64")
		qemuImgPath := filepath.Join(base, "qemu-img")
		if _, e := os.Stat(qemuPath); e == nil {
			if _, e := os.Stat(qemuImgPath); e == nil {
				return qemuPath, qemuImgPath, nil
			}
		}
	}
	return "", "", fmt.Errorf("QEMU not found. Install: brew install qemu")
}

func tailscaleIPv4() (string, error) {
	out, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func hashDisplay(name string) int {
	h := 0
	for _, c := range name {
		h = (h*31 + int(c)) % 100
	}
	return h
}

type generateUnitOpts struct {
	Name, QemuPath, Mem, DiskPath, ISOPath, TailscaleIP string
	CPU                                                  int
	HasISO                                               bool
	VncDisplay                                           int
}

func generateUnit(opts generateUnitOpts) string {
	isoPart := ""
	if opts.HasISO && opts.ISOPath != "" {
		isoPart = fmt.Sprintf(" -cdrom %s -boot once=d,menu=on", opts.ISOPath)
	}

	mem := opts.Mem
	if !strings.HasSuffix(mem, "M") && !strings.HasSuffix(mem, "G") {
		mem += "G"
	}

	return fmt.Sprintf(`[Unit]
Description=TailVM: %s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s \
  -name %s \
  -m %s \
  -cpu host \
  -smp %d \
  -machine q35,accel=kvm \
  -drive file=%s,if=virtio,format=qcow2 \
  -vnc %s:%d \
  -vga virtio \
  -display none \
  -netdev user,id=net0 \
  -device virtio-net-pci,netdev=net0 \
  -device virtio-rng-pci%s
Restart=no
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`, opts.Name, opts.QemuPath, opts.Name, mem, opts.CPU,
		opts.DiskPath, opts.TailscaleIP, opts.VncDisplay, isoPart)
}
