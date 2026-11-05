package macos

import (
	"bytes"
	"context"
	"io"
	"strings"
)

// macOS stores an obfuscated credential for automatic login, not an encrypted
// secret. Format reference: https://github.com/dockur/macos/blob/master/src/install/admin/create.py
// The password is UTF-8 bytes followed by NUL, padded to a multiple of 12 bytes.
func automaticLoginPassword(password string) []byte {
	key := [...]byte{0x7d, 0x89, 0x52, 0x23, 0xd2, 0xbc, 0xdd, 0xea, 0xa3, 0xb9, 0x1f}
	body := make([]byte, (len(password)+12)/12*12)
	copy(body, password)
	for i := range body {
		body[i] ^= key[i%len(key)]
	}
	return body
}

func applyAutomaticLogin(ctx context.Context, transport []string, user, password string, output io.Writer) error {
	body := automaticLoginPassword(password)
	defer clear(body)
	script := `set -euo pipefail
[ "$(/usr/bin/uname -s)" = Darwin ] && [ "$(/usr/bin/uname -m)" = x86_64 ] && [ "$EUID" = 0 ] || exit 70
` + virtualGuestGuard + `
virtual_guest || exit 70
[ "$(/usr/bin/fdesetup status)" = 'FileVault is Off.' ] || {
    echo 'Refusing automatic login: FileVault must already be off; encryption was not changed.' >&2
    exit 1
}
/usr/bin/id "$1" >/dev/null
current="$(/usr/bin/defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null || true)"
[ -z "$current" ] || [ "$current" = "$1" ] || {
    echo 'Refusing to replace another account automatic-login policy.' >&2
    exit 1
}
if [ -e /etc/kcpassword ] || [ -L /etc/kcpassword ]; then
    [ -f /etc/kcpassword ] && [ ! -L /etc/kcpassword ] && [ "$(/usr/bin/stat -f '%u' /etc/kcpassword)" = 0 ] || exit 70
fi
umask 077
tmp="$(/usr/bin/mktemp /etc/.corral-kcpassword.XXXXXXXX)"
trap '/bin/rm -f "$tmp"' EXIT
/bin/cat > "$tmp"
size="$(/usr/bin/stat -f '%z' "$tmp")"
[ "$size" -ge 12 ] && [ "$((size % 12))" = 0 ] || exit 70
/usr/sbin/chown root:wheel "$tmp"
/bin/chmod 600 "$tmp"
/bin/mv -f "$tmp" /etc/kcpassword
/usr/bin/defaults write /Library/Preferences/com.apple.loginwindow autoLoginUser -string "$1"
[ "$(/usr/bin/defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser)" = "$1" ] || exit 70
/usr/bin/pmset -a displaysleep 0
/usr/bin/sudo -H -u "$1" -- /usr/bin/defaults -currentHost write com.apple.screensaver idleTime -int 0
/usr/sbin/sysadminctl -autologin status 2>&1
echo 'Automatic GUI login configured; idle display sleep and screen saver disabled. Manual locking remains available; verify unattended GUI login after a guest reboot.'
`
	input := io.MultiReader(strings.NewReader(password+"\n"), bytes.NewReader(body))
	_, err := guestRemote(ctx, transport, guestScriptCommand(script, "corral-macos-autologin", true, user), input, output)
	return err
}
