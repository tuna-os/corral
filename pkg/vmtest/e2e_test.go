//go:build e2evmtest

// Real verification of the whole harness: a real bootc image, a real derived
// layer, a real boot, real screenshots.
//
// Its own build tag, like pkg/bootc's e2e suite, for the same reasons: it pulls
// a multi-gigabyte image, runs a privileged `bootc install to-disk`, and boots
// a VM. Minutes, root, and KVM — not something to attach to every CI run, and
// emphatically not something to run by accident.
//
// Run with:
//
//	sudo -E go test -tags e2evmtest -timeout 45m ./pkg/vmtest/
//	CORRAL_VMTEST_SUDO=1 go test -tags e2evmtest -timeout 45m ./pkg/vmtest/
//
// Everything lands in t.TempDir(): the VM is named per test and deleted at the
// end, so a failed run leaves no VM behind and a passing one leaves no state.

package vmtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/testenv"
)

// e2eImage is small and official. Overridable so the suite can be pointed at
// whatever image is already cached on a given host.
func e2eImage() string {
	if v := os.Getenv("CORRAL_VMTEST_IMAGE"); v != "" {
		return v
	}
	return "quay.io/fedora/fedora-bootc:41"
}

func e2eSpec(t *testing.T, name string) *Spec {
	t.Helper()
	spec := &Spec{
		Name:      name,
		Bootc:     e2eImage(),
		Artifacts: filepath.Join(t.TempDir(), "out"),
		Sudo:      os.Getenv("CORRAL_VMTEST_SUDO") != "",
		Timeout:   Duration(20 * time.Minute),
	}
	spec.WithDefaults()
	if _, err := Preflight(spec); err != nil {
		testenv.Skip(t, "bootc-local-build", err.Error())
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		testenv.Skip(t, "kvm", "a real boot under software emulation takes longer than this suite allows")
	}
	return spec
}

// The whole point: an image reference in, a booted system with a test account
// and a passing hook out.
func TestE2E_BootAndCustomize(t *testing.T) {
	spec := e2eSpec(t, "corral-e2e-vmtest")
	spec.Users = []User{{Name: "tester", Password: "corral-e2e", Sudo: true}}
	// tree, not jq: the base image already ships jq, so an assertion on it
	// would pass even if the layer installed nothing at all.
	spec.Packages = []string{"tree"}
	// Not "systemctl is-system-running --wait": the hook is part of the boot,
	// so waiting for the boot to finish inside it deadlocks.
	spec.Provision = []Provision{{Script: "systemctl is-active sshd\ntest -f /etc/os-release"}}
	spec.Checks = []string{
		// The layer really installed something.
		"command -v tree",
		// The account really exists, with real sudo.
		"id tester",
		"sudo -u tester sudo -n true",
		// And the guest really is a bootc system that knows what it booted.
		"bootc status --format json",
	}
	spec.Screenshots.Interval = Duration(5 * time.Second)

	// Keep is false, so Run deletes the VM itself — a suite that leaves VMs
	// behind poisons the next run on the same host.
	result, err := Run(spec, os.Stderr)
	if err != nil {
		t.Fatalf("Run: %v (artifacts in %s)", err, spec.Artifacts)
	}

	if !result.Ready || result.ExitCode != ExitOK {
		t.Fatalf("result = %+v", result)
	}
	if result.Hook == nil || !result.Hook.Ran || result.Hook.ExitCode != 0 {
		t.Errorf("hook = %+v", result.Hook)
	}
	for _, check := range result.Checks {
		if !check.Passed {
			t.Errorf("check %q failed: %s", check.Command, check.Output)
		}
	}

	// The evidence has to be there, because it is the reason to use this over a
	// shell script.
	for _, name := range []string{"result.json", "serial.log", "ready.png"} {
		path := filepath.Join(spec.Artifacts, name)
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Errorf("expected a non-empty %s: %v", name, err)
		}
	}
	if len(result.Frames) == 0 {
		t.Error("no frames were captured during the boot")
	}
	// The serial console proves the karg took: without console=ttyS0 the log is
	// empty however well the guest booted.
	log, err := os.ReadFile(filepath.Join(spec.Artifacts, "serial.log"))
	if err != nil {
		t.Fatalf("reading the console log: %v", err)
	}
	if !strings.Contains(string(log), ReadyMarker) {
		t.Errorf("the console log does not carry the readiness marker:\n%s", tail(string(log), 40))
	}
}

// A failing hook must fail the run, and say which part failed. A gate that
// reports a broken first boot as success is worse than no gate.
func TestE2E_FailingHookFailsTheRun(t *testing.T) {
	spec := e2eSpec(t, "corral-e2e-vmtest-hook")
	spec.Provision = []Provision{{Script: "echo deliberate failure >&2\nexit 9"}}

	result, err := Run(spec, os.Stderr)
	if err == nil {
		t.Fatal("a hook that exits 9 must fail the run")
	}
	if result.ExitCode != ExitHook {
		t.Errorf("exit code = %d, want %d", result.ExitCode, ExitHook)
	}
	if result.Hook == nil || result.Hook.ExitCode != 9 {
		t.Errorf("hook = %+v, want the hook's own exit code", result.Hook)
	}
	if !strings.Contains(result.Hook.Log, "deliberate failure") {
		t.Errorf("the hook's output should be collected: %q", result.Hook.Log)
	}
}

// These opt-in cases exercise the same suite through Run, not a container.
// Images and their expected identities are supplied by the operator.
func TestE2E_GuestAssertions(t *testing.T) {
	for _, tc := range []struct {
		name, imageEnv, idEnv, versionEnv string
		desktop                           bool
	}{
		{"headless", "CORRAL_VMTEST_HEADLESS_IMAGE", "CORRAL_VMTEST_HEADLESS_ID", "CORRAL_VMTEST_HEADLESS_VERSION", false},
		{"desktop", "CORRAL_VMTEST_DESKTOP_IMAGE", "CORRAL_VMTEST_DESKTOP_ID", "CORRAL_VMTEST_DESKTOP_VERSION", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image, id := os.Getenv(tc.imageEnv), os.Getenv(tc.idEnv)
			if image == "" || id == "" {
				t.Skipf("set %s and %s to exercise this image", tc.imageEnv, tc.idEnv)
			}
			spec := e2eSpec(t, "corral-e2e-guest-"+tc.name)
			spec.Bootc = image
			spec.GuestAssertions = &GuestAssertions{ID: id, VersionID: os.Getenv(tc.versionEnv)}
			if tc.desktop {
				spec.GuestAssertions.Desktop = &DesktopAssertions{
					SessionCommand:     os.Getenv("CORRAL_VMTEST_DESKTOP_COMMAND"),
					DisplayManagerUnit: os.Getenv("CORRAL_VMTEST_DESKTOP_UNIT"),
				}
			}
			result, err := Run(spec, os.Stderr)
			if err != nil {
				t.Fatalf("suite: %v; result: %+v", err, result)
			}
			if !result.Ready || result.ExitCode != ExitOK || len(result.Checks) < 4 {
				t.Fatalf("suite did not run: %+v", result)
			}
		})
	}
}

func TestE2E_GuestAssertionFailureEvidence(t *testing.T) {
	image := os.Getenv("CORRAL_VMTEST_HEADLESS_IMAGE")
	if image == "" {
		t.Skip("set CORRAL_VMTEST_HEADLESS_IMAGE to exercise failure evidence")
	}
	spec := e2eSpec(t, "corral-e2e-guest-broken")
	spec.Bootc = image
	spec.GuestAssertions = &GuestAssertions{ID: "corral-deliberately-invalid-id"}
	result, err := Run(spec, os.Stderr)
	if err == nil || result.ExitCode != ExitCheck || !result.Ready {
		t.Fatalf("expected a booted guest and failing check: %+v, %v", result, err)
	}
	for _, name := range []string{"serial.log", ResultFile} {
		info, err := os.Stat(filepath.Join(spec.Artifacts, name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(spec.Artifacts, ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var stored Result
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ExitCode != ExitCheck || len(stored.Checks) < 4 || !strings.Contains(stored.Checks[3].Output, "unexpected os-release ID") {
		t.Fatalf("identity failure evidence missing: %s", data)
	}
}

func tail(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
