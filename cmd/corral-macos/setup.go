package main

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/macos"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

func newVMCommand(contextName, namespace *string, provision bool) *cobra.Command {
	o := macos.Options{}
	verb, description := "create", "Create from prepared installer media (halted unless --start)"
	if provision {
		verb, description = "provision", "Prepare verified media and provision through a private CDI tunnel"
	}
	command := &cobra.Command{Use: verb + " NAME", Short: description, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		selected, err := target(*contextName)
		if err != nil {
			return err
		}
		o.Name, o.Namespace = args[0], *namespace
		if o.IdentityDir == "" {
			key := sha256.Sum256([]byte(selected.Context + "\x00" + o.Namespace + "\x00" + o.Name))
			o.IdentityDir = filepath.Join(o.MediaDir, "instances", fmt.Sprintf("%x", key[:8]))
		}
		client := macos.Client{Runner: shell.WithKubeContext(runner, selected.Context), Context: cmd.Context()}
		if provision {
			err = client.Provision(o, selected.Context, cmd.OutOrStdout())
		} else {
			err = client.Create(o)
		}
		if err != nil {
			return err
		}
		store, err := registry.NewStore()
		if err != nil {
			return err
		}
		ref := types.InstanceRef{Backend: "kubevirt", Context: selected.Context, Namespace: o.Namespace, Name: o.Name}
		entry, _ := store.GetRef(ref)
		entry.Backend, entry.Context, entry.Namespace = ref.Backend, ref.Context, ref.Namespace
		if err := store.SetRef(ref, entry); err != nil {
			return fmt.Errorf("saving VM registry: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "macOS VM %s/%s preserved/provisioned. A Running VMI is not installation or message-delivery proof.\n", o.Namespace, o.Name)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Complete macOS installation, then use corral macos bootstrap from a focused root Recovery Terminal for the first local account and SSH.")
		return nil
	}}
	command.Flags().StringVar(&o.Node, "node", "", "Kubernetes node for disk placement (required for a new VM)")
	mediaFlag := "media-dir"
	if provision {
		mediaFlag = "cache-dir"
	}
	command.Flags().StringVar(&o.MediaDir, mediaFlag, defaultCache(), "Release-specific installer cache directory")
	command.Flags().StringVar(&o.IdentityDir, "identity-dir", "", "Persistent private per-VM EFI identity directory (default: scoped path under the cache)")
	command.Flags().IntVar(&o.CPU, "cpu", 1, "vCPU cores (validate SMP separately on AMD)")
	command.Flags().StringVar(&o.Memory, "mem", "6Gi", "Memory")
	command.Flags().StringVar(&o.Disk, "disk", "64Gi", "System disk size (never resizes/replaces installed data)")
	if !provision {
		command.Flags().StringVar(&o.UploadProxyURL, "uploadproxy-url", "", "Loopback CDI uploadproxy URL (required only for incomplete media)")
	}
	command.Flags().BoolVar(&o.EnableSidecar, "enable-sidecar", false, "Explicitly append Sidecar while preserving other feature gates")
	command.Flags().BoolVar(&o.Start, "start", provision, "Start an owned stopped VM after provisioning; never restart a running VM")
	return command
}

func guestFlags(command *cobra.Command, target *macos.GuestTarget) {
	command.Flags().StringVar(&target.User, "user", "beeper", "Dedicated macOS GUI/SSH account")
	command.Flags().StringVar(&target.CacheDir, "cache-dir", defaultCache(), "Private local setup state directory")
	command.Flags().StringVar(&target.IdentityFile, "identity", "", "Private SSH identity (default: CACHE/NAME-ssh/id_ed25519)")
}

func bindGuestTarget(contextName, namespace *string, name string, destination *macos.GuestTarget) error {
	selected, err := target(*contextName)
	if err != nil {
		return err
	}
	destination.Name, destination.Namespace, destination.KubeContext = name, *namespace, selected.Context
	return nil
}

func newBootstrapCommand(contextName, namespace *string) *cobra.Command {
	o := macos.GuestBootstrapOptions{}
	command := &cobra.Command{Use: "bootstrap NAME", Short: "Create the first local account and key-only SSH from confirmed root Recovery", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := bindGuestTarget(contextName, namespace, args[0], &o.Target); err != nil {
			return err
		}
		return macos.BootstrapGuest(cmd.Context(), o, cmd.OutOrStdout())
	}}
	guestFlags(command, &o.Target)
	command.Flags().StringVar(&o.AdminItem, "admin-item", "", "Bitwarden login for the new dedicated local account (username must equal --user)")
	command.Flags().StringVar(&o.SystemVolume, "system-volume", "macos", "Installed APFS System volume name; volume-group identity is verified")
	command.Flags().StringVar(&o.DataVolume, "data-volume", "macos - Data", "Matching installed APFS Data volume name")
	command.Flags().BoolVar(&o.AutomaticLogin, "auto-login", false, "Explicitly enable automatic login (FileVault must be off; stores a reversible guest credential)")
	command.Flags().BoolVar(&o.ConfirmRecoveryShell, "confirm-recovery-shell", false, "Confirm root Recovery Terminal is focused, not Setup Assistant or the installed OS")
	command.Flags().BoolVar(&o.ConfirmUSLayout, "confirm-us-layout", false, "Confirm Recovery uses a plain US keyboard layout")
	command.Flags().BoolVar(&o.Reboot, "reboot", true, "Reboot only after complete verified account staging and temporary Recovery SSH cleanup")
	_ = command.MarkFlagRequired("admin-item")
	_ = command.MarkFlagRequired("confirm-recovery-shell")
	_ = command.MarkFlagRequired("confirm-us-layout")
	return command
}

func newSetupCommand(contextName, namespace *string) *cobra.Command {
	o := macos.GuestSetupOptions{}
	command := &cobra.Command{Use: "setup NAME", Short: "Apply or inspect the dedicated guest profile and Beeper bridge over authenticated SSH", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !o.Check && o.AdminItem == "" {
			return fmt.Errorf("--admin-item is required to apply the guest profile")
		}
		if err := bindGuestTarget(contextName, namespace, args[0], &o.Target); err != nil {
			return err
		}
		return macos.SetupGuest(cmd.Context(), o, cmd.OutOrStdout())
	}}
	guestFlags(command, &o.Target)
	command.Flags().BoolVar(&o.Check, "check", false, "Inspect guest state without vault reads, downloads or configuration changes")
	command.Flags().BoolVar(&o.AutomaticLogin, "auto-login", false, "Explicitly configure unattended GUI login and prevent idle screen locking; requires FileVault off and stores a reversible guest credential")
	command.Flags().StringVar(&o.AdminItem, "admin-item", "", "Bitwarden login for the selected local guest account")
	command.Flags().StringVar(&o.BeeperItem, "beeper-item", "", "Bitwarden native Beeper login for first authentication (not the IMAP desktop login)")
	return command
}

func newAuthCommand() *cobra.Command {
	var cacheDir, item string
	command := &cobra.Command{Use: "auth", Short: "Prepare or verify private native Beeper authentication without connecting a bridge", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		path, err := macos.PrepareBeeperAuth(cmd.Context(), cacheDir, item)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Native Beeper authentication validated privately: %s\nNo bridge connected; no Apple credential used.\n", path)
		return nil
	}}
	command.Flags().StringVar(&cacheDir, "cache-dir", defaultCache(), "Private local setup state directory")
	command.Flags().StringVar(&item, "item", "", "Bitwarden native Beeper login (required only for first authentication)")
	return command
}

func newAppleFieldCommand(contextName, namespace *string) *cobra.Command {
	var destination macos.GuestTarget
	var field, item string
	var confirmFocused, confirmUS, enter bool
	command := &cobra.Command{Use: "apple-field NAME", Short: "Type one selected vault field into an explicitly focused guest control", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := bindGuestTarget(contextName, namespace, args[0], &destination); err != nil {
			return err
		}
		if err := macos.TypeAppleField(cmd.Context(), destination, field, item, confirmFocused, confirmUS, enter); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Typed the selected %s field. Inspect the guest UI; authentication and Apple 2FA remain interactive.\n", field)
		return nil
	}}
	command.Flags().StringVar(&field, "field", "", "Vault login field: username or password")
	command.Flags().StringVar(&item, "item", "", "Explicit Bitwarden login item")
	command.Flags().BoolVar(&confirmFocused, "confirm-focused-field", false, "Confirm the intended guest field has focus")
	command.Flags().BoolVar(&confirmUS, "confirm-us-layout", false, "Confirm the guest uses a plain US keyboard layout")
	command.Flags().BoolVar(&enter, "enter", false, "Press Return after typing the field")
	_ = command.MarkFlagRequired("field")
	_ = command.MarkFlagRequired("item")
	return command
}
