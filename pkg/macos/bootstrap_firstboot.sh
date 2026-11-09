#!/bin/bash
# Root LaunchDaemon, intentionally silent. Failures leave this daemon and its
# private receipt in place for a retry; only verified success removes the job.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
export LC_ALL=C
umask 077
exec >/dev/null 2>&1
[ "$EUID" = 0 ] && [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = x86_64 ]
# Keep the exact active-instance test used by virtualGuestGuard: VMHide may
# suppress the hypervisor bit, but class-name presence alone is insufficient.
virtual_guest() {
    [ "$(/usr/sbin/sysctl -n kern.hv_vmm_present 2>/dev/null || true)" = 1 ] && return 0
    local registry line
    local instance='^[[:space:]|]*\+-o .+ <class AppleVirtIOPCITransport, id 0x[[:xdigit:]]+, registered, matched, active,'
    registry="$(/usr/sbin/ioreg -r -c AppleVirtIOPCITransport -d 1 -w 0 2>/dev/null)" || return 1
    while IFS= read -r line; do
        if [[ "$line" =~ $instance ]]; then return 0; fi
    done <<< "$registry"
    return 1
}
virtual_guest
csrutil status | grep -q 'status: enabled'
csrutil authenticated-root status | grep -q 'status: enabled'
[ "$(fdesetup status)" = 'FileVault is Off.' ]
PB=/usr/libexec/PlistBuddy
state=/Library/Corral
receipt="$state/firstboot-receipt.plist"
[ -d "$state" ] && [ ! -L "$state" ] && [ "$(stat -f '%u:%Lp' "$state")" = 0:700 ]
[ -f "$receipt" ] && [ ! -L "$receipt" ] && [ "$(stat -f '%u:%Lp' "$receipt")" = 0:600 ]
get() { "$PB" -c "Print :$2" "$1"; }
digest() { /usr/bin/openssl dgst -sha256 -r "$@" | cut -d ' ' -f 1; }
account=$(get "$receipt" User)
uuid=$(get "$receipt" UUID)
[[ "$account" =~ ^[a-z][a-z0-9_]{0,30}$ ]]
[[ "$uuid" =~ ^[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$ ]]
[ "$(id -u "$account")" = 501 ] && [ "$(id -g "$account")" = 20 ]
[ "$(dscl . -read "/Users/$account" GeneratedUID | cut -d ' ' -f 2)" = "$uuid" ]
dsmemberutil checkmembership -U "$account" -G admin | grep -q 'is a member'
record="/private/var/db/dslocal/nodes/Default/users/$account.plist"
[ -f "$record" ] && [ ! -L "$record" ] && [ "$(stat -f '%u:%Lp' "$record")" = 0:600 ]
[ "$(plutil -extract ShadowHashData xml1 -o - "$record" | digest)" = "$(get "$receipt" CredentialSHA256)" ]
home="/Users/$account"
[ -d "$home" ] && [ ! -L "$home" ] && [ "$(stat -f '%u' "$home")" = 501 ]
[ -d "$home/.ssh" ] && [ ! -L "$home/.ssh" ] && [ "$(stat -f '%u:%Lp' "$home/.ssh")" = 501:700 ]
[ -f "$home/.ssh/authorized_keys" ] && [ ! -L "$home/.ssh/authorized_keys" ] && [ "$(stat -f '%u:%Lp' "$home/.ssh/authorized_keys")" = 501:600 ]
[ "$(digest "$home/.ssh/authorized_keys")" = "$(get "$receipt" KeySHA256)" ]
# Use Apple's home initialization without copying over an existing user home.
createhomedir -c -u "$account"
for folder in Desktop Documents Downloads Library Movies Music Pictures Public; do
    [ ! -L "$home/$folder" ]
    if [ ! -e "$home/$folder" ]; then
        mkdir "$home/$folder"
        chown 501:20 "$home/$folder"
        chmod 700 "$home/$folder"
    fi
    [ -d "$home/$folder" ]
done
if ! dscl . -read /Groups/com.apple.access_ssh >/dev/null; then
    dseditgroup -o create -r 'Remote Login' com.apple.access_ssh
fi
dseditgroup -o edit -a "$account" -t user com.apple.access_ssh
dsmemberutil checkmembership -U "$account" -G com.apple.access_ssh | grep -q 'is a member'
ssh-keygen -A
# The global Include is installed before Apple's defaults, so a later setting
# cannot re-enable password or keyboard-interactive authentication.
effective=$(mktemp /private/var/run/corral-sshd.XXXXXXXX)
trap 'rm -f "$effective"' EXIT
/usr/sbin/sshd -t
/usr/sbin/sshd -T -C "user=$account,host=localhost,addr=127.0.0.1" > "$effective"
for setting in 'authenticationmethods publickey' 'pubkeyauthentication yes' 'passwordauthentication no' 'kbdinteractiveauthentication no' 'permitrootlogin no' "allowusers $account"; do
    grep -Fxq "$setting" "$effective"
done
launchctl enable system/com.openssh.sshd
if ! launchctl print system/com.openssh.sshd >/dev/null; then
    launchctl bootstrap system /System/Library/LaunchDaemons/ssh.plist
fi
launchctl kickstart system/com.openssh.sshd
launchctl print system/com.openssh.sshd >/dev/null
[ "$(launchctl print-disabled system | grep -F '"com.openssh.sshd"' | tr -d ' \t')" = '"com.openssh.sshd"=>false' ]
if [ "$(get "$receipt" AutomaticLogin)" = true ]; then
    [ "$(get /Library/Preferences/com.apple.loginwindow.plist autoLoginUser)" = "$account" ]
    [ -f /private/etc/kcpassword ] && [ ! -L /private/etc/kcpassword ] && [ "$(stat -f '%u:%Lp' /private/etc/kcpassword)" = 0:600 ]
else
    [ ! -e /private/etc/kcpassword ]
    [ -z "$(get /Library/Preferences/com.apple.loginwindow.plist autoLoginUser || true)" ]
fi
# Keep the private ownership receipt for safe Recovery resumption, but remove
# the startup job and executable only once real directory-service/SSH checks pass.
plutil -replace Finalized -bool true "$receipt"
rm -f /Library/LaunchDaemons/com.corral.bootstrap-firstboot.plist
rm -f "$state/bootstrap-firstboot.sh"
launchctl bootout system/com.corral.bootstrap-firstboot
