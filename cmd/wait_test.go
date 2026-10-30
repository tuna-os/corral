package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// fakeClock is a waiter clock that only moves when the waiter sleeps, so a
// ten-minute timeout runs in no time and elapsed values are exact.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time        { return c.t }
func (c *fakeClock) sleep(d time.Duration) { c.t = c.t.Add(d) }

func testWaiter(probes []waitProbe, timeout time.Duration) (waiter, *fakeClock, *bytes.Buffer) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	var progress bytes.Buffer
	return waiter{vm: "desk", probes: probes, timeout: timeout, interval: 5 * time.Second,
		progress: &progress, now: clock.now, sleep: clock.sleep}, clock, &progress
}

// scripted returns a probe that answers from a list, repeating the last
// answer, and counts its calls.
func scripted(name string, guest bool, calls *[]string, answers ...waitObservation) waitProbe {
	i := 0
	return waitProbe{name: name, guest: guest, check: func() waitObservation {
		*calls = append(*calls, name)
		obs := answers[min(i, len(answers)-1)]
		i++
		return obs
	}}
}

func yes(observed string) waitObservation { return waitObservation{ok: true, observed: observed} }
func no(observed string) waitObservation  { return waitObservation{observed: observed} }
func onBoot(id string, obs waitObservation) waitObservation {
	obs.bootID = id
	return obs
}

// Conditions run in order, a pass stops at the first one that does not hold,
// and each is reported once, when it becomes true.
func TestWaitChecksInOrderAndReportsEachCondition(t *testing.T) {
	var calls []string
	w, _, progress := testWaiter([]waitProbe{
		scripted("running", false, &calls, no("Starting"), yes("Running")),
		scripted("ssh", true, &calls, onBoot("b1", no("no SSH answer")), onBoot("b1", yes("boot b1"))),
		scripted("unit:gdm.service", true, &calls, onBoot("b1", yes("active"))),
	}, time.Minute)

	report, err := w.run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.Join(calls, " "); got != "running running ssh running ssh unit:gdm.service" {
		t.Errorf("calls = %q, want each pass to stop at the first unmet condition", got)
	}
	if !report.Ready || report.BootID != "b1" || report.ElapsedSeconds != 10 {
		t.Errorf("report = %+v, want ready on boot b1 after 10s", report)
	}
	for _, line := range []string{
		"desk: running is true after 5s (Running)",
		"desk: ssh is true after 10s (boot b1)",
		"desk: unit:gdm.service is true after 10s (active)",
		"desk is ready after 10s",
	} {
		if !strings.Contains(progress.String(), line) {
			t.Errorf("progress lacks %q:\n%s", line, progress)
		}
	}
	if strings.Count(progress.String(), "running is true") != 1 {
		t.Errorf("running reported more than once:\n%s", progress)
	}
}

// On timeout the error names the unmet condition and its last observation,
// and the report still lists every condition.
func TestWaitTimeoutNamesTheUnmetCondition(t *testing.T) {
	var calls []string
	w, _, _ := testWaiter([]waitProbe{
		scripted("running", false, &calls, yes("Running")),
		scripted("ssh", true, &calls, onBoot("b1", yes("boot b1"))),
		scripted("unit:gdm.service", true, &calls, onBoot("b1", no("inactive"))),
		scripted("paint", false, &calls, yes("painted")),
	}, 30*time.Second)

	report, err := w.run()
	if err == nil {
		t.Fatal("run: want a timeout")
	}
	want := "desk is not ready after 30s: unit:gdm.service is not true (last observed: inactive)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
	if report.Ready || len(report.Conditions) != 4 {
		t.Fatalf("report = %+v", report)
	}
	unit, paint := report.Conditions[2], report.Conditions[3]
	if unit.OK || unit.Observed != "inactive" || unit.ElapsedSeconds != nil {
		t.Errorf("unit condition = %+v", unit)
	}
	if paint.OK || paint.Observed != "" {
		t.Errorf("paint was never checked, but reports %+v", paint)
	}
	for _, name := range calls {
		if name == "paint" {
			t.Error("paint ran although the unit before it never held")
		}
	}
}

// The JSON object is the documented shape: vm, ready, elapsed_seconds,
// boot_id, and per-condition name, ok, elapsed_seconds and observed.
func TestWaitReportJSONShape(t *testing.T) {
	var calls []string
	w, _, _ := testWaiter([]waitProbe{
		scripted("running", false, &calls, yes("Running")),
		scripted("ssh", true, &calls, onBoot("b1", yes("boot b1"))),
		scripted("unit:gdm.service", true, &calls, onBoot("b1", yes("active"))),
	}, time.Minute)
	report, err := w.run()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"vm":"desk","ready":true,"elapsed_seconds":0,"boot_id":"b1","conditions":[` +
		`{"name":"running","ok":true,"elapsed_seconds":0,"observed":"Running"},` +
		`{"name":"ssh","ok":true,"elapsed_seconds":0,"observed":"boot b1"},` +
		`{"name":"unit:gdm.service","ok":true,"elapsed_seconds":0,"observed":"active"}]}`
	if string(raw) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", raw, want)
	}
}

// A reboot mid-wait (the boot ID changes) throws away what held on the old
// boot: guest conditions are checked again on the new one.
func TestWaitRechecksGuestConditionsAfterReboot(t *testing.T) {
	var calls []string
	w, _, progress := testWaiter([]waitProbe{
		scripted("running", false, &calls, yes("Running")),
		scripted("ssh", true, &calls, onBoot("old", yes("boot old")), onBoot("new", yes("boot new"))),
		scripted("unit:gdm.service", true, &calls,
			onBoot("old", yes("active")), onBoot("new", no("activating")), onBoot("new", yes("active"))),
		scripted("paint", false, &calls, no("blank 1280x800"), yes("painted 1280x800")),
	}, time.Minute)

	report, err := w.run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(progress.String(), "boot ID changed from old to new") {
		t.Errorf("progress does not report the reboot:\n%s", progress)
	}
	if got := strings.Count(strings.Join(calls, " "), "unit:gdm.service"); got != 3 {
		t.Errorf("unit checked %d times, want 3 (old boot, then twice on the new one): %v", got, calls)
	}
	if report.BootID != "new" || !report.Ready {
		t.Errorf("report = %+v, want ready on the new boot", report)
	}
	if strings.Count(progress.String(), "unit:gdm.service is true") != 2 {
		t.Errorf("unit should be reported true once per boot:\n%s", progress)
	}
}

// A probe that hangs is cut off at the deadline instead of holding the wait.
func TestWaitDoesNotOutliveAHungProbe(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	var progress bytes.Buffer
	w := waiter{vm: "desk", timeout: 50 * time.Millisecond, interval: time.Second,
		progress: &progress, now: time.Now, sleep: time.Sleep,
		probes: []waitProbe{{name: "exec:sleep infinity", check: func() waitObservation {
			<-hang
			return yes("")
		}}}}
	start := time.Now()
	_, err := w.run()
	if err == nil || !strings.Contains(err.Error(), "exec:sleep infinity is not true (last observed: no result before the timeout)") {
		t.Fatalf("run = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("wait took %s with a 50ms timeout", time.Since(start))
	}
}

// fakeWaitAdapter has every capability a wait can use, scripted per test.
type fakeWaitAdapter struct {
	vms      []types.VM
	exec     func(command string) ([]byte, error)
	frame    qemu.Frame
	commands []string
}

func (*fakeWaitAdapter) Backend() string             { return "fake" }
func (a *fakeWaitAdapter) List() ([]types.VM, error) { return a.vms, nil }
func (a *fakeWaitAdapter) Screenshot(_, path string) (qemu.Frame, error) {
	a.frame.Path = path
	return a.frame, nil
}
func (a *fakeWaitAdapter) Exec(_ string, opts backend.SSHOptions) ([]byte, error) {
	a.commands = append(a.commands, opts.Command)
	return a.exec(opts.Command)
}

// listerOnly lacks Execer and Screenshotter.
type listerOnly struct{}

func (listerOnly) Backend() string           { return "virtualbox" }
func (listerOnly) List() ([]types.VM, error) { return nil, nil }

func probeByName(t *testing.T, probes []waitProbe, name string) waitProbe {
	t.Helper()
	for _, p := range probes {
		if p.name == name {
			return p
		}
	}
	t.Fatalf("no probe %q", name)
	return waitProbe{}
}

// The flags become conditions in the documented order; guest conditions
// add ssh, and every guest command reports the boot ID first.
func TestWaitProbesOrderAndGuestCommands(t *testing.T) {
	adapter := &fakeWaitAdapter{exec: func(string) ([]byte, error) { return []byte("corral-boot-id=b1\n"), nil }}
	probes, err := waitProbes("desk", types.InstanceRef{}, adapter, waitSpec{
		bootComplete: true, units: []string{"gdm.service"}, execs: []string{"test -e /run/ok"}, paint: true, user: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range probes {
		names = append(names, p.name)
	}
	if got := strings.Join(names, " "); got != "running ssh boot-complete unit:gdm.service exec:test -e /run/ok paint" {
		t.Fatalf("conditions = %q", got)
	}
	for _, p := range probes[1:5] {
		p.check()
	}
	marker := "echo corral-boot-id=$(cat /proc/sys/kernel/random/boot_id)"
	want := []string{marker, marker + "; systemctl is-system-running",
		marker + "; systemctl is-active -- 'gdm.service'", marker + "; test -e /run/ok"}
	if fmt.Sprint(adapter.commands) != fmt.Sprint(want) {
		t.Errorf("guest commands =\n%q\nwant\n%q", adapter.commands, want)
	}

	running, err := waitProbes("desk", types.InstanceRef{}, adapter, waitSpec{})
	if err != nil || len(running) != 1 || running[0].name != "running" {
		t.Errorf("no flags = %v, %v; want running alone", running, err)
	}
}

func TestWaitProbeVerdicts(t *testing.T) {
	cases := []struct {
		name, condition string
		spec            waitSpec
		out             string
		err             error
		ok              bool
		observed        string
	}{
		{"boot running", "boot-complete", waitSpec{bootComplete: true}, "corral-boot-id=b1\nrunning\n", nil, true, "running"},
		{"boot degraded", "boot-complete", waitSpec{bootComplete: true}, "corral-boot-id=b1\ndegraded\n", errors.New("exit status 1"), true, "degraded"},
		{"boot starting", "boot-complete", waitSpec{bootComplete: true}, "corral-boot-id=b1\nstarting\n", errors.New("exit status 1"), false, "starting"},
		{"unit active", "unit:gdm.service", waitSpec{units: []string{"gdm.service"}}, "corral-boot-id=b1\nactive\n", nil, true, "active"},
		{"unit inactive", "unit:gdm.service", waitSpec{units: []string{"gdm.service"}}, "corral-boot-id=b1\ninactive\n", errors.New("exit status 3"), false, "inactive"},
		{"exec passes", "exec:true", waitSpec{execs: []string{"true"}}, "corral-boot-id=b1\n", nil, true, "exit status 0"},
		{"exec fails", "exec:false", waitSpec{execs: []string{"false"}}, "corral-boot-id=b1\nnope\n", errors.New("exit status 1"), false, "exit status 1: nope"},
		{"ssh refused", "unit:gdm.service", waitSpec{units: []string{"gdm.service"}}, "ssh: connect to host port 22: Connection refused\n", errors.New("exit status 255"), false, "no SSH answer: ssh: connect to host port 22: Connection refused"},
		{"ssh up", "ssh", waitSpec{ssh: true}, "corral-boot-id=b1\n", nil, true, "boot b1"},
		{"ssh old boot", "ssh", waitSpec{notBootID: "b1"}, "corral-boot-id=b1\n", nil, false, "boot b1 is the boot --boot-id names; waiting for the next boot"},
		{"ssh new boot", "ssh", waitSpec{notBootID: "b0"}, "corral-boot-id=b1\n", nil, true, "boot b1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &fakeWaitAdapter{exec: func(string) ([]byte, error) { return []byte(tc.out), tc.err }}
			probes, err := waitProbes("desk", types.InstanceRef{}, adapter, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			obs := probeByName(t, probes, tc.condition).check()
			if obs.ok != tc.ok || obs.observed != tc.observed {
				t.Errorf("%s = ok %v, %q; want ok %v, %q", tc.condition, obs.ok, obs.observed, tc.ok, tc.observed)
			}
		})
	}
}

func TestWaitRunningAndPaintVerdicts(t *testing.T) {
	adapter := &fakeWaitAdapter{vms: []types.VM{
		{Name: "desk", Namespace: "other", Running: true, Status: "Running"},
		{Name: "desk", Namespace: "corral-vms", Running: true, Status: "⏸ Paused"},
	}}
	probes, err := waitProbes("desk", types.InstanceRef{Namespace: "corral-vms"}, adapter, waitSpec{paint: true})
	if err != nil {
		t.Fatal(err)
	}
	if obs := probes[0].check(); obs.ok || obs.observed != "Paused" {
		t.Errorf("paused VM in the right namespace = %+v, want not running", obs)
	}
	adapter.vms[1].Status = "● Running"
	if obs := probes[0].check(); !obs.ok || obs.observed != "Running" {
		t.Errorf("running VM = %+v, want Running without the listing glyph", obs)
	}
	adapter.vms = nil
	if obs := probes[0].check(); obs.ok || obs.observed != "not found" {
		t.Errorf("missing VM = %+v", obs)
	}

	adapter.frame = qemu.Frame{Width: 1280, Height: 800, StdDev: 0}
	if obs := probes[1].check(); obs.ok || obs.observed != "blank 1280x800 (deviation 0.0000)" {
		t.Errorf("blank frame = %+v", obs)
	}
	adapter.frame.StdDev = 0.25
	if obs := probes[1].check(); !obs.ok || obs.observed != "painted 1280x800 (deviation 0.2500)" {
		t.Errorf("painted frame = %+v", obs)
	}
}

// A backend without the capability a condition needs is refused before the
// wait starts, by backend name.
func TestWaitRefusesMissingCapabilities(t *testing.T) {
	for _, spec := range []waitSpec{{units: []string{"gdm.service"}}, {paint: true}} {
		_, err := waitProbes("desk", types.InstanceRef{}, listerOnly{}, spec)
		if err == nil || !strings.Contains(err.Error(), "the virtualbox backend does not support wait") {
			t.Errorf("waitProbes(%+v) = %v, want a refusal naming the backend", spec, err)
		}
	}
}

func waitFlags(t *testing.T) {
	t.Helper()
	oldJSON := jsonOutput
	t.Cleanup(func() {
		jsonOutput = oldJSON
		waitSSH, waitBootComplete, waitPaint = false, false, false
		waitUnits, waitExecs, waitBootID = nil, nil, ""
		waitSSHUser, waitSSHIdentity = "root", ""
		waitTimeout, waitInterval = 10*time.Minute, 5*time.Second
	})
}

// End to end on KubeVirt: the listing says Running, then virtctl ssh answers
// for the unit, and --json prints the one report object.
func TestWaitCommandKubeVirt(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "corral-vms"})
	fake.AddResponse("kubectl get vms -A -o json",
		`{"items":[{"metadata":{"name":"web","namespace":"corral-vms"},"status":{"printableStatus":"Running","ready":true}}]}`, nil)
	fake.AddPrefixResponse("/fake/bin/virtctl ssh", "corral-boot-id=4b1d\nactive\n", nil)
	waitFlags(t)
	jsonOutput = true
	waitUnits = []string{"gdm.service"}
	waitTimeout, waitInterval = time.Minute, time.Millisecond

	var stderr bytes.Buffer
	waitCmd.SetErr(&stderr)
	t.Cleanup(func() { waitCmd.SetErr(nil) })
	stdout := captureStdout(t, func() {
		if err := waitCmd.RunE(waitCmd, []string{"web"}); err != nil {
			t.Fatalf("wait: %v", err)
		}
	})
	var report waitReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if !report.Ready || report.VM != "web" || report.BootID != "4b1d" || len(report.Conditions) != 3 ||
		report.Conditions[2].Name != "unit:gdm.service" || report.Conditions[2].Observed != "active" {
		t.Errorf("report = %+v", report)
	}
	if !ranCommandLine(fake, "virtctl ssh --namespace=corral-vms --username=root --command=echo corral-boot-id=$(cat /proc/sys/kernel/random/boot_id); systemctl is-active -- 'gdm.service'") {
		t.Errorf("no unit check over virtctl ssh; calls were %v", fake.Calls())
	}
	if !strings.Contains(stderr.String(), "web is ready after") {
		t.Errorf("stderr = %q, want progress", stderr.String())
	}
}
