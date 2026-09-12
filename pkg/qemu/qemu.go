package qemu

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// VMHome returns the QEMU VM directory.
func VMHome() string {
	if vmHomeOverride != "" {
		return vmHomeOverride
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "corral", "vms")
}

// State-dir + systemctl seams: demo mode (and tests) point the backend at
// throwaway directories and an in-memory service layer, so the full local
// lifecycle works without touching the host's real state (#91 Phase 4).
var (
	vmHomeOverride  string
	unitDirOverride string
	realSystemctl   = func(args ...string) ([]byte, error) {
		return exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	}
	systemctlRun = realSystemctl
)

// SetStateDirs overrides where VM state and systemd units live ("" = default).
func SetStateDirs(vmHome, unitDir string) { vmHomeOverride, unitDirOverride = vmHome, unitDir }

// SetSystemctl overrides the systemd --user command runner. Passing nil
// restores the real one: a test that left it nil used to be harmless and is
// not any more, since the lifecycle now asks systemd whether it is even there.
func SetSystemctl(f func(args ...string) ([]byte, error)) {
	if f == nil {
		systemctlRun = realSystemctl
		return
	}
	systemctlRun = f
}

// journalRun reads the journal. Its own seam rather than systemctlRun's,
// because it is a different binary with different arguments and tests script
// the two independently.
var realJournalctl = func(args ...string) ([]byte, error) {
	return exec.Command("journalctl", args...).CombinedOutput()
}

var journalRun = realJournalctl

// SetJournalctl overrides the journal reader (for tests). Passing nil restores
// the real one.
func SetJournalctl(f func(args ...string) ([]byte, error)) {
	if f == nil {
		journalRun = realJournalctl
		return
	}
	journalRun = f
}

// systemdUserDir returns the systemd user unit directory.
func systemdUserDir() string {
	if unitDirOverride != "" {
		return unitDirOverride
	}
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

		running := IsRunning(e.Name())

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

	// Create disk. Three sources, in precedence order:
	//   ExistingDisk — a prepared disk.qcow2 already sits in the VM dir
	//                  (bootc-built); booting it as-is is the whole point,
	//                  so never touch it.
	//   QCOW         — copy a template image and grow it to diskSize.
	//   default      — fresh empty disk.
	diskPath := filepath.Join(vmDir, "disk.qcow2")
	switch {
	case opts.ExistingDisk:
		if _, err := os.Stat(diskPath); err != nil {
			return fmt.Errorf("ExistingDisk set but %s is missing: %w", diskPath, err)
		}
	case opts.QCOW != "":
		if out, err := exec.Command(qemuImgPath, "convert", "-O", "qcow2", opts.QCOW, diskPath).CombinedOutput(); err != nil {
			return fmt.Errorf("copying template %s: %s: %w", opts.QCOW, string(out), err)
		}
		if opts.Disk != "" {
			// Grow to the requested size (shrinking is refused by qemu-img).
			if out, err := exec.Command(qemuImgPath, "resize", diskPath, diskSize).CombinedOutput(); err != nil {
				return fmt.Errorf("resizing disk to %s: %s: %w", diskSize, string(out), err)
			}
		}
	default:
		if out, err := exec.Command(qemuImgPath, "create", "-f", "qcow2", diskPath, diskSize).CombinedOutput(); err != nil {
			return fmt.Errorf("creating disk: %s: %w", string(out), err)
		}
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
	sshPort := 2200 + vncDisplay // host port forwarded to guest :22

	// Tailscale IP
	tailscaleIP, err := tailscaleIPv4()
	if err != nil {
		tailscaleIP = "127.0.0.1"
	}

	// QMP monitor socket — lets `corral screenshot` (and anything else that
	// wants programmatic monitor access) talk to the VM without a VNC client.
	qmpSocket := filepath.Join(vmDir, "qmp.sock")

	// Serial console log. The guest's ttyS0 is written to a file next to the
	// VM's other state, so a boot that never reaches SSH still leaves evidence
	// — the panic, the dracut emergency shell, the failed unit. `-display
	// none` means nobody is watching the console otherwise, and a CI job that
	// only reports "SSH never answered" wastes the run.
	//
	// The guest also needs a console=ttyS0 kernel argument to write there;
	// pkg/vmtest passes one at install time.
	serialLog := filepath.Join(vmDir, "serial.log")

	// Systemd unit
	unitOpts := generateUnitOpts{
		Name:        name,
		QemuPath:    qemuPath,
		Mem:         mem,
		CPU:         cpu,
		DiskPath:    diskPath,
		ISOPath:     isoPath,
		HasISO:      hasISO,
		TailscaleIP: tailscaleIP,
		VncDisplay:  vncDisplay,
		SSHPort:     sshPort,
		QMPSocket:   qmpSocket,
		SerialLog:   serialLog,
	}
	unit := generateUnit(unitOpts)

	// Recorded next to the VM's state, so it can be started without a systemd
	// user session — a CI runner has none (see direct.go).
	if err := writeLaunchCommand(vmDir, qemuPath, qemuArgs(unitOpts)); err != nil {
		return fmt.Errorf("recording the QEMU command: %w", err)
	}

	unitPath := filepath.Join(systemdUserDir(), "corral-"+name+".service")
	if err := os.MkdirAll(systemdUserDir(), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("writing unit file: %w", err)
	}

	// Reload systemd
	systemctlRun("daemon-reload")

	// Save metadata
	meta := map[string]any{
		"name":         name,
		"cpu":          cpu,
		"memory":       mem,
		"disk_size":    diskSize,
		"vnc_port":     vncPort,
		"vnc_display":  vncDisplay,
		"ssh_port":     sshPort,
		"tailscale_ip": tailscaleIP,
		"iso":          isoPath,
		"has_iso":      hasISO,
		"serial_log":   serialLog,
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(vmDir, "metadata.json"), data, 0644)

	fmt.Fprintf(os.Stderr, "VM %q created.\n", name)
	fmt.Fprintf(os.Stderr, "  Start:   corral start %s\n", name)
	fmt.Fprintf(os.Stderr, "  VNC:     vnc://%s:%d\n", tailscaleIP, vncPort)
	fmt.Fprintf(os.Stderr, "  SSH:     corral ssh %s  (forwarded %s:%d → guest :22)\n", name, tailscaleIP, sshPort)
	return nil
}

// Start starts a QEMU VM: through systemd where there is a user session, and
// as a detached process where there is not.
func Start(name string) error {
	if !userSystemd() {
		// No unit can run here, so the VM's own state directory is what says
		// whether it exists.
		if !Exists(name) {
			return fmt.Errorf("VM %q does not exist", name)
		}
		return startDirect(name)
	}
	svc := "corral-" + name
	unitPath := filepath.Join(systemdUserDir(), svc+".service")
	if _, err := os.Stat(unitPath); err != nil {
		return fmt.Errorf("VM %q does not exist", name)
	}

	out, _ := systemctlRun("is-active", svc)
	if strings.TrimSpace(string(out)) == "active" {
		fmt.Fprintf(os.Stderr, "VM %q is already running.\n", name)
		return nil
	}

	if out, err := systemctlRun("start", svc); err != nil {
		return fmt.Errorf("starting VM: %w: %s", err, strings.TrimSpace(string(out)))
	}

	fmt.Fprintf(os.Stderr, "VM %q started.\n", name)

	// Show VNC info
	if meta, err := readMetadata(name); err == nil {
		fmt.Fprintf(os.Stderr, "  VNC: vnc://%s:%d\n", meta.Tailscale, meta.VncPort)
	}
	return nil
}

// Stop stops a QEMU VM, whichever way it was started.
func Stop(name string) error {
	if _, alive := directPID(name); alive || !userSystemd() {
		if err := stopDirect(name); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "VM %q stopped.\n", name)
		return nil
	}
	svc := "corral-" + name
	if out, err := systemctlRun("stop", svc); err != nil {
		return fmt.Errorf("stopping VM: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(os.Stderr, "VM %q stopped.\n", name)
	return nil
}

// Delete removes a QEMU VM and its files.
func Delete(name string) error {
	// A directly started VM has no unit to stop, and leaving the process
	// running while its disk is deleted is the worst of both.
	_ = stopDirect(name)
	svc := "corral-" + name
	systemctlRun("stop", svc)
	systemctlRun("disable", svc)

	unitPath := filepath.Join(systemdUserDir(), svc+".service")
	os.Remove(unitPath)
	systemctlRun("daemon-reload")

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

// SSH opens an SSH session to a QEMU VM. The guest's port 22 is forwarded
// to a host port (bound on the host's Tailscale IP) via QEMU user networking.
// localForwards are raw ssh -L specs ([bind_address:]port:host:hostport),
// passed straight through to the ssh client.
func SSH(name, username, identityFile, command string, port int, password string, localForwards []string) error {
	meta, err := readMetadata(name)
	if err != nil {
		return fmt.Errorf("VM %q not found: %w", name, err)
	}

	host := meta.Tailscale
	if host == "" || host == "127.0.0.1" {
		return fmt.Errorf("VM %q has no Tailscale IP — is Tailscale running?", name)
	}

	// Default to the forwarded SSH port; -p overrides
	if port == 22 || port == 0 {
		if meta.SSHPort == 0 {
			return fmt.Errorf("VM %q has no forwarded SSH port — recreate it with this corral version", name)
		}
		port = meta.SSHPort
	}

	sshBin, _ := exec.LookPath("ssh")
	if sshBin == "" {
		return fmt.Errorf("ssh not found in PATH")
	}

	args := sshArgs(username, identityFile, command, port, host, localForwards)

	if password != "" {
		return shell.RunWithSSHPass(password, sshBin, args...)
	}

	cmd := exec.Command(sshBin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// sshArgs builds the ssh(1) argv SSH execs, factored out for unit testing —
// the exec itself is a real interactive process, not mockable.
func sshArgs(username, identityFile, command string, port int, host string, localForwards []string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-p", fmt.Sprintf("%d", port),
	}
	if identityFile != "" {
		args = append(args, "-i", identityFile)
	}
	for _, fwd := range localForwards {
		args = append(args, "-L", fwd)
	}
	args = append(args, fmt.Sprintf("%s@%s", username, host))
	if command != "" {
		args = append(args, command)
	}
	return args
}

// WaitSSH polls the VM's forwarded SSH port until a non-interactive login
// succeeds or timeout elapses. Unlike SSH() it tolerates the 127.0.0.1
// fallback host — CI runners have no tailnet, and the hostfwd is bound to
// loopback there, which is exactly where we probe. Returns nil as soon as
// `ssh user@host true` exits 0.
func WaitSSH(name, username string, timeout time.Duration) error {
	meta, err := readMetadata(name)
	if err != nil {
		return fmt.Errorf("VM %q not found: %w", name, err)
	}
	if meta.SSHPort == 0 {
		return fmt.Errorf("VM %q has no forwarded SSH port — recreate it with this corral version", name)
	}
	host := meta.Tailscale
	if host == "" {
		host = "127.0.0.1"
	}
	sshBin, _ := exec.LookPath("ssh")
	if sshBin == "" {
		return fmt.Errorf("ssh not found in PATH")
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		probe := exec.Command(sshBin,
			"-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=5",
			"-p", fmt.Sprintf("%d", meta.SSHPort),
			fmt.Sprintf("%s@%s", username, host),
			"true")
		if out, err := probe.CombinedOutput(); err == nil {
			fmt.Fprintf(os.Stderr, "VM %q is reachable over SSH (%s@%s:%d).\n", name, username, host, meta.SSHPort)
			return nil
		} else {
			lastErr = fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("VM %q not SSH-reachable within %s (last error: %v)", name, timeout, lastErr)
}

// vmMetadata is the on-disk metadata.json schema.
type vmMetadata struct {
	Name      string `json:"name"`
	CPU       int    `json:"cpu"`
	Memory    string `json:"memory"`
	Disk      string `json:"disk_size"`
	VncPort   int    `json:"vnc_port"`
	SSHPort   int    `json:"ssh_port"`
	Tailscale string `json:"tailscale_ip"`
	SerialLog string `json:"serial_log"`
}

// readMetadata parses the VM metadata.json file.
func readMetadata(name string) (vmMetadata, error) {
	var meta vmMetadata
	data, err := os.ReadFile(filepath.Join(VMHome(), name, "metadata.json"))
	if err != nil {
		return meta, err
	}
	if json.Unmarshal(data, &meta) != nil {
		return meta, fmt.Errorf("invalid metadata")
	}
	return meta, nil
}

// VNCAddr returns the host:port a VM's VNC server listens on (the host's
// Tailscale IP — QEMU binds there, never 0.0.0.0). Used by the web UI's
// websocket bridge to serve a local VM's console in the browser (#91).
func VNCAddr(name string) (string, error) {
	meta, err := readMetadata(name)
	if err != nil {
		return "", err
	}
	if meta.VncPort == 0 {
		return "", fmt.Errorf("VM %q has no VNC port", name)
	}
	host := meta.Tailscale
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", host, meta.VncPort), nil
}

// Available reports whether this host can run local VMs (QEMU binaries
// found). The web UI gates its "create on this host" target on it (#91).
func Available() bool {
	_, _, err := findQEMU()
	return err == nil
}

// Viewer launches VNC viewer.
func Viewer(name string) error {
	meta, err := readMetadata(name)
	if err != nil {
		return err
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
	svc := "corral-" + name
	cmd := exec.Command("journalctl", "--user", "-u", svc, "-n", "50", "-f")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func findQEMU() (qemu, qemuImg string, err error) {
	// PATH first — covers any install location (and lets tests inject fakes).
	if q, e1 := exec.LookPath("qemu-system-x86_64"); e1 == nil {
		if qi, e2 := exec.LookPath("qemu-img"); e2 == nil {
			return q, qi, nil
		}
	}
	// Fall back to known install locations not always on PATH.
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
	CPU                                                 int
	HasISO                                              bool
	VncDisplay                                          int
	SSHPort                                             int
	QMPSocket                                           string
	SerialLog                                           string
}

// qemuArgs is the VM's argv, one place. The systemd unit renders it into
// ExecStart and the direct launcher execs it, so a VM started either way is
// the same VM — a second copy of this list would drift the day someone adds a
// device to one of them.
func qemuArgs(opts generateUnitOpts) []string {
	mem := opts.Mem
	if !strings.HasSuffix(mem, "M") && !strings.HasSuffix(mem, "G") {
		mem += "G"
	}

	args := []string{"-name", opts.Name, "-m", mem}
	args = append(args, accelArgs(opts.CPU)...)
	args = append(args,
		"-drive", fmt.Sprintf("file=%s,if=virtio,format=qcow2", opts.DiskPath),
		"-vnc", fmt.Sprintf("%s:%d", opts.TailscaleIP, opts.VncDisplay),
		"-vga", "virtio",
		"-display", "none",
	)

	netdev := "user,id=net0"
	if opts.SSHPort != 0 {
		netdev += fmt.Sprintf(",hostfwd=tcp:%s:%d-:22", opts.TailscaleIP, opts.SSHPort)
	}
	args = append(args,
		"-netdev", netdev,
		"-device", "virtio-net-pci,netdev=net0",
		"-device", "virtio-rng-pci",
	)

	if opts.HasISO && opts.ISOPath != "" {
		args = append(args, "-cdrom", opts.ISOPath, "-boot", "once=d,menu=on")
	}
	if opts.QMPSocket != "" {
		// server,nowait: QEMU listens and accepts connect/disconnect any
		// number of times over the VM's life, rather than requiring a client
		// at startup.
		args = append(args, "-qmp", "unix:"+opts.QMPSocket+",server,nowait")
	}
	if opts.SerialLog != "" {
		// append=on: a restart adds to the log rather than truncating it, so
		// the record of a failed first boot survives the retry that follows.
		args = append(args,
			"-chardev", fmt.Sprintf("file,id=serial0,path=%s,append=on", opts.SerialLog),
			"-serial", "chardev:serial0",
		)
	}
	return args
}

// accelArgs picks the accelerator and the CPU model together, because they are
// one decision: KVM can pass the host CPU through, TCG cannot model it at all
// and needs "max" instead.
//
// A runner with no /dev/kvm is the case that matters. Hard-coding accel=kvm
// there produces a VM that will not start, and `-cpu host` under TCG produces
// one that starts and then cannot find its CPU model — both read as a broken
// image rather than a machine that cannot nest.
func accelArgs(cpu int) []string {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return []string{"-cpu", "max", "-smp", fmt.Sprintf("%d", cpu), "-machine", "q35,accel=tcg"}
	}
	return []string{"-cpu", "host", "-smp", fmt.Sprintf("%d", cpu), "-machine", "q35,accel=kvm"}
}

func generateUnit(opts generateUnitOpts) string {
	return fmt.Sprintf(`[Unit]
Description=TailVM: %s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s %s
Restart=no
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`, opts.Name, opts.QemuPath, strings.Join(qemuArgs(opts), " "))
}
