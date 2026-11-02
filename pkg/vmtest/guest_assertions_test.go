package vmtest

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestChecks(t *testing.T) {
	dir := t.TempDir()
	osRelease := filepath.Join(dir, "os-release")
	if err := os.WriteFile(osRelease, []byte("ID=testos\nVERSION_ID=42\nNAME=Test\nPRETTY_NAME='Test OS'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"systemctl":  "#!/bin/sh\ncase $1 in is-active) echo active;; list-units) echo systemd-journald.service;; get-default) echo graphical.target;; esac\n",
		"journalctl": "#!/bin/sh\necho journal\n",
		"session":    "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The inventory scenario needs grep, but all other tools are controlled.
	grep, err := exec.LookPath("grep")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(grep, filepath.Join(dir, "grep")); err != nil {
		t.Fatal(err)
	}
	run := func(check string) (string, error) {
		t.Helper()
		check = strings.ReplaceAll(check, "/etc/os-release", osRelease)
		cmd := exec.Command("/bin/sh", "-c", check)
		cmd.Env = []string{"PATH=" + dir}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	g := &GuestAssertions{ID: "testos", VersionID: "42", Desktop: &DesktopAssertions{SessionCommand: "session", DisplayManagerUnit: "dm.service"}}
	checks := guestChecks(g)
	if len(checks) != 7 || len(guestChecks(&GuestAssertions{ID: "testos"})) != 4 || guestChecks(nil) != nil {
		t.Fatal("unexpected scenario selection")
	}
	for _, check := range checks {
		if out, err := run(check); err != nil {
			t.Errorf("%s: %v: %s", check, err, out)
		}
	}
	g.ID = "testos'; touch " + filepath.Join(dir, "injected") + "; echo '"
	if out, err := run(guestChecks(g)[3]); err == nil || !strings.Contains(out, "unexpected os-release ID") {
		t.Errorf("wrong identity: %v, %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
		t.Error("identity was interpreted as shell syntax")
	}
	g.ID, g.VersionID = "testos", "wrong"
	if out, err := run(guestChecks(g)[3]); err == nil || !strings.Contains(out, "unexpected os-release VERSION_ID") {
		t.Errorf("wrong version: %v, %s", err, out)
	}
	// A failed systemctl must not be hidden by an empty inventory pipeline.
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\necho systemctl-failed >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 2, 4, 6} {
		if out, err := run(checks[index]); err == nil || !strings.Contains(out, "systemctl-failed") {
			t.Errorf("scenario %d ignored systemctl failure: %v, %s", index, err, out)
		}
	}
	if err := os.Remove(filepath.Join(dir, "journalctl")); err != nil {
		t.Fatal(err)
	}
	if out, err := run(checks[1]); err == nil || !strings.Contains(out, "required tool missing: journalctl") {
		t.Errorf("missing tool: %v, %s", err, out)
	}
	if err := os.Remove(filepath.Join(dir, "session")); err != nil {
		t.Fatal(err)
	}
	if out, err := run(checks[5]); err == nil || !strings.Contains(out, "required desktop command missing") {
		t.Errorf("missing desktop: %v, %s", err, out)
	}
}

func TestGuestAssertionsSpecValidation(t *testing.T) {
	for _, input := range []string{
		"bootc: img\nguestAssertions: {}\n",
		"bootc: img\nguestAssertions: {id: test, desktop: {}}\n",
		"bootc: img\nguestAssertions: {id: test, desktop: {sessionCommand: '-x', displayManagerUnit: dm.service}}\n",
		"bootc: img\nguestAssertions: {id: test, desktop: {sessionCommand: shell, displayManagerUnit: '-x'}}\n",
	} {
		spec, err := Parse([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		spec.WithDefaults()
		if err := spec.Validate(); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	spec, err := Parse([]byte("bootc: img\nguestAssertions: {id: fedora, versionId: '42', desktop: {sessionCommand: gnome-shell, displayManagerUnit: gdm.service}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	spec.WithDefaults()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}

type suiteProbes struct{ commands []string }

func (*suiteProbes) Hook(*Spec, Keypair, bool, func(string)) *HookResult { return nil }
func (*suiteProbes) Diagnostics(*Spec, Keypair, string)                  {}
func (p *suiteProbes) Checks(spec *Spec, _ Keypair, _ func(string)) []CheckResult {
	p.commands = append([]string(nil), spec.Checks...)
	results := make([]CheckResult, len(spec.Checks))
	for i, command := range spec.Checks {
		results[i] = CheckResult{Command: command, Passed: i != len(spec.Checks)-1}
	}
	results[len(results)-1].Output = "deliberately broken identity"
	return results
}

func TestGuestSuiteRequiresSSH(t *testing.T) {
	host := &fakeHost{readyBy: readyByMarker, sshErr: errors.New("refused")}
	spec := &Spec{Bootc: "img", Artifacts: t.TempDir(), GuestAssertions: &GuestAssertions{ID: "test"}}
	result, err := host.runner().Run(spec, nil)
	if err == nil || result.ExitCode != ExitCheck || !strings.Contains(err.Error(), "SSH never answered") {
		t.Fatalf("unreachable suite: %+v, %v", result, err)
	}
}

func TestGuestSuiteRunFailureAndReuse(t *testing.T) {
	host := &fakeHost{readyBy: readyBySSH}
	probes := &suiteProbes{}
	runner := host.runner()
	runner.Probes = probes
	spec := &Spec{Bootc: "img", Artifacts: t.TempDir(), Checks: []string{"true"}, GuestAssertions: &GuestAssertions{ID: "test"}}
	for i := 0; i < 2; i++ {
		result, err := runner.Run(spec, nil)
		if err == nil || result.ExitCode != ExitCheck || len(probes.commands) != 5 || len(spec.Checks) != 1 {
			t.Fatalf("run %d: result=%+v err=%v commands=%d", i, result, err, len(probes.commands))
		}
		data, err := os.ReadFile(filepath.Join(spec.Artifacts, ResultFile))
		if err != nil {
			t.Fatal(err)
		}
		var stored Result
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.ExitCode != ExitCheck || !strings.Contains(stored.Checks[4].Output, "deliberately broken") {
			t.Fatalf("missing evidence: %s", data)
		}
	}
}
