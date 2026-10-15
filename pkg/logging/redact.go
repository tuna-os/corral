package logging

import (
	"regexp"
	"strings"
)

// Redacted replaces every secret value that a log record would carry.
const Redacted = "[REDACTED]"

// secretWord matches the names of flags, environment variables, and fields
// whose value is a credential.
const secretWord = `(?:token|password|passwd|secret|authkey|auth[-_]key|api[-_]?key|credential|sshpass)`

var (
	// name=value or name: value, where the name contains a secret word.
	// Covers TS_AUTHKEY=..., --from-literal=password=..., "token":"...",
	// and cloud-init `password: ...`. The value runs to whitespace, a quote,
	// a comma, or a closing brace.
	secretAssign = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*` + secretWord + `[A-Za-z0-9_.-]*"?[ \t]*[=:][ \t]*"?)([^\s"',}&]+)`)
	// Tailscale auth and API keys, which also appear bare.
	tailscaleKey = regexp.MustCompile(`tskey-[A-Za-z0-9-]+`)
	// Proxmox API tokens: PVEAPIToken=user@realm!id=uuid, or the bare
	// user@realm!id=uuid form that the config and the CLI accept.
	proxmoxToken = regexp.MustCompile(`(?i)(PVEAPIToken[= ])\S+|([A-Za-z0-9._-]+@[A-Za-z0-9._-]+![A-Za-z0-9._-]+)=[0-9A-Fa-f-]{8,}`)
	// HTTP authorization schemes.
	bearer = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	// Passwords in URL userinfo: scheme://user:password@host.
	urlPassword = regexp.MustCompile(`(://[^/\s:@]+:)[^/\s@]+@`)
	// A flag whose next argument is its value: --token VALUE.
	secretFlag = regexp.MustCompile(`(?i)^--?[A-Za-z0-9-]*` + secretWord + `[A-Za-z0-9-]*$`)
)

// Redact replaces the credentials in s with [REDACTED]. It errs toward
// redacting too much: a log line that hides a harmless value costs a little
// debugging, and one that leaks a token costs a revocation.
func Redact(s string) string {
	s = proxmoxToken.ReplaceAllStringFunc(s, func(m string) string {
		sub := proxmoxToken.FindStringSubmatch(m)
		if sub[1] != "" {
			return sub[1] + Redacted
		}
		return sub[2] + "=" + Redacted
	})
	s = tailscaleKey.ReplaceAllString(s, Redacted)
	s = bearer.ReplaceAllString(s, "$1 "+Redacted)
	s = urlPassword.ReplaceAllString(s, "${1}"+Redacted+"@")
	s = secretAssign.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretAssign.FindStringSubmatch(m)
		if sub[2] == Redacted || strings.HasPrefix(sub[2], "[REDACTED") {
			return m
		}
		return sub[1] + Redacted
	})
	return s
}

// RedactArgs returns a copy of argv with every credential replaced: values
// of secret flags (--token VALUE as well as --token=VALUE) and secrets
// embedded in any argument.
func RedactArgs(argv []string) []string {
	out := make([]string, len(argv))
	for i, arg := range argv {
		if i > 0 && secretFlag.MatchString(argv[i-1]) && !strings.HasPrefix(arg, "-") {
			out[i] = Redacted
			continue
		}
		out[i] = Redact(arg)
	}
	return out
}

// Command renders name and args as one redacted, space-separated string for
// a log attribute.
func Command(name string, args []string) string {
	return strings.Join(RedactArgs(append([]string{name}, args...)), " ")
}
