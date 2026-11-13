package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/types"
)

type copyEndpoint struct {
	selector string
	path     string
}

func parseCopyEndpoint(value string) (copyEndpoint, error) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return copyEndpoint{}, fmt.Errorf("copy endpoints must be nonempty and cannot contain NUL, CR or LF")
	}
	// A leading ./, ../ or / explicitly names a local path, even with colons.
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || !strings.Contains(value, ":") {
		return copyEndpoint{path: value}, nil
	}
	colon, slash := strings.IndexByte(value, ':'), strings.IndexByte(value, '/')
	if slash >= 0 && slash < colon {
		// Fully scoped selectors may contain colons in their context. The
		// delimiter follows the name, not the context (which is URL-escaped).
		segments := 4
		if strings.HasPrefix(value, "peer/") {
			segments = 6
		}
		parts := strings.SplitN(value, "/", segments)
		if len(parts) == segments {
			if i := strings.IndexByte(parts[segments-1], ':'); i >= 0 {
				colon = len(value) - len(parts[segments-1]) + i
			}
		}
	}
	selector, path := value[:colon], value[colon+1:]
	if selector == "" || path == "" {
		return copyEndpoint{}, fmt.Errorf("remote copy endpoint must be <instance>:<path>")
	}
	if strings.Contains(selector, "/") && strings.Count(selector, "/") != 1 {
		if _, err := types.ParseInstanceRef(selector); err != nil {
			return copyEndpoint{}, err
		}
	}
	return copyEndpoint{selector: selector, path: path}, nil
}

func newCopyCommand() *cobra.Command {
	var user, identity string
	var recursive bool
	command := &cobra.Command{
		Use:   "cp <source> <destination>",
		Short: "Copy files between this machine and a KubeVirt guest",
		Long: `Copy one literal file or, with --recursive, a directory tree using SFTP
through the Kubernetes API. Exactly one endpoint must be <instance>:<path>.
Instance names and fully scoped selectors use the same resolution as corral ssh.
Other backends and Corral peers are explicitly unsupported.

The guest needs an SFTP server and key or SSH-agent authentication. First use
pins the guest key in Corral's private, context/namespace/name-scoped cache
(trust-on-first-use, not independent verification). Changed keys are refused;
user/global known_hosts and SSH configuration are not used or modified.
Identity/cache paths containing ${ are refused to prevent OpenSSH environment
expansion from changing a validated path; transferred filenames remain literal.

Paths are literal, not shell expressions or wildcards. Relative guest paths
start in the SFTP login directory; ~ is literal. Prefix local names containing
colons with ./ (or use an absolute path). NUL, CR and LF are not supported.
Existing destination files may be overwritten; missing parent directories are
not created. Recursive copies do not follow symlinks inside directory trees.`,
		Example: `  corral cp ./build.tar web:/tmp/build.tar -u ubuntu -i ~/.ssh/id_ed25519
  corral cp 'web:/tmp/report [final].txt' ./report.txt -u ubuntu
  corral cp -r ./workspace kubevirt/production/corral-vms/web:/home/ubuntu/ -u ubuntu`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			source, err := parseCopyEndpoint(args[0])
			if err != nil {
				return err
			}
			destination, err := parseCopyEndpoint(args[1])
			if err != nil {
				return err
			}
			if (source.selector == "") == (destination.selector == "") {
				return fmt.Errorf("cp requires exactly one local endpoint and one <instance>:<path>; local-to-local and guest-to-guest copies are unsupported")
			}
			local, remote, fromGuest := source, destination, false
			if source.selector != "" {
				local, remote, fromGuest = destination, source, true
			}
			if strings.HasPrefix(remote.selector, "peer/") || strings.Count(remote.selector, "/") == 1 {
				return fmt.Errorf("cp does not support Corral peer targets")
			}
			ref, err := instanceRef(remote.selector)
			if err != nil {
				return err
			}
			if ref.Peer != "" {
				return fmt.Errorf("cp does not support Corral peer targets")
			}
			adapter, err := backend.For(ref)
			if err != nil {
				return err
			}
			copier, ok := adapter.(backend.Copier)
			if !ok {
				return unsupported(adapter, "cp")
			}
			resolvedUser := user
			if resolvedUser == "" && registryStore != nil {
				if saved, ok := registryStore.GetRef(ref); ok {
					resolvedUser = saved.Username
				}
			}
			if resolvedUser == "" {
				resolvedUser = os.Getenv("USER")
			}
			if resolvedUser == "" {
				resolvedUser = "root"
			}
			return copier.Copy(ref.Name, types.CopyOptions{
				LocalPath: local.path, GuestPath: remote.path, FromGuest: fromGuest,
				Recursive: recursive, User: resolvedUser, Identity: identity,
			})
		},
	}
	command.Flags().BoolVarP(&recursive, "recursive", "r", false, "Copy a directory tree (without following symlinks inside it)")
	command.Flags().StringVarP(&user, "user", "u", "", "SSH username (remembered username, then $USER, then root)")
	command.Flags().StringVarP(&identity, "identity", "i", "", "Private SSH key (otherwise use only the existing SSH agent)")
	return command
}

func init() { rootCmd.AddCommand(newCopyCommand()) }
