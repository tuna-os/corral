// corral-bootc is the bootc Corral plugin: it builds a bootable container
// image into a VM disk and boots it. Installed via the Corral marketplace
// (`corral plugin install bootc`) and invoked as `corral bootc`.
//
// Two paths, chosen by the selected context (#130):
//
//   - **KubeVirt** — the original: a privileged Job on the cluster runs
//     `bootc install to-disk` into a PVC, and the VM is created around it.
//     The cluster does the work, so the workstation need not be able to.
//   - **Everything else** — pkg/bootc builds the disk here with podman and
//     hands it to the backend's target: local QEMU adopts it as a VM's disk,
//     libvirt defines a UEFI domain around it. Incus is a documented
//     rejection; see bootc.TargetFor.
//
// It's a separate binary built with `-tags bootc`, so the cluster pipeline in
// pkg/kubevirt (//go:build bootc) is registered here but stays out of the lean
// core binary.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/bootc"
	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/plugin/sdk"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/types"
	"golang.org/x/term"
)

// finishVM applies the final VM from a completed build's disk PVC, exposes
// it on the tailnet, and records it in the registry — the part of `create`
// that's shared between a fresh build and a resumed one (--resume).
// placement adds dedicated CPUs and tags; the builder never gets them.
func finishVM(name, ns, pvcName, image, key, mem string, cpu int, node string, placement kubevirt.Placement) error {
	vm := kubevirt.GenerateBootcVM(name, ns, pvcName, image, key, mem, cpu, node)
	if vm == nil {
		return fmt.Errorf("bootc VM manifest unavailable")
	}
	kubevirt.ApplyPlacement(vm, placement)
	if err := kubevirt.Apply(vm); err != nil {
		return fmt.Errorf("creating VM: %w", err)
	}
	// Expose SSH/VNC/RDP on the tailnet via the Tailscale operator proxy.
	if err := kubevirt.ApplyProxy(name, ns, kubevirt.ConsolePorts); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tailnet expose failed: %v\n", err)
	}
	if store, err := registry.NewStore(); err == nil {
		store.Set(name, types.RegistryEntry{
			Backend:   "kubevirt",
			Namespace: ns,
		})
	}
	fmt.Fprintf(os.Stderr, "VM %q created in ns/%s — start: corral start %s\n", name, ns, name)
	return nil
}

// resolveContext picks the Corral context to build for.
func resolveContext(name string) (config.ContextConfig, error) {
	if name == "" {
		return config.DefaultContext(), nil
	}
	found, ok := config.FindContext(name)
	if !ok {
		return config.ContextConfig{}, fmt.Errorf("unknown context %q (see corral context ls)", name)
	}
	return found, nil
}

// newBuilder and targetFor are indirection vars over bootc.LocalBuilder and
// bootc.TargetFor so tests can inject fakes without touching podman or a
// backend's storage. Production behavior is unchanged.
var newBuilder = func(sudo bool) bootc.Builder { return bootc.LocalBuilder{Sudo: sudo} }

var targetFor = bootc.TargetFor

// buildAndImport is the off-cluster path: build the disk here with podman,
// then hand it to the backend's target. The split is the point of #130 — the
// same built disk works for local QEMU and for libvirt, and neither needs a
// cluster to produce it.
func buildAndImport(name string, target config.ContextConfig, image, sshKey, disk, mem string, cpu int, filesystem string, kargs, customize []string, provisionScript string, useSudo bool) error {
	if image == "" {
		return fmt.Errorf("--image is required — a catalog name (see `corral bootc images`) or an OCI ref like quay.io/fedora/fedora-bootc:41")
	}
	image = catalog.ResolveBootc(image)

	importer, err := targetFor(target.Backend)
	if err != nil {
		return err
	}
	builder := newBuilder(useSudo)
	if err := builder.Available(); err != nil {
		// Report what the target needs too, so one run tells the operator
		// everything to install rather than one thing per attempt.
		fmt.Fprintf(os.Stderr, "the %s target also needs:\n", target.Backend)
		for _, req := range importer.Requires() {
			fmt.Fprintf(os.Stderr, "  - %s\n", req)
		}
		return err
	}

	key := sshKey
	if key == "" {
		key = kubevirt.LoadSSHPublicKey()
	}
	size := disk
	if size == "" {
		size = "20G"
	}

	// The disk is built into the image cache and consumed by Import, which
	// converts it into the backend's own storage.
	workDir := filepath.Join(qemu.VMHome(), "cache", "bootc")
	built, err := builder.Build(bootc.BuildRequest{
		Image: image, Dest: filepath.Join(workDir, name+".raw"),
		Size: size, SSHKey: key, Filesystem: filesystem, Kargs: kargs,
		Customize: customize, ProvisionScript: provisionScript,
	}, func(msg string) { fmt.Fprintln(os.Stderr, msg) })
	if err != nil {
		return err
	}
	defer os.Remove(built.Path)

	ref := types.InstanceRef{Backend: target.Backend, Context: target.Context, Name: name}
	if err := importer.Import(ref, built.Path, bootc.CreateOpts{
		CPU: cpu, Memory: mem, Disk: size, SSHKey: key,
	}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "created %s from %s\n", ref.String(), image)
	return nil
}

// checkResumeKargs refuses a --resume that asks for kernel arguments the
// interrupted build did not install. A resume reuses the finished disk and
// runs no `bootc install`, so it has no way to add one.
func checkResumeKargs(name string, requested, installed []string) error {
	missing := kubevirt.MissingKargs(requested, installed)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("the resumable build for %q did not install --karg %s, and --resume cannot add kernel arguments to a finished disk — rerun without --resume to build a fresh disk",
		name, strings.Join(missing, " --karg "))
}

var (
	readPassword = term.ReadPassword
	isTerminal   = term.IsTerminal
)

// loginOptions are the account flags shared by the bootc lifecycle commands.
type loginOptions struct {
	user           string
	passwordHash   string
	passwordPrompt bool
	passwordStdin  bool
}

// loginProvisionScript validates the account flags and, when requested, reads
// a password without putting plaintext in argv or shell history: from the
// terminal (--password-prompt) or as one line of stdin (--password-stdin).
// The returned script contains only the SHA-512 crypt hash.
func loginProvisionScript(opts loginOptions, in io.Reader, out io.Writer) (string, error) {
	if out == nil {
		out = io.Discard
	}
	sources := 0
	for _, set := range []bool{opts.passwordPrompt, opts.passwordStdin, opts.passwordHash != ""} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return "", fmt.Errorf("--password-prompt, --password-stdin, and --password-hash are mutually exclusive")
	}
	if opts.user == "" {
		if sources > 0 {
			return "", fmt.Errorf("--password-prompt, --password-stdin, and --password-hash require --user")
		}
		return "", nil
	}
	if err := bootc.ValidateLoginUser(opts.user); err != nil {
		return "", err
	}
	if err := bootc.ValidatePasswordHash(opts.passwordHash); err != nil {
		return "", err
	}
	passwordHash := opts.passwordHash
	var plain []byte
	var err error
	switch {
	case opts.passwordPrompt:
		plain, err = promptPassword(out)
	case opts.passwordStdin:
		plain, err = readPasswordLine(in)
	}
	if err != nil {
		return "", err
	}
	if opts.passwordPrompt || opts.passwordStdin {
		defer clearBytes(plain)
		if len(plain) == 0 {
			return "", fmt.Errorf("password cannot be empty")
		}
		if passwordHash, err = bootc.HashPassword(string(plain)); err != nil {
			return "", err
		}
	}
	return bootc.AccountProvisionScript(opts.user, passwordHash)
}

// promptPassword reads a password and its confirmation from the terminal
// without echo. It refuses a non-terminal stdin, so a script that pipes a
// password gets a pointer to --password-stdin instead of a hang or a
// half-read line.
func promptPassword(out io.Writer) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !isTerminal(fd) {
		return nil, fmt.Errorf("--password-prompt needs a terminal; to pipe the password, use --password-stdin")
	}
	if _, err := fmt.Fprint(out, "Password: "); err != nil {
		return nil, fmt.Errorf("writing password prompt: %w", err)
	}
	plain, err := readPassword(fd)
	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}
	if _, err := fmt.Fprint(out, "\nConfirm password: "); err != nil {
		clearBytes(plain)
		return nil, fmt.Errorf("writing password confirmation prompt: %w", err)
	}
	confirm, err := readPassword(fd)
	if err != nil {
		clearBytes(plain)
		return nil, fmt.Errorf("reading password confirmation: %w", err)
	}
	defer clearBytes(confirm)
	if _, err := fmt.Fprintln(out); err != nil {
		clearBytes(plain)
		return nil, fmt.Errorf("writing password prompt newline: %w", err)
	}
	if string(plain) != string(confirm) {
		clearBytes(plain)
		return nil, fmt.Errorf("passwords do not match")
	}
	return plain, nil
}

// readPasswordLine reads the first line of stdin for --password-stdin. A
// trailing "\n" or "\r\n" is not part of the password; anything after the
// first line is ignored.
func readPasswordLine(in io.Reader) ([]byte, error) {
	if in == nil {
		return nil, fmt.Errorf("--password-stdin: no stdin")
	}
	line, err := bufio.NewReader(in).ReadBytes('\n')
	if err != nil && err != io.EOF {
		clearBytes(line)
		return nil, fmt.Errorf("reading password from stdin: %w", err)
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line, nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func main() {
	if sdk.HandleMetadata(sdk.Metadata{Name: "bootc", Version: "0.3.0", Description: "Build bootable container images into VMs", Capabilities: []string{"cli-command", "backend-workflow"}, Permissions: []string{"mutate Kubernetes resources and instance definitions", "pull container images", "run privileged containers to partition a disk image"}, SupportedBackends: bootc.SupportedBackends}) {
		return
	}
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newRootCmd builds the corral-bootc command tree.
func newRootCmd() *cobra.Command {
	var (
		namespace, disk, mem, sshKey, node, storageClass string
		contextName, filesystem                          string
		login                                            loginOptions
		kargs                                            []string
		customize                                        []string
		tags                                             []string
		cpu                                              int
		resume, useSudo                                  bool
		dedicatedCPUs, isolateEmulator                   bool
	)
	addLoginFlags := func(cmd *cobra.Command) {
		cmd.Flags().StringVar(&login.user, "user", "", "Create a login user in the installed image")
		cmd.Flags().BoolVar(&login.passwordPrompt, "password-prompt", false, "Prompt on the terminal for the login user's password (never accepts plaintext in argv)")
		cmd.Flags().BoolVar(&login.passwordStdin, "password-stdin", false, "Read the login user's password from the first line of stdin, for scripts")
		cmd.Flags().StringVar(&login.passwordHash, "password-hash", "", "Set the login user's SHA-512 crypt password hash (requires --user)")
	}

	create := &cobra.Command{
		Use:   "create <name> --image <bootc-image>",
		Short: "Build a bootc container image into a VM and create it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := kubevirt.ValidateKargs(kargs); err != nil {
				return err
			}
			if err := bootc.ValidateCustomize(customize); err != nil {
				return err
			}
			parsedTags, err := kubevirt.ParseTags(tags)
			if err != nil {
				return err
			}
			placement := kubevirt.Placement{DedicatedCPUs: dedicatedCPUs, IsolateEmulatorThread: isolateEmulator, Tags: parsedTags}
			if err := placement.Validate(); err != nil {
				return err
			}
			ns := namespace
			if ns == "" {
				ns = kubevirt.DefaultNamespace
			}
			// Off-cluster backends build locally and import, rather than
			// running a privileged Job on a cluster they do not have (#130).
			target, err := resolveContext(contextName)
			if err != nil {
				return err
			}
			if target.Backend != "kubevirt" {
				if dedicatedCPUs || isolateEmulator || len(tags) > 0 {
					return fmt.Errorf("--dedicated-cpus, --isolate-emulator-thread, and --tag are supported on the KubeVirt backend only, not on backend %s", target.Backend)
				}
				image, _ := cmd.Flags().GetString("image")
				provisionScript, err := loginProvisionScript(login, cmd.InOrStdin(), cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				return buildAndImport(name, target, image, sshKey, disk, mem, cpu, filesystem, kargs, customize, provisionScript, useSudo)
			}
			if len(customize) > 0 {
				return fmt.Errorf("--customize runs scripts on this machine before the build, which the kubevirt backend cannot do — build locally (qemu context) or bake the customization into the image")
			}
			if !kubevirt.BootcAvailable() {
				return fmt.Errorf("this corral-bootc build lacks the bootc pipeline (build with -tags bootc)")
			}
			// Refuse before the build: it takes minutes, and a VM with
			// dedicated CPUs on a node without the CPU manager never starts.
			if dedicatedCPUs {
				if err := kubevirt.CheckDedicatedCPUs(node); err != nil {
					return err
				}
			}
			kubevirt.EnsureNamespace(ns)

			key := sshKey
			if key == "" {
				key = kubevirt.LoadSSHPublicKey()
			}

			if resume && (login.user != "" || login.passwordPrompt || login.passwordStdin || login.passwordHash != "") {
				return fmt.Errorf("--resume reuses an already-installed disk and cannot add a login user — rerun without --resume")
			}
			// --resume picks up an interrupted build: a builder VM that
			// finished installing but never got its final VM created because
			// the CLI died mid-wait (Ctrl+C, SSH drop) — see
			// kubevirt.BootcResumeState. Skips the build step entirely, so it
			// accepts --karg only when that build already installed it.
			if resume {
				image, pvcName, ready, failed := kubevirt.BootcResumeState(name, ns)
				if failed {
					return fmt.Errorf("the previous build for %q failed — rerun without --resume to start a fresh build", name)
				}
				if !ready {
					return fmt.Errorf("no resumable build found for %q (no completed builder + disk PVC)", name)
				}
				if err := checkResumeKargs(name, kargs, kubevirt.BootcBuiltKargs(name, ns)); err != nil {
					return err
				}
				if err := finishVM(name, ns, pvcName, image, key, mem, cpu, node, placement); err != nil {
					return err
				}
				kubevirt.BootcCleanupBuilder(name+"-bootc-builder", ns)
				return nil
			}

			image, _ := cmd.Flags().GetString("image")
			if image == "" {
				return fmt.Errorf("--image is required — a catalog name (see `corral bootc images`) or an OCI ref like quay.io/centos-bootc/centos-bootc:stream9")
			}
			image = catalog.ResolveBootc(image)
			if key == "" {
				return fmt.Errorf("no SSH public key (--ssh-key or ~/.ssh/*.pub) — needed for bootc VM access")
			}
			size := disk
			if size == "" {
				size = "80Gi"
			}
			provisionScript, err := loginProvisionScript(login, cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}

			build, err := kubevirt.BootcBuildDisk(name, ns, image, key, size, storageClass, provisionScript, kargs, node, os.Stderr)
			if err != nil {
				return fmt.Errorf("bootc build: %w — if the builder actually finished after this failed, retry with --resume", err)
			}
			return finishVM(name, ns, build.PVCName, image, key, mem, cpu, node, placement)
		},
	}
	create.Flags().String("image", "", "Bootc container image — catalog name or OCI ref")
	create.Flags().StringVar(&disk, "disk", "80Gi", "Disk size")
	create.Flags().StringVar(&mem, "mem", "4G", "Memory")
	create.Flags().IntVar(&cpu, "cpu", 2, "vCPUs")
	create.Flags().StringVar(&node, "node", "", "Schedule on a specific node")
	create.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")
	addLoginFlags(create)
	create.Flags().StringVarP(&storageClass, "storage-class", "s", "", "StorageClass for the disk PVC (default: cluster preference)")
	create.Flags().BoolVar(&resume, "resume", false, "Finish a build that completed after a previous `create` was interrupted (with --karg: only if that build installed it)")
	create.Flags().StringArrayVar(&kargs, "karg", nil, "Kernel argument to install into the boot entry, there from the first boot (repeatable)")
	create.Flags().StringArrayVar(&customize, "customize", nil, "Script to run in the image before building the disk, e.g. to add users or packages (repeatable, local backends only)")
	create.Flags().StringVar(&filesystem, "filesystem", "", "Override the root filesystem a local build would pick from the image (xfs for ostree, ext4 or btrfs for composefs)")
	create.Flags().BoolVar(&useSudo, "sudo", false, "Run the local build's podman under sudo — bootc install needs root (off-cluster backends only)")
	create.Flags().BoolVar(&dedicatedCPUs, "dedicated-cpus", false, "Pin each vCPU of the final VM to its own host CPU (dedicatedCpuPlacement, guaranteed QoS); needs a node labeled cpumanager=true (KubeVirt only)")
	create.Flags().BoolVar(&isolateEmulator, "isolate-emulator-thread", false, "Give the final VM's QEMU emulator thread one more dedicated host CPU (needs --dedicated-cpus; KubeVirt only)")
	create.Flags().StringArrayVar(&tags, "tag", nil, "Tag the VM as key=value (key alone means key=true), stored as label corral.dev/tag.<key> (repeatable; KubeVirt only)")

	images := &cobra.Command{
		Use:   "images",
		Short: "List the curated bootc image catalog",
		Long: `Well-maintained bootable container bases from Fedora, CentOS, and
Universal Blue. Use a catalog name directly:

  corral bootc create myvm --image fedora-bootc

This is the bootc-only view. "corral images" (core CLI, no plugin needed)
lists this same catalog alongside the OS image catalog in one place.`,
		Example: `  corral bootc images
  corral bootc create myvm --image bluefin`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("%-22s %-46s %s\n", "NAME", "IMAGE", "DESCRIPTION")
			for _, b := range catalog.BootcImages {
				fmt.Printf("%-22s %-46s %s\n", b.Name, b.Image, b.Description)
			}
			return nil
		},
	}

	// rebuild reruns the on-cluster build and re-points the VM. newImage "" =
	// keep the recorded image (an upgrade — pull the latest of the same tag).
	rebuild := func(name, newImage, provisionScript string) error {
		ns := namespace
		if ns == "" {
			ns = kubevirt.DefaultNamespace
		}
		if !kubevirt.BootcAvailable() {
			return fmt.Errorf("this corral-bootc build lacks the bootc pipeline (build with -tags bootc)")
		}
		image := catalog.ResolveBootc(newImage)
		if image == "" {
			image = kubevirt.BootcImageOf(name, ns)
		}
		if image == "" {
			return fmt.Errorf("no image recorded for %q — pass --image <ref>", name)
		}
		key := sshKey
		if key == "" {
			key = kubevirt.LoadSSHPublicKey()
		}
		if err := kubevirt.BootcRebuild(name, ns, image, key, disk, provisionScript, os.Stderr); err != nil {
			return err
		}
		if store, err := registry.NewStore(); err == nil {
			store.Set(name, types.RegistryEntry{Backend: "kubevirt", Namespace: ns})
		}
		fmt.Fprintf(os.Stderr, "VM %q rebuilt from %s and restarted\n", name, image)
		return nil
	}

	rebuildCmd := &cobra.Command{
		Use:   "rebuild <name>",
		Short: "Rebuild a bootc VM's disk from its image and restart it",
		Long: `Rebuilds the on-cluster disk from the VM's recorded bootc image (or
--image to override), swaps it in, and restarts the VM. Sizing/networking are
preserved. Use this to apply image updates.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			img, _ := cmd.Flags().GetString("image")
			provisionScript, err := loginProvisionScript(login, cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return rebuild(args[0], img, provisionScript)
		},
	}
	rebuildCmd.Flags().String("image", "", "Override the image (default: the recorded one)")
	rebuildCmd.Flags().StringVar(&disk, "disk", "", "Override disk size (default: keep existing)")
	rebuildCmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")
	addLoginFlags(rebuildCmd)

	upgradeCmd := &cobra.Command{
		Use:   "upgrade <name>",
		Short: "Rebuild a bootc VM from the latest of its current image (alias for rebuild)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provisionScript, err := loginProvisionScript(login, cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return rebuild(args[0], "", provisionScript)
		},
	}
	upgradeCmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")
	addLoginFlags(upgradeCmd)

	switchCmd := &cobra.Command{
		Use:   "switch <name> --image <ref>",
		Short: "Rebuild a bootc VM onto a different image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			img, _ := cmd.Flags().GetString("image")
			if img == "" {
				return fmt.Errorf("--image is required (catalog name or OCI ref)")
			}
			provisionScript, err := loginProvisionScript(login, cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return rebuild(args[0], img, provisionScript)
		},
	}
	switchCmd.Flags().String("image", "", "New bootc image — catalog name or OCI ref")
	switchCmd.Flags().StringVar(&disk, "disk", "", "Override disk size (default: keep existing)")
	switchCmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")
	addLoginFlags(switchCmd)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "List bootc VMs and the image each was built from",
		RunE: func(cmd *cobra.Command, args []string) error {
			vms, err := kubevirt.NewClient(namespace).ListVMs()
			if err != nil {
				return err
			}
			fmt.Printf("%-24s %-12s %s\n", "VM", "NAMESPACE", "BOOTC IMAGE")
			for _, vm := range vms {
				if !vm.Bootc {
					continue
				}
				ns := vm.Namespace
				if ns == "" {
					ns = kubevirt.DefaultNamespace
				}
				fmt.Printf("%-24s %-12s %s\n", vm.Name, ns, kubevirt.BootcImageOf(vm.Name, ns))
			}
			return nil
		},
	}

	root := &cobra.Command{
		Use:   "corral-bootc",
		Short: "Corral bootc plugin — boot a container image as a VM",
	}
	root.PersistentFlags().StringVarP(&namespace, "namespace", "n", kubevirt.DefaultNamespace, "Namespace (KubeVirt only)")
	root.PersistentFlags().StringVar(&contextName, "context", "", "Corral context to build for (default: the selected context)")
	root.AddCommand(create, images, rebuildCmd, upgradeCmd, switchCmd, statusCmd)
	return root
}
