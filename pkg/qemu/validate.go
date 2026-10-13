package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Reuse-time command validation: a VM is reused only when its recorded QEMU
// command is proven to still be the admitted one. The digest pins the SOURCE
// image, but the running VM is defined by its argv — binary, disk, machine,
// accelerator, firmware, transports — plus the systemd unit that executes it
// on systemd hosts. Every element below is bound to the creation metadata:
// required elements must match exactly, and every flag must be recognized
// with an admitted shape — anything the generator never emits (extra
// listeners, replaced boot media, direct-kernel boot, unknown devices) is
// refused, because under a loopback-only launcher contract those are not
// harmless drift.
//
// Presence is lenient where the generator is conditional, but shape is
// strict everywhere: an observability or display flag that IS present must
// be exactly the admitted one (one unix QMP monitor at most, the serial
// chardev pair at most, -vga virtio at most, -display none at most), and
// singletons (-name, -m, -smp, -cpu, -machine, -vnc, -serial, -boot) must
// not repeat. The record is per-VM (recorded at its own creation), so
// strictness here cannot break a VM the admitting generator made — only a
// record that no longer matches its admitted definition.

// reuseMetadata is the creation-metadata subset command validation binds.
// Decoded locally (rather than via vmMetadata) because reuse needs fields
// the runtime struct does not carry (vnc_display, iso).
type reuseMetadata struct {
	CPU        int    `json:"cpu"`
	Memory     string `json:"memory"`
	VncPort    int    `json:"vnc_port"`
	VncDisplay int    `json:"vnc_display"`
	SSHPort    int    `json:"ssh_port"`
	Bind       string `json:"tailscale_ip"`
	Firmware   string `json:"firmware"`
	// FirmwareCode/FirmwareCodeSHA256 are the authoritative identity of the
	// admitted UEFI code image: its path and its bytes' digest. Without
	// them a replacement code.fd would be admitted as the same definition.
	FirmwareCode       string `json:"firmware_code"`
	FirmwareCodeSHA256 string `json:"firmware_code_sha256"`
	GuestArch          string `json:"guest_arch"`
	Machine            string `json:"machine"`
	Accel              string `json:"accel"`
	CPUModel           string `json:"cpu_model"`
	Emulated           bool   `json:"emulated"`
	ISO                string `json:"iso"`
	HasISO             bool   `json:"has_iso"`
}

// commandFacts is the binding-relevant subset of a recorded argv.
type commandFacts struct {
	name       string
	hasName    bool
	mem        string
	hasMem     bool
	smp        string
	hasSMP     bool
	cpu        string
	hasCPU     bool
	machine    string
	accel      string
	hasMachine bool
	diskFile   string
	diskFormat string
	diskIf     string
	hasDisk    bool
	vncBind    string
	vncDisplay int
	vncCount   int
	netdevs    []string
	sshBind    string
	sshPort    int
	hasHostfwd bool
	codeFile   string
	varsFile   string
	codeCount  int
	varsCount  int
	qmps       []string
	cdroms     []string
	chardevs   []string
	serial     string
	hasSerial  bool
	vga        string
	hasVGA     bool
	display    string
	hasDisplay bool
	boot       string
	hasBoot    bool
	devices    []string
	otherDrive []string
	// unknown collects every flag outside the recognized allowlist (plus
	// any stray token): an unrecognized argv element is refused, never
	// ignored, so a widened record cannot pass as the admitted command.
	unknown []string
	denied  string
}

// deniedArgvFlags are never emitted by the generator and always widen the
// VM: extra listeners, replaced boot media, snapshots, or direct-kernel
// boot bypassing the disk's bootloader. Any recorded argv containing one is
// not the admitted command.
var deniedArgvFlags = []string{
	"-incoming", // migration listener: an extra network port QEMU serves
	"-monitor",  // extra monitor socket beyond the recorded QMP one
	"-snapshot", // silently discards all disk writes
	"-loadvm",   // boots snapshot state, not the recorded disk
	"-kernel",   // direct-kernel boot bypassing the disk bootloader
	"-initrd",   //
	"-append",   // kernel cmdline: only meaningful with -kernel
	"-dtb",      //
	"-bios",     // replaces the board's firmware image
	"-L",        // redirects firmware/data-file lookup
}

// parseCommandFacts extracts the binding-relevant facts from recorded argv.
// It errors only on structurally unparsable input; value checks against
// metadata live in ValidateCommand.
func parseCommandFacts(args []string) (commandFacts, error) {
	var f commandFacts
	f.vncDisplay = -1
	f.sshPort = -1
	denied := map[string]bool{}
	for _, d := range deniedArgvFlags {
		denied[d] = true
	}
	value := func(i int, flag string) (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("flag %s has no value", flag)
		}
		return args[i+1], nil
	}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if denied[flag] {
			f.denied = flag
			continue
		}
		switch flag {
		case "-name":
			if f.hasName {
				return f, fmt.Errorf("duplicate -name flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.name, f.hasName = v, true
			i++
		case "-m":
			if f.hasMem {
				return f, fmt.Errorf("duplicate -m flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.mem, f.hasMem = v, true
			i++
		case "-smp":
			if f.hasSMP {
				return f, fmt.Errorf("duplicate -smp flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.smp, f.hasSMP = v, true
			i++
		case "-cpu":
			if f.hasCPU {
				return f, fmt.Errorf("duplicate -cpu flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.cpu, f.hasCPU = v, true
			i++
		case "-machine":
			if f.hasMachine {
				return f, fmt.Errorf("duplicate -machine flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			board, accel, err := parseMachine(v)
			if err != nil {
				return f, err
			}
			f.machine, f.accel, f.hasMachine = board, accel, true
			i++
		case "-drive":
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			if err := f.addDrive(v); err != nil {
				return f, err
			}
			i++
		case "-vnc":
			if f.vncCount > 0 {
				return f, fmt.Errorf("duplicate -vnc flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			bind, display, err := parseVNC(v)
			if err != nil {
				return f, err
			}
			f.vncCount++
			f.vncBind, f.vncDisplay = bind, display
			i++
		case "-netdev":
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.netdevs = append(f.netdevs, v)
			i++
		case "-qmp":
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.qmps = append(f.qmps, v)
			i++
		case "-cdrom":
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.cdroms = append(f.cdroms, v)
			i++
		case "-chardev":
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.chardevs = append(f.chardevs, v)
			i++
		case "-serial":
			if f.hasSerial {
				return f, fmt.Errorf("duplicate -serial flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.serial, f.hasSerial = v, true
			i++
		case "-vga":
			if f.hasVGA {
				return f, fmt.Errorf("duplicate -vga flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.vga, f.hasVGA = v, true
			i++
		case "-display":
			if f.hasDisplay {
				return f, fmt.Errorf("duplicate -display flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.display, f.hasDisplay = v, true
			i++
		case "-boot":
			if f.hasBoot {
				return f, fmt.Errorf("duplicate -boot flag")
			}
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.boot, f.hasBoot = v, true
			i++
		case "-device":
			v, err := value(i, flag)
			if err != nil {
				return f, err
			}
			f.devices = append(f.devices, v)
			i++
		default:
			// Outside the recognized allowlist: recorded for refusal
			// below, never silently ignored. (A denied flag's VALUE
			// lands here too when the denied flag takes one — the
			// denied refusal wins, since it is checked first.)
			f.unknown = append(f.unknown, flag)
		}
	}
	if len(f.netdevs) == 1 {
		bind, port, ok := parseNetdevStrict(f.netdevs[0])
		if ok {
			f.sshBind, f.sshPort, f.hasHostfwd = bind, port, true
		}
	}
	return f, nil
}

func parseMachine(v string) (board, accel string, err error) {
	parts := strings.SplitN(v, ",", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", fmt.Errorf("unparsable -machine %q (want board,accel=...)", v)
	}
	for _, opt := range strings.Split(parts[1], ",") {
		if a, ok := strings.CutPrefix(opt, "accel="); ok {
			return parts[0], a, nil
		}
	}
	return "", "", fmt.Errorf("unparsable -machine %q (no accel=)", v)
}

func parseVNC(v string) (bind string, display int, err error) {
	idx := strings.LastIndex(v, ":")
	if idx <= 0 {
		return "", -1, fmt.Errorf("unparsable -vnc %q (want bind:display)", v)
	}
	d, err := strconv.Atoi(v[idx+1:])
	if err != nil || d < 0 {
		return "", -1, fmt.Errorf("unparsable -vnc %q (bad display)", v)
	}
	return v[:idx], d, nil
}

// parseNetdevStrict binds a -netdev value to the admitted user-network
// shape: exactly `user` with id=net0 and exactly one tcp hostfwd carrying
// the SSH forward (tcp:<bind>:<port>-:22). Any deviation — another type or
// id, a neighboring forward (a udp listener alongside the valid tcp one),
// an extra option, a duplicate key, a missing dash — reports ok=false, and
// the caller refuses. A substring match here admitted exactly those
// wideners, so the shape is exact, not searched.
func parseNetdevStrict(netdev string) (bind string, port int, ok bool) {
	parts := strings.Split(netdev, ",")
	// Exact cardinality first: the type plus exactly the two admitted
	// options, no more, no fewer. The per-option checks below then bind
	// each key and value.
	if len(parts) != 3 || parts[0] != "user" {
		return "", -1, false
	}
	seen := map[string]bool{}
	var id, hostfwd string
	for _, opt := range parts[1:] {
		k, v, hasEq := strings.Cut(opt, "=")
		if !hasEq || k == "" {
			return "", -1, false
		}
		if seen[k] {
			return "", -1, false
		}
		seen[k] = true
		switch k {
		case "id":
			id = v
		case "hostfwd":
			hostfwd = v
		default:
			return "", -1, false
		}
	}
	if !seen["id"] || id != "net0" {
		return "", -1, false
	}
	if !seen["hostfwd"] {
		return "", -1, false
	}
	rest, found := strings.CutPrefix(hostfwd, "tcp:")
	if !found {
		return "", -1, false
	}
	// <bind>:<port>-:22 — bind is an IPv4 literal (no colons).
	f := strings.SplitN(rest, ":", 3)
	if len(f) != 3 || f[2] != "22" {
		return "", -1, false
	}
	portStr, dashed := strings.CutSuffix(f[1], "-")
	if !dashed {
		return "", -1, false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 {
		return "", -1, false
	}
	if f[0] == "" {
		return "", -1, false
	}
	return f[0], p, true
}

// driveOpts splits a -drive value into its key=value options, refusing
// duplicate keys (last-wins would silently admit a shadowed file=) and
// keyless options.
func driveOpts(v string) (map[string]string, error) {
	opts := map[string]string{}
	for _, opt := range strings.Split(v, ",") {
		k, val, hasEq := strings.Cut(opt, "=")
		if !hasEq || k == "" {
			return nil, fmt.Errorf("unparsable -drive %q (option %q is not key=value)", v, opt)
		}
		if _, dup := opts[k]; dup {
			return nil, fmt.Errorf("unparsable -drive %q (duplicate option %q)", v, k)
		}
		opts[k] = val
	}
	return opts, nil
}

func (f *commandFacts) addDrive(v string) error {
	opts, err := driveOpts(v)
	if err != nil {
		return err
	}
	if opts["if"] == "pflash" {
		format := opts["format"]
		if format != "raw" {
			return fmt.Errorf("unparsable -drive %q (pflash must be format=raw)", v)
		}
		file := opts["file"]
		if file == "" {
			return fmt.Errorf("unparsable -drive %q (pflash without file=)", v)
		}
		if ro, hasRO := opts["readonly"]; hasRO {
			// The code drive is read-only hardware: only readonly=on
			// admits. Presence alone accepted readonly=off (a writable
			// "code" drive) and a bare readonly alike.
			if ro != "on" {
				return fmt.Errorf("unparsable -drive %q (pflash code must be readonly=on)", v)
			}
			if len(opts) != 4 {
				return fmt.Errorf("unparsable -drive %q (unadmitted pflash code option)", v)
			}
			f.codeFile = file
			f.codeCount++
		} else {
			// The vars drive is the VM's writable NVRAM: the generator
			// emits no readonly key for it at all.
			if len(opts) != 3 {
				return fmt.Errorf("unparsable -drive %q (unadmitted pflash vars option)", v)
			}
			f.varsFile = file
			f.varsCount++
		}
		return nil
	}
	// The guest disk: exactly file=, if=, format= — anything that is
	// neither this shape nor pflash is collected for refusal below.
	if opts["file"] == "" {
		return fmt.Errorf("unparsable -drive %q (no file=)", v)
	}
	if len(opts) != 3 {
		return fmt.Errorf("unparsable -drive %q (want exactly file=,if=,format=)", v)
	}
	if f.hasDisk {
		f.otherDrive = append(f.otherDrive, v)
		return nil
	}
	f.diskFile, f.diskFormat, f.diskIf, f.hasDisk = opts["file"], opts["format"], opts["if"], true
	return nil
}

// ValidateCommand proves the VM's recorded QEMU command is still the
// admitted one: the binary exists and is runnable, the argv carries every
// security-relevant element the creation metadata binds (name, memory, CPU,
// machine, accelerator, CPU model, disk, VNC/SSH transports, firmware,
// boot media), no denied widener is present, and — where a systemd unit
// exists — the unit executes exactly this command. A VM whose emulator was
// uninstalled, whose record is corrupt, or whose argv no longer matches its
// admitted definition is refused before start, not mid-boot.
func ValidateCommand(name string) error {
	cmd, err := readLaunchCommand(name)
	if err != nil {
		return err
	}
	if err := validateCommandBinary(name, cmd.Binary); err != nil {
		return err
	}
	if len(cmd.Args) == 0 {
		return fmt.Errorf("VM %q records an empty QEMU argv — refusing to start a VM with no admitted command (recreate it to re-admit)", name)
	}
	facts, err := parseCommandFacts(cmd.Args)
	if err != nil {
		return fmt.Errorf("VM %q records an unparsable QEMU command (%v) — refusing to start it (recreate it to re-admit)", name, err)
	}
	if facts.denied != "" {
		return fmt.Errorf("VM %q records denied QEMU flag %s, which the admitted command never contains — refusing to start it (recreate it to re-admit)", name, facts.denied)
	}
	meta, err := readReuseMetadata(name)
	if err != nil {
		return fmt.Errorf("VM %q: %w — refusing to start a VM whose admitted definition cannot be read (recreate it to re-admit)", name, err)
	}
	if err := meta.checkCoherent(name); err != nil {
		return err
	}
	if err := facts.checkAgainst(name, meta, cmd.Binary); err != nil {
		return err
	}
	return validateUnitMatches(name, cmd)
}

func validateCommandBinary(name, binary string) error {
	if filepath.IsAbs(binary) {
		st, err := os.Stat(binary)
		if err != nil {
			return fmt.Errorf("VM %q records QEMU binary %q, which is not present (%v) — reinstall QEMU or recreate the VM", name, binary, err)
		}
		if st.IsDir() {
			return fmt.Errorf("VM %q records QEMU binary %q, which is a directory — recreate the VM", name, binary)
		}
		// Mode-bit approximation: no execute bit for anyone means the exec
		// would fail. (ACLs and capabilities are not consulted; a binary
		// that passes here but cannot execute fails honestly at start.)
		if st.Mode()&0o111 == 0 {
			return fmt.Errorf("VM %q records QEMU binary %q, which is not executable — reinstall QEMU or recreate the VM", name, binary)
		}
		return nil
	}
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("VM %q records QEMU binary %q, which is not on PATH — reinstall QEMU or recreate the VM", name, binary)
	}
	return nil
}

func readReuseMetadata(name string) (reuseMetadata, error) {
	var meta reuseMetadata
	data, err := os.ReadFile(filepath.Join(VMHome(), name, "metadata.json"))
	if err != nil {
		return meta, fmt.Errorf("metadata is unreadable: %w", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, fmt.Errorf("metadata is invalid")
	}
	return meta, nil
}

// checkCoherent enforces the metadata's internal contract: a complete
// machine binding whose board, accelerator, CPU model and emulation marking
// agree with each other, the guest arch, and this host.
func (m reuseMetadata) checkCoherent(name string) error {
	if m.GuestArch == "" || m.Machine == "" || m.Accel == "" || m.CPUModel == "" {
		return fmt.Errorf("VM %q records an incomplete machine binding (arch/machine/accel/cpu model) — refusing to start it (recreate it to re-admit)", name)
	}
	arch := GuestArch(m.GuestArch)
	if arch != GuestAMD64 && arch != GuestARM64 {
		return fmt.Errorf("VM %q records unknown guest arch %q — refusing to start it (recreate it to re-admit)", name, m.GuestArch)
	}
	if m.Machine != arch.Machine() {
		return fmt.Errorf("VM %q records machine %q for a %s guest (want %q) — refusing to start it (recreate it to re-admit)", name, m.Machine, m.GuestArch, arch.Machine())
	}
	nativeAccel, nativeCPU := "", ""
	switch runtime.GOOS {
	case "linux":
		nativeAccel, nativeCPU = "kvm", "host"
	case "darwin":
		nativeAccel, nativeCPU = "hvf", "host"
	default:
		return fmt.Errorf("VM %q cannot be validated on %s (Linux/macOS only)", name, runtime.GOOS)
	}
	crossArch := m.GuestArch != runtime.GOARCH
	switch {
	case crossArch && m.Accel != "tcg":
		return fmt.Errorf("VM %q is a %s guest on a %s host but records non-TCG accelerator %q — refusing to start it (recreate it to re-admit)", name, m.GuestArch, runtime.GOARCH, m.Accel)
	case !crossArch && m.Accel != "tcg" && m.Accel != nativeAccel:
		return fmt.Errorf("VM %q records accelerator %q, which is neither this host's native %s nor TCG — refusing to start it (recreate it to re-admit)", name, m.Accel, nativeAccel)
	}
	if wantEmulated := m.Accel == "tcg"; m.Emulated != wantEmulated {
		return fmt.Errorf("VM %q records emulated=%v with accelerator %q — refusing an incoherent definition (recreate it to re-admit)", name, m.Emulated, m.Accel)
	}
	wantCPU := nativeCPU
	if m.Accel == "tcg" {
		wantCPU = "max"
	}
	if m.CPUModel != wantCPU {
		return fmt.Errorf("VM %q records CPU model %q with accelerator %q (want %q) — refusing to start it (recreate it to re-admit)", name, m.CPUModel, m.Accel, wantCPU)
	}
	if m.VncPort == 0 || m.SSHPort == 0 || m.Bind == "" {
		return fmt.Errorf("VM %q records an incomplete transport binding (vnc/ssh/bind) — refusing to start it (recreate it to re-admit)", name)
	}
	if m.VncPort != 5900+m.VncDisplay || m.SSHPort != 2200+m.VncDisplay {
		return fmt.Errorf("VM %q records incoherent ports (vnc %d/ssh %d for display %d) — refusing to start it (recreate it to re-admit)", name, m.VncPort, m.SSHPort, m.VncDisplay)
	}
	if m.Firmware != "uefi" && m.Firmware != "bios" {
		return fmt.Errorf("VM %q records firmware %q (want uefi or bios) — refusing to start it (recreate it to re-admit)", name, m.Firmware)
	}
	return nil
}

// allowedDevices is the complete -device value allowlist: the generator's
// fixed set (virtio-net, virtio-rng, and the virt board's explicit
// graphics/input devices). Anything else — vsock, TPM, sound, USB
// passthrough, host devices — is refused: the validating launcher never
// admits those transports, and an unknown device combination can widen the
// VM outside the checked surface.
var allowedDevices = map[string]bool{
	"virtio-net-pci,netdev=net0": true,
	"virtio-rng-pci":             true,
	"virtio-gpu-pci":             true,
	"qemu-xhci":                  true,
	"usb-kbd":                    true,
	"usb-mouse":                  true,
}

// checkAgainst binds every argv element to the metadata.
func (f commandFacts) checkAgainst(name string, meta reuseMetadata, binary string) error {
	vmDir := filepath.Join(VMHome(), name)
	fail := func(detail string) error {
		return fmt.Errorf("VM %q records a QEMU command that no longer matches its admitted definition (%s) — refusing to start it (recreate it to re-admit)", name, detail)
	}
	if len(f.unknown) > 0 {
		return fail(fmt.Sprintf("unrecognized QEMU flag %q, which the admitted command never contains", f.unknown[0]))
	}
	if !f.hasName || f.name != name {
		return fail(fmt.Sprintf("name %q, want %q", f.name, name))
	}
	// Memory spelling follows the generator: a bare number means GiB.
	wantMem := meta.Memory
	if !strings.HasSuffix(wantMem, "M") && !strings.HasSuffix(wantMem, "G") {
		wantMem += "G"
	}
	if !f.hasMem || f.mem != wantMem {
		return fail(fmt.Sprintf("memory %q, want %q", f.mem, wantMem))
	}
	if !f.hasSMP || f.smp != strconv.Itoa(meta.CPU) {
		return fail(fmt.Sprintf("smp %q, want %d", f.smp, meta.CPU))
	}
	if !f.hasMachine || f.machine != meta.Machine || f.accel != meta.Accel {
		return fail(fmt.Sprintf("machine %q,accel=%q, want %q,accel=%q", f.machine, f.accel, meta.Machine, meta.Accel))
	}
	if !f.hasCPU || f.cpu != meta.CPUModel {
		return fail(fmt.Sprintf("cpu %q, want %q", f.cpu, meta.CPUModel))
	}
	if want := GuestArch(meta.GuestArch).Emulator(); filepath.Base(binary) != want {
		return fail(fmt.Sprintf("emulator %q, want %s", filepath.Base(binary), want))
	}
	// Disk: exactly the VM's own converted image, virtio, qcow2 — and no
	// other disk-shaped drive.
	wantDisk := filepath.Join(vmDir, "disk.qcow2")
	if !f.hasDisk || f.diskFile != wantDisk || f.diskFormat != "qcow2" || f.diskIf != "virtio" {
		return fail(fmt.Sprintf("disk %q (format %q), want %q", f.diskFile, f.diskFormat, wantDisk))
	}
	if len(f.otherDrive) > 0 {
		return fail(fmt.Sprintf("extra drive %q", f.otherDrive[0]))
	}
	// Transports: exactly one VNC and one netdev, bound where admitted.
	if f.vncCount != 1 || f.vncBind != meta.Bind || f.vncDisplay != meta.VncDisplay {
		return fail(fmt.Sprintf("vnc %q:%d (x%d), want %s:%d", f.vncBind, f.vncDisplay, f.vncCount, meta.Bind, meta.VncDisplay))
	}
	if len(f.netdevs) != 1 || !f.hasHostfwd || f.sshBind != meta.Bind || f.sshPort != meta.SSHPort {
		return fail(fmt.Sprintf("ssh forward %q:%d, want %s:%d", f.sshBind, f.sshPort, meta.Bind, meta.SSHPort))
	}
	// QMP: at most one monitor, and exactly the admitted unix socket. A
	// second monitor — or the recorded unix socket replaced by a TCP
	// listener — is a widened record, not observability drift.
	if len(f.qmps) > 1 {
		return fail(fmt.Sprintf("%d -qmp sockets, want at most 1", len(f.qmps)))
	}
	if len(f.qmps) == 1 {
		if want := "unix:" + filepath.Join(vmDir, "qmp.sock") + ",server,nowait"; f.qmps[0] != want {
			return fail(fmt.Sprintf("-qmp %q, want the admitted unix monitor %q", f.qmps[0], want))
		}
	}
	// Serial console: at most the admitted file chardev plus its -serial,
	// paired. An extra chardev (a socket listener) or an unpaired half is
	// refused with the same force as an extra drive.
	if len(f.chardevs) > 1 {
		return fail(fmt.Sprintf("%d -chardev entries, want at most 1", len(f.chardevs)))
	}
	if len(f.chardevs) == 1 {
		if want := "file,id=serial0,path=" + filepath.Join(vmDir, "serial.log") + ",append=on"; f.chardevs[0] != want {
			return fail(fmt.Sprintf("-chardev %q, want the admitted serial log %q", f.chardevs[0], want))
		}
	}
	if f.hasSerial && f.serial != "chardev:serial0" {
		return fail(fmt.Sprintf("-serial %q, want chardev:serial0", f.serial))
	}
	if (len(f.chardevs) > 0) != f.hasSerial {
		return fail("unpaired serial console wiring (a -chardev without its -serial, or a -serial without its chardev)")
	}
	// Display: at most the admitted shapes. No other value is admitted —
	// a replaced display is not the recorded VM.
	if f.hasVGA && f.vga != "virtio" {
		return fail(fmt.Sprintf("-vga %q, want virtio", f.vga))
	}
	if f.hasDisplay && f.display != "none" {
		return fail(fmt.Sprintf("-display %q, want none", f.display))
	}
	// Devices: every entry must be allowlisted, and none may repeat. An
	// unknown -device can add disks, networks, or host passthrough
	// outside every other check.
	seenDevice := map[string]bool{}
	for _, dev := range f.devices {
		if !allowedDevices[dev] {
			return fail(fmt.Sprintf("-device %q is not an admitted device", dev))
		}
		if seenDevice[dev] {
			return fail(fmt.Sprintf("duplicate -device %q", dev))
		}
		seenDevice[dev] = true
	}
	// Pflash cardinality: at most one code drive and one vars drive. Two
	// code drives used to overwrite each other silently, admitting a
	// record no generator emits.
	if f.codeCount > 1 {
		return fail(fmt.Sprintf("%d pflash code drives, want at most 1", f.codeCount))
	}
	if f.varsCount > 1 {
		return fail(fmt.Sprintf("%d pflash vars drives, want at most 1", f.varsCount))
	}
	// Firmware: uefi requires its AUTHORITATIVE code identity — the
	// admitted path whose current bytes still match the admitted digest.
	// Presence on disk alone would admit a replacement code.fd as the
	// same definition. Bios forbids any pflash.
	if meta.Firmware == "uefi" {
		if meta.FirmwareCode == "" || meta.FirmwareCodeSHA256 == "" {
			return fail("records no admitted firmware code identity (predates firmware-code binding)")
		}
		if f.codeFile == "" {
			return fail("no pflash code drive for a uefi VM")
		}
		if f.codeFile != meta.FirmwareCode {
			return fail(fmt.Sprintf("uefi code %q is not the admitted firmware %q", f.codeFile, meta.FirmwareCode))
		}
		if sum, err := firmwareSHA256(f.codeFile); err != nil || sum != meta.FirmwareCodeSHA256 {
			return fail(fmt.Sprintf("uefi code %q bytes do not match the admitted firmware digest", f.codeFile))
		}
		if f.varsFile != "" && f.varsFile != filepath.Join(vmDir, "vars.fd") {
			return fail(fmt.Sprintf("uefi vars %q, want the VM's own vars.fd", f.varsFile))
		}
	} else if f.codeFile != "" || f.varsFile != "" {
		return fail("pflash drives on a bios VM")
	}
	// Boot media: an ISO is admitted iff creation recorded one, exactly
	// it — and the admitted ISO boot carries exactly one -boot
	// once=d,menu=on. The cdrom identity is checked before the boot
	// shape, so a wrong disk names the disk.
	switch {
	case meta.HasISO && len(f.cdroms) != 1:
		return fail(fmt.Sprintf("%d -cdrom drives, want the recorded ISO", len(f.cdroms)))
	case meta.HasISO && f.cdroms[0] != meta.ISO:
		return fail(fmt.Sprintf("cdrom %q, want %q", f.cdroms[0], meta.ISO))
	case !meta.HasISO && len(f.cdroms) > 0:
		return fail(fmt.Sprintf("unadmitted cdrom %q", f.cdroms[0]))
	}
	switch {
	case meta.HasISO && !f.hasBoot:
		return fail("no -boot for a VM with an admitted ISO (want once=d,menu=on)")
	case meta.HasISO && f.boot != "once=d,menu=on":
		return fail(fmt.Sprintf("-boot %q, want once=d,menu=on", f.boot))
	case !meta.HasISO && f.hasBoot:
		return fail(fmt.Sprintf("unadmitted -boot %q (no ISO recorded)", f.boot))
	}
	return nil
}

// loadedUnitFiles asks the manager which fragment it loads for svc and
// which drop-ins extend it. It reports ok=false on any query failure or
// unparsable answer — uncertainty, never an identity. (Parsed from the same
// key=value `systemctl show` shape unitActivation already uses.)
//
// The evidence must be complete AND unique: both keys must be present
// exactly once (a present-but-empty DropInPaths proves no drop-ins, but a
// missing key proves nothing, and a repeated key is malformed evidence,
// not a value to last-win), and the drop-in list itself must hold no
// repeated path. Treating a missing key as empty admitted an unverified
// load; silently overwriting duplicates would admit unverified content.
func loadedUnitFiles(svc string) (fragment, dropins string, ok bool) {
	out, err := systemctlRun(context.Background(), "show", "-p", "FragmentPath", "-p", "DropInPaths", svc)
	if err != nil {
		return "", "", false
	}
	var fragFound, dropFound bool
	for _, line := range strings.Split(string(out), "\n") {
		if v, found := strings.CutPrefix(line, "FragmentPath="); found {
			if fragFound {
				return "", "", false
			}
			fragment = strings.TrimSpace(v)
			fragFound = true
		}
		if v, found := strings.CutPrefix(line, "DropInPaths="); found {
			if dropFound {
				return "", "", false
			}
			dropins = strings.TrimSpace(v)
			dropFound = true
		}
	}
	if !fragFound || fragment == "" || fragment == "n/a" {
		return "", "", false
	}
	if !dropFound {
		return "", "", false
	}
	seen := map[string]bool{}
	for _, p := range strings.Fields(dropins) {
		if seen[p] {
			return "", "", false
		}
		seen[p] = true
	}
	return fragment, dropins, true
}

// validateUnitMatches proves the systemd unit (where one exists) executes
// exactly the recorded command. A missing unit refuses only where systemd
// would run it; a direct-only host starts from command.json and needs no
// unit. (Whitespace split: the generator never quotes, so paths with spaces
// already break unit rendering — a pre-existing limitation, not a new one.)
//
// Where systemd would run the unit, the on-disk match is necessary but not
// sufficient: a changed cached unit or a drop-in can execute a different
// command despite the main file matching. The manager's load is therefore
// refreshed from the known-good file and the LOADED unit is proven to be
// exactly it — the fragment it executes plus no drop-ins.
func validateUnitMatches(name string, cmd launchCommand) error {
	svc := "corral-" + name
	unitPath := filepath.Join(systemdUserDir(), svc+".service")
	data, err := os.ReadFile(unitPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("VM %q: unit file %s is unreadable (%v) — refusing to start it", name, unitPath, err)
		}
		if userSystemd() {
			return fmt.Errorf("VM %q has no systemd unit but this host starts VMs through systemd — refusing to start it (recreate it to re-admit)", name)
		}
		return nil
	}
	// The unit's executed shape, parsed COMPLETELY: every ExecStart line
	// counts (exactly one, the admitted command — a first-match scan
	// ignored siblings), and every other command-shaping directive
	// refuses. The generator emits one bare ExecStart and nothing else
	// from this family, so anything more is not the admitted unit.
	// Detection normalizes whitespace around the key (indented and
	// spaced `ExecStart = ...` spellings execute, so they count);
	// comments and blank lines never count.
	var execStarts []string
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		dirName := line
		value := ""
		if i := strings.Index(line, "="); i >= 0 {
			dirName = strings.TrimSpace(line[:i])
			value = strings.TrimSpace(line[i+1:])
		}
		switch {
		case dirName == "ExecStart":
			execStarts = append(execStarts, value)
		case unadmittedExecDirective[dirName]:
			return fmt.Errorf("VM %q: unit file %s carries unadmitted directive %q — refusing to start it (recreate it to re-admit)", name, unitPath, dirName)
		}
	}
	if len(execStarts) == 0 {
		return fmt.Errorf("VM %q: unit file %s has no ExecStart — refusing to start it (recreate it to re-admit)", name, unitPath)
	}
	if len(execStarts) > 1 {
		return fmt.Errorf("VM %q: unit file %s carries %d ExecStart directives — refusing to start it (recreate it to re-admit)", name, unitPath, len(execStarts))
	}
	execStart := execStarts[0]
	if execStart == "" {
		return fmt.Errorf("VM %q: unit file %s has no ExecStart — refusing to start it (recreate it to re-admit)", name, unitPath)
	}
	fields := strings.Fields(execStart)
	if len(fields) == 0 || fields[0] != cmd.Binary || !equalStrings(fields[1:], cmd.Args) {
		return fmt.Errorf("VM %q: unit file %s executes a different command than recorded — refusing to start it (recreate it to re-admit)", name, unitPath)
	}
	if !userSystemd() {
		return nil
	}
	// The manager runs this unit: reload the known-good file first (a
	// stale load of an older revision must not survive into the check),
	// then prove the loaded unit is exactly this file with no drop-ins.
	if out, err := systemctlRun(context.Background(), "daemon-reload"); err != nil {
		return fmt.Errorf("VM %q: cannot refresh the manager load of %s (%w: %s) — run `systemctl --user daemon-reload` and retry", name, unitPath, err, strings.TrimSpace(string(out)))
	}
	fragment, dropins, ok := loadedUnitFiles(svc)
	if !ok {
		return fmt.Errorf("VM %q: cannot prove the manager loads %s — refusing to start on an unverifiable unit load (run `systemctl --user daemon-reload` and retry)", name, unitPath)
	}
	if fragment != unitPath {
		return fmt.Errorf("VM %q: the manager loads %q, not the admitted unit %s — refusing to start it (recreate it to re-admit)", name, fragment, unitPath)
	}
	if dropins != "" {
		return fmt.Errorf("VM %q: the loaded unit has unadmitted drop-ins %q — refusing to start it (recreate it to re-admit)", name, dropins)
	}
	return nil
}

// unadmittedExecDirective is the command-shaping directive family the
// admitted unit never carries: pre/post/condition wrappers around the
// start, reload/stop commands, and environment shaping (an
// Environment=LD_PRELOAD rewrites the executed process as surely as a
// second ExecStart). Purely supervisory directives (Restart, WantedBy,
// StandardOutput, ...) are not command shape and stay ignored: they
// cannot widen what the unit executes.
var unadmittedExecDirective = map[string]bool{
	"ExecStartPre":     true,
	"ExecStartPost":    true,
	"ExecCondition":    true,
	"ExecReload":       true,
	"ExecStop":         true,
	"ExecStopPost":     true,
	"Environment":      true,
	"EnvironmentFile":  true,
	"PassEnvironment":  true,
	"UnsetEnvironment": true,
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
