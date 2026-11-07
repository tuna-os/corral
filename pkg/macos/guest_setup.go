package macos

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// GuestTarget identifies one guest through an explicit Kubernetes context. SSH
// state is scoped to that context, namespace and VM, never the ambient context.
type GuestTarget struct {
	Name, Namespace, KubeContext, User, IdentityFile, CacheDir string
}

// GuestSetupOptions controls inspection or installation in the dedicated guest.
// Vault item names are supplied by the caller, never plugin defaults.
type GuestSetupOptions struct {
	Target                GuestTarget
	Check                 bool
	AutomaticLogin        bool
	AdminItem, BeeperItem string
}

//go:embed guest_profile.sh
var guestProfile string

//go:embed beeper_bridge.sh
var beeperBridge string

// VMHide can conceal kern.hv_vmm_present on Sequoia. Only an actual registered,
// matched and active instance of this exact driver class is alternate evidence.
const virtualGuestGuard = `virtual_guest() {
    [ "$(/usr/sbin/sysctl -n kern.hv_vmm_present 2>/dev/null || true)" = 1 ] && return 0
    local registry line
    local instance='^[[:space:]|]*\+-o .+ <class AppleVirtIOPCITransport, id 0x[[:xdigit:]]+, registered, matched, active,'
    registry="$(/usr/sbin/ioreg -r -c AppleVirtIOPCITransport -d 1 -w 0 2>/dev/null)" || return 1
    while IFS= read -r line; do
        if [[ "$line" =~ $instance ]]; then return 0; fi
    done <<< "$registry"
    return 1
}
`

// ValidateRunningGuest checks ownership and running state before any credential
// is accessed. Metadata and command errors are captured, not logged verbatim.
func ValidateRunningGuest(ctx context.Context, target GuestTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(target.KubeContext) == "" {
		return errors.New("an explicit Kubernetes context is required for guest operations")
	}
	if len(target.Name) > 48 || !dnsName.MatchString(target.Name) || len(target.Namespace) > 63 || !dnsName.MatchString(target.Namespace) {
		return errors.New("guest name and namespace must be valid DNS labels")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "--context", target.KubeContext, "-n", target.Namespace, "get", "vmi", target.Name, "-o", "json")
	data, err := cmd.Output()
	if err != nil {
		return errors.New("cannot inspect the target VMI; check the explicit context, namespace and name")
	}
	var instance object
	if json.Unmarshal(data, &instance) != nil || nested(instance, "metadata", "name") != target.Name || !owned(instance, target.Name) || nested(instance, "status", "phase") != "Running" {
		return errors.New("refusing guest operation: destination is not this plugin's running macOS VM")
	}
	return nil
}

func guestExpandedPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	return filepath.Abs(path)
}

func guestShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func guestShellCommand(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = guestShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// OpenSSH expands percent tokens even inside a shell-quoted ProxyCommand or a
// quoted known-hosts path. Escape before handing any caller-controlled text to it.
func guestSSHLiteral(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func guestSSHConfigPath(value string) string {
	value = guestSSHLiteral(value)
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

func guestSSHTransport(target GuestTarget) ([]string, error) {
	return guestSSHTransportScope(target, "22", "guest-ssh")
}

// Recovery has its own root account, port and host-key trust. Installed guest
// callers retain exactly the existing transport and known-hosts scope.
func guestSSHTransportScope(target GuestTarget, port, trust string) ([]string, error) {
	if strings.TrimSpace(target.User) == "" || strings.TrimSpace(target.CacheDir) == "" {
		return nil, errors.New("guest SSH user and private cache directory are required")
	}
	cache, err := guestExpandedPath(target.CacheDir)
	if err != nil {
		return nil, err
	}
	key := target.IdentityFile
	if key == "" {
		key = filepath.Join(cache, target.Name+"-ssh", "id_ed25519")
	}
	key, err = guestExpandedPath(key)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(key)
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("SSH requires an owned, private regular identity file")
	}
	hash := sha256.Sum256([]byte(target.KubeContext + "\x00" + target.Namespace + "\x00" + target.Name))
	scope := hex.EncodeToString(hash[:8])
	dir := filepath.Join(cache, trust, scope)
	for _, path := range []string{cache, filepath.Join(cache, trust), dir} {
		if err := privateDirectory(path); err != nil {
			return nil, err
		}
	}
	knownHosts := filepath.Join(dir, "known_hosts")
	if _, err := os.Lstat(knownHosts); err == nil {
		if err := privateConfig(knownHosts); err != nil {
			return nil, errors.New("refusing an unsafe guest known-hosts file")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		file, err := privateOpenFile(knownHosts, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, errors.New("cannot create private guest known-hosts file")
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("cannot inspect guest known-hosts file")
	}
	proxy := guestShellCommand("virtctl", "--context", target.KubeContext, "-n", target.Namespace, "port-forward", "--stdio=true", "vmi/"+target.Name, port)
	alias := "corral-macos-" + scope
	if trust != "guest-ssh" {
		alias += "-" + trust
	}
	return []string{"-F", "/dev/null", "-T", "-i", guestSSHLiteral(key),
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no",
		"-o", "ConnectTimeout=15", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "UserKnownHostsFile=" + guestSSHConfigPath(knownHosts),
		"-o", "HostKeyAlias=" + alias,
		"-o", "ProxyCommand=" + guestSSHLiteral(proxy), "-l", target.User, "corral-macos"}, nil
}

var errGuestHostKeyChanged = errors.New("guest SSH host-key verification failed; refusing changed or untrusted Recovery/installed identity")

func guestRemote(ctx context.Context, transport []string, command string, input io.Reader, output io.Writer) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 600*time.Second)
	defer cancel()
	args := make([]string, len(transport)+1)
	copy(args, transport)
	args[len(transport)] = command
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		// OpenSSH runs ProxyCommand through $SHELL -c, but the generated
		// proxy is POSIX-sh quoting. A non-POSIX login shell (fish) breaks
		// argument parsing and PATH resolution, and any shell startup
		// output would corrupt the stdio tunnel.
		cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	}
	cmd.Stdin = input
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	// Only trusted embedded scripts write public output. bbctl's raw auth
	// responses are redirected to private files inside the bridge installer.
	if output != nil {
		if _, writeErr := output.Write(stdout.Bytes()); writeErr != nil {
			return nil, writeErr
		}
		if _, writeErr := output.Write(stderr.Bytes()); writeErr != nil {
			return nil, writeErr
		}
	}
	if err != nil {
		if bytes.Contains(stderr.Bytes(), []byte("REMOTE HOST IDENTIFICATION HAS CHANGED")) || bytes.Contains(stderr.Bytes(), []byte("Host key verification failed")) {
			return nil, errGuestHostKeyChanged
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("guest command failed; check the dedicated SSH key, Remote Login and guest state; no authentication success is assumed")
	}
	return stdout.Bytes(), nil
}

func guestScriptCommand(body, name string, sudo bool, args ...string) string {
	command := []string{"/bin/bash", "-c", body, name}
	command = append(command, args...)
	if sudo {
		command = append([]string{"/usr/bin/sudo", "-k", "-S", "-p", "", "--"}, command...)
	}
	return guestShellCommand(command...)
}

func parseGuestPreflight(data []byte) (bool, error) {
	var state struct {
		Guest       string `json:"guest"`
		AuthPresent *bool  `json:"auth_present"`
	}
	if json.Unmarshal(data, &state) != nil || state.Guest != "verified" || state.AuthPresent == nil {
		return false, errors.New("guest preflight did not return a verified setup state")
	}
	return *state.AuthPresent, nil
}

// SetupGuest installs or inspects only the dedicated, ownership-checked virtual
// guest. Check never accesses the vault, prepares auth, or downloads artifacts.
func SetupGuest(ctx context.Context, options GuestSetupOptions, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	target := options.Target
	if err := ValidateRunningGuest(ctx, target); err != nil {
		return err
	}
	transport, err := guestSSHTransport(target)
	if err != nil {
		return err
	}
	mode := "apply"
	if options.Check {
		mode = "check"
	}
	preflight := `set -eu
[ "$(/usr/bin/uname -s)" = Darwin ] || exit 70
[ "$(/usr/bin/uname -m)" = x86_64 ] || exit 70
` + virtualGuestGuard + `
virtual_guest || exit 70
[ "$(/usr/bin/id -un)" = "$1" ] || exit 70
[ "$(/usr/bin/id -u)" != 0 ] || exit 70
if [ "$2" = apply ]; then
    [ "$(/usr/bin/stat -f '%u' /dev/console)" = "$(/usr/bin/id -u)" ] || exit 70
fi
if [ -e "$HOME/Library/Application Support/corral-macos/bbctl/config.json" ]; then
    present=true
else
    present=false
fi
printf '{"guest":"verified","auth_present":%s}\n' "$present"
`
	inspected, err := guestRemote(ctx, transport, guestShellCommand("/bin/bash", "-c", preflight, "corral-macos-guest", target.User, mode), nil, nil)
	if err != nil {
		return err
	}
	authPresent, err := parseGuestPreflight(inspected)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(output, "Verified dedicated virtual macOS guest and SSH account.")
	if options.Check {
		_, profileErr := guestRemote(ctx, transport, guestScriptCommand(guestProfile, "guest_profile.sh", false, "--check"), nil, output)
		_, bridgeErr := guestRemote(ctx, transport, guestScriptCommand(beeperBridge, "beeper_bridge.sh", false, "--check"), nil, output)
		if profileErr != nil || bridgeErr != nil {
			return errors.New("guest inspection found missing or unhealthy configuration")
		}
		return nil
	}
	if strings.TrimSpace(options.AdminItem) == "" {
		return errors.New("a dedicated guest admin vault item is required for apply")
	}
	username, password, err := vaultLogin(ctx, options.AdminItem)
	if err != nil {
		return err
	}
	if username != target.User || strings.ContainsAny(password, "\r\n\x00") {
		return errors.New("the dedicated VM vault login must match the SSH user and contain a single-line password")
	}
	var auth *os.File
	if !authPresent {
		if strings.TrimSpace(options.BeeperItem) == "" {
			return errors.New("a Beeper vault item is required when guest authentication is absent")
		}
		cache, err := guestExpandedPath(target.CacheDir)
		if err != nil {
			return err
		}
		config, err := PrepareBeeperAuth(ctx, cache, options.BeeperItem)
		if err != nil {
			return err
		}
		if err := privateConfig(config); err != nil {
			return err
		}
		auth, err = privateOpenFile(config, os.O_RDONLY, 0)
		if err != nil {
			return errors.New("cannot open the private native Beeper config")
		}
		defer func() { _ = auth.Close() }()
	}
	_, err = guestRemote(ctx, transport, guestScriptCommand(guestProfile, "guest_profile.sh", true, "--apply"), strings.NewReader(password+"\n"), output)
	if err == nil && options.AutomaticLogin {
		err = applyAutomaticLogin(ctx, transport, target.User, password, output)
	}
	username, password = "", ""
	if err != nil {
		return err
	}
	args := []string{"--apply"}
	var input io.Reader
	if auth != nil {
		args = append(args, "--auth-config-stdin")
		input = auth
	}
	if _, err := guestRemote(ctx, transport, guestScriptCommand(beeperBridge, "beeper_bridge.sh", false, args...), input, output); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(output, "Guest profile and bridge installer applied. Verify the guest permissions and real message delivery.")
	if options.AutomaticLogin {
		_, _ = fmt.Fprintln(output, "Automatic login was explicitly configured for the selected local account. FileVault was not changed.")
	} else {
		_, _ = fmt.Fprintln(output, "FileVault and automatic-login settings were not changed by this command.")
	}
	return nil
}
