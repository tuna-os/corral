package kubevirt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuna-os/corral/pkg/guestssh"
	"github.com/tuna-os/corral/pkg/types"
)

// Copy transfers one literal path over the guest's SFTP subsystem. Unlike
// legacy scp, no caller-controlled path is interpreted by a remote shell.
func (c *Client) Copy(name string, opts types.CopyOptions) error {
	batch, err := copyBatch(opts)
	if err != nil {
		return err
	}
	if _, err := c.ensureVirtctl(); err != nil {
		return err
	}
	sftp, err := c.runner().LookPath("sftp")
	if err != nil {
		return fmt.Errorf("sftp not found; install the OpenSSH client: %w", err)
	}
	kubeContext := c.Context
	if kubeContext == "" {
		kubeContext = os.Getenv("CORRAL_KUBE_CONTEXT")
	}
	if kubeContext == "" {
		data, err := c.runner().Run("kubectl", "config", "current-context")
		if err != nil {
			return fmt.Errorf("resolve Kubernetes context before trusting a guest key: %w", err)
		}
		kubeContext = strings.TrimSpace(string(data))
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	transport, err := guestssh.Options(guestssh.Target{
		Context: kubeContext, Namespace: c.Namespace, Name: name,
		User: opts.User, Identity: opts.Identity, CacheDir: filepath.Join(cache, "corral", "guest-ssh"),
	})
	if err != nil {
		return err
	}
	args := append([]string{"-b", "-"}, transport...)
	args = append(args, guestssh.Host)
	output, err := c.runner().RunStdin(batch, sftp, args...)
	if err != nil {
		return fmt.Errorf("SFTP copy failed (guest requires an SFTP server and key/agent authentication): %w: %s", err, output)
	}
	return nil
}

func copyBatch(opts types.CopyOptions) (string, error) {
	for _, path := range []string{opts.LocalPath, opts.GuestPath} {
		if path == "" || strings.ContainsAny(path, "\x00\r\n") {
			return "", fmt.Errorf("copy paths must be nonempty and cannot contain NUL, CR or LF")
		}
	}
	local, err := filepath.Abs(opts.LocalPath)
	if err != nil {
		return "", err
	}
	remote := opts.GuestPath
	// Relative paths are relative to the SFTP login directory. Prefixing them
	// also prevents option and ~user expansion, including for names like -r.
	if !strings.HasPrefix(remote, "/") {
		remote = "./" + remote
	}
	command, source, destination := "put", local, remote
	if opts.FromGuest {
		command, source, destination = "get", remote, local
	}
	if opts.Recursive {
		command += " -R"
	}
	return command + " " + sftpQuote(source) + " " + sftpQuote(destination) + "\n", nil
}

// OpenSSH's batch parser escapes quoted glob metacharacters itself. Only
// backslashes and double quotes need quoting here; adding a second glob escape
// would change the filename. Both the source and destination are literal.
func sftpQuote(path string) string {
	path = strings.ReplaceAll(path, "\\", "\\\\")
	path = strings.ReplaceAll(path, "\"", "\\\"")
	return "\"" + path + "\""
}
