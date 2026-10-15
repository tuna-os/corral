package bootc

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// ── local builder ─────────────────────────────────────────────────
//
// `bootc install to-disk` run from the image itself, in a privileged podman
// container. This is upstream's own documented method: the bootc image
// contains the installer, so the container is given the target file and asked
// to partition and populate it.
//
// Privileged and root are both unavoidable. bootc install partitions a block
// device, creates filesystems, and installs a bootloader — it needs loop
// devices, mount, and SELinux relabelling. Rootless podman cannot do any of
// it, so this checks for what it needs up front and says so rather than
// failing four gigabytes into a pull.

type LocalBuilder struct {
	// Sudo runs podman through sudo. Required unless the caller is already
	// root, since the container needs privileges rootless cannot grant.
	Sudo bool
}

func (b LocalBuilder) Available() error {
	if _, err := exec.LookPath("podman"); err != nil {
		return unsupported("the local builder",
			"podman is not installed, and bootc install runs from inside the image",
			"install podman")
	}
	if os.Geteuid() != 0 && !b.Sudo {
		return unsupported("the local builder",
			"bootc install partitions a disk and installs a bootloader, which needs root",
			"re-run with --sudo, or as root")
	}
	if _, err := os.Stat("/dev/loop-control"); err != nil {
		return unsupported("the local builder",
			"the kernel exposes no loop devices, and bootc install needs one to write a disk image",
			"load the loop module (modprobe loop)")
	}
	return nil
}

// podman builds the command, honouring Sudo.
func (b LocalBuilder) podman(args ...string) (string, []string) {
	if b.Sudo && os.Geteuid() != 0 {
		return "sudo", append([]string{"-n", "podman"}, args...)
	}
	return "podman", args
}

func (b LocalBuilder) Build(req BuildRequest, progress func(string)) (BuildResult, error) {
	if err := b.Available(); err != nil {
		return BuildResult{}, err
	}
	if req.Image == "" {
		return BuildResult{}, fmt.Errorf("a bootc container image is required")
	}
	if req.Dest == "" {
		return BuildResult{}, fmt.Errorf("a destination disk path is required")
	}
	if err := os.MkdirAll(filepath.Dir(req.Dest), 0o755); err != nil {
		return BuildResult{}, err
	}

	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}

	if err := b.fetch(req.Image, report); err != nil {
		return BuildResult{}, err
	}

	backend, err := b.DetectBackend(req.Image)
	if err != nil {
		return BuildResult{}, err
	}
	if req.Filesystem != "" {
		backend.Filesystem = req.Filesystem
	}
	report(fmt.Sprintf("image uses the %s backend (%s)", backend.Kind, backend.Filesystem))

	keyPath, cleanupKey, err := writeKey(req.SSHKey)
	if err != nil {
		return BuildResult{}, err
	}
	defer cleanupKey()

	// bootc writes into the file it is given, so it has to exist at full size
	// first — it partitions what is there rather than growing it.
	size := req.Size
	if size == "" {
		size = "20G"
	}
	report(fmt.Sprintf("allocating a %s disk at %s", size, req.Dest))
	if err := allocate(req.Dest, size); err != nil {
		return BuildResult{}, err
	}

	report("running bootc install to-disk (this takes a few minutes)")
	name, args := b.podman(b.installArgs(req, backend, keyPath)...)
	if out, err := runner.Run(name, args...); err != nil {
		// Leave nothing half-written: a truncated disk that looks like a disk
		// is worse than no disk.
		os.Remove(req.Dest)
		return BuildResult{}, installError(out, err)
	}
	if backend.Kind == "composefs" {
		report("composefs: restoring the kernel and initrd on the ESP")
		name, args := b.podman(b.composefsFixupArgs(req, keyPath)...)
		if out, err := runner.Run(name, args...); err != nil {
			os.Remove(req.Dest)
			return BuildResult{}, fmt.Errorf("finishing the composefs disk: %s", commandError(out, err))
		}
	}

	info, err := os.Stat(req.Dest)
	if err != nil {
		return BuildResult{}, fmt.Errorf("bootc reported success but %s is missing: %w", req.Dest, err)
	}
	report(fmt.Sprintf("built %s (%d bytes)", req.Dest, info.Size()))
	return BuildResult{Path: req.Dest, Format: "raw", Bytes: info.Size(), Backend: backend.Kind}, nil
}

// fetch makes sure the image is in local storage, pulling it if it has to.
//
// Its own method, like installArgs, so a test can exercise it on any host: the
// decisions here are about references and storage, and none of them need the
// root and the loop devices that Build as a whole does.
//
// Pulling is its own step because it is the slow part by far, and a failure
// here means "bad reference or no network" — worth telling apart from a failure
// during the install.
func (b LocalBuilder) fetch(image string, progress func(string)) error {
	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}
	// A localhost/ reference names an image that only exists in local storage.
	// podman reads it as a registry called "localhost" and fails against
	// https://localhost/v2/. A locally built image is the whole point of
	// `podman build` then `bootc install`, so it must not be pulled.
	if isLocalRef(image) {
		report("using " + image + " from local storage")
		return nil
	}
	report("pulling " + image)
	name, args := b.podman("pull", image)
	out, err := runner.Run(name, args...)
	if err == nil {
		return nil
	}
	// A pull that fails over an image already in storage is a network problem,
	// not a missing image. Say so and carry on rather than discarding a usable
	// local copy.
	if !b.imageExists(image) {
		return fmt.Errorf("podman pull %s: %s", image, commandError(out, err))
	}
	report(fmt.Sprintf("could not pull %s (%s); using the copy in local storage",
		image, firstLineOf(commandError(out, err))))
	return nil
}

// installError turns a failed `bootc install` into the error a reader can act
// on. Pure, so a test needs no container.
func installError(out []byte, err error) error {
	message := commandError(out, err)
	// bootc re-execs itself in the host's mount namespace, which a nested
	// container does not have. The error it prints names a pid and a
	// permission, and says nothing about where to run this instead.
	if strings.Contains(message, "mountns") || strings.Contains(message, "mount namespace") {
		return fmt.Errorf("bootc install to-disk: %s\n"+
			"bootc install needs the host's mount namespace, which a container inside a container does not have — "+
			"run this on the host, or on a CI runner rather than in a container on one", message)
	}
	return fmt.Errorf("bootc install to-disk: %s", message)
}

// probeCommand is the placeholder argv given to `podman create` when the only
// purpose is to copy files back out of the image.
//
// A bootc OS image is not an application container, so it ships neither CMD
// nor ENTRYPOINT, and `podman create` refuses such an image outright: "no
// command or entrypoint provided, and no CMD or ENTRYPOINT from image". That
// rejected the whole Universal Blue family — the images these probes were
// written for.
//
// Any argv satisfies podman, because nothing here ever starts the container.
// The path deliberately does not exist, so a container that somehow did start
// would fail loudly rather than run something real. `--entrypoint ""` does not
// work: podman still reports the image as having no command.
var probeCommand = []string{"/corral-probe-does-not-execute"}

// IsLocalRef reports whether a reference names an image that exists only in
// local storage, which no registry can serve.
func IsLocalRef(image string) bool { return isLocalRef(image) }

func isLocalRef(image string) bool {
	return strings.HasPrefix(image, "localhost/") || strings.HasPrefix(image, "containers-storage:")
}

// imageExists reports whether podman already has the image.
func (b LocalBuilder) imageExists(image string) bool {
	name, args := b.podman("image", "exists", image)
	_, err := runner.Run(name, args...)
	return err == nil
}

// firstLineOf keeps an error message to one line: podman's pull failures are
// several lines of retries that say the same thing.
func firstLineOf(msg string) string {
	if line, _, found := strings.Cut(msg, "\n"); found {
		return line
	}
	return msg
}

// Backend is the bootc storage backend an image needs. It is a property of
// the image, not a choice: GRUB update payloads identify OSTree; systemd-boot
// without those payloads needs composefs and a filesystem with fs-verity.
type Backend struct {
	Kind       string // "ostree" or "composefs"
	Filesystem string
	Flag       string // --generic-image or --composefs-backend
}

var (
	ostreeBackend    = Backend{Kind: "ostree", Filesystem: "xfs", Flag: "--generic-image"}
	composefsBackend = Backend{Kind: "composefs", Filesystem: "btrfs", Flag: "--composefs-backend"}
)

// DetectBackend reads the image to decide which backend it needs, mirroring
// what pkg/kubevirt's cluster builder does — the rule was worked out there
// (f63917e) and getting it wrong produces a disk that does not boot.
//
// GRUB update payloads identify traditional OSTree images. A bootupd
// executable alone does not: Marlin also ships it with systemd-boot.
//
// The probe inspects the image *filesystem* with `podman cp` and never
// executes anything in it. Universal Blue desktop images ship Rust uutils,
// where /usr/bin/test is a symlink into a multicall binary that podman
// --entrypoint cannot dispatch, so any "run a binary in the image" probe
// misdetects exactly the images this matters most for.
func (b LocalBuilder) DetectBackend(image string) (Backend, error) {
	name, args := b.podman(append([]string{"create", image}, probeCommand...)...)
	out, err := runner.Run(name, args...)
	if err != nil {
		return Backend{}, fmt.Errorf("podman create %s: %s", image, commandError(out, err))
	}
	container := strings.TrimSpace(lastLine(string(out)))
	defer func() {
		rm, rmArgs := b.podman("rm", "-f", container)
		_, _ = runner.Run(rm, rmArgs...)
	}()

	has := func(path string) bool {
		cp, cpArgs := b.podman("cp", container+":"+path, "-")
		_, err := runner.Run(cp, cpArgs...)
		return err == nil
	}
	// bootupctl alone is not a payload: Marlin ships the binary alongside
	// systemd-boot, without GRUB update metadata. Inspect both bootupd layouts.
	containsEFI := func(dir, prefix string) bool {
		cp, args := b.podman("cp", container+":"+dir, "-")
		data, err := runner.Run(cp, args...)
		if err != nil {
			return false
		}
		tr := tar.NewReader(bytes.NewReader(data))
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return false
			}
			if err != nil {
				return false
			}
			name := path.Base(h.Name)
			if h.Typeflag == tar.TypeReg && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".efi") {
				return true
			}
		}
	}
	payload := containsEFI("/usr/lib/bootupd/updates/EFI", "grub") ||
		(has("/usr/lib/bootupd/updates/EFI.json") && containsEFI("/usr/lib/efi/grub2", "grub") && containsEFI("/usr/lib/efi/shim", "shim"))
	backend := ostreeBackend
	if !payload {
		if has("/usr/lib/systemd/boot/efi/systemd-bootx64.efi") || has("/usr/lib/systemd/boot/efi/systemd-bootaa64.efi") {
			backend = composefsBackend
		} else if !has("/usr/sbin/bootupctl") && !has("/usr/bin/bootupctl") {
			return Backend{}, fmt.Errorf("image has no supported bootloader payload")
		}
	}
	// Ext4 provides fs-verity for sealed roots; keep the existing btrfs
	// default when the image only supplies that formatter.
	if backend.Kind == "composefs" && (has("/usr/sbin/mkfs.ext4") || has("/usr/bin/mkfs.ext4")) {
		backend.Filesystem = "ext4"
	}
	if backend.Kind == "ostree" && !has("/usr/sbin/mkfs.xfs") && !has("/usr/bin/mkfs.xfs") && (has("/usr/sbin/mkfs.btrfs") || has("/usr/bin/mkfs.btrfs")) {
		backend.Filesystem = "btrfs"
	}
	return backend, nil
}

// installArgs builds the podman invocation. Split out so a test can assert the
// flags without running anything: every one of them is load-bearing and a
// silently dropped one produces a disk that does not boot.
func (b LocalBuilder) installArgs(req BuildRequest, backend Backend, keyPath string) []string {
	dest, _ := filepath.Abs(req.Dest)
	args := []string{
		"run", "--rm",
		// bootc install needs to partition, mount, and relabel.
		"--privileged",
		"--pid=host",
		// The container writes the host's disk file, and needs the host's
		// /dev to reach loop devices.
		"-v", "/dev:/dev",
		"-v", "/var/lib/containers:/var/lib/containers",
		"-v", dest + ":/target.img",
		"--security-opt", "label=type:unconfined_t",
	}
	if keyPath != "" {
		args = append(args, "-v", keyPath+":/buildkey.pub:ro")
	}
	args = append(args, req.Image,
		"bootc", "install", "to-disk",
		// A file, not a block device: --via-loopback is what makes bootc
		// attach the image file as a loop device rather than refusing.
		//
		// The cluster builder cannot use this — pod security contexts there
		// lack loop module access, which is what e141dbb worked around — but a
		// privileged container on a real host has loop devices, so the simpler
		// route is available here.
		"--via-loopback",
		// The backend flag is not cosmetic. For ostree images bootupd installs
		// the bootloader to EFI/<vendor>/ plus an efibootmgr NVRAM entry, and a
		// fresh VM has empty NVRAM — so without --generic-image's removable
		// EFI/BOOT/BOOTX64.EFI fallback the disk builds fine and then boots to
		// nothing.
		backend.Flag,
		"--filesystem", backend.Filesystem,
		// Without this bootc refuses to overwrite a disk that already has a
		// partition table — and the file was just allocated, so there is
		// nothing to preserve.
		"--wipe",
	)
	for _, karg := range req.Kargs {
		if karg = strings.TrimSpace(karg); karg != "" {
			args = append(args, "--karg", karg)
		}
	}
	if keyPath != "" {
		args = append(args, "--root-ssh-authorized-keys", "/buildkey.pub")
	}
	args = append(args, "/target.img")
	return args
}

// composefsFixupArgs builds the second container run a composefs disk needs
// after `bootc install`, which the cluster builder (pkg/kubevirt/bootc.go)
// does in its builder VM:
//
//   - bootc writes the ESP's kernel and initrd out of its EROFS store, which
//     zero-fills files past the inline threshold. The 70 MB+ initrd on the ESP
//     is then not the one in the image, the boot runs a garbled initramfs, and
//     it ends in a dracut emergency shell. The real bytes are copied back from
//     the image's own /usr/lib/modules.
//   - --root-ssh-authorized-keys does nothing on composefs. /root is
//     /var/roothome, and /var comes from state/os/default/var on the root
//     partition, so the key is written there.
//
// Partitions are found by what they hold, not by number: the ESP is the one
// with bootc_composefs-* entries, the root the one with state/os/default. The
// script fails when either is missing, because a disk without them does not
// boot, or cannot be reached.
func (b LocalBuilder) composefsFixupArgs(req BuildRequest, keyPath string) []string {
	dest, _ := filepath.Abs(req.Dest)
	args := []string{
		"run", "--rm",
		// losetup and mount on the disk file, as the install itself did.
		"--privileged",
		"-v", "/dev:/dev",
		"-v", dest + ":/target.img",
		"--security-opt", "label=type:unconfined_t",
	}
	if keyPath != "" {
		args = append(args, "-v", keyPath+":/buildkey.pub:ro")
	}
	return append(args, req.Image, "sh", "-c", composefsFixupScript)
}

// composefsFixupScript runs inside the image under test, so the kernel it
// copies is the image's own.
const composefsFixupScript = `set -eu
KDIR=
for d in /usr/lib/modules/*/; do
  if [ -f "${d}vmlinuz" ] && [ -f "${d}initramfs.img" ]; then KDIR=$d; break; fi
done
[ -n "$KDIR" ] || { echo "corral: no kernel with an initramfs under /usr/lib/modules" >&2; exit 1; }
LOOP=$(losetup --find --show --partscan /target.img)
MNT=$(mktemp -d)
trap 'umount "$MNT" 2>/dev/null || true; losetup -d "$LOOP" 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do
  ls "${LOOP}"p* >/dev/null 2>&1 && break
  sleep 1
done
ESP=0; ROOT=0
for part in "${LOOP}"p*; do
  mount "$part" "$MNT" 2>/dev/null || continue
  for entry in "$MNT"/EFI/Linux/bootc_composefs-*; do
    [ -d "$entry" ] || continue
    cp -f "${KDIR}vmlinuz" "$entry/vmlinuz"
    cp -f "${KDIR}initramfs.img" "$entry/initrd"
    ESP=1
  done
  if [ -d "$MNT/state/os/default" ]; then
    ROOT=1
    if [ -f /buildkey.pub ]; then
      SSHDIR="$MNT/state/os/default/var/roothome/.ssh"
      mkdir -p "$SSHDIR"
      chmod 700 "$SSHDIR"
      cp /buildkey.pub "$SSHDIR/authorized_keys"
      chmod 600 "$SSHDIR/authorized_keys"
      chown -R 0:0 "$SSHDIR"
    fi
  fi
  sync
  umount "$MNT"
done
[ "$ESP" = 1 ] || { echo "corral: no bootc_composefs-* entry on any partition, so there is no kernel to restore" >&2; exit 1; }
[ "$ROOT" = 1 ] || { echo "corral: no partition holds state/os/default, so the root SSH key has nowhere to go" >&2; exit 1; }
echo "corral: restored $(basename "$KDIR") on the ESP"
`

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// allocate creates a sparse file of the requested size. Sparse so a 20G disk
// costs only what is written — bootc populates a few gigabytes at most.
func allocate(path, size string) error {
	bytes, err := parseSize(size)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(bytes); err != nil {
		os.Remove(path)
		return fmt.Errorf("allocating %s: %w", path, err)
	}
	return nil
}

// parseSize accepts the sizes the rest of Corral uses: 20G, 20Gi, 20480M.
func parseSize(s string) (int64, error) {
	value := strings.TrimSpace(s)
	if value == "" {
		return 0, fmt.Errorf("empty size")
	}
	var multiplier int64 = 1
	upper := strings.ToUpper(value)
	switch {
	case strings.HasSuffix(upper, "GIB"), strings.HasSuffix(upper, "GI"), strings.HasSuffix(upper, "G"):
		multiplier = 1 << 30
	case strings.HasSuffix(upper, "MIB"), strings.HasSuffix(upper, "MI"), strings.HasSuffix(upper, "M"):
		multiplier = 1 << 20
	case strings.HasSuffix(upper, "TIB"), strings.HasSuffix(upper, "TI"), strings.HasSuffix(upper, "T"):
		multiplier = 1 << 40
	}
	digits := strings.TrimRight(upper, "GIMBT")
	var n int64
	if _, err := fmt.Sscanf(digits, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a size like 20G", s)
	}
	return n * multiplier, nil
}

// ── local QEMU target ─────────────────────────────────────────────
//
// The disk is converted to qcow2 and placed where pkg/qemu expects a VM's
// disk, then the VM is created with ExistingDisk so Create adopts it rather
// than making a fresh one.

type QEMUTarget struct{}

func (QEMUTarget) Requires() []string {
	return []string{"qemu-system-x86_64 and qemu-img", "/dev/kvm for acceptable speed"}
}

func (QEMUTarget) Import(ref types.InstanceRef, disk string, opts CreateOpts) error {
	if ref.Context != "" {
		return unsupported("the local qemu backend", "it has no contexts", "drop --context for local VMs")
	}
	if _, err := os.Stat(disk); err != nil {
		return fmt.Errorf("built disk %s is missing: %w", disk, err)
	}
	vmDir := filepath.Join(vmHome(), ref.Name)
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		return err
	}
	// qcow2 rather than the raw bootc output: sparse on disk, snapshot-capable
	// (pkg/snapshot's local adapter needs qcow2), and what pkg/qemu expects.
	target := filepath.Join(vmDir, "disk.qcow2")
	if out, err := runner.Run(qemuImg(), "convert", "-O", "qcow2", disk, target); err != nil {
		return fmt.Errorf("qemu-img convert: %s", commandError(out, err))
	}
	return createLocalVM(types.CreateOpts{
		Name: ref.Name, Backend: "qemu",
		CPU: opts.CPU, Mem: opts.Memory, Disk: opts.Disk,
		SSHPublicKey: opts.SSHKey,
		// The disk is already built and populated; Create must adopt it.
		ExistingDisk: true,
		// And Create must not refuse the directory this function just made.
		// Exists() is true the moment the disk is written into it, so without
		// Force every import fails with "VM already exists" — a VM that only
		// exists because we are in the middle of creating it. Whether an older
		// VM of the same name may be replaced is the caller's decision, taken
		// before the build; by here the disk is built and the name is claimed.
		Force: true,
		UEFI:  opts.UEFI,
	})
}

// ── libvirt target ────────────────────────────────────────────────
//
// A UEFI domain around the converted disk. UEFI because bootc images install
// a UEFI bootloader — a BIOS domain simply will not boot one, and the symptom
// is a blank screen rather than an error.

type LibvirtTarget struct{}

func (LibvirtTarget) Requires() []string {
	return []string{
		"virsh and a reachable libvirt connection",
		"OVMF/edk2 firmware — bootc images boot via UEFI",
		"qemu-img",
	}
}

// DomainXML is exported so the definition can be inspected and tested without
// defining anything. The uri scopes firmware resolution: it is the same
// connection Import will define the domain on.
//
// For a local target (qemu:///system, qemu:///session, or empty for the
// default), when OVMF firmware resolves to concrete host files (the distro
// candidate table in pkg/qemu, overridable with CORRAL_OVMF_CODE/VARS), the
// domain names the loader explicitly instead of asking libvirt to
// auto-select with <os firmware='efi'>. Auto-selection depends on the
// daemon's firmware-descriptor cache, which is not uniformly populated: on
// the same host a qemu:///session daemon can resolve nothing while
// qemu:///system defines fine, failing the define with "Unable to find 'efi'
// firmware" (e2e-incus #482). An explicit pflash loader defines on either
// local URI; the <nvram> carries only the vars template and libvirt
// allocates the per-domain NVRAM file itself, which keeps this working for
// both the system daemon (root-owned nvram dir) and the session daemon
// (user-owned one).
//
// For a remote target (e.g. qemu+ssh://host/system) the manager's local
// files mean nothing — the firmware must exist on the hypervisor — so the
// domain always keeps firmware='efi' auto-selection and lets the target
// daemon resolve against its own descriptors. Local hosts without
// discoverable firmware fall back the same way, so descriptor-only hosts
// keep working.
func (LibvirtTarget) DomainXML(uri, name, diskPath string, opts CreateOpts) string {
	memory := opts.Memory
	if memory == "" {
		memory = "4Gi"
	}
	cpu := opts.CPU
	if cpu <= 0 {
		cpu = 2
	}
	osXML := `  <os firmware='efi'>
    <type arch='x86_64' machine='q35'>hvm</type>
    <boot dev='hd'/>
  </os>`
	if localLibvirtURI(uri) {
		if code, vars, err := qemu.FindUEFIFirmware(); err == nil {
			var b strings.Builder
			b.WriteString("  <os>\n    <type arch='x86_64' machine='q35'>hvm</type>\n")
			fmt.Fprintf(&b, "    <loader readonly='yes' type='pflash'>%s</loader>\n", xmlEscape(code))
			if vars != "" {
				fmt.Fprintf(&b, "    <nvram template='%s'/>\n", xmlEscape(vars))
			}
			b.WriteString("    <boot dev='hd'/>\n  </os>")
			osXML = b.String()
		}
	}

	return fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='MiB'>%s</memory>
  <vcpu>%d</vcpu>
%s
  <features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <interface type='network'>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <graphics type='vnc' port='-1' listen='127.0.0.1'/>
    <video><model type='virtio'/></video>
    <console type='pty'/>
  </devices>
</domain>
`, xmlEscape(name), memoryMiB(memory), cpu, osXML, xmlEscape(diskPath))
}

// localLibvirtURI reports whether uri names a libvirt daemon on this host,
// where files this manager can stat are files the daemon can use. Anything
// else (qemu+ssh, qemu+tcp, …) defines on a remote hypervisor whose
// filesystem we cannot see, so firmware must auto-select there.
func localLibvirtURI(uri string) bool {
	switch uri {
	case "", "qemu:///system", "qemu:///session":
		return true
	}
	return false
}

func (l LibvirtTarget) Import(ref types.InstanceRef, disk string, opts CreateOpts) error {
	if _, err := os.Stat(disk); err != nil {
		return fmt.Errorf("built disk %s is missing: %w", disk, err)
	}
	target := filepath.Join(filepath.Dir(disk), ref.Name+".qcow2")
	if out, err := runner.Run(qemuImg(), "convert", "-O", "qcow2", disk, target); err != nil {
		return fmt.Errorf("qemu-img convert: %s", commandError(out, err))
	}
	uri := ref.Context
	if uri == "" {
		uri = "qemu:///system"
	}
	doc := l.DomainXML(uri, ref.Name, target, opts)
	path, cleanup, err := writeTemp(doc)
	if err != nil {
		return err
	}
	defer cleanup()
	if out, err := runner.Run("virsh", "-c", uri, "define", path); err != nil {
		return fmt.Errorf("virsh define: %s", commandError(out, err))
	}
	return nil
}
