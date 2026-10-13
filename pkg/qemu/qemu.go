package qemu

import (
	"context"
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
	realSystemctl   = func(ctx context.Context, args ...string) ([]byte, error) {
		return runBoundedCmd(ctx, systemctlTimeout, systemctlOutputLimit, "systemctl", append([]string{"--user"}, args...)...)
	}
	systemctlRun = realSystemctl
)

// SetStateDirs overrides where VM state and systemd units live ("" = default).
func SetStateDirs(vmHome, unitDir string) { vmHomeOverride, unitDirOverride = vmHome, unitDir }

// SetSystemctl overrides the systemd --user command runner. Passing nil
// restores the real one: a test that left it nil used to be harmless and is
// not any more, since the lifecycle now asks systemd whether it is even there.
//
// The runner takes the caller's context first: the real runner honors it
// mid-call (a cancel kills the subprocess group within milliseconds;
// see bounded.go). Stubs receive it too and must honor it whenever they
// block; immediate stubs ignore it. Callers without a context pass
// context.Background and are governed by the default bound alone.
func SetSystemctl(f func(ctx context.Context, args ...string) ([]byte, error)) {
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
		// cache/ and locks/ are home-level namespaces, never VMs: even
		// a hand-planted metadata.json inside them must not list.
		if !e.IsDir() || e.Name() == "cache" || e.Name() == "locks" {
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
			Status    string `json:"status"`
		}
		if json.Unmarshal(data, &meta) != nil {
			continue
		}

		if meta.Status == "Creating" {
			vms = append(vms, types.VM{
				Name:    meta.Name,
				Backend: "qemu",
				Status:  "◐ Creating",
				Ready:   false,
				Running: false,
				CPU:     meta.CPU,
				Mem:     meta.Memory,
				Disk:    meta.Disk,
			})
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

// RecordCreating writes an initial metadata.json recording that the VM is
// currently being created, so that corral list and the web UI can display
// it as in-progress rather than invisible during long image builds.
func RecordCreating(name string, opts types.CreateOpts) error {
	if err := ValidateVMName(name); err != nil {
		return err
	}
	vmDir := filepath.Join(VMHome(), name)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
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
	meta := map[string]any{
		"name":      name,
		"cpu":       cpu,
		"memory":    mem,
		"disk_size": diskSize,
		"status":    "Creating",
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vmDir, "metadata.json"), data, 0644)
}

// Create creates a new QEMU VM.
func Create(opts types.CreateOpts) error {
	name := opts.Name
	if err := ValidateVMName(name); err != nil {
		return err
	}
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// One creation at a time per VM home: the existence check, port
	// allocation and state/metadata writes below are one critical section,
	// so concurrent same-name creates cannot interleave and concurrent
	// different-name creates cannot take one display twice.
	unlock, err := lockCreateCtx(ctx)
	if err != nil {
		return fmt.Errorf("locking VM creation: %w", err)
	}
	defer unlock()
	vmDir := filepath.Join(VMHome(), name)

	if Exists(name) && !opts.Force {
		return fmt.Errorf("VM %q already exists. Use --force to overwrite", name)
	}

	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("creating VM directory: %w", err)
	}
	// The lifecycle lock makes the force-check → convert → record sequence
	// atomic against concurrent starts and stops of this VM: no start can
	// slip between ProvenStopped and the disk conversion, and no stop can
	// land mid-record. Lock order is creation, then lifecycle (released in
	// reverse by the deferred unlocks).
	unlockLife, err := lockLifecycleCtx(ctx, name)
	if err != nil {
		return fmt.Errorf("locking VM lifecycle: %w", err)
	}
	defer unlockLife()

	if Exists(name) && opts.Force {
		// --force on a running VM would record a new command and new
		// metadata while the old QEMU still owns the disk. Stop it first
		// and prove it stopped; a VM that cannot be proven stopped is
		// refused, never recreated underneath.
		if !ProvenStopped(name) {
			if err := stopLocked(name); err != nil {
				return fmt.Errorf("VM %q is running and could not be stopped for --force recreation: %w", name, err)
			}
			if !ProvenStopped(name) {
				return fmt.Errorf("VM %q is still running after stop — refusing --force recreation of a running VM (stop it with: corral stop %s)", name, name)
			}
		}
	}

	// Bind an explicit guest arch when requested: emulator, machine,
	// accelerator, CPU and firmware all follow it. Empty preserves the
	// legacy x86-only path.
	var spec GuestSpec
	var specOK bool
	if opts.GuestArch != "" {
		guest, err := ParseGuestArch(opts.GuestArch)
		if err != nil {
			return err
		}
		spec, err = ResolveGuestSpec(guest, ResolveOpts{
			AllowEmulation: opts.AllowEmulation,
			AllowCrossArch: opts.AllowCrossArch,
		})
		if err != nil {
			return err
		}
		specOK = true
		if spec.GuestArch == GuestARM64 && !opts.UEFI {
			return fmt.Errorf("arm64 guests require UEFI — the virt board has no BIOS boot contract (a BIOS ARM VM would show a blank screen and nothing else)")
		}
	}

	// Find QEMU binaries
	var qemuPath, qemuImgPath string
	if specOK {
		var err error
		qemuPath, qemuImgPath, err = findQEMUForArch(spec.GuestArch)
		if err != nil {
			return err
		}
	} else {
		var err error
		qemuPath, qemuImgPath, err = findQEMU()
		if err != nil {
			return err
		}
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
		if opts.SourceSHA256 != "" {
			// Digest-bound creation: the template must be self-contained
			// (no backing/data file outside the digest) in the manifest's
			// format, and the virtual-size conversion demand must fit —
			// rechecked here, immediately before the mutating convert.
			format := opts.SourceFormat
			if format == "" {
				format = "qcow2"
			}
			if err := verifyTemplateSelfContained(ctx, qemuImgPath, opts.QCOW, format); err != nil {
				return err
			}
			if err := verifyConversionCapacity(ctx, qemuImgPath, opts.QCOW); err != nil {
				return err
			}
		}
		if out, err := exec.CommandContext(ctx, qemuImgPath, "convert", "-O", "qcow2", opts.QCOW, diskPath).CombinedOutput(); err != nil {
			return fmt.Errorf("copying template %s: %s: %w", opts.QCOW, string(out), err)
		}
		if opts.Disk != "" {
			// Grow to the requested size (shrinking is refused by qemu-img).
			if out, err := exec.CommandContext(ctx, qemuImgPath, "resize", diskPath, diskSize).CombinedOutput(); err != nil {
				return fmt.Errorf("resizing disk to %s: %s: %w", diskSize, string(out), err)
			}
		}
	default:
		if out, err := exec.CommandContext(ctx, qemuImgPath, "create", "-f", "qcow2", diskPath, diskSize).CombinedOutput(); err != nil {
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

	// Exclusive VNC/SSH allocation: the name hash is the stable starting
	// point, but a collision moves to the next free display instead of
	// preventing launch.
	alloc, err := AllocatePorts(name)
	if err != nil {
		return err
	}
	vncDisplay := alloc.Display
	vncPort := alloc.VNCPort
	sshPort := alloc.SSHPort // host port forwarded to guest :22

	// Console bind address: loopback for the one-line launcher, otherwise
	// the Tailscale IP with a loopback fallback.
	tailscaleIP := Loopback
	if !opts.Loopback {
		if ip, err := tailscaleIPv4(); err == nil {
			tailscaleIP = ip
		} else {
			tailscaleIP = Loopback
		}
	}
	bindIP := ConsoleBindAddr(opts.Loopback, tailscaleIP)

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

	var uefiCode, uefiVars, firmwareCodeSHA string
	firmware := "bios"
	if opts.UEFI {
		var code, varsTmpl string
		var err error
		if specOK {
			code, varsTmpl, err = FindUEFIFirmwareForArch(string(spec.GuestArch))
		} else {
			code, varsTmpl, err = FindUEFIFirmware()
		}
		if err != nil {
			return fmt.Errorf("UEFI requested: %w", err)
		}
		uefiCode = code
		// The authoritative firmware code identity, recorded into the
		// metadata below: reuse re-hashes and compares, so a replacement
		// code.fd is never admitted as the same definition. A code image
		// that cannot be identified fails creation — there is no admitted
		// firmware without an identity.
		if firmwareCodeSHA, err = firmwareSHA256(code); err != nil {
			return fmt.Errorf("UEFI requested: cannot identify firmware code %s: %w", code, err)
		}
		firmware = "uefi"
		if varsTmpl != "" {
			vmVarsPath := filepath.Join(vmDir, "vars.fd")
			if _, err := os.Stat(vmVarsPath); os.IsNotExist(err) {
				if err := copyFile(varsTmpl, vmVarsPath); err != nil {
					return fmt.Errorf("copying UEFI vars template: %w", err)
				}
			}
			uefiVars = vmVarsPath
		}
	}

	// Vsock: per-VM CID + keypair, args add vhost-vsock-pci + SMBIOS credentials
	var vsockCID uint32
	var vsockArgs []string
	if opts.Vsock {
		vsockCID = VsockCID(name, opts.VsockCID)
		// Best-effort modprobe like iso-e2e.sh
		if _, err := os.Stat("/dev/vhost-vsock"); err != nil {
			_ = exec.Command("modprobe", "vhost_vsock").Run()
			_ = exec.Command("sudo", "-n", "modprobe", "vhost_vsock").Run()
		}
		if ok, reason := VsockHostAvailable(); !ok {
			return fmt.Errorf("vsock requested but not available: %s (install socat with vsock, ensure /dev/vhost-vsock is writable, and install ssh-keygen)", reason)
		}
		if _, err := EnsureVsockKey(name); err != nil {
			return fmt.Errorf("preparing vsock key: %w", err)
		}
		if args, err := VsockArgs(name, vsockCID); err != nil {
			return err
		} else {
			vsockArgs = args
		}
	}

	// TPM: per-VM swtpm state dir + device args
	var tpmArgs []string
	if opts.TPM {
		if ok, reason := TPMHostAvailable(); !ok {
			return fmt.Errorf("tpm requested but not available: %s", reason)
		}
		if err := EnsureTPMDir(name, false); err != nil {
			return fmt.Errorf("preparing TPM dir: %w", err)
		}
		tpmArgs = TPMArgs(name)
	}

	// Systemd unit
	unitOpts := generateUnitOpts{
		Name:        name,
		QemuPath:    qemuPath,
		Mem:         mem,
		CPU:         cpu,
		DiskPath:    diskPath,
		ISOPath:     isoPath,
		HasISO:      hasISO,
		TailscaleIP: bindIP,
		VncDisplay:  vncDisplay,
		SSHPort:     sshPort,
		QMPSocket:   qmpSocket,
		SerialLog:   serialLog,
		UEFICode:    uefiCode,
		UEFIVars:    uefiVars,
		VsockCID:    vsockCID,
		VsockArgs:   vsockArgs,
		TPMArgs:     tpmArgs,
	}
	if specOK {
		unitOpts.Machine = spec.Machine
		unitOpts.Accel = spec.Accel
		unitOpts.CPUModel = spec.CPU
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

	// Reload systemd (best-effort: a missing user bus fails here and the
	// unit is still on disk for the next reload). The creation context
	// applies: a cancelled create never spawns the reload.
	_, _ = systemctlRun(ctx, "daemon-reload")

	// Save metadata
	meta := map[string]any{
		"name":         name,
		"cpu":          cpu,
		"memory":       mem,
		"disk_size":    diskSize,
		"vnc_port":     vncPort,
		"vnc_display":  vncDisplay,
		"ssh_port":     sshPort,
		"tailscale_ip": bindIP,
		"iso":          isoPath,
		"has_iso":      hasISO,
		"serial_log":   serialLog,
		"firmware":     firmware,
		"vsock":        opts.Vsock,
		"vsock_cid":    vsockCID,
		"tpm":          opts.TPM,
	}
	if firmware == "uefi" {
		meta["firmware_code"] = uefiCode
		meta["firmware_code_sha256"] = firmwareCodeSHA
	}
	if specOK {
		meta["guest_arch"] = string(spec.GuestArch)
		meta["machine"] = spec.Machine
		meta["accel"] = spec.Accel
		meta["cpu_model"] = spec.CPU
		meta["emulated"] = spec.Emulated
		meta["loopback"] = opts.Loopback
	}
	if opts.SourceSHA256 != "" {
		// Immutable creation-source provenance for digest-bound VMs: reuse
		// requires an exact match on these, so a VM created from another
		// image is never mistaken for this manifest's VM.
		meta["source_url"] = opts.SourceURL
		meta["source_sha256"] = opts.SourceSHA256
		meta["source_size"] = opts.SourceSize
		meta["source_format"] = opts.SourceFormat
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	if err := writeMetadataFile(vmDir, data); err != nil {
		return fmt.Errorf("recording VM metadata: %w (the VM's disk, command and unit exist but its definition is unrecorded — delete and recreate it: corral delete %s)", err, name)
	}

	fmt.Fprintf(os.Stderr, "VM %q created.\n", name)
	fmt.Fprintf(os.Stderr, "  Start:   corral start %s\n", name)
	fmt.Fprintf(os.Stderr, "  VNC:     vnc://%s:%d\n", bindIP, vncPort)
	fmt.Fprintf(os.Stderr, "  SSH:     corral ssh %s  (forwarded %s:%d → guest :22)\n", name, bindIP, sshPort)
	if specOK && spec.Emulated {
		fmt.Fprintf(os.Stderr, "  Mode:    emulated (%s/%s, not native)\n", spec.Machine, spec.Accel)
	}
	return nil
}

// writeMetadataFile records metadata.json atomically (temp file + rename)
// with a checked close. Creation reports success only once the definition
// — ports, provenance, machine binding — is durably recorded: an unchecked
// or torn write would release the locks on a "successful" create whose port
// reservation the next allocation cannot see.
func writeMetadataFile(vmDir string, data []byte) error {
	tmp, err := os.CreateTemp(vmDir, ".metadata-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// A failed close can mean buffered bytes never reached the disk — the
	// record is only as proven as this close.
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(vmDir, "metadata.json"))
}

// Start starts a QEMU VM: through systemd where there is a user session, and
// as a detached process where there is not.
func Start(name string) error {
	_, err := StartOwned(name)
	return err
}

// StartOwned starts a QEMU VM like Start and reports whether this call
// started it (false = it was already running). Callers that own the running
// state they create — the launcher's failure cleanup — need the distinction:
// stopping a VM this call did not start would kill somebody else's session.
// Automatic cleanup should use StartOwnedToken/StopOwned instead, which bind
// the cleanup stop to the captured start identity rather than the name.
func StartOwned(name string) (bool, error) {
	_, started, err := StartOwnedToken(name)
	return started, err
}

// Stop stops a QEMU VM, whichever way it was started: an explicit stop by
// name. An unverifiable direct child refuses the stop even where systemd is
// available — stopping an unrelated unit and printing "VM stopped" while an
// unknown direct child holds the disk would be a false claim.
func Stop(name string) error {
	if err := ValidateVMName(name); err != nil {
		return err
	}
	unlock, err := lockLifecycle(name)
	if err != nil {
		return err
	}
	defer unlock()
	return stopLocked(name)
}

// StopCtx is Stop with the caller's context bounding the lifecycle-lock
// wait: a cancelled caller fails the wait instead of queueing past it.
// In-operation subcalls keep their own default bounds (see bounded.go).
func StopCtx(ctx context.Context, name string) error {
	if err := ValidateVMName(name); err != nil {
		return err
	}
	unlock, err := lockLifecycleCtx(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	return stopLocked(name)
}

// stopLocked is Stop under the caller's lifecycle lock.
func stopLocked(name string) error {
	if _, _, st := directState(name); st == pidOwned || st == pidUnknown || !userSystemd() {
		if err := stopDirectLocked(name); err != nil {
			return err
		}
		_ = StopSwtpm(name)
		fmt.Fprintf(os.Stderr, "VM %q stopped.\n", name)
		return nil
	}
	svc := "corral-" + name
	if out, err := systemctlRun(context.Background(), "stop", svc); err != nil {
		return fmt.Errorf("stopping VM: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_ = StopSwtpm(name)
	fmt.Fprintf(os.Stderr, "VM %q stopped.\n", name)
	return nil
}

// Delete removes a QEMU VM and its files. It serializes against creation
// (home-wide) and against start/stop of this VM (lifecycle), in that order,
// so no create, start or stop runs against a half-deleted VM.
func Delete(name string) error {
	if err := ValidateVMName(name); err != nil {
		return err
	}
	unlockCreate, err := lockCreate()
	if err != nil {
		return fmt.Errorf("locking VM creation: %w", err)
	}
	defer unlockCreate()
	unlockLife, err := lockLifecycle(name)
	if err != nil {
		return err
	}
	defer unlockLife()
	return deleteLocked(name)
}

// DeleteCtx is Delete with the caller's context bounding both lock waits
// (the creation lock — unbounded without a context — and the lifecycle
// lock): a cancelled caller fails the waits instead of queueing past
// them. In-operation subcalls keep their own default bounds (see
// bounded.go).
func DeleteCtx(ctx context.Context, name string) error {
	if err := ValidateVMName(name); err != nil {
		return err
	}
	unlockCreate, err := lockCreateCtx(ctx)
	if err != nil {
		return fmt.Errorf("locking VM creation: %w", err)
	}
	defer unlockCreate()
	unlockLife, err := lockLifecycleCtx(ctx, name)
	if err != nil {
		return err
	}
	defer unlockLife()
	return deleteLocked(name)
}

// deleteLocked is Delete under both locks (creation, then lifecycle).
// Destruction is gated twice: every applicable stop must succeed, and the
// VM must then be PROVEN stopped, before any file is touched. Anything
// else — an unstoppable or unverifiable direct child, a failed unit stop,
// a unit still active after its stop — preserves every file and reports
// the uncertainty instead of printing a false "deleted".
func deleteLocked(name string) error {
	// A directly started VM has no unit to stop, and deleting the disk
	// under a live or unverified QEMU is the worst outcome. An unknown
	// direct record refuses here: uncertainty preserves files.
	if err := stopDirectLocked(name); err != nil {
		return fmt.Errorf("VM %q not deleted: %v (files preserved)", name, err)
	}
	svc := "corral-" + name
	unitPath := filepath.Join(systemdUserDir(), svc+".service")
	// An unanswered manager is unknown, and unknown never deletes: with no
	// direct child left to stop, skipping the unit block below would hand
	// the files to removal while a unit may exist. Only a proven-absent
	// manager (or a live one, handled in the block) proceeds.
	if !userSystemd() && !managerProvenAbsent() {
		return fmt.Errorf("VM %q not deleted: the systemd user manager did not answer (state unknown) — refusing to delete while unit %q may be live (files preserved; settle it genuinely — `systemctl --user stop %s` once the manager answers, or stop any live QEMU by hand — and retry)", name, svc, svc)
	}
	if userSystemd() {
		// Refresh the manager's view before acting on it: a unit file
		// written but never loaded (a failed create-time reload) would
		// otherwise mislead the stop. Best-effort like Create's — the
		// stops below are checked, and the ProvenStopped gate after them
		// is authoritative.
		_, _ = systemctlRun(context.Background(), "daemon-reload")
		_, statErr := os.Stat(unitPath)
		if statErr == nil || !unitProvenStopped(svc, unitPath) {
			// A unit file exists, or an activation is live or
			// uncertain: stop it, checked. Only this VM's own unit is
			// ever named — the stop cannot fan out to another VM.
			if out, err := systemctlRun(context.Background(), "stop", svc); err != nil {
				return fmt.Errorf("VM %q not deleted: stopping its unit failed: %w: %s (files preserved)", name, err, strings.TrimSpace(string(out)))
			}
			if statErr == nil {
				if out, err := systemctlRun(context.Background(), "disable", svc); err != nil {
					return fmt.Errorf("VM %q not deleted: disabling its unit failed: %w: %s (files preserved)", name, err, strings.TrimSpace(string(out)))
				}
			}
		}
	}
	// The destructive gate: no owned or unverifiable direct child, and
	// (where a manager exists) a unit proven stopped. A stop that
	// reported success without retiring anything fails here, not after
	// the files are gone.
	if !ProvenStopped(name) {
		return fmt.Errorf("VM %q not deleted: it is still running or its state cannot be verified (files preserved)", name)
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("VM %q not deleted: removing its unit file failed: %w (VM state preserved in %s)", name, err, filepath.Join(VMHome(), name))
	}
	_, _ = systemctlRun(context.Background(), "daemon-reload")

	vmDir := filepath.Join(VMHome(), name)
	if err := os.RemoveAll(vmDir); err != nil {
		return fmt.Errorf("VM %q not deleted: removing %s failed: %w", name, vmDir, err)
	}

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
	Firmware  string `json:"firmware,omitempty"`
	Status    string `json:"status,omitempty"`
	VsockCID  uint32 `json:"vsock_cid,omitempty"`
	Vsock     bool   `json:"vsock,omitempty"`
	TPM       bool   `json:"tpm,omitempty"`
	// GuestArch/Machine/Accel/CPUModel/Emulated/Loopback are recorded only
	// for VMs created with an explicit guest arch (the launcher path).
	GuestArch string `json:"guest_arch,omitempty"`
	Machine   string `json:"machine,omitempty"`
	Accel     string `json:"accel,omitempty"`
	CPUModel  string `json:"cpu_model,omitempty"`
	Emulated  bool   `json:"emulated,omitempty"`
	Loopback  bool   `json:"loopback,omitempty"`
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

// LogsSerial prints the guest serial console log. tail==0 prints all; else last N lines.
// Falls back to reporting the missing-log error that tells how to recreate the VM.
func LogsSerial(name string, tail int) error {
	data, err := SerialLog(name)
	if err != nil {
		return err
	}
	text := string(data)
	if tail > 0 {
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		text = strings.Join(lines, "\n") + "\n"
	}
	_, _ = os.Stdout.WriteString(text)
	return nil
}

// SSHViaVsock opens an SSH session via AF_VSOCK using the per-VM keypair.
// This is the fallback transport for live ISO / published-media guests whose
// TCP sshd is disabled and which instead listen via systemd-ssh-generator's
// AF_VSOCK listener (tuna-os/tunaos iso-e2e.sh setup_vsock).
func SSHViaVsock(name, username, command string) error {
	meta, err := readMetadata(name)
	if err != nil {
		return fmt.Errorf("VM %q not found: %w", name, err)
	}
	if !meta.Vsock {
		return fmt.Errorf("VM %q has no vsock — create with --vsock (or --vsock-cid)", name)
	}
	sshBin, _ := exec.LookPath("ssh")
	if sshBin == "" {
		return fmt.Errorf("ssh not found in PATH")
	}
	// Build vsock ProxyCommand args; VsockSSHArgs returns [ -i key -o ... user@host ]
	vsockArgs, err := VsockSSHArgs(name, meta.VsockCID, username)
	if err != nil {
		return err
	}
	// Append command if given.
	if command != "" {
		vsockArgs = append(vsockArgs, command)
	}
	cmd := exec.Command(sshBin, vsockArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func findQEMU() (qemu, qemuImg string, err error) {
	// Legacy x86-only entry point. The arch-aware lookup adds the missing
	// Homebrew Apple Silicon fallback (/opt/homebrew/bin); both share it.
	return findQEMUForArch(GuestAMD64)
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
	UEFICode                                            string
	UEFIVars                                            string
	VsockCID                                            uint32
	VsockArgs                                           []string
	TPMArgs                                             []string
	// Machine/Accel/CPUModel bind an explicit guest spec. Empty preserves
	// the legacy accelArgs path (x86 q35, KVM-or-TCG by /dev/kvm).
	Machine  string
	Accel    string
	CPUModel string
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
	if opts.Machine != "" && opts.Accel != "" && opts.CPUModel != "" {
		args = append(args, accelArgsForSpec(GuestSpec{Machine: opts.Machine, Accel: opts.Accel, CPU: opts.CPUModel}, opts.CPU)...)
	} else {
		args = append(args, accelArgs(opts.CPU)...)
	}
	args = append(args,
		"-drive", fmt.Sprintf("file=%s,if=virtio,format=qcow2", opts.DiskPath),
		"-vnc", fmt.Sprintf("%s:%d", opts.TailscaleIP, opts.VncDisplay),
	)
	if opts.Machine == "virt" {
		// ARM virt has no default display, no VGA the machine consumes
		// (-vga only warns in vl.c and renders nothing), and no input
		// devices — graphics and input must be explicit devices. virtio-
		// gpu-pci is the KVM path the official QEMU virt board docs name;
		// USB carries the keyboard and pointer. VNC serves the resulting
		// framebuffer exactly as on x86.
		args = append(args,
			"-device", "virtio-gpu-pci",
			"-device", "qemu-xhci",
			"-device", "usb-kbd",
			"-device", "usb-mouse",
		)
	} else {
		args = append(args, "-vga", "virtio")
	}
	args = append(args, "-display", "none")

	if opts.UEFICode != "" {
		args = append(args, "-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", opts.UEFICode))
		if opts.UEFIVars != "" {
			args = append(args, "-drive", fmt.Sprintf("if=pflash,format=raw,file=%s", opts.UEFIVars))
		}
	}

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
	if len(opts.VsockArgs) > 0 {
		args = append(args, opts.VsockArgs...)
	}
	if len(opts.TPMArgs) > 0 {
		args = append(args, opts.TPMArgs...)
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
