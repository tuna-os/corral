package vmtest

import "fmt"

// guestChecks translates the optional suite into ordinary SSH checks. Each
// scenario stands alone so result.json retains its output even if another fails.
func guestChecks(g *GuestAssertions) []string {
	if g == nil {
		return nil
	}
	checks := []string{
		`# Scenario: the system message bus is active
command -v systemctl >/dev/null || { echo 'required tool missing: systemctl' >&2; exit 1; }
systemctl is-active dbus.service`,
		`# Scenario: the journal is readable
command -v journalctl >/dev/null || { echo 'required tool missing: journalctl' >&2; exit 1; }
journalctl --no-pager -n 1`,
		`# Scenario: systemd has a coherent service inventory
command -v systemctl >/dev/null || { echo 'required tool missing: systemctl' >&2; exit 1; }
command -v grep >/dev/null || { echo 'required tool missing: grep' >&2; exit 1; }
units=$(systemctl list-units --type=service --no-legend --plain --no-pager) || exit $?
printf '%s\n' "$units" | grep -F systemd-journald.service`,
		fmt.Sprintf(`# Scenario: the image reports its expected identity
[ -r /etc/os-release ] || { echo 'required file missing: /etc/os-release' >&2; exit 1; }
. /etc/os-release
printf 'ID=%%s VERSION_ID=%%s NAME=%%s PRETTY_NAME=%%s\n' "${ID-}" "${VERSION_ID-}" "${NAME-}" "${PRETTY_NAME-}"
[ "${ID-}" = %s ] || { echo 'unexpected os-release ID' >&2; exit 1; }
[ -n "${NAME-}" ] && [ -n "${PRETTY_NAME-}" ] || { echo 'os-release NAME or PRETTY_NAME missing' >&2; exit 1; }%s`, shellQuote(g.ID), versionCheck(g.VersionID)),
	}
	if d := g.Desktop; d != nil {
		checks = append(checks,
			`# Scenario: a graphical target is the default
command -v systemctl >/dev/null || { echo 'required tool missing: systemctl' >&2; exit 1; }
target=$(systemctl get-default) || exit $?
printf 'default target: %s\n' "$target"
[ "$target" = graphical.target ] || { echo 'expected graphical.target' >&2; exit 1; }`,
			fmt.Sprintf(`# Scenario: the desktop session command is installed
command -v %s || { echo 'required desktop command missing' >&2; exit 1; }`, shellQuote(d.SessionCommand)),
			fmt.Sprintf(`# Scenario: the display manager is active
command -v systemctl >/dev/null || { echo 'required tool missing: systemctl' >&2; exit 1; }
systemctl is-active %s`, shellQuote(d.DisplayManagerUnit)),
		)
	}
	return checks
}

func versionCheck(version string) string {
	if version == "" {
		return ""
	}
	return fmt.Sprintf("\n[ \"${VERSION_ID-}\" = %s ] || { echo 'unexpected os-release VERSION_ID' >&2; exit 1; }", shellQuote(version))
}
