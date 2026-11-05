// corral-macos installs Sequoia using Apple recovery media and OSX-KVM OpenCore.
// Guest credentials are handled privately at runtime, never placed in manifests.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/macos"
	"github.com/tuna-os/corral/pkg/plugin/sdk"
	"github.com/tuna-os/corral/pkg/shell"
)

var runner shell.Runner = shell.Real{}

// target deliberately rejects an unconfigured kubeconfig context. Falling back
// to kubectl's ambient current-context could provision on the wrong cluster.
func target(name string) (config.ContextConfig, error) {
	ctx := config.DefaultContext()
	if name != "" {
		var ok bool
		ctx, ok = config.FindContext(name)
		if !ok {
			return ctx, fmt.Errorf("unknown Corral context %q (see corral context ls)", name)
		}
	}
	if ctx.Backend != "kubevirt" {
		return ctx, fmt.Errorf("macos requires a KubeVirt Corral context, got %q", ctx.Backend)
	}
	if ctx.Context == "" {
		return ctx, fmt.Errorf("Corral context %q has no explicit kubeconfig context; configure it before provisioning macOS", ctx.Name)
	}
	return ctx, nil
}

func defaultCache() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "corral", "macos", "sequoia")
}

func newRoot() *cobra.Command {
	var contextName, namespace, cacheDir string
	root := &cobra.Command{Use: "corral-macos", Short: "Corral plugin — macOS Sequoia on KubeVirt", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&contextName, "context", "", "Corral context name (must map to an explicit kubeconfig context)")
	root.PersistentFlags().StringVarP(&namespace, "namespace", "n", kubevirt.DefaultNamespace, "KubeVirt namespace (must already exist)")
	prepare := &cobra.Command{Use: "prepare", Short: "Download, verify and convert Sequoia recovery media (no cluster changes)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if cacheDir == "" {
			return fmt.Errorf("--cache-dir is required when no user cache directory is available")
		}
		media, err := macos.PrepareMedia(cmd.Context(), cacheDir)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "OpenCore: %s\nRecovery: %s\nVMHide: %s\nLilu: %s\n", media.OpenCore, media.Recovery, media.VMHide, media.Lilu)
		return nil
	}}
	prepare.Flags().StringVar(&cacheDir, "cache-dir", defaultCache(), "Media cache directory")
	requirements := &cobra.Command{Use: "requirements", Short: "Check KubeVirt, CDI and Sidecar prerequisites without changing the cluster", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, err := target(contextName)
		if err != nil {
			return err
		}
		client := macos.Client{Runner: shell.WithKubeContext(runner, ctx.Context)}
		if err := client.Requirements(namespace); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Ready: Corral context %s → kubeconfig context %s, namespace %s; KubeVirt + CDI + Sidecar enabled.\n", ctx.Name, ctx.Context, namespace)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Uses managed persistent UEFI, a VM-local Intel CPU/USB/VGA hook, OpenCore VirtualSMC and SATA media; no host changes. Sidecar is deprecated since KubeVirt 1.9. macOS licensing remains the operator's responsibility.")
		return nil
	}}
	root.AddCommand(prepare, requirements,
		newVMCommand(&contextName, &namespace, false),
		newVMCommand(&contextName, &namespace, true),
		newBootstrapCommand(&contextName, &namespace),
		newSetupCommand(&contextName, &namespace),
		newAuthCommand(),
		newAppleFieldCommand(&contextName, &namespace))
	return root
}

func main() {
	if sdk.HandleMetadata(sdk.Metadata{Name: "macos", Version: "0.3.0", Description: "macOS Sequoia provisioning, first-account bootstrap and private Messages/Beeper setup", Capabilities: []string{"cli-command", "backend-workflow", "guest-bootstrap", "guest-setup"}, Permissions: []string{"download public Apple recovery, OpenCore and release kext media", "write installer images, dedicated SSH keys and private retry-safe account hashes to the local cache", "create owned Kubernetes VM, DataVolume and hook ConfigMap resources", "optionally enable the KubeVirt Sidecar gate", "retrieve explicitly selected Bitwarden login fields at runtime", "send focused credential input or integrity-checked public root Recovery bootstrap through authenticated guest VNC", "temporarily start key-only Recovery SSH with separate persistent host-key trust", "temporarily stage a disabled Recovery NSS privilege-separation alias and stock sandbox libraries with owned hash-guarded restoration", "create the first local administrator and key-only SSH offline on verified unencrypted installed macOS volumes", "request guest reboot only after verified account staging and guarded Recovery cleanup", "apply the dedicated guest profile and Beeper bridge through authenticated SSH", "explicitly configure automatic login with a reversible local credential in an unencrypted guest"}, SupportedBackends: []string{"kubevirt"}}) {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRoot().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
