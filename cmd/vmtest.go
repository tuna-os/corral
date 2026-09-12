package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/vmtest"
)

// `corral vmtest` — boot a bootc image and test it.
//
// The CLI's job is small on purpose: read the spec, let flags override it, run
// it, print the summary, and exit with the run's own code. Everything that
// decides anything lives in pkg/vmtest, which is also what the TUI and the web
// UI would call.

// vmtestOptions are the command's flags. A struct rather than package-level
// variables so the flag-to-spec merge can be tested on a fresh command.
type vmtestOptions struct {
	file        string
	bootc       string
	cpus        int
	mem         string
	disk        string
	timeout     time.Duration
	sshUser     string
	readyMarker string
	user        string
	password    string
	sudoUser    bool
	rootPass    string
	packages    []string
	checks      []string
	postBoot    string
	artifacts   string
	interval    time.Duration
	video       bool
	paint       bool
	rm          bool
	sudo        bool
	engine      string
}

// newVMTestCmd builds the command and the options it writes into.
func newVMTestCmd() (*cobra.Command, *vmtestOptions) {
	o := &vmtestOptions{}
	cmd := &cobra.Command{
		Use:     "vmtest [name]",
		Aliases: []string{"boot-test", "gate"},
		Short:   "Boot a bootc image as a local VM and test it",
		Long: `Boot a bootable container image and hand back a running system to test.

Point it at an image reference. It builds the disk with ` + "`bootc install`" + `,
boots it under local QEMU, waits for the guest to answer, and leaves the VM
running with SSH ready — so the next step of a pipeline is an ordinary ssh
command. Exit status is the verdict, and everything the run saw is written to
the artifact directory: the serial console, screenshots of the boot, a
timelapse, and result.json.

It is also a customiser, not only a gate. Accounts, passwords, packages,
files and scripts go into a thin image layer built on top of the reference
under test — the published image is never modified, and with no
customisation asked for, nothing is layered at all.

Artifacts (in --artifacts, default ` + vmtest.DefaultArtifactDir + `):
  result.json      the whole run, machine-readable
  serial.log       the guest's console, from the firmware on
  frames/          one screenshot per --screenshot-interval while booting
  ready.png        the screen when the guest became ready (failure.png if not)
  timelapse.webm   the boot as a video (--video, needs ffmpeg)
  diagnostics/     failed units, bootc status, journal warnings
  layer/           the generated Containerfile and overlay, when one was built
  ssh/             the run's own keypair

Exit codes: 0 passed, 1 bad spec, 2 this host cannot run it, 3 layer build
failed, 4 disk build failed, 5 VM would not start, 6 never became ready,
7 post-boot hook failed, 8 a check failed, 9 nothing was ever painted.`,
		Example: `  # A boot gate: does this image come up at all?
  corral vmtest --bootc quay.io/fedora/fedora-bootc:41 --rm

  # A system to test: an account with a password, a package, and SSH ready.
  corral vmtest gate --bootc ghcr.io/tuna-os/yellowfin:latest \
    --user tester --password hunter2 --sudo-user --package jq
  ssh -i corral-vmtest-out/ssh/id_ed25519 -p 2242 tester@127.0.0.1

  # Assertions, a first-boot hook, and a video of the boot.
  corral vmtest gate --bootc "$IMAGE" --post-boot ./firstboot.sh \
    --check 'systemctl is-active sshd' --check 'systemctl --failed --no-legend' \
    --video

  # A desktop image, which must actually paint something.
  corral vmtest desk --bootc "$IMAGE" --require-paint --timeout 20m

  # Everything in a file (Lima-shaped).
  corral vmtest gate -f verify.yaml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := &vmtest.Spec{}
			if o.file != "" {
				loaded, err := vmtest.Load(o.file)
				if err != nil {
					return err
				}
				spec = loaded
			}
			o.apply(cmd, spec, args)

			result, runErr := vmtest.Run(spec, os.Stderr)
			if jsonOutput {
				if err := printJSON(result); err != nil {
					return err
				}
			} else {
				printVMTestSummary(result)
			}
			if runErr != nil {
				// The exit code is the verdict, so it is set here rather than
				// left to cobra's blanket 1.
				fmt.Fprintln(os.Stderr, "vmtest: "+runErr.Error())
				os.Exit(vmtest.ExitCodeOf(result))
			}
			return nil
		},
	}
	o.bind(cmd)
	return cmd, o
}

// apply lets a flag override the spec file, and only when the flag was
// actually given — a default must never silently replace a file's value.
func (o *vmtestOptions) apply(cmd *cobra.Command, spec *vmtest.Spec, args []string) {
	changed := cmd.Flags().Changed
	if len(args) == 1 {
		spec.Name = args[0]
	}
	if o.bootc != "" {
		// Catalog names resolve the same way `corral bootc create --image`
		// resolves them, so "fedora-bootc" works here too.
		spec.Bootc = catalog.ResolveBootc(o.bootc)
	}
	if changed("cpu") {
		spec.CPUs = o.cpus
	}
	if changed("mem") {
		spec.Memory = o.mem
	}
	if changed("disk") {
		spec.Disk = o.disk
	}
	if changed("timeout") {
		spec.Timeout = vmtest.Duration(o.timeout)
	}
	if changed("ssh-user") {
		spec.SSHUser = o.sshUser
	}
	if changed("ready-marker") {
		spec.ReadyMarker = o.readyMarker
	}
	if changed("root-password") {
		spec.RootPassword = o.rootPass
	}
	if o.user != "" {
		spec.Users = append(spec.Users, vmtest.User{
			Name:     o.user,
			Password: o.password,
			Sudo:     o.sudoUser,
		})
	}
	spec.Packages = append(spec.Packages, o.packages...)
	spec.Checks = append(spec.Checks, o.checks...)
	if o.postBoot != "" {
		if script, err := os.ReadFile(o.postBoot); err == nil {
			spec.Provision = append(spec.Provision, vmtest.Provision{Script: string(script)})
		} else {
			// Not a path: treat it as the script itself, so a one-liner needs
			// no file.
			spec.Provision = append(spec.Provision, vmtest.Provision{Script: o.postBoot})
		}
	}
	if changed("artifacts") {
		spec.Artifacts = o.artifacts
	}
	if changed("screenshot-interval") {
		spec.Screenshots.Interval = vmtest.Duration(o.interval)
	}
	if changed("video") {
		spec.Screenshots.Video = o.video
	}
	if changed("require-paint") {
		spec.Screenshots.RequirePaint = o.paint
	}
	if changed("layer-engine") {
		spec.LayerEngine = o.engine
	}
	if changed("sudo") {
		spec.Sudo = o.sudo
	}
	// The VM is kept by default: the command exists to hand over a system to
	// test. --rm is for a pure gate, where nothing follows.
	spec.Keep = !o.rm
}

// printVMTestSummary is what a human reads in the job log: the verdict, how to
// get in, and where the evidence is.
func printVMTestSummary(result *vmtest.Result) {
	if result == nil {
		return
	}
	verdict := "PASSED"
	if result.Status != "passed" {
		verdict = "FAILED"
	}
	fmt.Printf("\n%s — %s (%s)\n", verdict, result.Name, result.Image)
	if result.Layer.Derived {
		fmt.Printf("  layer:      %s, built with the %s engine\n", result.Layer.Image, result.Layer.Engine)
	}
	if result.Ready {
		fmt.Printf("  ready:      %.0fs, by %s\n", result.BootSeconds, result.ReadyBy)
	}
	if result.Hook != nil {
		fmt.Printf("  hook:       exit %d (%s)\n", result.Hook.ExitCode, result.Hook.Source)
	}
	for _, check := range result.Checks {
		status := "ok  "
		if !check.Passed {
			status = "FAIL"
		}
		fmt.Printf("  check %s  %s\n", status, check.Command)
	}
	if result.FinalFrame != nil {
		fmt.Printf("  screen:     %s (%dx%d, deviation %.4f)\n",
			result.FinalFrame.Path, result.FinalFrame.Width, result.FinalFrame.Height, result.FinalFrame.StdDev)
	}
	if result.Kept && result.SSH.Command != "" {
		fmt.Printf("  ssh:        %s\n", result.SSH.Command)
		if result.SSH.Password != "" {
			fmt.Printf("  password:   %s (%s)\n", result.SSH.Password, result.SSH.User)
		}
		fmt.Printf("  delete:     corral delete %s\n", result.Name)
	}
	fmt.Printf("  artifacts:  %s\n", result.Artifacts)
	for _, note := range result.Notes {
		fmt.Printf("  note:       %s\n", note)
	}
	if result.Failure != "" {
		fmt.Printf("  failure:    %s\n", firstLineOf(result.Failure))
	}
}

func firstLineOf(s string) string {
	if line, _, found := strings.Cut(s, "\n"); found {
		return line
	}
	return s
}

// bind declares the flags. Defaults come from pkg/vmtest, so the help text and
// the behaviour cannot drift apart.
func (o *vmtestOptions) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVarP(&o.file, "file", "f", "", "Test spec YAML (Lima-shaped; flags override it)")
	f.StringVar(&o.bootc, "bootc", "", "Bootc image to test — OCI reference or a catalog name")
	f.IntVar(&o.cpus, "cpu", vmtest.DefaultCPUs, "vCPUs")
	f.StringVar(&o.mem, "mem", vmtest.DefaultMemory, "Memory")
	f.StringVar(&o.disk, "disk", vmtest.DefaultDisk, "Disk size")
	f.DurationVar(&o.timeout, "timeout", vmtest.DefaultTimeout, "How long the guest gets to become ready")
	f.StringVar(&o.sshUser, "ssh-user", vmtest.DefaultSSHUser, "Account the readiness probe and the checks log in as")
	f.StringVar(&o.readyMarker, "ready-marker", "", "Gate readiness on this regular expression appearing on the guest console, instead of on SSH")
	f.StringVar(&o.user, "user", "", "Create this account in the derived image")
	f.StringVar(&o.password, "password", "", "Password for --user")
	f.BoolVar(&o.sudoUser, "sudo-user", false, "Give --user passwordless sudo")
	f.StringVar(&o.rootPass, "root-password", "", "Set root's password in the derived image")
	f.StringArrayVar(&o.packages, "package", nil, "Layer a package into the image (repeatable)")
	f.StringArrayVar(&o.checks, "check", nil, "Command that must exit 0 in the guest (repeatable)")
	f.StringVar(&o.postBoot, "post-boot", "", "Script to run in the guest on boot — a file path, or the script itself")
	f.StringVar(&o.artifacts, "artifacts", vmtest.DefaultArtifactDir, "Directory for logs, screenshots and result.json")
	f.DurationVar(&o.interval, "screenshot-interval", vmtest.DefaultFrameInterval, "Time between boot screenshots (0 or less turns capture off)")
	f.BoolVar(&o.video, "video", false, "Assemble the boot screenshots into a WebM timelapse (needs ffmpeg)")
	f.BoolVar(&o.paint, "require-paint", false, "Fail when the guest never painted anything — for desktop images")
	f.BoolVar(&o.rm, "rm", false, "Delete the VM when the run finishes (default: leave it running to test)")
	f.BoolVar(&o.sudo, "sudo", false, "Run podman under sudo — bootc install needs root")
	f.StringVar(&o.engine, "layer-engine", vmtest.EngineAuto, "What generates the layer's Containerfile: auto, remora, or builtin")
}

func init() {
	cmd, _ := newVMTestCmd()
	rootCmd.AddCommand(cmd)
}
