package bootc

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The relabel runs inside `chroot "$R" /bin/sh -c '...'`, so the wrapper must
// be defined inside that single-quoted script, before the setfiles calls, and
// must not itself contain a single quote that would end the quoting early.
func TestInstalledRootRunnerEmbedsSetfilesTolerant(t *testing.T) {
	if strings.Contains(setfilesTolerant, "'") {
		t.Fatal("setfilesTolerant contains a single quote; it would break the runner's chroot quoting")
	}
	open := strings.Index(InstalledRootRunner, `chroot "$R" /bin/sh -c '`)
	def := strings.Index(InstalledRootRunner, "corral_setfiles() {")
	call := strings.Index(InstalledRootRunner, "corral_setfiles -c ")
	if open < 0 || def < open || call < def {
		t.Fatalf("corral_setfiles must be defined inside the chroot relabel script before it is called (open %d, def %d, call %d)", open, def, call)
	}
	for _, line := range strings.Split(InstalledRootRunner, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "setfiles ") {
			t.Errorf("bare setfiles call bypasses corral_setfiles: %q", line)
		}
	}
}

// corral_setfiles accepts a failing setfiles only when every stderr line is
// the kernel's EINVAL for a context it cannot map; everything else fails.
func TestSetfilesTolerantClassification(t *testing.T) {
	const (
		einval1 = "setfiles: Could not set context for /etc/kmscon:  Invalid argument\n"
		einval2 = "setfiles: Could not set context for /etc/kmscon/kmscon.conf.example:  Invalid argument\n"
	)
	cases := []struct {
		name     string
		stderr   string
		exit     int
		wantExit int
	}{
		{"clean run", "", 0, 0},
		{"success with output passes through", "setfiles: warning\n", 0, 0},
		// The wahoo (Fedora ELN) failure on the Fedora 42 builder.
		{"unmapped contexts only", einval1 + einval2, 255, 0},
		{"path containing a colon", "setfiles: Could not set context for /etc/a: b:  Invalid argument\n", 255, 0},
		{"unmapped context plus another error", einval1 + "setfiles: Could not set context for /var/x:  Operation not permitted\n", 255, 255},
		{"another errno", "setfiles: Could not set context for /etc/shadow:  Read-only file system\n", 255, 255},
		{"invalid context from validation", "setfiles: /etc/selinux/targeted/contexts/files/file_contexts:  line 12 has invalid context system_u:object_r:pasta_pid_t:s0\n", 255, 255},
		{"trailing text after the errno", "setfiles: Could not set context for /etc/kmscon:  Invalid argument (extra)\n", 255, 255},
		{"blank line beside unmapped context", einval1 + "\n", 255, 255},
		{"failure with no diagnosis", "", 1, 1},
	}
	shells := []string{}
	for _, shell := range []string{"sh", "bash", "dash", "busybox"} {
		if _, err := exec.LookPath(shell); err == nil {
			shells = append(shells, shell)
		}
	}
	if len(shells) == 0 {
		t.Skip("no POSIX shell")
	}
	script := setfilesTolerant + `
fake_setfiles() {
  printf "%s" "$FAKE_STDERR" >&2
  return "$FAKE_EXIT"
}
corral_setfiles fake_setfiles -c policy file_contexts /etc /var
`
	for _, shell := range shells {
		for _, tc := range cases {
			t.Run(shell+"/"+tc.name, func(t *testing.T) {
				tmp := t.TempDir()
				args := []string{"-c", script}
				if shell == "busybox" {
					args = append([]string{"sh"}, args...)
				}
				cmd := exec.Command(shell, args...)
				cmd.Env = append(os.Environ(),
					"TMPDIR="+tmp,
					"FAKE_STDERR="+tc.stderr,
					"FAKE_EXIT="+strconv.Itoa(tc.exit),
				)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				err := cmd.Run()
				got := 0
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					got = exitErr.ExitCode()
				} else if err != nil {
					t.Fatal(err)
				}
				if got != tc.wantExit {
					t.Errorf("exit = %d, want %d; stderr:\n%s", got, tc.wantExit, stderr.String())
				}
				if !strings.HasPrefix(stderr.String(), tc.stderr) {
					t.Errorf("setfiles stderr not passed through; got:\n%s", stderr.String())
				}
				tolerated := strings.Contains(stderr.String(), "corral: setfiles could not set the contexts above")
				if tolerated != (tc.exit != 0 && tc.wantExit == 0) {
					t.Errorf("tolerance note printed = %v; stderr:\n%s", tolerated, stderr.String())
				}
				if left, _ := os.ReadDir(tmp); len(left) != 0 {
					t.Errorf("corral_setfiles left %d file(s) in TMPDIR", len(left))
				}
			})
		}
	}
}
