package bootc

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

var loginUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[$]?$`)
var passwordHashPattern = regexp.MustCompile(`^\$6\$(rounds=[0-9]+\$)?[./0-9A-Za-z]{1,16}\$[./0-9A-Za-z]+$`)

// ValidateLoginUser rejects names that could not be safely used as a Linux
// login account and home-directory component.
func ValidateLoginUser(name string) error {
	if len(name) > 32 || !loginUserPattern.MatchString(name) {
		return fmt.Errorf("--user %q is not a valid login name (use lowercase letters, digits, _ or -, with an optional trailing $)", name)
	}
	return nil
}

// ValidatePasswordHash accepts the SHA-512 crypt form stored in /etc/shadow.
// Plaintext never crosses the build boundary: callers either provide this hash
// or hash a password read from a terminal with HashPassword.
func ValidatePasswordHash(hash string) error {
	if hash == "" {
		return nil
	}
	if !passwordHashPattern.MatchString(hash) {
		return fmt.Errorf("--password-hash must be a SHA-512 crypt hash in the $6$...$... format")
	}
	return nil
}

// HashPassword turns a terminal-only plaintext password into the SHA-512 crypt
// format accepted by bootc guest images. The plaintext is never included in a
// generated provisioning script or command line.
func HashPassword(plain string) (string, error) {
	salt, err := cryptSalt()
	if err != nil {
		return "", err
	}
	hash, err := sha512_crypt.New().Generate([]byte(plain), []byte(salt))
	if err != nil {
		return "", fmt.Errorf("hashing a password: %w", err)
	}
	return hash, nil
}

// AccountProvisionScript creates the script run inside the installed root
// filesystem. It is deliberately independent of cloud-init and tacklebox so
// the same account setup works for local and KubeVirt bootc builds.
func AccountProvisionScript(user, passwordHash string) (string, error) {
	if user == "" {
		if passwordHash != "" {
			return "", fmt.Errorf("--password-hash requires --user")
		}
		return "", nil
	}
	if err := ValidateLoginUser(user); err != nil {
		return "", err
	}
	if err := ValidatePasswordHash(passwordHash); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\n\n")
	fmt.Fprintf(&b, "name=%s\nhash=%s\nhome=\"/var/home/$name\"\n\n", shellQuote(user), shellQuote(passwordHash))
	b.WriteString(accountProvisionBody)
	return b.String(), nil
}

const accountProvisionBody = `mkdir -p /var/home
if ! id "$name" >/dev/null 2>&1; then
  if command -v useradd >/dev/null 2>&1; then
    useradd --create-home --home-dir "$home" --shell /bin/bash "$name"
  elif command -v adduser >/dev/null 2>&1; then
    adduser -D -h "$home" -s /bin/bash "$name"
  else
    echo "corral: this image has neither useradd nor adduser; cannot create $name" >&2
    exit 1
  fi
fi
mkdir -p "$home"
chmod 0700 "$home"
chown "$name:$(id -g "$name")" "$home"
if [ -n "$hash" ]; then
  if command -v usermod >/dev/null 2>&1; then
    usermod -p "$hash" "$name"
  elif command -v chpasswd >/dev/null 2>&1; then
    printf '%s:%s\n' "$name" "$hash" | chpasswd -e
  else
    echo "corral: this image has neither usermod nor chpasswd; cannot set the password for $name" >&2
    exit 1
  fi
fi
`

func cryptSalt() (string, error) {
	const alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("reading random bytes for a password salt: %w", err)
	}
	for i, value := range buf {
		buf[i] = alphabet[int(value)%len(alphabet)]
	}
	return "$6$" + string(buf) + "$", nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
