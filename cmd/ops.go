package cmd

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/types"
)

// Advanced KubeVirt operations mirrored from the web UI: restart, pause,
// migrate, scale (CPU/RAM), snapshot, and disk hotplug.

var (
	migrateNode      string
	scaleCPU         int
	scaleMem         string
	addDiskSize      string
	rmDiskVol        string
	exportVolume     string
	exportOutput     string
	screenshotOutput string
	screenshotPaint  bool
	addNicNAD        string
	addNicIface      string
)

// kubevirtOnly resolves the VM and errors if it is not a KubeVirt VM.
func kubevirtOnly(args []string, action string) (*kubevirt.Client, string, error) {
	name, err := requireOrPrompt(args, action)
	if err != nil {
		return nil, "", err
	}
	backend, err := requireBackend(name)
	if err != nil {
		return nil, "", err
	}
	if backend != "kubevirt" {
		return nil, "", fmt.Errorf("%s is only supported for KubeVirt VMs", action)
	}
	ns, _ := resolveNamespace(name)
	return kubevirt.NewClient(ns), name, nil
}

// qemuOnly resolves the VM and errors if it is not a QEMU (local) VM.
func qemuOnly(args []string, action string) (string, error) {
	name, err := requireOrPrompt(args, action)
	if err != nil {
		return "", err
	}
	backend, err := requireBackend(name)
	if err != nil {
		return "", err
	}
	if backend != "qemu" {
		return "", fmt.Errorf("%s is only supported for QEMU VMs", action)
	}
	return name, nil
}

var screenshotCmd = &cobra.Command{
	Use:   "screenshot [name]",
	Short: "Capture a screenshot of a QEMU or KubeVirt VM's framebuffer",
	Long: `Capture the current framebuffer of a running VM and save it as a PNG —
no VNC client needed. Useful as boot evidence in CI.

A local QEMU VM is captured over its QMP monitor socket. A KubeVirt VM is
captured through KubeVirt's VNC screenshot subresource (virtctl vnc
screenshot), so it needs virtctl and shows what the web UI console shows.

The capture reports the frame's size and the standard deviation of its
luminance. A deviation at or under the blank threshold means nothing painted:
the guest is up and the compositor never drew, which is the failure a passing
SSH probe hides. --require-paint turns that into a nonzero exit, so one command
is a desktop gate.`,
	Example: `  corral screenshot myvm
  corral screenshot myvm -o boot-evidence.png
  corral screenshot myvm --require-paint   # fail if the screen is blank`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "screenshot")
		if err != nil {
			return err
		}
		adapter, err := instanceAdapter(name)
		if err != nil {
			return err
		}
		shooter, ok := adapter.(backend.Screenshotter)
		if !ok {
			return unsupported(adapter, "screenshot")
		}
		out := screenshotOutput
		if out == "" {
			out = name + "-screenshot.png"
		}
		frame, err := shooter.Screenshot(name, out)
		if err != nil {
			return err
		}
		verdict := "painted"
		if frame.Blank() {
			verdict = "blank"
		}
		fmt.Printf("Screenshot written to %s (%dx%d, deviation %.4f — %s)\n",
			frame.Path, frame.Width, frame.Height, frame.StdDev, verdict)
		if screenshotPaint && frame.Blank() {
			return fmt.Errorf("%s painted nothing: framebuffer deviation %.4f is at or under %.2f",
				name, frame.StdDev, qemu.BlankStdDev)
		}
		return nil
	},
}

// The console keyboard and pointer. A guest that cannot answer SSH is not out
// of reach: local QEMU injects keys at the emulated keyboard through QMP, and
// a KubeVirt VM takes key and pointer events on its VNC console. That is the
// only way into a LUKS passphrase prompt, a greeter, or a login shell on an
// image that ships sshd off. TunaOS's published-media tests are driven this
// way.

var (
	typeEnter     bool
	pointerClick  string
	pointerDouble bool
)

// keyboarderFor resolves the VM's adapter as a Keyboarder, or refuses by
// backend name.
func keyboarderFor(name, action string) (backend.Keyboarder, error) {
	adapter, err := instanceAdapter(name)
	if err != nil {
		return nil, err
	}
	keyboarder, ok := adapter.(backend.Keyboarder)
	if !ok {
		return nil, unsupported(adapter, action)
	}
	return keyboarder, nil
}

// The --json results of `key`, `type`, and `pointer`: what was sent to which
// VM. `type` reports how many characters it typed, never the text, because
// the text is often a passphrase.
type keyResult struct {
	VM     string   `json:"vm"`
	Action string   `json:"action"`
	Keys   []string `json:"keys"`
}

type typeResult struct {
	VM         string `json:"vm"`
	Action     string `json:"action"`
	Characters int    `json:"characters"`
	Enter      bool   `json:"enter"`
}

type pointerResult struct {
	VM     string `json:"vm"`
	Action string `json:"action"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	// Button is empty for a move without a click.
	Button string `json:"button,omitempty"`
	Double bool   `json:"double"`
}

// printConsoleInput prints a console-input result under --json; plain output
// stays silent, as it always has for `key` and `type`.
func printConsoleInput(result any) error {
	if !jsonOutput {
		return nil
	}
	return printJSON(result)
}

var typeCmd = &cobra.Command{
	Use:   "type <name> <text>",
	Short: "Type text at a VM's console",
	Long: `Type text at a running VM's keyboard — no VNC client, and no SSH in
the guest. A local QEMU VM gets the keys through its QMP monitor. A KubeVirt
VM gets them as key events on its VNC console (virtctl vnc), so it needs
virtctl; KubeVirt allows one VNC session, so an open web console disconnects.

Use it to answer what only a keyboard can answer: a LUKS passphrase prompt, a
greeter, a login on an image that ships sshd disabled. Pair it with
` + "`corral screenshot`" + ` to see what the guest is showing first.

Only characters a US keyboard produces are typed; anything else is refused
rather than silently dropped, because a passphrase missing one character fails
as if the guest were at fault.

With --json, prints {"vm", "action": "type", "characters", "enter"}. The text
itself is not printed.`,
	Example: `  corral screenshot vm -o before.png
  corral type vm 'correct horse battery staple' --enter
  corral key vm ctrl alt f2`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args[:1], "type")
		if err != nil {
			return err
		}
		keyboarder, err := keyboarderFor(name, "type")
		if err != nil {
			return err
		}
		text := args[1]
		if typeEnter {
			// A newline types as Return, on the same connection as the text.
			text += "\n"
		}
		if err := keyboarder.TypeText(name, text); err != nil {
			return err
		}
		return printConsoleInput(typeResult{VM: name, Action: "type",
			Characters: utf8.RuneCountInString(args[1]), Enter: typeEnter})
	},
}

var keyCmd = &cobra.Command{
	Use:   "key <name> <key>...",
	Short: "Press a key combination at a VM's console",
	Long: `Press one combination of QEMU key names together at a running VM's
keyboard, e.g. "ret", "esc", or "ctrl alt f2" to switch virtual console. The
keys go down in the order given and come up in reverse.

A local QEMU VM gets the keys through its QMP monitor. A KubeVirt VM gets them
as key events on its VNC console (virtctl vnc); KubeVirt allows one VNC
session, so an open web console disconnects.

Key names: letters, digits, ret, esc, tab, spc, backspace, delete, insert,
home, end, pgup, pgdn, up, down, left, right, f1 to f24, shift, shift_r, ctrl,
ctrl_r, alt, alt_r, meta_l and meta_r (Super), menu, minus, equal,
bracket_left, bracket_right, semicolon, apostrophe, grave_accent, backslash,
comma, dot, slash, the kp_ keypad keys, print, sysrq, pause, and media keys
such as volumeup and audiomute.

With --json, prints {"vm", "action": "key", "keys"}.`,
	Example: `  corral key vm ret
  corral key vm ctrl alt delete
  corral key vm meta_l`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args[:1], "key")
		if err != nil {
			return err
		}
		keyboarder, err := keyboarderFor(name, "key")
		if err != nil {
			return err
		}
		if err := keyboarder.PressKeys(name, args[1:]...); err != nil {
			return err
		}
		return printConsoleInput(keyResult{VM: name, Action: "key", Keys: args[1:]})
	},
}

var pointerCmd = &cobra.Command{
	Use:   "pointer <name> <x> <y>",
	Short: "Move the pointer at a VM's console, and click",
	Long: `Move the pointer of a running VM's display to (x, y), in screen
pixels from the top-left corner, and click there with --click. The position
must be inside the screen that ` + "`corral screenshot`" + ` shows.

A KubeVirt VM gets the events on its VNC console (virtctl vnc); KubeVirt
allows one VNC session, so an open web console disconnects. The VMI needs a
tablet input device, which puts the pointer at an exact position; without one
the command stops before it sends anything. Local QEMU VMs have no tablet, so
this command does not support them.

With --json, prints {"vm", "action": "pointer", "x", "y", "button", "double"}.`,
	Example: `  corral pointer vm 640 400                 # move only
  corral pointer vm 1236 12 --click left     # open GNOME quick settings
  corral pointer vm 300 200 --double         # double-click with the left button`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		x, err := strconv.Atoi(args[1])
		if err != nil || x < 0 {
			return fmt.Errorf("x must be a pixel position of 0 or more, got %q", args[1])
		}
		y, err := strconv.Atoi(args[2])
		if err != nil || y < 0 {
			return fmt.Errorf("y must be a pixel position of 0 or more, got %q", args[2])
		}
		button := pointerClick
		if pointerDouble && button == "" {
			button = "left"
		}
		switch button {
		case "", "left", "middle", "right":
		default:
			return fmt.Errorf("--click must be left, middle, or right, got %q", button)
		}
		name, err := requireOrPrompt(args[:1], "pointer")
		if err != nil {
			return err
		}
		adapter, err := instanceAdapter(name)
		if err != nil {
			return err
		}
		clicker, ok := adapter.(backend.Clicker)
		if !ok {
			return unsupported(adapter, "pointer")
		}
		if button == "" {
			err = clicker.MovePointer(name, x, y)
		} else {
			err = clicker.Click(name, x, y, button, pointerDouble)
		}
		if err != nil {
			return err
		}
		return printConsoleInput(pointerResult{VM: name, Action: "pointer",
			X: x, Y: y, Button: button, Double: pointerDouble})
	},
}

var restartCmd = &cobra.Command{
	Use:     "restart [name]",
	Short:   "Restart a VM",
	Example: `  corral restart myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "restart")
		if err != nil {
			return err
		}
		adapter, err := instanceAdapter(name)
		if err != nil {
			return err
		}
		restarter, ok := adapter.(backend.Restarter)
		if !ok {
			return unsupported(adapter, "restart")
		}
		return restarter.Restart(name)
	},
}

var pauseCmd = &cobra.Command{
	Use:     "pause [name]",
	Short:   "Pause a running VM",
	Example: `  corral pause myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		suspender, name, err := suspenderFor(args, "pause")
		if err != nil {
			return err
		}
		return suspender.Pause(name)
	},
}

var unpauseCmd = &cobra.Command{
	Use:     "unpause [name]",
	Short:   "Resume a paused VM",
	Example: `  corral unpause myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		suspender, name, err := suspenderFor(args, "unpause")
		if err != nil {
			return err
		}
		return suspender.Resume(name)
	},
}

var migrateCmd = &cobra.Command{
	Use:   "migrate [name]",
	Short: "Migrate a VM to another node or host",
	Example: `  corral migrate myvm                # let the scheduler choose
  corral migrate myvm --node karnataka`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "migrate")
		if err != nil {
			return err
		}
		adapter, err := instanceAdapter(name)
		if err != nil {
			return err
		}
		mover, ok := adapter.(backend.Mover)
		if !ok {
			return unsupported(adapter, "migrate")
		}
		return mover.Migrate(name, migrateNode)
	},
}

var scaleCmd = &cobra.Command{
	Use:   "scale [name]",
	Short: "Change a VM's CPU and/or memory (live when the backend can)",
	Long: `Change CPU and/or memory through the VM's backend. Where the backend
hotplugs, the change applies without downtime; otherwise it applies on the
next boot, or the backend refuses a running VM and says so.`,
	Example: `  corral scale web --cpu 4
  corral scale web --mem 8G
  corral scale web --cpu 4 --mem 8G`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "scale")
		if err != nil {
			return err
		}
		if scaleCPU == 0 && scaleMem == "" {
			return fmt.Errorf("specify --cpu and/or --mem")
		}
		adapter, err := instanceAdapter(name)
		if err != nil {
			return err
		}
		sizer, ok := adapter.(backend.Sizer)
		if !ok {
			return unsupported(adapter, "scale")
		}
		if err := sizer.Scale(name, scaleCPU, scaleMem); err != nil {
			return err
		}
		if scaleCPU > 0 {
			fmt.Printf("CPU → %d\n", scaleCPU)
		}
		if scaleMem != "" {
			fmt.Printf("Memory → %s\n", scaleMem)
		}
		return nil
	},
}

var addDiskCmd = &cobra.Command{
	Use:     "adddisk [name]",
	Short:   "Create and hotplug a new disk onto a VM (KubeVirt)",
	Example: `  corral adddisk myvm --size 20Gi`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "adddisk")
		if err != nil {
			return err
		}
		pvc, err := c.AddVolume(name, addDiskSize)
		if err != nil {
			return err
		}
		fmt.Printf("Attached disk %s\n", pvc)
		return nil
	},
}

var rmDiskCmd = &cobra.Command{
	Use:     "rmdisk [name]",
	Short:   "Detach a hotplugged disk from a VM (KubeVirt)",
	Example: `  corral rmdisk myvm --volume myvm-data-a1b2c3`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "rmdisk")
		if err != nil {
			return err
		}
		if rmDiskVol == "" {
			return fmt.Errorf("specify --volume <name>")
		}
		return c.RemoveVolume(name, rmDiskVol)
	},
}

var networksCmd = &cobra.Command{
	Use:     "networks",
	Short:   "List Multus NetworkAttachmentDefinitions available for --lan/--network-nad (KubeVirt)",
	Example: `  corral networks`,
	RunE: func(cmd *cobra.Command, args []string) error {
		nads := kubevirt.ListNADs()
		if len(nads) == 0 {
			fmt.Println("No NetworkAttachmentDefinitions found — Multus may not be installed on this cluster.")
			return nil
		}
		for _, n := range nads {
			fmt.Println(n)
		}
		return nil
	},
}

var addNicCmd = &cobra.Command{
	Use:   "addnic [name]",
	Short: "Bridge a secondary NIC onto the LAN for an existing VM (KubeVirt)",
	Long: `Attach a bridge-bound secondary NIC backed by a Multus NetworkAttachmentDefinition
— same mechanism as ` + "`corral create --lan`" + `, for a VM that already exists.
Hotplugs on a running VM where KubeVirt supports it, otherwise it takes
effect on the next boot.`,
	Example: `  corral addnic myvm
  corral addnic myvm --network-nad default/lan-bridge --iface net1`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "addnic")
		if err != nil {
			return err
		}
		nad, err := kubevirt.ResolveNAD(addNicNAD, kubevirt.ListNADs())
		if err != nil {
			return err
		}
		if err := c.AddNIC(name, nad, addNicIface); err != nil {
			return err
		}
		iface := addNicIface
		if iface == "" {
			iface = "net1"
		}
		fmt.Printf("Bridged %s onto %s (guest iface %s)\n", name, nad, iface)
		return nil
	},
}

var lanServiceCmd = &cobra.Command{
	Use:   "lanservice [name]",
	Short: "Expose an existing VM on the LAN via a LoadBalancer Service (KubeVirt)",
	Long: `Create a LoadBalancer-type Service for a VM — no Multus, no secondary
NIC. Needs a controller that fulfills LoadBalancer Services already running
on the cluster (Cilium's own L2 Announcement or BGP Control Plane, or
MetalLB); without one the Service just sits <pending> forever, same as any
other LoadBalancer Service on a cluster without one. Same mechanism as
` + "`corral create --lan-service`" + `, for a VM that already exists.`,
	Example: `  corral lanservice myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, name, err := kubevirtOnly(args, "lanservice")
		if err != nil {
			return err
		}
		ns, _ := resolveNamespace(name)
		if err := kubevirt.ApplyLANService(name, ns, []int{22}); err != nil {
			return err
		}
		fmt.Printf("LAN service %s-lan created — check its external IP: kubectl get svc %s-lan -n %s\n", name, name, ns)
		if ip := kubevirt.LANServiceIP(name, ns); ip != "" {
			fmt.Printf("External IP: %s\n", ip)
		}
		return nil
	},
}

var exportCmd = &cobra.Command{
	Use:   "export [name]",
	Short: "Back up a VM's disk or appliance through its backend",
	Long: `Export (back up) a VM through its backend's export adapter. The artifact
is the backend's native format: a gzip disk image on KubeVirt, qcow2 on local
QEMU and libvirt, an OVA on VirtualBox, an XVA on XenServer/XCP-ng. Some
backends refuse a running VM and say so.`,
	Example: `  corral export web                       # → web.img.gz on KubeVirt
  corral export web -o /backups/web.gz
  corral export web --volume web-disk     # KubeVirt only`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if exportVolume != "" {
			// A named volume is a KubeVirt PVC; no other backend has one.
			c, name, err := kubevirtOnly(args, "export --volume")
			if err != nil {
				return err
			}
			out, err := c.Export(name, exportVolume, exportOutput)
			if err != nil {
				return err
			}
			fmt.Printf("Exported %s → %s\n", name, out)
			return nil
		}
		name, err := requireOrPrompt(args, "export")
		if err != nil {
			return err
		}
		ref, err := instanceRef(name)
		if err != nil {
			return err
		}
		out, err := exportRef(ref, exportOutput)
		if err != nil {
			return err
		}
		fmt.Printf("Exported %s → %s\n", name, out)
		return nil
	},
}

// ── template subcommands ──────────────────────────────────────────

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage golden VM templates (KubeVirt)",
}

var templateMarkCmd = &cobra.Command{
	Use:   "mark [name]",
	Short: "Mark a VM as a template",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "template")
		if err != nil {
			return err
		}
		return c.MarkTemplate(name, true)
	},
}

var templateUnmarkCmd = &cobra.Command{
	Use:   "unmark [name]",
	Short: "Remove the template mark from a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "template")
		if err != nil {
			return err
		}
		return c.MarkTemplate(name, false)
	},
}

var templateListCmd = &cobra.Command{
	Use:   "ls",
	Short: "List template VMs",
	RunE: func(cmd *cobra.Command, args []string) error {
		vms, err := kubevirt.NewClient("").ListVMs()
		if err != nil {
			return err
		}
		n := 0
		for _, vm := range vms {
			if vm.IsTemplate {
				fmt.Printf("%-30s  %s  %d CPU / %s\n", vm.Name, vm.Namespace, vm.CPU, vm.Mem)
				n++
			}
		}
		if n == 0 {
			fmt.Println("No templates. Mark one: corral template mark <vm>")
		}
		return nil
	},
}

var templateNewCmd = &cobra.Command{
	Use:     "new [template] [newname]",
	Short:   "Create a new VM from a template (clone)",
	Example: `  corral template new tmpl-ubuntu dev1`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, tmpl, err := kubevirtOnly(args[:1], "template")
		if err != nil {
			return err
		}
		if err := c.CreateFromTemplate(tmpl, args[1]); err != nil {
			return err
		}
		fmt.Printf("Creating %s from template %s…\n", args[1], tmpl)
		return nil
	},
}

// ── snapshot subcommands ──────────────────────────────────────────

// snapshotTarget resolves the VM a snapshot command names to its pkg/snapshot
// adapter, the same contract the TUI and the web API use. A backend that
// cannot snapshot answers with *snapshot.Unsupported and its reason, rather
// than every non-KubeVirt VM being refused by name (#491).
func snapshotTarget(args []string) (snapshot.Adapter, types.InstanceRef, error) {
	name, err := requireOrPrompt(args, "snapshot")
	if err != nil {
		return nil, types.InstanceRef{}, err
	}
	ref, err := instanceRef(name)
	if err != nil {
		return nil, types.InstanceRef{}, err
	}
	adapter, err := snapshot.For(ref)
	if err != nil {
		return nil, types.InstanceRef{}, err
	}
	return adapter, ref, nil
}

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage VM snapshots",
	Long: `Take, list, restore and delete snapshots through each backend's own
mechanism: KubeVirt VirtualMachineSnapshots, incus snapshot, virsh
snapshot-*, qcow2 internal snapshots for local QEMU, and the remote APIs.
Each snapshot reports its consistency: offline (taken stopped), filesystem
(guest frozen) or crash (taken live with nothing quiesced).`,
	Example: `  corral snapshot create myvm
  corral snapshot create myvm --name before-upgrade
  corral snapshot ls myvm
  corral snapshot restore myvm before-upgrade
  corral snapshot rm myvm before-upgrade`,
}

var snapshotName string

var snapshotCreateCmd = &cobra.Command{
	Use:     "create [name]",
	Short:   "Take a snapshot of a VM",
	Example: `  corral snapshot create myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		adapter, ref, err := snapshotTarget(args)
		if err != nil {
			return err
		}
		snap, err := adapter.Create(ref, snapshotName)
		if err != nil {
			return err
		}
		fmt.Printf("%s (%s)\n", snap.Name, snap.Consistency)
		return nil
	},
}

var snapshotListCmd = &cobra.Command{
	Use:   "ls [name]",
	Short: "List snapshots for a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		adapter, ref, err := snapshotTarget(args)
		if err != nil {
			return err
		}
		snaps, err := adapter.List(ref)
		if err != nil {
			return err
		}
		if len(snaps) == 0 {
			fmt.Println("No snapshots.")
			return nil
		}
		for _, s := range snaps {
			ready := "…"
			if s.Ready {
				ready = "ready"
			}
			fmt.Printf("%-40s  %-6s  %-10s  %s\n", s.Name, ready, s.Consistency, s.Created)
		}
		return nil
	},
}

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore [name] [snapshot]",
	Short: "Restore a VM from a snapshot (VM must be stopped)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		adapter, ref, err := snapshotTarget(args[:1])
		if err != nil {
			return err
		}
		if len(args) < 2 {
			return fmt.Errorf("specify the snapshot name (corral snapshot ls %s)", ref.Name)
		}
		return adapter.Restore(ref, args[1])
	},
}

var snapshotDeleteCmd = &cobra.Command{
	Use:   "rm [name] [snapshot]",
	Short: "Delete a snapshot",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		adapter, ref, err := snapshotTarget(args[:1])
		if err != nil {
			return err
		}
		if len(args) < 2 {
			return fmt.Errorf("specify the snapshot name")
		}
		return adapter.Delete(ref, args[1])
	},
}

func init() {
	rootCmd.AddCommand(restartCmd, pauseCmd, unpauseCmd, migrateCmd, scaleCmd, addDiskCmd, rmDiskCmd, exportCmd, snapshotCmd, templateCmd, screenshotCmd, typeCmd, keyCmd, pointerCmd, networksCmd, addNicCmd, lanServiceCmd)
	typeCmd.Flags().BoolVar(&typeEnter, "enter", false, "Press Return after the text")
	pointerCmd.Flags().StringVar(&pointerClick, "click", "", "Click this button at the position: left, middle, or right")
	pointerCmd.Flags().BoolVar(&pointerDouble, "double", false, "Double-click (the left button unless --click names another)")
	templateCmd.AddCommand(templateMarkCmd, templateUnmarkCmd, templateListCmd, templateNewCmd)

	migrateCmd.Flags().StringVar(&migrateNode, "node", "", "Target node (default: scheduler chooses)")
	scaleCmd.Flags().IntVar(&scaleCPU, "cpu", 0, "New vCPU count")
	scaleCmd.Flags().StringVar(&scaleMem, "mem", "", "New memory (e.g. 8G)")
	addDiskCmd.Flags().StringVar(&addDiskSize, "size", "10Gi", "Disk size")
	rmDiskCmd.Flags().StringVar(&rmDiskVol, "volume", "", "Volume (PVC) name to detach")
	exportCmd.Flags().StringVar(&exportVolume, "volume", "", "Volume/PVC to export (default: primary disk)")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output file (default: <name> plus the format's extension)")
	screenshotCmd.Flags().StringVarP(&screenshotOutput, "output", "o", "", "Output PNG path (default: <name>-screenshot.png)")
	screenshotCmd.Flags().BoolVar(&screenshotPaint, "require-paint", false, "Exit nonzero when the captured frame is blank")
	addNicCmd.Flags().StringVar(&addNicNAD, "network-nad", "", "NetworkAttachmentDefinition to bridge onto (\"ns/name\"); default: the cluster's only one")
	addNicCmd.Flags().StringVar(&addNicIface, "iface", "", "Guest interface name for the new NIC (default: net1)")

	snapshotCmd.AddCommand(snapshotCreateCmd, snapshotListCmd, snapshotRestoreCmd, snapshotDeleteCmd)
	snapshotCreateCmd.Flags().StringVar(&snapshotName, "name", "", "Snapshot name (default: generated)")
}

// suspenderFor resolves the VM's adapter as a Suspender, or refuses by
// backend name.
func suspenderFor(args []string, action string) (backend.Suspender, string, error) {
	name, err := requireOrPrompt(args, action)
	if err != nil {
		return nil, "", err
	}
	adapter, err := instanceAdapter(name)
	if err != nil {
		return nil, "", err
	}
	suspender, ok := adapter.(backend.Suspender)
	if !ok {
		return nil, "", unsupported(adapter, action)
	}
	return suspender, name, nil
}
