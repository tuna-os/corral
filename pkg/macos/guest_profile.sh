#!/bin/bash
# Run inside the dedicated macOS guest, never on the Linux hypervisor.
# Keep Apple push, identity, keychain, trust, Contacts and Messages services intact.
set -euo pipefail

if [ "$(/usr/bin/uname -s)" != Darwin ]; then
    echo 'Refusing: this profile only runs inside the dedicated macOS guest.' >&2
    exit 1
fi
[ "$(/usr/bin/uname -m)" = x86_64 ] || {
    echo 'Refusing: this profile requires the Intel x86_64 macOS guest.' >&2
    exit 1
}

# VMHide filters sysctl; this exact live driver class is independent evidence.
# IORegistry example: https://github.com/utmapp/UTM/discussions/4791#discussioncomment-4747118
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
if ! virtual_guest; then
    echo 'Refusing: this profile requires a visible hypervisor or an active Apple VirtIO PCI transport.' >&2
    exit 1
fi

case "${1:---check}" in
    --apply)
        if [ "$EUID" -ne 0 ]; then
            echo 'Apply requires root inside the macOS guest.' >&2
            exit 1
        fi
        # The GUI may sleep its display; the VM must remain available to Messages.
        /usr/bin/pmset -a sleep 0 displaysleep 5
        # A dedicated bridge does not need a searchable local document index.
        /usr/bin/mdutil -i off /
        ;;
    --check) ;;
    *) echo 'Usage: guest_profile.sh [--check|--apply]' >&2; exit 2 ;;
esac

/usr/bin/sw_vers
/usr/bin/pmset -g custom
/usr/bin/mdutil -s /
/usr/bin/fdesetup status
/usr/sbin/sysadminctl -autologin status 2>&1
echo 'Apple messaging, identity, keychain, trust and security-update services were not disabled.'
echo 'Decline optional Siri and analytics during Setup Assistant; do not disable their dependencies with launchctl.'
