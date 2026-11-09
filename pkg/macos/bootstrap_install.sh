#!/bin/bash
# Invoked only through the authenticated, private Recovery transport. Never trace
# this script: stdin includes a native password hash and optional kcpassword.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
export LC_ALL=C
umask 077
exec 2>/dev/null
fail() { exit 70; }
[ "$#" = 6 ] || fail
mode=$1; system_name=$2; data_name=$3; account=$4; account_uuid=$5; autologin=$6
case "$mode" in preflight|apply) ;; *) fail ;; esac
case "$autologin" in true|false) ;; *) fail ;; esac
[[ "$account" =~ ^[a-z][a-z0-9_]{0,30}$ ]] || fail
case "$account" in root|daemon|nobody|guest|admin|administrator|wheel|staff|everyone|sshd|corral) fail ;; esac
if [ "$account_uuid" != - ]; then
    [[ "$account_uuid" =~ ^[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$ ]] || fail
else
    [ "$mode" = preflight ] || fail
fi
[ "$EUID" = 0 ] && [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = x86_64 ] || fail
# VMHide can conceal the hypervisor bit. Match the same active native VirtIO
# transport evidence as virtualGuestGuard, never a class-name-only listing.
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
virtual_guest || fail
csrutil status | /usr/bin/grep -q 'status: enabled' || fail
csrutil authenticated-root status | /usr/bin/grep -q 'status: enabled' || fail
# Names select already mounted candidates only; APFS identity, roles and device
# checks below are authoritative. No mount, erase, unlock or encryption changes.
for name in "$system_name" "$data_name"; do
    [ -n "$name" ] && [ "$name" != . ] && [ "$name" != .. ] || fail
    case "$name" in */*|*$'\n'*|*$'\r'*) fail ;; esac
done
system="/Volumes/$system_name"; data="/Volumes/$data_name"
[ "$system" != "$data" ] && [ -d "$system" ] && [ -d "$data" ] && [ ! -L "$system" ] && [ ! -L "$data" ] || fail
[ ! -L /Volumes ] || fail
for component in "$system/usr" "$system/usr/bin" "$system/usr/libexec" "$system/usr/libexec/PlistBuddy" "$system/usr/bin/plutil" "$system/usr/bin/openssl" "$system/usr/bin/ssh-keygen" "$system/usr/bin/cmp" "$system/usr/bin/install"; do
    [ -e "$component" ] && [ ! -L "$component" ] || fail
done
export PATH="$PATH:$system/usr/bin"
PB="$system/usr/libexec/PlistBuddy"
plutil() { "$system/usr/bin/plutil" "$@"; }
get() { "$PB" -c "Print :$2" "$1"; }
digest() { "$system/usr/bin/openssl" dgst -sha256 -r "$@" | cut -d ' ' -f 1; }
root_info=$(mktemp /private/tmp/corral-root.XXXXXXXX)
work=$(mktemp -d /private/tmp/corral-preflight.XXXXXXXX)
stage=
lock=
trap 'rm -f "$root_info"; rm -rf "$work"; if [ -n "$stage" ]; then rm -rf "$stage"; fi; if [ -n "$lock" ]; then rmdir "$lock"; fi' EXIT
chmod 700 "$work"
diskutil info -plist / > "$root_info"
# Recovery's observed root is a writable HFS image with an empty volume name,
# not an APFS installed root. Its CDIS tree is the native Recovery environment.
[ -d /System/Installation/CDIS ] && [ ! -L /System/Installation/CDIS ] || fail
[ "$(get "$root_info" FilesystemType)" = hfs ] && [ "$(get "$root_info" MountPoint)" = / ] || fail
root_device=$(get "$root_info" DeviceIdentifier)
diskutil info -plist "$system" > "$work/system.plist"
diskutil info -plist "$data" > "$work/data.plist"
system_device=$(get "$work/system.plist" DeviceIdentifier)
data_device=$(get "$work/data.plist" DeviceIdentifier)
[[ "$system_device" =~ ^disk[0-9]+s[0-9]+$ ]] && [[ "$data_device" =~ ^disk[0-9]+s[0-9]+$ ]] || fail
[ "$system_device" != "$data_device" ] && [ "$system_device" != "$root_device" ] && [ "$data_device" != "$root_device" ] || fail
[ "$(get "$work/system.plist" MountPoint)" = "$system" ] && [ "$(get "$work/data.plist" MountPoint)" = "$data" ] || fail
[ "$(get "$work/system.plist" FilesystemType)" = apfs ] && [ "$(get "$work/data.plist" FilesystemType)" = apfs ] || fail
[ "$(get "$work/system.plist" APFSVolumeGroupID)" = "$(get "$work/data.plist" APFSVolumeGroupID)" ] || fail
volume_group=$(get "$work/system.plist" APFSVolumeGroupID)
[[ "$volume_group" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]] || fail
system_uuid=$(get "$work/system.plist" VolumeUUID)
data_uuid=$(get "$work/data.plist" VolumeUUID)
for uuid in "$system_uuid" "$data_uuid"; do
    [[ "$uuid" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]] || fail
done
[ "$system_uuid" != "$data_uuid" ] || fail
for info in "$work/system.plist" "$work/data.plist"; do
    [ "$(get "$info" FileVault)" = false ] || fail
    [ "$(get "$info" Encryption)" = false ] && [ "$(get "$info" EncryptionThisVolumeProper)" = false ] || fail
    [ "$(get "$info" Locked)" = false ] || fail
done
[ "$(get "$work/data.plist" WritableVolume)" = true ] || fail
[ "$(get "$work/system.plist" APFSContainerReference)" = "$(get "$work/data.plist" APFSContainerReference)" ] || fail
diskutil apfs list -plist > "$work/apfs.plist"
system_matches=0; data_matches=0
system_container=; data_container=
container_index=0
while container=$(get "$work/apfs.plist" "Containers:$container_index:ContainerReference"); do
    volume_index=0
    while device=$(get "$work/apfs.plist" "Containers:$container_index:Volumes:$volume_index:DeviceIdentifier"); do
        volume_path="Containers:$container_index:Volumes:$volume_index"
        if [ "$device" = "$system_device" ] || [ "$device" = "$data_device" ]; then
            [ "$(get "$work/apfs.plist" "$volume_path:Encryption")" = false ] || fail
            [ "$(get "$work/apfs.plist" "$volume_path:FileVault")" = false ] || fail
            [ "$(get "$work/apfs.plist" "$volume_path:Locked")" = false ] || fail
            [ "$(get "$work/apfs.plist" "$volume_path:CryptoMigrationOn")" = false ] || fail
            if get "$work/apfs.plist" "$volume_path:Roles:1" >/dev/null; then fail; fi
            if [ "$device" = "$system_device" ]; then
                [ "$(get "$work/apfs.plist" "$volume_path:Roles:0")" = System ] || fail
                [ "$(get "$work/apfs.plist" "$volume_path:APFSVolumeUUID")" = "$system_uuid" ] || fail
                system_matches=$((system_matches+1)); system_container=$container
            else
                [ "$(get "$work/apfs.plist" "$volume_path:Roles:0")" = Data ] || fail
                [ "$(get "$work/apfs.plist" "$volume_path:APFSVolumeUUID")" = "$data_uuid" ] || fail
                data_matches=$((data_matches+1)); data_container=$container
            fi
        fi
        volume_index=$((volume_index+1))
    done
    container_index=$((container_index+1))
done
[ "$system_matches" = 1 ] && [ "$data_matches" = 1 ] && [ "$system_container" = "$data_container" ] || fail
[ "$system_container" = "$(get "$work/system.plist" APFSContainerReference)" ] || fail
# Do not traverse a symlink at any point under the selected Data mount.
safe() {
    local path=$1 parent
    case "$path" in "$data"|"$data"/*) ;; *) fail ;; esac
    parent=$path
    while [ "$parent" != "$data" ]; do
        [ ! -L "$parent" ] || fail
        parent=${parent%/*}
    done
}
root_file() {
    safe "$1"
    [ -f "$1" ] && [ "$(stat -f '%u' "$1")" = 0 ] || fail
}
ensure_dir() {
    safe "$1"
    if [ -e "$1" ]; then
        [ -d "$1" ] && [ "$(stat -f '%u' "$1")" = 0 ] || fail
    else
        mkdir "$1"
        chown 0:0 "$1"
    fi
}
version="$system/System/Library/CoreServices/SystemVersion.plist"
[ -f "$version" ] && [ ! -L "$version" ] || fail
[ "$(get "$version" ProductName)" = macOS ] || fail
[[ "$(get "$version" ProductVersion)" =~ ^(1[3-9]|[2-9][0-9])\. ]] || fail
users="$data/private/var/db/dslocal/nodes/Default/users"
admin="$data/private/var/db/dslocal/nodes/Default/groups/admin.plist"
state="$data/Library/Corral"
receipt="$state/firstboot-receipt.plist"
home="$data/Users/$account"
record="$users/$account.plist"
setup_done="$data/private/var/db/.AppleSetupDone"
for dir in "$users" "$data/Users" "$data/Library" "$data/Library/LaunchDaemons" "$data/Library/Preferences" "$data/private/etc/ssh"; do
    safe "$dir"; [ -d "$dir" ] && [ "$(stat -f '%u' "$dir")" = 0 ] || fail
done
root_file "$admin"
safe "$state"; safe "$receipt"; safe "$home"; safe "$record"; safe "$setup_done"
if [ "$mode" = apply ]; then
    lock_candidate="$data/private/var/db/.corral-bootstrap-lock"
    safe "$lock_candidate"
    mkdir "$lock_candidate" || fail
    lock=$lock_candidate
fi
owned=false
if [ -e "$receipt" ]; then
    root_file "$receipt"
    [ "$(stat -f '%Lp' "$receipt")" = 600 ] && [ "$(stat -f '%Lp' "$state")" = 700 ] || fail
    [ "$account_uuid" != - ] || fail
    [ "$(get "$receipt" User)" = "$account" ] && [ "$(get "$receipt" UUID)" = "$account_uuid" ] || fail
    [ "$(get "$receipt" VolumeGroup)" = "$volume_group" ] || fail
    [ "$(get "$receipt" SystemUUID)" = "$system_uuid" ] && [ "$(get "$receipt" DataUUID)" = "$data_uuid" ] || fail
    [ "$(get "$receipt" AutomaticLogin)" = "$autologin" ] || fail
    owned=true
else
    [ ! -e "$state" ] && [ ! -e "$record" ] && [ ! -e "$home" ] && [ ! -e "$setup_done" ] || fail
fi
for entry in "$users"/*.plist; do
    root_file "$entry"
    uid=$(get "$entry" uid:0)
    [[ "$uid" =~ ^-?[0-9]+$ ]] || fail
    index=0
    while alias_name=$(get "$entry" "name:$index"); do
        if [ "$alias_name" = "$account" ]; then
            [ "$owned" = true ] && [ "$entry" = "$record" ] || fail
        fi
        index=$((index+1))
    done
    if [ "$account_uuid" != - ] && [ "$(get "$entry" generateduid:0 || true)" = "$account_uuid" ]; then
        [ "$owned" = true ] && [ "$entry" = "$record" ] || fail
    fi
    if [ "$uid" -ge 500 ] || [ "$uid" = 201 ]; then
        [ "$owned" = true ] && [ "$entry" = "$record" ] && [ "$uid" = 501 ] || fail
        [ "$(get "$entry" generateduid:0)" = "$account_uuid" ] || fail
        [ "$(get "$entry" name:0)" = "$account" ] || fail
        [ "$(get "$entry" home:0)" = "/Users/$account" ] && [ "$(get "$entry" gid:0)" = 20 ] || fail
        credential_sha=$(plutil -extract ShadowHashData xml1 -o - "$entry" | digest)
        [ "$credential_sha" = "$(get "$receipt" CredentialSHA256)" ] || fail
    fi
done
login="$data/Library/Preferences/com.apple.loginwindow.plist"
safe "$login"
if [ -e "$login" ]; then
    root_file "$login"
    current=$(get "$login" autoLoginUser || true)
    if [ -n "$current" ]; then
        [ "$owned" = true ] && [ "$current" = "$account" ] && [ "$autologin" = true ] || fail
    fi
fi
safe "$data/private/etc/kcpassword"
if [ -e "$data/private/etc/kcpassword" ]; then
    [ "$owned" = true ] && [ "$autologin" = true ] || fail
    root_file "$data/private/etc/kcpassword"
fi
emit_verified() {
    printf '{"verified":true,"system_device":"%s","data_device":"%s","volume_group":"%s","system_volume_uuid":"%s","data_volume_uuid":"%s"}\n' "$system_device" "$data_device" "$volume_group" "$system_uuid" "$data_uuid"
}
if [ "$mode" = preflight ]; then emit_verified; exit 0; fi
# Fixed-member validation precedes extraction. No links, directories, duplicate
# names, pax extensions or arbitrary paths are accepted from stdin.
stage=$(mktemp -d "$data/private/var/db/.corral-bootstrap.XXXXXXXX")
chmod 700 "$stage"
cat > "$stage/payload.tar"
[ "$(stat -f '%z' "$stage/payload.tar")" -le 262144 ] || fail
tar -tf "$stage/payload.tar" > "$stage/members"
expected=4; [ "$autologin" = false ] || expected=5
[ "$(wc -l < "$stage/members" | tr -d ' ')" = "$expected" ] || fail
[ "$(sort -u "$stage/members" | wc -l | tr -d ' ')" = "$expected" ] || fail
while IFS= read -r member; do
    case "$member" in user.plist|authorized_keys|bootstrap-firstboot.sh|bootstrap-firstboot.plist) ;;
        kcpassword) [ "$autologin" = true ] || fail ;; *) fail ;; esac
done < "$stage/members"
[ "$(tar -tvf "$stage/payload.tar" | /usr/bin/grep -c '^-')" = "$expected" ] || fail
tar -xpf "$stage/payload.tar" -C "$stage"
for member in user.plist authorized_keys bootstrap-firstboot.sh bootstrap-firstboot.plist; do
    [ -f "$stage/$member" ] && [ ! -L "$stage/$member" ] || fail
done
plutil -lint "$stage/user.plist" >/dev/null
[ "$(get "$stage/user.plist" name:0)" = "$account" ] && [ "$(get "$stage/user.plist" uid:0)" = 501 ] || fail
[ "$(get "$stage/user.plist" gid:0)" = 20 ] && [ "$(get "$stage/user.plist" generateduid:0)" = "$account_uuid" ] || fail
[ "$(get "$stage/user.plist" home:0)" = "/Users/$account" ] && [ "$(get "$stage/user.plist" shell:0)" = /bin/zsh ] || fail
[ "$(get "$stage/user.plist" passwd:0)" = '********' ] || fail
[ "$(get "$stage/user.plist" authentication_authority:0)" = ';ShadowHash;HASHLIST:<SALTED-SHA512-PBKDF2>' ] || fail
[ -n "$(get "$stage/user.plist" ShadowHashData:0)" ] || fail
"$system/usr/bin/ssh-keygen" -lf "$stage/authorized_keys" >/dev/null
record_sha=$(digest "$stage/user.plist")
credential_sha=$(plutil -extract ShadowHashData xml1 -o - "$stage/user.plist" | digest)
key_sha=$(digest "$stage/authorized_keys")
if [ "$owned" = true ]; then
    [ "$(get "$receipt" RecordSHA256)" = "$record_sha" ] && [ "$(get "$receipt" KeySHA256)" = "$key_sha" ] || fail
else
    ensure_dir "$state"; chmod 700 "$state"
    plutil -create xml1 "$stage/receipt.plist"
    {
        for pair in "User:$account" "UUID:$account_uuid" "VolumeGroup:$volume_group" "SystemUUID:$system_uuid" "DataUUID:$data_uuid" "AutomaticLogin:$autologin" "RecordSHA256:$record_sha" "CredentialSHA256:$credential_sha" "KeySHA256:$key_sha"; do
            printf 'Add :%s string %s\n' "${pair%%:*}" "${pair#*:}"
        done
        printf 'Save\n'
    } | "$PB" "$stage/receipt.plist" >/dev/null
    chmod 600 "$stage/receipt.plist"; chown 0:0 "$stage/receipt.plist"
    mv "$stage/receipt.plist" "$receipt"
fi
# From this point all destinations are owned by this private receipt. Existing
# credentials are never replaced: a retry must match the original record bytes.
if [ ! -e "$record" ]; then
    install -o 0 -g 0 -m 600 "$stage/user.plist" "$stage/installed-user.plist"
    mv "$stage/installed-user.plist" "$record"
fi
admin_destination=$admin
cp -p "$admin" "$stage/admin.plist"
admin="$stage/admin.plist"
append_member() {
    local array=$1 value=$2 i=0 current
    if ! "$PB" -c "Print :$array" "$admin" >/dev/null; then "$PB" -c "Add :$array array" "$admin"; fi
    while current=$(get "$admin" "$array:$i"); do
        [ "$current" != "$value" ] || return 0
        i=$((i+1))
    done
    "$PB" -c "Add :$array: string $value" "$admin"
}
append_member users "$account"
append_member groupmembers "$account_uuid"
plutil -lint "$admin" >/dev/null
mv "$admin" "$admin_destination"
admin=$admin_destination
if [ ! -e "$home" ]; then
    mkdir "$stage/home"
    chown 501:20 "$stage/home"; chmod 700 "$stage/home"
    mv "$stage/home" "$home"
fi
[ -d "$home" ] && [ "$(stat -f '%u' "$home")" = 501 ] || fail
safe "$home/.ssh"; safe "$home/.ssh/authorized_keys"
if [ ! -e "$home/.ssh" ]; then mkdir "$home/.ssh"; fi
[ -d "$home/.ssh" ] || fail
if [ -e "$home/.ssh/authorized_keys" ]; then
    [ -f "$home/.ssh/authorized_keys" ] && cmp -s "$home/.ssh/authorized_keys" "$stage/authorized_keys" || fail
else
    install -o 501 -g 20 -m 600 "$stage/authorized_keys" "$home/.ssh/authorized_keys"
fi
chown 501:20 "$home/.ssh"; chmod 700 "$home/.ssh"; chmod 600 "$home/.ssh/authorized_keys"
# Locale and Setup Assistant policy apply only to this blank-install bootstrap.
global="$data/Library/Preferences/.GlobalPreferences.plist"
safe "$global"
if [ ! -e "$global" ]; then plutil -create xml1 "$global"; else root_file "$global"; fi
plutil -replace AppleLocale -string en_US "$global"
plutil -replace AppleLanguages -json '["en-US"]' "$global"
plutil -replace Country -string US "$global"
if [ ! -e "$login" ]; then plutil -create xml1 "$login"; fi
plutil -replace SHOWFULLNAME -bool true "$login"
if [ "$autologin" = true ]; then
    [ -f "$stage/kcpassword" ] && [ ! -L "$stage/kcpassword" ] || fail
    size=$(stat -f '%z' "$stage/kcpassword")
    [ "$size" -ge 12 ] && [ "$size" -le 1032 ] && [ "$((size % 12))" = 0 ] || fail
    if [ -e "$data/private/etc/kcpassword" ]; then cmp -s "$stage/kcpassword" "$data/private/etc/kcpassword" || fail; fi
    install -o 0 -g 0 -m 600 "$stage/kcpassword" "$data/private/etc/kcpassword"
    plutil -replace autoLoginUser -string "$account" "$login"
fi
chown 0:0 "$global" "$login"
chmod 644 "$global" "$login"
sshdir="$data/private/etc/ssh/sshd_config.d"
ensure_dir "$sshdir"
sshpolicy="$sshdir/000-corral-firstboot.conf"
safe "$sshpolicy"
cat > "$stage/ssh-policy" <<EOF
# Corral's first-boot policy: authenticated keys for this account only.
AuthenticationMethods publickey
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
AllowUsers $account
EOF
if [ -e "$sshpolicy" ]; then root_file "$sshpolicy"; cmp -s "$sshpolicy" "$stage/ssh-policy" || fail; fi
install -o 0 -g 0 -m 600 "$stage/ssh-policy" "$sshpolicy"
sshconfig="$data/private/etc/ssh/sshd_config"
root_file "$sshconfig"
if ! /usr/bin/grep -Fxq 'Include /etc/ssh/sshd_config.d/000-corral-firstboot.conf' "$sshconfig"; then
    { printf 'Include /etc/ssh/sshd_config.d/000-corral-firstboot.conf\n'; cat "$sshconfig"; } > "$stage/sshd_config"
    install -o 0 -g 0 -m 644 "$stage/sshd_config" "$sshconfig"
fi
for dest in "$state/bootstrap-firstboot.sh" "$data/Library/LaunchDaemons/com.corral.bootstrap-firstboot.plist"; do
    safe "$dest"; [ ! -e "$dest" ] || root_file "$dest"
done
install -o 0 -g 0 -m 700 "$stage/bootstrap-firstboot.sh" "$state/bootstrap-firstboot.sh"
install -o 0 -g 0 -m 644 "$stage/bootstrap-firstboot.plist" "$data/Library/LaunchDaemons/com.corral.bootstrap-firstboot.plist"
# Verify prerequisites before suppressing Setup Assistant. The daemon still
# verifies directory-service resolution and enables native SSH on the real boot.
[ "$(get "$record" generateduid:0)" = "$account_uuid" ] || fail
[ "$(plutil -extract ShadowHashData xml1 -o - "$record" | digest)" = "$credential_sha" ] || fail
cmp -s "$home/.ssh/authorized_keys" "$stage/authorized_keys" || fail
plutil -lint "$admin" "$login" "$global" "$receipt" >/dev/null
if [ ! -e "$setup_done" ]; then install -o 0 -g 0 -m 644 /dev/null "$setup_done"; else root_file "$setup_done"; fi
sync
emit_verified
