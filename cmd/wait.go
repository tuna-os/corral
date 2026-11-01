package cmd

// corral wait blocks until a VM is ready by every measure a script asked for:
// running, answering SSH, finished booting, units active, a command passing,
// the screen painted. Without it every desktop journey hand-rolled the same
// loop around `corral ssh -c 'systemctl is-active gdm'`, each with its own idea
// of a timeout and none of them noticing a reboot halfway through.

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/types"
)

var (
	waitRunning      bool
	waitSSH          bool
	waitBootComplete bool
	waitUnits        []string
	waitExecs        []string
	waitPaint        bool
	waitBootID       string
	waitSSHUser      string
	waitSSHIdentity  string
	waitTimeout      time.Duration
	waitInterval     time.Duration
)

var waitCmd = &cobra.Command{
	Use:   "wait <name>",
	Short: "Wait until a VM is running, answers SSH, has booted, and shows a desktop",
	Long: `Wait until every condition you name holds for the VM, then exit 0.

Corral checks the conditions in this order, every --interval, and reports
each one on stderr when it becomes true:

  running        the backend reports the VM as running (always checked)
  ssh            the guest runs a command over SSH as --ssh-user
  boot-complete  systemctl is-system-running is "running" or "degraded"
  unit:NAME      systemctl is-active NAME is "active" (--unit, repeatable)
  exec:CMD       CMD exits 0 in the guest (--exec, repeatable)
  paint          a screenshot of the console is not blank (--paint)

--boot-complete, --unit, --exec and --boot-id need SSH, so they add the ssh
condition. Each SSH check also reads the guest's boot ID. If the boot ID
changes during the wait, the guest rebooted, and Corral checks the guest
conditions again. --boot-id ID waits for a boot other than ID: give it the
boot ID from before a restart, and an old boot cannot satisfy the wait.

If a condition is not true before --timeout, the command exits nonzero and
names the condition and its last observation. With --json, the result is one
JSON object on stdout, ready or not.`,
	Example: `  corral wait web                                   # running
  corral wait web --ssh --timeout 5m
  corral wait desk --unit gdm.service --paint --timeout 15m
  corral wait desk --boot-complete --exec 'loginctl list-sessions | grep -q greeter'
  old=$(corral --json wait desk --ssh | jq -r .boot_id)
  corral restart desk && corral wait desk --boot-id "$old" --unit gdm.service`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if waitTimeout <= 0 || waitInterval <= 0 {
			return fmt.Errorf("--timeout and --interval must be more than zero")
		}
		name := args[0]
		ref, err := instanceRef(name)
		if err != nil {
			return err
		}
		adapter, err := backend.For(ref)
		if err != nil {
			return err
		}
		probes, err := waitProbes(ref.Name, ref, adapter, waitSpec{
			ssh: waitSSH, bootComplete: waitBootComplete, units: waitUnits,
			execs: waitExecs, paint: waitPaint, notBootID: waitBootID,
			user: waitSSHUser, identity: waitSSHIdentity,
		})
		if err != nil {
			return err
		}
		w := waiter{
			vm: name, probes: probes, timeout: waitTimeout, interval: waitInterval,
			progress: cmd.ErrOrStderr(), now: time.Now, sleep: time.Sleep,
		}
		report, waitErr := w.run()
		if jsonOutput {
			if err := printJSON(report); err != nil {
				return err
			}
		}
		return waitErr
	},
}

func init() {
	rootCmd.AddCommand(waitCmd)
	f := waitCmd.Flags()
	f.BoolVar(&waitRunning, "running", false, "Wait until the backend reports the VM as running (always checked; the default condition)")
	f.BoolVar(&waitSSH, "ssh", false, "Wait until the guest runs a command over SSH")
	f.BoolVar(&waitBootComplete, "boot-complete", false, `Wait until systemctl is-system-running is "running" or "degraded"`)
	f.StringArrayVar(&waitUnits, "unit", nil, "Wait until this systemd unit is active, e.g. gdm.service (repeatable)")
	f.StringArrayVar(&waitExecs, "exec", nil, "Wait until this guest command exits 0 over SSH (repeatable)")
	f.BoolVar(&waitPaint, "paint", false, "Wait until a screenshot of the console is not blank")
	f.StringVar(&waitBootID, "boot-id", "", "Wait for a boot other than this boot ID (the boot_id from before a restart)")
	f.StringVar(&waitSSHUser, "ssh-user", "root", "User for the SSH checks")
	f.StringVar(&waitSSHIdentity, "ssh-identity", "", "Private key for the SSH checks")
	f.DurationVar(&waitTimeout, "timeout", 10*time.Minute, "Give up after this long")
	f.DurationVar(&waitInterval, "interval", 5*time.Second, "Time between checks")
}

// waitSpec is the set of conditions the flags asked for.
type waitSpec struct {
	ssh, bootComplete, paint bool
	units, execs             []string
	notBootID                string
	user, identity           string
}

// needsGuest reports whether any condition runs inside the guest, which makes
// SSH a condition of its own.
func (s waitSpec) needsGuest() bool {
	return s.ssh || s.bootComplete || len(s.units) > 0 || len(s.execs) > 0 || s.notBootID != ""
}

// waitProbe is one condition and how to observe it once.
type waitProbe struct {
	name string
	// guest marks a condition that holds for one boot only: when the boot ID
	// changes, it must be checked again.
	guest bool
	check func() waitObservation
}

// waitObservation is one look at one condition.
type waitObservation struct {
	ok       bool
	observed string
	// bootID is the guest's boot ID when the check went over SSH, else "".
	bootID string
}

// waitCondition is one condition in the report.
type waitCondition struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// ElapsedSeconds is when the condition last became true, from the start
	// of the wait; absent while it does not hold.
	ElapsedSeconds *float64 `json:"elapsed_seconds,omitempty"`
	// Observed is the last thing the check saw; absent for a condition never
	// checked because one before it did not hold.
	Observed string `json:"observed,omitempty"`
}

// waitReport is what --json prints.
type waitReport struct {
	VM             string          `json:"vm"`
	Ready          bool            `json:"ready"`
	ElapsedSeconds float64         `json:"elapsed_seconds"`
	BootID         string          `json:"boot_id,omitempty"`
	Conditions     []waitCondition `json:"conditions"`
}

// waitProbes turns the spec into checks against the adapter's capabilities, in
// the order the wait runs them. A capability the backend lacks is refused here,
// before the wait starts, rather than after a timeout.
func waitProbes(name string, ref types.InstanceRef, adapter backend.Adapter, spec waitSpec) ([]waitProbe, error) {
	lister, ok := adapter.(backend.Lister)
	if !ok {
		return nil, unsupported(adapter, "wait")
	}
	probes := []waitProbe{{name: "running", check: func() waitObservation {
		return observeRunning(lister, name, ref.Namespace)
	}}}

	if spec.needsGuest() {
		execer, ok := adapter.(backend.Execer)
		if !ok {
			return nil, unsupported(adapter, "wait for guest conditions (--ssh, --boot-complete, --unit, --exec, --boot-id)")
		}
		run := func(command string) ([]byte, error) {
			return execer.Exec(name, backend.SSHOptions{
				User: spec.user, Identity: spec.identity, Port: 22,
				Command: guestCommand(command),
			})
		}
		probes = append(probes, waitProbe{name: "ssh", guest: true, check: func() waitObservation {
			out, err := run("")
			bootID, rest := splitBootID(out)
			if bootID == "" {
				return waitObservation{observed: noSSHAnswer(rest, err)}
			}
			if spec.notBootID != "" && bootID == spec.notBootID {
				return waitObservation{bootID: bootID,
					observed: "boot " + bootID + " is the boot --boot-id names; waiting for the next boot"}
			}
			return waitObservation{ok: true, bootID: bootID, observed: "boot " + bootID}
		}})
		guest := func(probeName, command string, judge func(rest string, err error) (bool, string)) waitProbe {
			return waitProbe{name: probeName, guest: true, check: func() waitObservation {
				out, err := run(command)
				bootID, rest := splitBootID(out)
				if bootID == "" {
					return waitObservation{observed: noSSHAnswer(rest, err)}
				}
				ok, observed := judge(rest, err)
				return waitObservation{ok: ok, observed: observed, bootID: bootID}
			}}
		}
		if spec.bootComplete {
			probes = append(probes, guest("boot-complete", "systemctl is-system-running",
				func(rest string, _ error) (bool, string) {
					state := firstLine(rest)
					return state == "running" || state == "degraded", state
				}))
		}
		for _, unit := range spec.units {
			probes = append(probes, guest("unit:"+unit, "systemctl is-active -- "+shellSingleQuote(unit),
				func(rest string, err error) (bool, string) {
					state := firstLine(rest)
					return err == nil && state == "active", state
				}))
		}
		for _, command := range spec.execs {
			probes = append(probes, guest("exec:"+command, command,
				func(rest string, err error) (bool, string) {
					status := "exit status 0"
					if err != nil {
						status = err.Error()
					}
					if last := lastLine(rest); last != "" {
						status += ": " + last
					}
					return err == nil, status
				}))
		}
	}

	if spec.paint {
		shooter, ok := adapter.(backend.Screenshotter)
		if !ok {
			return nil, unsupported(adapter, "wait --paint")
		}
		probes = append(probes, waitProbe{name: "paint", check: func() waitObservation {
			return observePaint(shooter, name)
		}})
	}
	return probes, nil
}

// observeRunning reads the backend's own status. Paused and Stopping count as
// running in a listing, but a script that waits for running means a VM that
// does work, so they do not hold here. The status loses the glyph a listing
// draws in front of it ("● Running" is "Running").
func observeRunning(lister backend.Lister, name, namespace string) waitObservation {
	vms, err := lister.List()
	if err != nil {
		return waitObservation{observed: "listing failed: " + err.Error()}
	}
	for _, vm := range vms {
		if vm.Name != name || (namespace != "" && vm.Namespace != "" && vm.Namespace != namespace) {
			continue
		}
		status := strings.TrimLeftFunc(vm.Status, func(r rune) bool { return !unicode.IsLetter(r) })
		if status == "" {
			status = "unknown"
		}
		lower := strings.ToLower(status)
		busy := strings.Contains(lower, "paused") || strings.Contains(lower, "stopping") || strings.Contains(lower, "terminating")
		return waitObservation{ok: vm.Running && !busy, observed: status}
	}
	return waitObservation{observed: "not found"}
}

// observePaint captures the console into a scratch file and judges it with the
// same blank threshold as `corral screenshot --require-paint`.
func observePaint(shooter backend.Screenshotter, name string) waitObservation {
	dir, err := os.MkdirTemp("", "corral-wait-")
	if err != nil {
		return waitObservation{observed: err.Error()}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	frame, err := shooter.Screenshot(name, filepath.Join(dir, "screen.png"))
	if err != nil {
		return waitObservation{observed: "screenshot failed: " + firstLine(err.Error())}
	}
	verdict := "painted"
	if frame.Blank() {
		verdict = "blank"
	}
	return waitObservation{ok: !frame.Blank(),
		observed: fmt.Sprintf("%s %dx%d (deviation %.4f)", verdict, frame.Width, frame.Height, frame.StdDev)}
}

// bootIDMarker prefixes the boot ID line every guest check prints first, so
// the check's own output and exit status stay the command's.
const bootIDMarker = "corral-boot-id="

func guestCommand(command string) string {
	probe := "echo " + bootIDMarker + "$(cat /proc/sys/kernel/random/boot_id)"
	if command == "" {
		return probe
	}
	return probe + "; " + command
}

// splitBootID takes the marker line out of a guest check's output. No marker
// means the command never ran: SSH did not get through.
func splitBootID(out []byte) (bootID, rest string) {
	var kept []string
	for _, line := range strings.Split(string(out), "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), bootIDMarker); ok && bootID == "" {
			bootID = id
			continue
		}
		kept = append(kept, line)
	}
	return bootID, strings.TrimSpace(strings.Join(kept, "\n"))
}

func noSSHAnswer(rest string, err error) string {
	detail := lastLine(rest)
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if detail == "" {
		return "no SSH answer"
	}
	return "no SSH answer: " + detail
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSpace(s[strings.LastIndex(s, "\n")+1:])
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// waiter runs the probes until all hold in one pass or the timeout ends.
type waiter struct {
	vm                string
	probes            []waitProbe
	timeout, interval time.Duration
	progress          io.Writer
	now               func() time.Time
	sleep             func(time.Duration)
}

func (w waiter) run() (waitReport, error) {
	start := w.now()
	deadline := start.Add(w.timeout)
	n := len(w.probes)
	met := make([]bool, n)
	since := make([]time.Duration, n)
	observed := make([]string, n)
	names := make([]string, n)
	for i, p := range w.probes {
		names[i] = p.name
	}
	w.say("Waiting up to %s for %s: %s", w.timeout, w.vm, strings.Join(names, ", "))

	bootID := ""
	for {
		failed := -1 // the condition that stopped this pass
		rebooted := false
		timedOut := false
		for i, p := range w.probes {
			obs, finished := w.check(p, deadline)
			if !finished {
				observed[i] = "no result before the timeout"
				failed, timedOut = i, true
				break
			}
			if obs.bootID != "" && bootID != "" && obs.bootID != bootID {
				w.say("%s: boot ID changed from %s to %s; the guest rebooted, so the guest conditions are checked again",
					w.vm, bootID, obs.bootID)
				for j := range w.probes {
					if w.probes[j].guest {
						met[j] = false
					}
				}
				bootID = obs.bootID
				observed[i] = obs.observed
				failed, rebooted = i, true
				break
			}
			if obs.bootID != "" {
				bootID = obs.bootID
			}
			observed[i] = obs.observed
			elapsed := w.now().Sub(start)
			if obs.ok {
				if !met[i] {
					met[i], since[i] = true, elapsed
					w.say("%s: %s is true after %s (%s)", w.vm, p.name, roundSecond(elapsed), obs.observed)
				}
				continue
			}
			if met[i] {
				met[i] = false
				w.say("%s: %s is no longer true (%s)", w.vm, p.name, obs.observed)
			}
			failed = i
			break
		}
		if failed < 0 {
			report := w.report(start, bootID, met, since, observed)
			w.say("%s is ready after %s", w.vm, roundSecond(w.now().Sub(start)))
			return report, nil
		}
		if !timedOut && !rebooted {
			if remaining := deadline.Sub(w.now()); remaining > 0 {
				w.sleep(min(w.interval, remaining))
			}
		}
		// A pass that starts at the deadline has no time to run, so the
		// last finished pass is the verdict.
		if timedOut || !w.now().Before(deadline) {
			report := w.report(start, bootID, met, since, observed)
			last := observed[failed]
			if last == "" {
				last = "no observation"
			}
			return report, fmt.Errorf("%s is not ready after %s: %s is not true (last observed: %s)",
				w.vm, w.timeout, names[failed], last)
		}
	}
}

// say writes one progress line. Progress is a courtesy on stderr; a closed
// stderr must not fail the wait.
func (w waiter) say(format string, args ...any) {
	_, _ = fmt.Fprintf(w.progress, format+"\n", args...)
}

// check runs one probe, but returns at the deadline even when the probe has
// not: a guest command that hangs must not hold the wait past --timeout.
func (w waiter) check(p waitProbe, deadline time.Time) (waitObservation, bool) {
	done := make(chan waitObservation, 1)
	go func() { done <- p.check() }()
	timer := time.NewTimer(max(deadline.Sub(w.now()), 0))
	defer timer.Stop()
	select {
	case obs := <-done:
		return obs, true
	case <-timer.C:
		select {
		case obs := <-done:
			return obs, true
		default:
			return waitObservation{}, false
		}
	}
}

func (w waiter) report(start time.Time, bootID string, met []bool, since []time.Duration, observed []string) waitReport {
	report := waitReport{
		VM: w.vm, Ready: true, BootID: bootID,
		ElapsedSeconds: seconds(w.now().Sub(start)),
		Conditions:     make([]waitCondition, len(w.probes)),
	}
	for i, p := range w.probes {
		c := waitCondition{Name: p.name, OK: met[i], Observed: observed[i]}
		if met[i] {
			s := seconds(since[i])
			c.ElapsedSeconds = &s
		} else {
			report.Ready = false
		}
		report.Conditions[i] = c
	}
	return report
}

func seconds(d time.Duration) float64 { return math.Round(d.Seconds()*10) / 10 }

func roundSecond(d time.Duration) time.Duration { return d.Round(time.Second) }
