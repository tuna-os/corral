// Package guestssh builds a private, context-scoped OpenSSH transport to KubeVirt.
// First use is trust-on-first-use, not independent host-key verification.
package guestssh

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Target identifies the API tunnel and the private host-key database. Identity
// may be empty to use only the user's existing SSH agent, not implicit key files.
type Target struct {
	Context, Namespace, Name, User, Identity, CacheDir string
}

// Host is a synthetic SSH destination; ProxyCommand supplies the actual tunnel.
const Host = "corral-guest"

// Options returns options understood by both ssh and sftp. It never consults
// user SSH configuration or the global known_hosts database, and never replaces
// a pinned key. The caller must resolve an ambient Kubernetes context first.
func Options(target Target) ([]string, error) {
	for _, value := range []string{target.Context, target.Namespace, target.Name, target.User, target.CacheDir} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("SSH requires a context, namespace, guest, user and private cache directory without control characters")
		}
	}
	cache, err := expandedPath(target.CacheDir)
	if err != nil {
		return nil, err
	}
	args := []string{"-F", "/dev/null", "-o", "BatchMode=yes",
		"-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no",
		"-o", "ConnectTimeout=15", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "UpdateHostKeys=no", "-o", "GlobalKnownHostsFile=/dev/null"}
	if target.Identity != "" {
		key, err := expandedPath(target.Identity)
		if err != nil {
			return nil, err
		}
		if err := privateFile(key, false); err != nil {
			return nil, fmt.Errorf("SSH requires an owned, private regular identity file: %w", err)
		}
		args = append(args, "-i", literal(key), "-o", "IdentitiesOnly=yes")
	} else {
		args = append(args, "-o", "IdentityFile=none")
	}
	hash := sha256.Sum256([]byte(target.Context + "\x00" + target.Namespace + "\x00" + target.Name))
	scope := hex.EncodeToString(hash[:8])
	dir := filepath.Join(cache, scope)
	for _, path := range []string{cache, dir} {
		if err := privateDirectory(path); err != nil {
			return nil, err
		}
	}
	knownHosts := filepath.Join(dir, "known_hosts")
	if err := privateFile(knownHosts, true); err != nil {
		return nil, fmt.Errorf("refusing an unsafe guest known-hosts file: %w", err)
	}
	proxy := shellCommand("virtctl", "--context", target.Context, "-n", target.Namespace,
		"port-forward", "--stdio=true", "vmi/"+target.Name, "22")
	return append(args, "-o", "UserKnownHostsFile="+configPath(knownHosts),
		"-o", "HostKeyAlias=corral-guest-"+scope,
		"-o", "ProxyCommand="+literal(proxy), "-o", "User="+target.User), nil
}

func expandedPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// OpenSSH expands ${VAR} in identity/known-hosts paths after parsing its
	// configuration, even inside quotes. No portable literal escape exists.
	if strings.Contains(absolute, "${") {
		return "", errors.New("SSH identity and cache paths cannot contain ${ because OpenSSH expands environment variables there")
	}
	return absolute, nil
}

func shellCommand(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	return strings.Join(quoted, " ")
}

// OpenSSH expands percent tokens even inside shell quotes and config paths.
func literal(value string) string { return strings.ReplaceAll(value, "%", "%%") }

func configPath(value string) string {
	value = literal(value)
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}
