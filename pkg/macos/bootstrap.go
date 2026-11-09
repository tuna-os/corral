package macos

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GuestBootstrapOptions selects a blank installed macOS volume pair. Recovery
// Terminal focus and the US keyboard layout must be acknowledged explicitly.
type GuestBootstrapOptions struct {
	Target                                                        GuestTarget
	AdminItem, SystemVolume, DataVolume                           string
	AutomaticLogin, ConfirmRecoveryShell, ConfirmUSLayout, Reboot bool
}

type bootstrapPreflight struct {
	Verified         bool   `json:"verified"`
	SystemDevice     string `json:"system_device"`
	DataDevice       string `json:"data_device"`
	VolumeGroup      string `json:"volume_group"`
	SystemVolumeUUID string `json:"system_volume_uuid"`
	DataVolumeUUID   string `json:"data_volume_uuid"`
}

type bootstrapState struct {
	Version                                    int
	Context, Namespace, Name, VMUID, PublicKey string
	SystemVolume, DataVolume                   string
	AutomaticLogin                             bool
	Volumes                                    bootstrapPreflight
	Account                                    bootstrapAccount
}

func bootstrapInspect(data []byte) (bootstrapPreflight, error) {
	var result bootstrapPreflight
	if json.Unmarshal(data, &result) != nil || !result.Verified || result.SystemDevice == "" || result.DataDevice == "" || result.VolumeGroup == "" || result.SystemDevice == result.DataDevice || result.SystemVolumeUUID == "" || result.DataVolumeUUID == "" || result.SystemVolumeUUID == result.DataVolumeUUID {
		return result, errors.New("Recovery did not verify the installed System/Data volume pair")
	}
	return result, nil
}

// bootstrapVMUID binds retries to the persistent VM, not a reboot-specific VMI.
func bootstrapVMUID(ctx context.Context, target GuestTarget) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "kubectl", "--context", target.KubeContext, "-n", target.Namespace, "get", "vm/"+target.Name, "vmi/"+target.Name, "-o", "json").Output()
	if err != nil {
		return "", errors.New("cannot bind bootstrap to its owned VM and running VMI")
	}
	var list struct {
		Items []object `json:"items"`
	}
	if json.Unmarshal(data, &list) != nil {
		return "", errors.New("cannot inspect bootstrap VM ownership")
	}
	var vm, vmi object
	for _, item := range list.Items {
		switch item["kind"] {
		case "VirtualMachine":
			vm = item
		case "VirtualMachineInstance":
			vmi = item
		}
	}
	uid, _ := nested(vm, "metadata", "uid").(string)
	if uid == "" || !owned(vm, target.Name) || !owned(vmi, target.Name) || nested(vmi, "status", "phase") != "Running" {
		return "", errors.New("bootstrap requires this plugin's owned VM and running VMI")
	}
	refs, _ := nested(vmi, "metadata", "ownerReferences").([]any)
	for _, entry := range refs {
		ref, ok := entry.(map[string]any)
		if ok && ref["kind"] == "VirtualMachine" && ref["name"] == target.Name && ref["uid"] == uid && ref["controller"] == true {
			return uid, nil
		}
	}
	return "", errors.New("running VMI is not controlled by the selected persistent VM")
}

func bootstrapIdentity(ctx context.Context, target *GuestTarget) ([]byte, error) {
	if strings.TrimSpace(target.CacheDir) == "" {
		return nil, errors.New("a private bootstrap cache directory is required")
	}
	cache, err := guestExpandedPath(target.CacheDir)
	if err != nil || cache == "" {
		return nil, errors.New("a private bootstrap cache directory is required")
	}
	target.CacheDir = cache
	if err := privateDirectory(cache); err != nil {
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
	if err := privateDirectory(filepath.Dir(key)); err != nil {
		return nil, err
	}
	unlock, err := lockPrivateState(ctx, filepath.Dir(key))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := os.Lstat(key); errors.Is(err, os.ErrNotExist) {
		// The owned directory lock serializes generation. Never replace a key.
		cmd := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "corral-macos-bootstrap", "-f", key)
		if cmd.Run() != nil {
			return nil, errors.New("cannot generate the private dedicated bootstrap identity")
		}
	} else if err != nil {
		return nil, errors.New("cannot inspect the dedicated bootstrap identity")
	}
	if err := privateConfig(key); err != nil {
		return nil, errors.New("bootstrap requires an owned private regular SSH identity")
	}
	public, err := exec.CommandContext(ctx, "ssh-keygen", "-y", "-P", "", "-f", key).Output()
	if err != nil {
		return nil, errors.New("bootstrap requires a usable dedicated key without interactive passphrase prompts")
	}
	public = bytes.TrimSpace(public)
	if !bytes.HasPrefix(public, []byte("ssh-ed25519 ")) || !printableUS(string(public)) {
		return nil, errors.New("bootstrap requires a dedicated ed25519 SSH identity")
	}
	target.IdentityFile = key
	return public, nil
}

func bootstrapLoad(path string) (*bootstrapState, error) {
	file, err := privateOpenFile(path, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("refusing unsafe private bootstrap state")
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	defer clear(body)
	if err != nil || len(body) > 1<<20 {
		return nil, errors.New("cannot read private bootstrap state")
	}
	var state bootstrapState
	if json.Unmarshal(body, &state) != nil || state.Version != 1 || state.Account.User == "" || state.Account.UUID == "" || len(state.Account.Record) == 0 {
		clear(state.Account.Record)
		clear(state.Account.KCPassword)
		return nil, errors.New("private bootstrap state is invalid; preserve it for explicit repair, never reset an existing account")
	}
	return &state, nil
}

// Unlike public identity JSON, this includes password-equivalent account data.
// Clear the encoded bytes as well as the account buffers after atomic storage.
func bootstrapSave(ctx context.Context, path string, state *bootstrapState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	defer clear(body)
	file, err := os.CreateTemp(filepath.Dir(path), ".bootstrap-account-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func bootstrapInstallCommand(options GuestBootstrapOptions, mode, uuid string) string {
	auto := "false"
	if options.AutomaticLogin {
		auto = "true"
	}
	return guestScriptCommand(bootstrapInstallScript, "corral-bootstrap-install", false, mode, options.SystemVolume, options.DataVolume, options.Target.User, uuid, auto)
}

func bootstrapRecoveryReady(ctx context.Context, transport []string, command string) error {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	for {
		if _, err := guestRemote(ctx, transport, command, nil, nil); err == nil {
			return nil
		} else if errors.Is(err, errGuestHostKeyChanged) {
			return err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("temporary key-only Recovery SSH did not become ready; no vault item was read")
		case <-timer.C:
		}
	}
}

func bootstrapStream(ctx context.Context, transport []string, command string, account bootstrapAccount, public []byte) ([]byte, error) {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := writeBootstrapArchive(writer, account, public)
		writer.CloseWithError(err)
		done <- err
	}()
	data, err := guestRemote(ctx, transport, command, reader, nil)
	// Closing the reader also unblocks the archive producer after cancellation,
	// early SSH rejection, or a consumer that stops before reading the archive.
	reader.CloseWithError(err)
	writeErr := <-done
	if err != nil {
		return nil, err
	}
	if writeErr != nil {
		return nil, errors.New("cannot stream the private bootstrap account archive")
	}
	return data, nil
}

// BootstrapGuest creates the first local account offline without sending a
// password through the console or placing it in a guest plaintext file.
func BootstrapGuest(ctx context.Context, options GuestBootstrapOptions, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	if !options.ConfirmRecoveryShell || !options.ConfirmUSLayout {
		return errors.New("confirm a focused root Recovery Terminal and plain US keyboard layout before bootstrap")
	}
	if strings.TrimSpace(options.AdminItem) == "" {
		return errors.New("an explicit local-account Bitwarden login item is required")
	}
	if options.SystemVolume == "" || options.DataVolume == "" || options.SystemVolume == options.DataVolume || strings.ContainsAny(options.SystemVolume+options.DataVolume, "/\x00\r\n") {
		return errors.New("select distinct installed System and Data volume names, without path separators")
	}
	if !validBootstrapUser(options.Target.User) {
		return errors.New("a non-reserved simple dedicated local account username is required")
	}
	if err := ValidateRunningGuest(ctx, options.Target); err != nil {
		return err
	}
	vmUID, err := bootstrapVMUID(ctx, options.Target)
	if err != nil {
		return err
	}
	public, err := bootstrapIdentity(ctx, &options.Target)
	if err != nil {
		return err
	}
	target := options.Target
	scopeHash := sha256.Sum256([]byte(target.KubeContext + "\x00" + target.Namespace + "\x00" + target.Name))
	scope := hex.EncodeToString(scopeHash[:8])
	dir := filepath.Join(target.CacheDir, "guest-bootstrap", scope)
	if err := privateDirectory(dir); err != nil {
		return err
	}
	unlock, err := lockPrivateState(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	statePath := filepath.Join(dir, "account.json")
	state, err := bootstrapLoad(statePath)
	if err != nil {
		return err
	}
	defer func() {
		if state != nil {
			clear(state.Account.Record)
			clear(state.Account.KCPassword)
		}
	}()
	uuid := "-"
	if state != nil {
		if state.Context != target.KubeContext || state.Namespace != target.Namespace || state.Name != target.Name || state.VMUID != vmUID || state.PublicKey != string(public) || state.Account.User != target.User || state.SystemVolume != options.SystemVolume || state.DataVolume != options.DataVolume || state.AutomaticLogin != options.AutomaticLogin {
			return errors.New("bootstrap retry does not match its private VM, identity, volume or account receipt; refusing to reset an account")
		}
		uuid = state.Account.UUID
	}
	recovery := target
	recovery.User = "root"
	transport, err := guestSSHTransportScope(recovery, "2222", "recovery-ssh")
	if err != nil {
		return err
	}
	readyCommand := guestScriptCommand(bootstrapRecoveryGuard+bootstrapRecoveryStateGuard+bootstrapRecoveryHelpers+`
recovery_file "$dir/ready"
recovery_listener
`, "corral-recovery-ready", false, scope)
	probeCtx, cancelProbe := context.WithTimeout(ctx, 20*time.Second)
	_, readyErr := guestRemote(probeCtx, transport, readyCommand, nil, nil)
	cancelProbe()
	if errors.Is(readyErr, errGuestHostKeyChanged) {
		return readyErr
	}
	cleanupPending := true
	defer func() {
		if !cleanupPending {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if _, err := guestRemote(cleanupCtx, transport, guestScriptCommand(bootstrapRecoveryFinish, "corral-bootstrap-finish", false, scope, "cleanup", options.SystemVolume), nil, nil); err != nil {
			_, _ = fmt.Fprintln(output, "Temporary Recovery SSH cleanup was not acknowledged; inspect Recovery before leaving it unattended.")
		}
	}()
	if readyErr != nil {
		if err := bootstrapRecoveryConsole(ctx, target, public, scope, options.SystemVolume, vmUID); err != nil {
			return err
		}
		if err := bootstrapRecoveryReady(ctx, transport, readyCommand); err != nil {
			return err
		}
	}
	// No credential is read until the authenticated API/SSH transport and the
	// native root Recovery / installed-volume / account collision checks pass.
	data, err := guestRemote(ctx, transport, bootstrapInstallCommand(options, "preflight", uuid), nil, nil)
	if err != nil {
		return errors.New("Recovery SSH/preflight failed; no vault item was read and no installed account was changed")
	}
	volumes, err := bootstrapInspect(data)
	if err != nil {
		return err
	}
	if state != nil && (state.Volumes.VolumeGroup != volumes.VolumeGroup || state.Volumes.SystemVolumeUUID != volumes.SystemVolumeUUID || state.Volumes.DataVolumeUUID != volumes.DataVolumeUUID) {
		return errors.New("installed APFS group or System/Data volume UUID differs from the private bootstrap receipt")
	}
	_, _ = fmt.Fprintln(output, "Verified owned VM, key-authenticated root Recovery and installed blank/resumable System/Data volume pair.")
	username, password, err := vaultLogin(ctx, options.AdminItem)
	if err != nil {
		return err
	}
	defer func() { username, password = "", "" }()
	if username != target.User {
		return errors.New("the selected vault login username must equal the dedicated target account")
	}
	if state == nil {
		account, err := newBootstrapAccount(target.User, password, false)
		if err != nil {
			return err
		}
		state = &bootstrapState{Version: 1, Context: target.KubeContext, Namespace: target.Namespace, Name: target.Name, VMUID: vmUID, PublicKey: string(public), SystemVolume: options.SystemVolume, DataVolume: options.DataVolume, AutomaticLogin: options.AutomaticLogin, Volumes: volumes, Account: account}
		if err := bootstrapSave(ctx, statePath, state); err != nil {
			return errors.New("cannot persist private retry-safe account state; no account was installed")
		}
	} else if !bootstrapPasswordMatches(state.Account, password) {
		return errors.New("vault credentials differ from the pending bootstrap account; refusing any password reset")
	}
	if options.AutomaticLogin {
		state.Account.KCPassword = automaticLoginPassword(password)
	}
	username, password = "", ""
	data, err = bootstrapStream(ctx, transport, bootstrapInstallCommand(options, "apply", state.Account.UUID), state.Account, public)
	if err != nil {
		return errors.New("bootstrap installation did not complete; preserve private state and retry the identical target/credentials; no reboot requested")
	}
	installed, err := bootstrapInspect(data)
	if err != nil || installed.VolumeGroup != volumes.VolumeGroup || installed.SystemVolumeUUID != volumes.SystemVolumeUUID || installed.DataVolumeUUID != volumes.DataVolumeUUID {
		return errors.New("bootstrap installation did not return verified completion for the selected APFS volume identities; no reboot requested")
	}
	_, _ = fmt.Fprintln(output, "Staged the dedicated local administrator, private SSH key and native first-boot SSH setup. This is not GUI login or iMessage activation proof.")
	if options.Reboot {
		if _, err := guestRemote(ctx, transport, guestScriptCommand(bootstrapRecoveryFinish, "corral-bootstrap-finish", false, scope, "reboot", options.SystemVolume), nil, nil); err != nil {
			return errors.New("account staging completed, but Recovery cleanup/reboot was not acknowledged; inspect Recovery before continuing")
		}
		_, _ = fmt.Fprintln(output, "Recovery cleanup acknowledged; reboot requested. After macOS starts, inspect its GUI and run macos setup --check.")
	} else {
		if _, err := guestRemote(ctx, transport, guestScriptCommand(bootstrapRecoveryFinish, "corral-bootstrap-finish", false, scope, "cleanup", options.SystemVolume), nil, nil); err != nil {
			return errors.New("account staging completed, but temporary Recovery SSH cleanup was not acknowledged")
		}
		_, _ = fmt.Fprintln(output, "Recovery SSH cleanup acknowledged; reboot into the installed macOS when ready.")
	}
	cleanupPending = false
	return nil
}

func bootstrapRecoveryConsole(ctx context.Context, target GuestTarget, public []byte, scope, system, vmUID string) error {
	payload := []byte("set -- " + guestShellQuote(string(public)) + " " + guestShellQuote(scope) + " " + guestShellQuote(system) + "\n" + strings.TrimRight(bootstrapRecoveryScript, "\n"))
	var compressed bytes.Buffer
	zip, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return errors.New("cannot encode the public Recovery bootstrap")
	}
	if _, err := zip.Write(payload); err != nil {
		return errors.New("cannot compress the public Recovery bootstrap")
	}
	if err := zip.Close(); err != nil {
		return errors.New("cannot finish the public Recovery bootstrap encoding")
	}
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())
	digest := sha256.Sum256(payload)
	expected := base64.StdEncoding.EncodeToString(digest[:])
	openssl := guestShellQuote("/Volumes/" + system + "/usr/bin/openssl")
	gunzip := guestShellQuote("/Volumes/" + system + "/usr/bin/gzip")
	base64Tool := guestShellQuote("/Volumes/" + system + "/usr/bin/base64")
	// Only a small public guarded mount precedes integrity verification. The
	// complete consequential script AND its public arguments are checksummed;
	// dropped console characters cannot silently mutate a different payload.
	vm := `{ [ "$(/usr/sbin/sysctl -n kern.hv_vmm_present 2>/dev/null)" = 1 ] || /usr/sbin/ioreg -r -c AppleVirtIOPCITransport -d 1 -w 0 | /usr/bin/grep -Eq '^[[:space:]|]*\+-o .+ <class AppleVirtIOPCITransport, id 0x[[:xdigit:]]+, registered, matched, active,'; }`
	command := `if [ "$(/usr/bin/id -u)" = 0 ] && [ "$(/usr/bin/uname -s)" = Darwin ] && [ "$(/usr/bin/uname -m)" = x86_64 ] && [ -d /System/Installation/CDIS ] && ` + vm + `; then if /usr/sbin/diskutil mount ` + guestShellQuote(system) + ` >/dev/null 2>&1; then s="$(printf %s ` + guestShellQuote(encoded) + ` | ` + base64Tool + ` -D | ` + gunzip + ` -dc)"; if [ "$(printf %s "$s" | ` + openssl + ` dgst -sha256 -binary | ` + base64Tool + `)" = ` + guestShellQuote(expected) + ` ]; then /bin/bash -c "$s"; else printf 'Public Recovery bootstrap checksum failed; nothing executed.\n'; fi; fi; fi`
	if !printableUS(command) {
		return errors.New("public Recovery bootstrap requires printable US input")
	}
	if err := typeRecoveryPublicQMP(ctx, target, vmUID, command); err != nil {
		return err
	}
	return nil
}

const bootstrapRecoveryGuard = `set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
[ "$(/usr/bin/id -u)" = 0 ] || exit 70
[ "$(/usr/bin/uname -s)" = Darwin ] || exit 70
[ "$(/usr/bin/uname -m)" = x86_64 ] || exit 70
[ -d /System/Installation/CDIS ] || exit 70
case "$(/sbin/mount)" in *" on / (hfs,"*) ;; *) exit 70;; esac
` + virtualGuestGuard + `
virtual_guest || exit 70
`

// These checks apply both to startup and to the authenticated cleanup command.
// The persistent Recovery host key is deliberately retained for trusted retries.
const bootstrapRecoveryStateGuard = `
scope="$1"
case "$scope" in ''|*[!0-9a-f]*) exit 70;; esac
[ "${#scope}" = 16 ] || exit 70
root=/.corral-recovery
dir="$root/$scope"
for path in "$root" "$dir"; do
    [ -d "$path" ] && [ ! -L "$path" ] || exit 70
    [ "$(/usr/bin/stat -f '%u:%g:%Lp' "$path")" = 0:0:700 ] || exit 70
done
`

const bootstrapRecoveryHelpers = `
recovery_file() {
    [ -f "$1" ] && [ ! -L "$1" ] || exit 70
    [ "$(/usr/bin/stat -f '%u:%g:%Lp:%l' "$1")" = 0:0:600:1 ] || exit 70
}
recovery_digest() {
    "$tools/openssl" dgst -sha256 -binary "$1" | "$tools/base64"
}
recovery_listener() {
    recovery_file "$dir/sshd.pid"
    IFS= read -r pid < "$dir/sshd.pid"
    case "$pid" in ''|*[!0-9]*) exit 70;; esac
    [ "$pid" -gt 1 ] || exit 70
    /bin/kill -0 "$pid" 2>/dev/null || return 1
    [ "$(/bin/ps -p "$pid" -o uid= | /usr/bin/tr -d ' ')" = 0 ] || exit 70
    case "$(/bin/ps -p "$pid" -o command=)" in
        *"/usr/sbin/sshd -f $dir/sshd_config"*) ;;
        *) exit 70;;
    esac
}
recovery_stop_listener() {
    if ! recovery_listener; then return 0; fi
    /bin/kill -TERM "$pid"
    for attempt in 1 2 3 4 5 6 7 8 9 10; do
        /bin/kill -0 "$pid" 2>/dev/null || return 0
        /bin/sleep 1
    done
    exit 70
}
recovery_library() {
    [ -f "$1" ] && [ ! -L "$1" ] || exit 70
    [ "$(/usr/bin/stat -f '%u:%g:%l' "$1")" = 0:0:1 ] || exit 70
    case "$(/usr/bin/stat -f '%Lp' "$1")" in 444|555|644|755) ;; *) exit 70;; esac
}
recovery_jail_directory() {
    [ -d "$1" ] && [ ! -L "$1" ] || exit 70
    [ "$(/usr/bin/stat -f '%u:%Lp' "$1")" = 0:755 ] || exit 70
}
recovery_prepare_jail() {
    recovery_jail_directory /var/empty
    for name in usr usr/lib; do
        path="/var/empty/$name"
        marker="$dir/jail-${name//\//-}.created"
        if [ -e "$marker" ] || [ -L "$marker" ]; then recovery_file "$marker"; fi
        if [ ! -e "$path" ] && [ ! -L "$path" ]; then
            /bin/mkdir -m 755 "$path"
            printf 'created\n' > "$marker"
        fi
        recovery_jail_directory "$path"
    done
    for name in libsandbox.1.dylib libMatch.1.dylib; do
        source="/usr/lib/$name"
        target="/var/empty/usr/lib/$name"
        marker="$dir/jail-$name.sha256"
        staged="$dir/jail-$name.new"
        recovery_library "$source"
        digest="$(recovery_digest "$source")"
        if [ -e "$marker" ] || [ -L "$marker" ]; then
            recovery_file "$marker"
            [ "$(/bin/cat "$marker")" = "$digest" ] || exit 70
        fi
        if [ -e "$target" ] || [ -L "$target" ]; then
            recovery_library "$target"
            "$tools/cmp" -s "$source" "$target" || exit 70
        else
            if [ -e "$staged" ] || [ -L "$staged" ]; then recovery_library "$staged"; fi
            /bin/cp -p "$source" "$staged"
            "$tools/cmp" -s "$source" "$staged" || exit 70
            printf '%s\n' "$digest" > "$marker"
            /bin/mv "$staged" "$target"
        fi
    done
}
recovery_restore_jail() {
    for name in libsandbox.1.dylib libMatch.1.dylib; do
        marker="$dir/jail-$name.sha256"
        target="/var/empty/usr/lib/$name"
        if [ -e "$marker" ] || [ -L "$marker" ]; then
            recovery_file "$marker"
            if [ -e "$target" ] || [ -L "$target" ]; then
                recovery_jail_directory /var/empty
                recovery_jail_directory /var/empty/usr
                recovery_jail_directory /var/empty/usr/lib
                recovery_library "$target"
                [ "$(recovery_digest "$target")" = "$(/bin/cat "$marker")" ] || exit 70
                /bin/rm "$target"
            fi
            /bin/rm "$marker"
        fi
        if [ -e "$dir/jail-$name.new" ] || [ -L "$dir/jail-$name.new" ]; then
            recovery_library "$dir/jail-$name.new"
            /bin/rm "$dir/jail-$name.new"
        fi
    done
    for name in usr/lib usr; do
        marker="$dir/jail-${name//\//-}.created"
        if [ -e "$marker" ] || [ -L "$marker" ]; then
            recovery_file "$marker"
            recovery_jail_directory "/var/empty/$name"
            /bin/rmdir "/var/empty/$name"
            /bin/rm "$marker"
        fi
    done
}
recovery_restore_master() {
    [ ! -L "$dir/master.passwd.original" ] || exit 70
    [ -e "$dir/master.passwd.original" ] || return 0
    recovery_file "$dir/master.passwd.original"
    recovery_file "$dir/master.passwd.expected"
    recovery_file /etc/master.passwd
    original="$(recovery_digest "$dir/master.passwd.original")"
    current="$(recovery_digest /etc/master.passwd)"
    expected="$(/bin/cat "$dir/master.passwd.expected")"
    if [ "$current" != "$original" ]; then
        [ "$current" = "$expected" ] || exit 70
        /bin/cp -p "$dir/master.passwd.original" /etc/master.passwd
        "$tools/cmp" -s "$dir/master.passwd.original" /etc/master.passwd || exit 70
    fi
    /bin/rm -f "$dir/master.passwd.original" "$dir/master.passwd.expected" "$dir/master.passwd.modified"
}
`

const bootstrapRecoveryScript = bootstrapRecoveryGuard + `
public="$1"; scope="$2"; system="$3"
case "$scope" in ''|*[!0-9a-f]*) exit 70;; esac
[ "${#scope}" = 16 ] || exit 70
case "$system" in ''|*/*|*$'\n'*|*$'\r'*) exit 70;; esac
case "$public" in "ssh-ed25519 "*) ;; *) exit 70;; esac
tools="/Volumes/$system/usr/bin"
[ -x "$tools/ssh-keygen" ] && [ -x "$tools/openssl" ] && [ -x "$tools/base64" ] && [ -x "$tools/cmp" ] || exit 70
[ -w /etc/master.passwd ] || /sbin/mount -uw /
umask 077
root=/.corral-recovery
dir="$root/$scope"
for path in "$root" "$dir"; do
    if [ ! -e "$path" ] && [ ! -L "$path" ]; then
        /bin/mkdir "$path"
        /bin/chmod 700 "$path"
    fi
done
set -- "$scope"
` + bootstrapRecoveryStateGuard + bootstrapRecoveryHelpers + `
for file in master.passwd.original master.passwd.modified master.passwd.expected ssh_host_ed25519_key ssh_host_ed25519_key.pub sshd_config authorized_keys sshd.pid ready; do
    if [ -e "$dir/$file" ] || [ -L "$dir/$file" ]; then recovery_file "$dir/$file"; fi
done
if [ -e "$dir/sshd.pid" ]; then
    if recovery_listener; then
        recovery_file "$dir/authorized_keys"
        [ "$(/bin/cat "$dir/authorized_keys")" = "$public" ] || exit 70
        recovery_file "$dir/sshd_config"
        recovery_file "$dir/ready"
        exit 0
    fi
    for file in authorized_keys sshd_config ready; do
        if [ -e "$dir/$file" ]; then recovery_file "$dir/$file"; fi
    done
    /bin/rm -f "$dir/sshd.pid" "$dir/authorized_keys" "$dir/sshd_config" "$dir/ready"
fi
started=false
failed_start() {
    status=$?
    trap - EXIT
    if [ "$status" -ne 0 ]; then
        if [ "$started" = true ]; then
            recovery_stop_listener
        fi
        recovery_restore_master
        recovery_restore_jail
        /bin/rm -f "$dir/authorized_keys" "$dir/sshd_config" "$dir/sshd.pid" "$dir/ready"
    fi
    exit "$status"
}
trap failed_start EXIT
# Recovery's file-backed NSS uses master.passwd, not passwd or OD aliases.
# Never modify an installed account or the stock Recovery _sshd DS record.
if /usr/bin/id -u sshd >/dev/null 2>&1; then
    [ "$(/usr/bin/id -u sshd)" = 75 ] && [ "$(/usr/bin/id -g sshd)" = 75 ] || exit 70
    if [ -e "$dir/master.passwd.original" ]; then
        recovery_file "$dir/master.passwd.expected"
        [ "$(recovery_digest /etc/master.passwd)" = "$(/bin/cat "$dir/master.passwd.expected")" ] || exit 70
    fi
else
    [ "$(/usr/bin/id -u _sshd)" = 75 ] && [ "$(/usr/bin/id -g _sshd)" = 75 ] || exit 70
    recovery_file /etc/master.passwd
    if [ ! -e "$dir/master.passwd.original" ]; then
        /bin/cp -p /etc/master.passwd "$dir/master.passwd.original"
    fi
    recovery_file "$dir/master.passwd.original"
    ! /usr/bin/grep -q '^sshd:' "$dir/master.passwd.original" || exit 70
    if [ -e "$dir/master.passwd.modified" ]; then recovery_file "$dir/master.passwd.modified"; fi
    /bin/cat "$dir/master.passwd.original" > "$dir/master.passwd.modified"
    printf '\nsshd:*:75:75::0:0:OpenSSH privilege separation:/var/empty:/usr/bin/false\n' >> "$dir/master.passwd.modified"
    modified="$(recovery_digest "$dir/master.passwd.modified")"
    if [ -e "$dir/master.passwd.expected" ]; then
        recovery_file "$dir/master.passwd.expected"
        [ "$(/bin/cat "$dir/master.passwd.expected")" = "$modified" ] || exit 70
    else
        printf '%s\n' "$modified" > "$dir/master.passwd.expected"
    fi
    current="$(recovery_digest /etc/master.passwd)"
    [ "$current" = "$(recovery_digest "$dir/master.passwd.original")" ] || [ "$current" = "$modified" ] || exit 70
    /bin/cp -p "$dir/master.passwd.modified" /etc/master.passwd
    [ "$(/usr/bin/id -u sshd)" = 75 ] && [ "$(/usr/bin/id -g sshd)" = 75 ] || exit 70
    /bin/rm -f "$dir/master.passwd.modified"
fi
recovery_prepare_jail
if [ -e "$dir/ssh_host_ed25519_key" ]; then
    recovery_file "$dir/ssh_host_ed25519_key"
else
    [ ! -L "$dir/ssh_host_ed25519_key" ] || exit 70
    "$tools/ssh-keygen" -q -t ed25519 -N '' -f "$dir/ssh_host_ed25519_key"
    /bin/chmod 600 "$dir/ssh_host_ed25519_key" "$dir/ssh_host_ed25519_key.pub"
fi
for file in authorized_keys sshd_config ready; do
    if [ -e "$dir/$file" ] || [ -L "$dir/$file" ]; then recovery_file "$dir/$file"; fi
done
printf '%s\n' "$public" > "$dir/authorized_keys"
/bin/cat > "$dir/sshd_config" <<CONFIG
Port 2222
ListenAddress 0.0.0.0
HostKey $dir/ssh_host_ed25519_key
PidFile $dir/sshd.pid
AuthorizedKeysFile $dir/authorized_keys
PermitRootLogin prohibit-password
AllowUsers root
AuthenticationMethods publickey
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitEmptyPasswords no
UsePAM no
StrictModes yes
PermitUserEnvironment no
AllowTcpForwarding no
AllowAgentForwarding no
X11Forwarding no
PermitTunnel no
LogLevel ERROR
CONFIG
/usr/sbin/sshd -t -f "$dir/sshd_config"
/usr/sbin/sshd -f "$dir/sshd_config"
started=true
for attempt in 1 2 3 4 5; do
    [ ! -e "$dir/sshd.pid" ] || break
    /bin/sleep 1
done
[ -f "$dir/sshd.pid" ] && [ ! -L "$dir/sshd.pid" ] || exit 70
[ "$(/usr/bin/stat -f '%u:%g:%l' "$dir/sshd.pid")" = 0:0:1 ] || exit 70
/bin/chmod 600 "$dir/sshd.pid"
recovery_listener
printf 'ready\n' > "$dir/ready"
/bin/sync
`

const bootstrapRecoveryFinish = bootstrapRecoveryGuard + bootstrapRecoveryStateGuard + bootstrapRecoveryHelpers + `
case "$2" in cleanup|reboot) ;; *) exit 70;; esac
case "$3" in ''|*/*|*$'\n'*|*$'\r'*) exit 70;; esac
tools="/Volumes/$3/usr/bin"
[ -x "$tools/openssl" ] || exit 70
for name in sshd_config authorized_keys ready; do recovery_file "$dir/$name"; done
recovery_stop_listener
recovery_restore_master
recovery_restore_jail
/bin/rm -f "$dir/authorized_keys" "$dir/sshd_config" "$dir/sshd.pid" "$dir/ready"
/bin/sync
if [ "$2" = reboot ]; then
    (/bin/sleep 2; /sbin/reboot) </dev/null >/dev/null 2>&1 &
fi
printf 'Recovery cleanup acknowledged.\n'
`
