#!/bin/bash
# Opt-in, guest-local installation; never run this through the fleet runner.
set -euo pipefail
# A caller's shell tracing must not disclose authentication output.
set +x
umask 077

fail() { printf '%s\n' "$*" >&2; exit 1; }

mode="${1:---check}"
if [ "$#" -gt 0 ]; then shift; fi
read_auth=0
if [ "${1:-}" = --auth-config-stdin ]; then read_auth=1; shift; fi
[ "$#" = 0 ] || fail 'Usage: beeper_bridge.sh [--check|--apply [--auth-config-stdin]]'
case "$mode" in
    --apply) ;;
    --check) [ "$read_auth" = 0 ] || fail 'Check never consumes authentication input.' ;;
    *) fail 'Usage: beeper_bridge.sh [--check|--apply [--auth-config-stdin]]' ;;
esac
[ "$(/usr/bin/uname -s)" = Darwin ] || fail 'Refusing: run only inside the dedicated macOS guest.'
[ "$(/usr/bin/uname -m)" = x86_64 ] || fail 'Refusing: this installer requires the Intel x86_64 macOS guest.'
# VMHide filters sysctl; require a real virtual-driver object, not a plist header.
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
virtual_guest || fail 'Refusing: macOS must expose a hypervisor or an active Apple VirtIO PCI transport.'
[ "$EUID" -ne 0 ] || fail 'Run as the dedicated GUI user, not root or sudo.'
case "${HOME:-}" in
    /*) [ "$HOME" != / ] || fail 'A normal user HOME is required.' ;;
    *) fail 'An absolute user HOME is required.' ;;
esac
[ "$(/usr/bin/stat -f '%u' "$HOME")" = "$EUID" ] || fail 'HOME must belong to the current user.'

state="$HOME/Library/Application Support/corral-macos"
binary="$state/bin/bbctl"
config="$state/bbctl/config.json"
data="$state/bbctl/data"
logs="$state/logs"
label=io.github.tuna-os.corral.imessage
agents="$HOME/Library/LaunchAgents"
plist="$agents/$label.plist"
domain="gui/$EUID"
service="$domain/$label"
sha256=887b8a344c2782c150bb84aecd6c144f5b8d028bf9960622cf497730fb5dbf2c
url=https://github.com/beeper/bridge-manager/releases/download/v0.15.0/bbctl-macos-amd64
cache="$HOME/.cache/corral/macos/sequoia/tools/bbctl-macos-amd64"

valid_binary() {
    local digest
    [ -f "$1" ] && [ ! -L "$1" ] || return 1
    digest="$(/usr/bin/shasum -a 256 "$1" 2>/dev/null)" || return 1
    [ "${digest%% *}" = "$sha256" ]
}

# Do not dump launchctl's environment, arguments, or logs: report only safe fields.
service_status() {
    local info line value current=unknown pid=none last_exit=unknown
    if ! info="$(/bin/launchctl print "$service" 2>/dev/null)"; then
        printf 'LaunchAgent %s: not loaded in %s.\n' "$label" "$domain"
        return 1
    fi
    while IFS= read -r line; do
        # Strip launchctl's indentation without interpreting any values as code.
        line="${line#"${line%%[![:space:]]*}"}"
        case "$line" in
            'state = '*)
                value="${line#state = }"
                case "$value" in running|waiting|'not running'|spawned|exited) current="$value" ;; esac
                ;;
            'pid = '*)
                value="${line#pid = }"
                case "$value" in ''|*[!0-9]*) ;; *) pid="$value" ;; esac
                ;;
            'last exit code = '*)
                value="${line#last exit code = }"
                case "$value" in ''|*[!0-9-]*) ;; *) last_exit="$value" ;; esac
                ;;
        esac
    done <<< "$info"
    printf 'LaunchAgent %s: state=%s pid=%s last-exit=%s.\n' "$label" "$current" "$pid" "$last_exit"
    [ "$current" = running ]
}

# Refuse links before any chmod, write, or bbctl invocation. Only dedicated paths
# are made private; existing Library and LaunchAgents permissions are untouched.
for path in "$HOME/Library" "$HOME/Library/Application Support" "$state" \
    "$state/bin" "$state/bbctl" "$data" "$logs" "$agents"; do
    [ ! -L "$path" ] || fail 'Refusing a symlink in the guest state or LaunchAgents path.'
    if [ -e "$path" ]; then
        [ -d "$path" ] || fail 'A guest state or LaunchAgents directory is not a directory.'
        [ "$(/usr/bin/stat -f '%u' "$path")" = "$EUID" ] || fail 'Guest state and LaunchAgents paths must belong to this user.'
    fi
done
for path in "$binary" "$config" "$plist" "$logs/stdout.log" "$logs/stderr.log"; do
    [ ! -L "$path" ] || fail 'Refusing a symlink at a managed file path.'
    if [ -e "$path" ]; then
        [ -f "$path" ] || fail 'A managed file path is not a regular file.'
        [ "$(/usr/bin/stat -f '%u' "$path")" = "$EUID" ] || fail 'Managed files must belong to this user.'
        [ "$(/usr/bin/stat -f '%l' "$path")" = 1 ] || fail 'Refusing a hard-linked managed file.'
    fi
done

if [ "$mode" = --check ]; then
    result=0
    if valid_binary "$binary"; then
        echo 'bbctl v0.15.0: pinned SHA256 matches.'
    else
        echo 'bbctl: missing or pinned SHA256 does not match.'
        result=1
    fi
    if [ -f "$config" ] && [ "$(/usr/bin/stat -f '%Lp' "$config")" = 600 ] \
        && [ -d "$state/bbctl" ] && [ "$(/usr/bin/stat -f '%Lp' "$state/bbctl")" = 700 ]; then
        echo 'Dedicated auth config: present with private modes (not an authentication test).'
    else
        echo 'Dedicated auth config: missing or not private.'
        result=1
    fi
    if [ -f "$plist" ]; then
        echo 'LaunchAgent plist: installed.'
    else
        echo 'LaunchAgent plist: absent.'
        result=1
    fi
    service_status || result=1
    echo 'Service state is not proof of iMessage connectivity or message delivery.'
    exit "$result"
fi

[ "$(/usr/bin/stat -f '%u' /dev/console)" = "$EUID" ] || fail 'Apply requires this user to be logged into the macOS GUI console.'
/bin/launchctl print "$domain" >/dev/null 2>&1 || fail 'The current user has no normal GUI launchd domain.'
[ -s "$config" ] || [ "$read_auth" = 1 ] || fail 'Missing dedicated auth config: securely copy native bbctl config.json or use --auth-config-stdin; no unattended login is attempted.'

/bin/mkdir -p "$state/bin" "$state/bbctl" "$data" "$logs" "$agents"
/bin/chmod 700 "$state" "$state/bin" "$state/bbctl" "$data" "$logs"
if [ -f "$config" ]; then /bin/chmod 600 "$config"; fi
lock="$state/.beeper-install.lock"
/bin/mkdir "$lock" 2>/dev/null || fail 'Another installation owns the dedicated install lock; if interrupted previously, remove that lock only after confirming no installer is running.'
staging=''
plist_staging=''
cleanup() {
    [ -z "$plist_staging" ] || /bin/rm -f "$plist_staging"
    [ -z "$staging" ] || /bin/rm -rf "$staging"
    /bin/rmdir "$lock" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
staging="$(/usr/bin/mktemp -d "$state/.beeper-install.XXXXXX")"

# SSH supplies the native config on stdin, never in a command argument.
# Existing guest authentication remains authoritative, even if it is invalid.
auth_source="$config"
if [ "$read_auth" = 1 ]; then
    if [ -e "$config" ]; then
        /bin/cat >/dev/null
    else
        auth_source="$staging/incoming-auth.json"
        /bin/cat >"$auth_source"
        /bin/chmod 600 "$auth_source"
    fi
fi

if valid_binary "$binary"; then
    /bin/chmod 700 "$binary"
else
    if valid_binary "$cache"; then
        /bin/cp "$cache" "$staging/bbctl"
    else
        /usr/bin/curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
            --output "$staging/bbctl" "$url" || fail 'Unable to download the pinned official bbctl release.'
    fi
    valid_binary "$staging/bbctl" || fail 'bbctl checksum mismatch; the existing installation was not replaced.'
    /bin/chmod 700 "$staging/bbctl"
    /bin/mv -f "$staging/bbctl" "$binary"
fi

# Native plutil handles JSON and escaping, including spaces and XML characters in
# HOME. Never print config values or use the user's unrelated default bbctl config.
/usr/bin/plutil -extract environments.prod xml1 -o /dev/null "$auth_source" >/dev/null 2>&1 \
    || fail 'Dedicated auth config is invalid: expected native bbctl JSON with a prod environment.'
/usr/bin/plutil -convert json -o "$staging/config.json" "$auth_source" >/dev/null 2>&1 \
    || fail 'Dedicated auth config is not valid JSON/plist data.'
# A copied desktop-login pointer could read unrelated credentials on this machine.
if /usr/bin/plutil -extract environments.prod.desktop_data_dir raw -o "$staging/desktop-path" \
    "$staging/config.json" >/dev/null 2>&1 && [ -s "$staging/desktop-path" ]; then
    fail 'Dedicated config must use native token authentication, not a Beeper Desktop data-directory pointer.'
fi
/usr/bin/plutil -replace environments.prod.bridge_data_dir -string "$data" "$staging/config.json" >/dev/null 2>&1 \
    || /usr/bin/plutil -insert environments.prod.bridge_data_dir -string "$data" "$staging/config.json" >/dev/null 2>&1 \
    || fail 'Unable to set the dedicated prod bridge data directory.'
/usr/bin/plutil -convert json "$staging/config.json" >/dev/null 2>&1 \
    || fail 'Unable to preserve the dedicated config as JSON.'
/bin/chmod 600 "$staging/config.json"

# bbctl v0.15.0 can return zero even after an API/authentication error. Validate
# raw whoami JSON rather than trusting its status. Its raw response can itself
# contain credentials: keep every byte private and remove it with our staging.
/bin/cp "$staging/config.json" "$staging/auth-check.json"
if ! BEEPER_ENV=prod BBCTL_DATA_HOME= "$binary" --config "$staging/auth-check.json" whoami --raw \
    >"$staging/whoami.json" 2>"$staging/whoami.stderr"; then
    fail 'Native bbctl whoami failed; securely refresh the dedicated config and confirm guest network access. No private output was printed.'
fi
if ! /usr/bin/plutil -extract userInfo.username raw -expect string -o "$staging/identity" \
    "$staging/whoami.json" >/dev/null 2>&1 || [ ! -s "$staging/identity" ]; then
    fail 'Native bbctl whoami did not authenticate; securely refresh the dedicated config and confirm guest network access. No private output was printed.'
fi

plist_staging="$(/usr/bin/mktemp "$agents/.$label.XXXXXX")"
/usr/bin/plutil -create xml1 "$plist_staging"
/usr/bin/plutil -insert Label -string "$label" "$plist_staging"
/usr/bin/plutil -insert ProgramArguments -json '[]' "$plist_staging"
args=("$binary" --config "$config" run --param imessage_platform=mac sh-imessage)
for ((index=0; index<${#args[@]}; index++)); do
    /usr/bin/plutil -insert "ProgramArguments.$index" -string "${args[$index]}" "$plist_staging"
done
/usr/bin/plutil -insert RunAtLoad -bool YES "$plist_staging"
/usr/bin/plutil -insert KeepAlive -bool YES "$plist_staging"
/usr/bin/plutil -insert ThrottleInterval -integer 30 "$plist_staging"
/usr/bin/plutil -insert LimitLoadToSessionType -string Aqua "$plist_staging"
/usr/bin/plutil -insert WorkingDirectory -string "$state" "$plist_staging"
/usr/bin/plutil -insert StandardOutPath -string "$logs/stdout.log" "$plist_staging"
/usr/bin/plutil -insert StandardErrorPath -string "$logs/stderr.log" "$plist_staging"
/usr/bin/plutil -insert Umask -integer 63 "$plist_staging"
/usr/bin/plutil -insert EnvironmentVariables -json '{}' "$plist_staging"
/usr/bin/plutil -insert EnvironmentVariables.HOME -string "$HOME" "$plist_staging"
/usr/bin/plutil -insert EnvironmentVariables.PATH -string /usr/bin:/bin:/usr/sbin:/sbin "$plist_staging"
/usr/bin/plutil -insert EnvironmentVariables.BEEPER_ENV -string prod "$plist_staging"
# Empty means native default, not a global redirection. The explicit prod data
# path above controls this bridge; no default bbctl config is read or rewritten.
/usr/bin/plutil -insert EnvironmentVariables.BBCTL_DATA_HOME -string '' "$plist_staging"
# The pinned mautrix-imessage binary links libolm.3.dylib by absolute Homebrew
# path (/usr/local/opt/libolm), which no clean guest provides and Homebrew no
# longer ships. bbctl always downloads its own libolm.3.dylib into the prod
# data binaries directory before starting the bridge, so point the loader
# there: no sudo, no system paths, survives bbctl-managed updates.
/usr/bin/plutil -insert EnvironmentVariables.DYLD_LIBRARY_PATH -string "$data/binaries" "$plist_staging"
/bin/chmod 600 "$plist_staging"
/usr/bin/touch "$logs/stdout.log" "$logs/stderr.log"
/bin/chmod 600 "$logs/stdout.log" "$logs/stderr.log"

# Touch only our exact service target, never other user agents or Apple services.
if /bin/launchctl print "$service" >/dev/null 2>&1; then
    /bin/launchctl bootout "$service" || fail 'Unable to stop the dedicated existing LaunchAgent.'
fi
/bin/mv -f "$staging/auth-check.json" "$config"
/bin/mv -f "$plist_staging" "$plist"
plist_staging=''
/bin/launchctl enable "$service" || fail 'Unable to enable the dedicated LaunchAgent.'
# RunAtLoad starts it on bootstrap; do not kill that first registration/download.
/bin/launchctl bootstrap "$domain" "$plist" || fail 'Unable to bootstrap the dedicated LaunchAgent in the GUI domain.'
echo 'Installed official bbctl v0.15.0 and started the SIP-preserving mac iMessage connector.'
service_status || true
echo 'REQUIRED: in the guest GUI, open System Settings > Privacy & Security > Full Disk Access and grant access to the dedicated bbctl launcher and its downloaded native connector:'
printf '  %s\n' "$binary" "$data/binaries/mautrix-imessage"
echo 'Approve the native connector prompts for Automation (Messages) and Contacts. If first startup was denied, restart only this LaunchAgent after granting access.'
echo 'Sign into Messages in the guest yourself. Verify both inbound and outbound message delivery in Beeper and Messages; a running LaunchAgent is not a connectivity test.'
echo 'This bridge installer did not change SIP, ADP, FileVault, automatic login, Apple credentials, or unrelated services. Private logs remain under the dedicated state/logs directory; do not publish them.'
