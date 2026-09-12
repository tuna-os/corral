package vmtest

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/bootc"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/types"
)

// The run itself: image in, booted VM and a verdict out.

// Run executes spec and returns the result. The error is the run's failure;
// the result carries the exit code and is written to the artifact directory
// either way, so a caller reports both.
//
// Nothing here is interactive. Progress goes to out, evidence goes to the
// artifact directory, and the decision goes to the result.
func Run(spec *Spec, out io.Writer) (*Result, error) {
	if out == nil {
		out = io.Discard
	}
	progress := func(msg string) { _, _ = fmt.Fprintln(out, "==> "+msg) }

	spec.WithDefaults()
	if err := spec.Validate(); err != nil {
		result := &Result{Name: spec.Name, Started: time.Now()}
		return result, result.fail(ExitSpec, err)
	}

	artifacts, err := filepath.Abs(spec.Artifacts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		return nil, err
	}

	result := &Result{
		Name:      spec.Name,
		Image:     spec.Bootc,
		Backend:   "qemu",
		Started:   time.Now(),
		Artifacts: artifacts,
	}
	// The artifacts are the point of the run, so they are written whatever
	// happens — including a panic-free early return three lines from here.
	defer func() {
		if path, err := result.Write(artifacts); err != nil {
			_, _ = fmt.Fprintf(out, "warning: could not write the result file: %v\n", err)
		} else {
			progress("result: " + path)
		}
	}()

	notes, err := Preflight(spec)
	if err != nil {
		return result, result.fail(ExitHost, err)
	}
	for _, note := range notes {
		result.Note("%s", note)
		progress(note)
	}

	keys, err := EnsureKeypair(filepath.Join(artifacts, "ssh"), operatorKey())
	if err != nil {
		return result, result.fail(ExitHost, err)
	}
	result.SSH.IdentityFile = keys.PrivatePath

	layered, err := BuildLayer(spec, keys.AuthorizedKeys(), filepath.Join(artifacts, "layer"), progress)
	result.Layer = layered
	if err != nil {
		return result, result.fail(ExitLayer, err)
	}

	if err := buildAndImport(spec, layered, keys, progress); err != nil {
		code := ExitBuild
		if strings.Contains(err.Error(), "creating the VM") {
			code = ExitStart
		}
		return result, result.fail(code, err)
	}
	recordInRegistry(spec, out)

	// Capture starts before the VM does, so the first frames show the firmware
	// and a disk that never boots is not a run with no screenshots.
	frames := newFrameRecorder(spec, artifacts)
	frames.start()
	defer func() {
		frames.stop()
		result.Frames = frames.frames()
	}()

	bootStarted := time.Now()
	progress("starting " + spec.Name)
	if err := qemu.Start(spec.Name); err != nil {
		return result, result.fail(ExitStart, fmt.Errorf("starting the VM: %w", err))
	}

	readyErr := waitReady(spec, keys, result, progress)
	// The console log is evidence in both directions, so it is copied before
	// the first chance to return.
	if path, err := copySerialLog(spec.Name, artifacts); err == nil {
		result.SerialLog = path
	} else {
		result.Note("no console log: %v", err)
	}
	if readyErr != nil {
		frames.stop()
		result.FinalFrame = captureFinal(spec, artifacts, "failure", result)
		return result, result.fail(ExitNotReady, readyErr)
	}
	result.Ready = true
	result.BootSeconds = time.Since(bootStarted).Seconds()
	progress(fmt.Sprintf("%s is ready after %.0fs (%s)", spec.Name, result.BootSeconds, result.ReadyBy))

	if endpoint, err := qemu.SSHEndpoint(spec.Name); err == nil {
		result.SSH.User, result.SSH.Host, result.SSH.Port = spec.SSHUser, endpoint.Host, endpoint.Port
		result.SSH.Password = spec.passwordFor(spec.SSHUser)
		result.SSH.Command = fmt.Sprintf("ssh -i %s -p %d %s@%s", keys.PrivatePath, endpoint.Port, spec.SSHUser, endpoint.Host)
	}

	// Frames stop once the guest is ready: a timelapse of an idle login screen
	// is not worth the disk.
	frames.stop()
	result.Frames = frames.frames()
	result.FinalFrame = captureFinal(spec, artifacts, "ready", result)
	if video, err := assembleVideo(spec, artifacts); err != nil {
		result.Note("no timelapse: %v", err)
	} else if video != "" {
		result.Video = video
		progress("timelapse: " + video)
	}

	if path, err := copySerialLog(spec.Name, artifacts); err == nil {
		result.SerialLog = path
	}

	sshReachable := result.ReadyBy == readyBySSH
	if !sshReachable {
		// The marker gate does not prove SSH works. Try it anyway, because
		// everything below needs it and "ready but unreachable" is worth
		// reporting as itself.
		if err := qemu.WaitSSHKey(spec.Name, spec.SSHUser, keys.PrivatePath, 60*time.Second); err == nil {
			sshReachable = true
		} else {
			result.Note("the guest reported ready on its console but SSH did not answer: %v", err)
		}
	}

	if layered.Derived {
		result.Hook = hookOutcome(spec, keys, sshReachable, progress)
	}
	if sshReachable {
		collectDiagnostics(spec, keys, artifacts)
		result.Checks = runChecks(spec, keys, progress)
	} else if len(spec.Checks) > 0 {
		result.Note("skipped %d checks: SSH never answered", len(spec.Checks))
	}

	result.Kept = spec.Keep
	if !spec.Keep {
		progress("deleting " + spec.Name)
		if err := qemu.Delete(spec.Name); err != nil {
			result.Note("could not delete the VM: %v", err)
		}
	}

	// Verdicts, in the order a reader cares about them.
	switch {
	case result.Hook != nil && !result.Hook.Ran:
		return result, result.fail(ExitHook, fmt.Errorf("the post-boot hook never reported — see %s in the guest and the console log", StatusFile))
	case result.Hook != nil && result.Hook.ExitCode != 0:
		return result, result.fail(ExitHook, fmt.Errorf("the post-boot hook failed (exit %d)", result.Hook.ExitCode))
	case !sshReachable && len(spec.Checks) > 0:
		return result, result.fail(ExitCheck, fmt.Errorf("the checks could not run: SSH never answered"))
	}
	for _, check := range result.Checks {
		if !check.Passed {
			return result, result.fail(ExitCheck, fmt.Errorf("check failed: %s", check.Command))
		}
	}
	if spec.Screenshots.RequirePaint && result.FinalFrame != nil && result.FinalFrame.Blank() {
		return result, result.fail(ExitBlank, fmt.Errorf(
			"the guest booted but never painted anything (framebuffer deviation %.4f, at or under %.2f) — see %s",
			result.FinalFrame.StdDev, qemu.BlankStdDev, result.FinalFrame.Path))
	}
	result.pass()
	return result, nil
}

// readyBy values.
const (
	readyBySSH    = "ssh"
	readyByMarker = "console-marker"
)

// Preflight reports why this host cannot run the spec, and what a reader should
// know before waiting ten minutes for the answer.
//
// Checked before the pull, not after: a runner with no KVM or no loop device
// fails the same way every time, and finding that out after a four-gigabyte
// download is the difference between a useful error and a wasted run.
func Preflight(spec *Spec) (notes []string, err error) {
	if err := (bootc.LocalBuilder{Sudo: spec.Sudo}).Available(); err != nil {
		return nil, err
	}
	if !qemu.Available() {
		return nil, fmt.Errorf("qemu-system-x86_64 and qemu-img are not installed, and the VM runs under QEMU")
	}
	if _, err := lookPath("ssh"); err != nil {
		return nil, fmt.Errorf("the OpenSSH client is not installed, and every probe and check logs in over SSH")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		// Not fatal: TCG boots, eventually. Said out loud because the usual
		// symptom is a timeout that looks like a broken image.
		notes = append(notes, "no /dev/kvm: the guest will run under software emulation, which is slow enough that the default timeout may not be enough")
	}
	return notes, nil
}

// buildAndImport turns the image into this VM's disk.
func buildAndImport(spec *Spec, layered Layered, keys Keypair, progress func(string)) error {
	builder := bootc.LocalBuilder{Sudo: spec.Sudo}
	workDir := filepath.Join(qemu.VMHome(), "cache", "vmtest")
	dest := filepath.Join(workDir, spec.Name+".raw")
	built, err := builder.Build(bootc.BuildRequest{
		Image:  layered.Image,
		Dest:   dest,
		Size:   spec.Disk,
		SSHKey: keys.AuthorizedKeys(),
		// Without a serial console the boot log goes nowhere, and a guest that
		// fails before sshd has no way to say so.
		Kargs: []string{SerialKarg},
	}, progress)
	if err != nil {
		return err
	}
	// The disk is consumed by Import, which converts it into the backend's own
	// storage; what is left is a copy nothing reads again.
	defer func() { _ = os.Remove(built.Path) }()

	// Import creates the VM, and Create refuses to overwrite. A test VM is
	// disposable by definition, so the old one goes.
	if qemu.Exists(spec.Name) {
		report(progress, "replacing the existing VM named %s", spec.Name)
		if err := qemu.Delete(spec.Name); err != nil {
			return fmt.Errorf("creating the VM: replacing %s: %w", spec.Name, err)
		}
	}
	target := bootc.QEMUTarget{}
	if err := target.Import(types.InstanceRef{Backend: "qemu", Name: spec.Name}, built.Path, bootc.CreateOpts{
		CPU: spec.CPUs, Memory: spec.Memory, Disk: spec.Disk, SSHKey: keys.AuthorizedKeys(),
	}); err != nil {
		return fmt.Errorf("creating the VM: %w", err)
	}
	return nil
}

// recordInRegistry makes the VM visible to the rest of corral, so `corral ssh`,
// `corral list` and `corral delete` work on what the run left behind. A
// failure here does not fail the run: the VM exists either way.
func recordInRegistry(spec *Spec, out io.Writer) {
	store, err := registry.NewStore()
	if err != nil {
		_, _ = fmt.Fprintf(out, "warning: %s is not in the registry: %v\n", spec.Name, err)
		return
	}
	entry := types.RegistryEntry{Backend: "qemu", Password: spec.RootPassword}
	if err := store.Set(spec.Name, entry); err != nil {
		_, _ = fmt.Fprintf(out, "warning: %s is not in the registry: %v\n", spec.Name, err)
	}
}

// waitReady blocks until the guest says it is up, by whichever gate the spec
// chose.
func waitReady(spec *Spec, keys Keypair, result *Result, progress func(string)) error {
	timeout := spec.Timeout.Duration()
	pattern, err := spec.readyPattern()
	if err != nil {
		return err
	}
	if pattern != nil {
		report(progress, "waiting up to %s for /%s/ on the console", timeout, spec.ReadyMarker)
		line, err := qemu.WaitSerial(spec.Name, pattern, timeout)
		if err != nil {
			return err
		}
		result.ReadyBy = readyByMarker
		result.ReadyEvidence = strings.TrimSpace(line)
		return nil
	}
	report(progress, "waiting up to %s for SSH as %s", timeout, spec.SSHUser)
	if err := qemu.WaitSSHKey(spec.Name, spec.SSHUser, keys.PrivatePath, timeout); err != nil {
		return fmt.Errorf("%w\n%s", err, consoleTail(spec.Name, 25))
	}
	result.ReadyBy = readyBySSH
	return nil
}

// consoleTail is the last lines of the guest console, for the error message of
// a boot that never answered. The whole log is in the artifacts; this is the
// part that says why without making the reader go and look.
func consoleTail(name string, lines int) string {
	data, err := qemu.SerialLog(name)
	if err != nil {
		return "(no console log: " + err.Error() + ")"
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	if len(all) == 0 || (len(all) == 1 && all[0] == "") {
		return "(the guest console said nothing — it may not have reached the bootloader)"
	}
	return "last console output:\n  " + strings.Join(all, "\n  ")
}

// hookOutcome reads the post-boot hook's verdict: from the status file when
// SSH works, from the console markers when it does not.
func hookOutcome(spec *Spec, keys Keypair, sshReachable bool, progress func(string)) *HookResult {
	if sshReachable {
		// The unit runs after sshd, so SSH can answer before the hook has
		// finished. Poll rather than read once.
		deadline := time.Now().Add(3 * time.Minute)
		for {
			out, err := qemu.Exec(spec.Name, "cat "+StatusFile, qemu.ExecOpts{
				User: spec.SSHUser, IdentityFile: keys.PrivatePath, Timeout: 20 * time.Second,
			})
			if err == nil {
				code, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
				if convErr == nil {
					log, _ := qemu.Exec(spec.Name, "cat "+HookLogFile, qemu.ExecOpts{
						User: spec.SSHUser, IdentityFile: keys.PrivatePath, Timeout: 30 * time.Second,
					})
					report(progress, "post-boot hook exited %d", code)
					return &HookResult{Ran: true, ExitCode: code, Log: string(log), Source: "status-file"}
				}
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Second)
		}
	}
	// Fall back to the console, which is all a guest with no SSH has.
	data, err := qemu.SerialLog(spec.Name)
	if err != nil {
		return &HookResult{Ran: false}
	}
	log := string(data)
	switch {
	case strings.Contains(log, HookOKMarker):
		return &HookResult{Ran: true, ExitCode: 0, Source: "console"}
	case strings.Contains(log, HookFailMarker):
		code := 1
		if _, after, found := strings.Cut(log, HookFailMarker+" rc="); found {
			if parsed, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(after, "\n", 2)[0])); err == nil {
				code = parsed
			}
		}
		return &HookResult{Ran: true, ExitCode: code, Source: "console"}
	}
	return &HookResult{Ran: false}
}

// runChecks runs the spec's assertions in order and keeps going after a
// failure: one run should report every broken thing, not the first.
func runChecks(spec *Spec, keys Keypair, progress func(string)) []CheckResult {
	var results []CheckResult
	for _, command := range spec.Checks {
		out, err := qemu.Exec(spec.Name, command, qemu.ExecOpts{
			User: spec.SSHUser, IdentityFile: keys.PrivatePath, Timeout: 2 * time.Minute,
		})
		check := CheckResult{Command: command, Passed: err == nil, Output: strings.TrimRight(string(out), "\n")}
		if err != nil {
			check.Error = err.Error()
			report(progress, "check FAILED: %s", command)
		} else {
			report(progress, "check passed: %s", command)
		}
		results = append(results, check)
	}
	return results
}

// diagnostics are collected from every guest that answers, passing or not.
// They are the questions a human asks first, and asking them now costs one SSH
// round trip while the VM is definitely up.
var diagnostics = []struct{ name, command string }{
	{"failed-units.txt", "systemctl --failed --no-legend --no-pager || true"},
	{"bootc-status.json", "bootc status --format json 2>/dev/null || echo '{}'"},
	{"journal-warnings.txt", "journalctl -b -p warning --no-pager 2>/dev/null | tail -300 || true"},
}

func collectDiagnostics(spec *Spec, keys Keypair, artifacts string) {
	dir := filepath.Join(artifacts, "diagnostics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	for _, d := range diagnostics {
		out, _ := qemu.Exec(spec.Name, d.command, qemu.ExecOpts{
			User: spec.SSHUser, IdentityFile: keys.PrivatePath, Timeout: time.Minute,
		})
		_ = os.WriteFile(filepath.Join(dir, d.name), out, 0o644)
	}
}

// copySerialLog puts the guest console in the artifact directory, where it
// survives the VM being deleted.
func copySerialLog(name, artifacts string) (string, error) {
	data, err := qemu.SerialLog(name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(artifacts, "serial.log")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// passwordFor returns the password the spec set for a user, if any.
func (s *Spec) passwordFor(user string) string {
	if user == "root" {
		return s.RootPassword
	}
	for _, u := range s.Users {
		if u.Name == user {
			return u.Password
		}
	}
	return ""
}

// operatorKey returns the caller's own public key, so the VM the run leaves
// behind is reachable with the key they already use. Best effort — a runner
// with no key is the normal case, not an error.
func operatorKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub"} {
		if data, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}

// ── frames and the timelapse ──────────────────────────────────────

// frameRecorder captures the framebuffer on an interval while the guest boots.
//
// A boot nobody watched is the hardest failure to explain, and the frames are
// what turn "SSH never answered" into "it sat in the UEFI setup menu". TunaOS
// and wootc both build their CI evidence this way — screendump on a timer, then
// ffmpeg — so this does the capture in Go and leaves only the encode to ffmpeg.
type frameRecorder struct {
	name     string
	dir      string
	interval time.Duration

	mu        sync.Mutex
	collected []qemu.Frame

	stopOnce sync.Once
	done     chan struct{}
	finished chan struct{}
}

func newFrameRecorder(spec *Spec, artifacts string) *frameRecorder {
	return &frameRecorder{
		name:     spec.Name,
		dir:      filepath.Join(artifacts, "frames"),
		interval: spec.Screenshots.Interval.Duration(),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
}

func (f *frameRecorder) start() {
	if f.interval <= 0 {
		close(f.finished)
		return
	}
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		close(f.finished)
		return
	}
	go func() {
		defer close(f.finished)
		ticker := time.NewTicker(f.interval)
		defer ticker.Stop()
		for index := 0; ; index++ {
			select {
			case <-f.done:
				return
			case <-ticker.C:
			}
			// A failure is expected and ignored: before the VM starts there is
			// no monitor socket, and a guest in a mode with no surface has no
			// frame to give. Either way the next tick tries again.
			frame, err := qemu.Capture(f.name, filepath.Join(f.dir, fmt.Sprintf("f%06d.png", index)))
			if err != nil {
				index--
				continue
			}
			f.mu.Lock()
			f.collected = append(f.collected, frame)
			f.mu.Unlock()
		}
	}()
}

func (f *frameRecorder) stop() {
	f.stopOnce.Do(func() { close(f.done) })
	<-f.finished
}

func (f *frameRecorder) frames() []qemu.Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]qemu.Frame(nil), f.collected...)
}

// captureFinal takes the frame that shows how the run ended.
func captureFinal(spec *Spec, artifacts, label string, result *Result) *qemu.Frame {
	frame, err := qemu.Capture(spec.Name, filepath.Join(artifacts, label+".png"))
	if err != nil {
		result.Note("no %s screenshot: %v", label, err)
		return nil
	}
	if frame.Blank() {
		result.Note("the %s screenshot is blank (framebuffer deviation %.4f): nothing was painted", label, frame.StdDev)
	}
	return &frame
}

// assembleVideo encodes the captured frames into a WebM timelapse.
//
// Optional on purpose. ffmpeg is not something a boot gate should require, so
// its absence is a note in the result rather than a failed run — but where it
// is present, one file that shows the whole boot is worth more than two hundred
// PNGs nobody opens.
func assembleVideo(spec *Spec, artifacts string) (string, error) {
	if !spec.Screenshots.Video {
		return "", nil
	}
	ffmpeg, err := lookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("ffmpeg is not installed")
	}
	frames := filepath.Join(artifacts, "frames")
	entries, err := os.ReadDir(frames)
	if err != nil || len(entries) == 0 {
		return "", fmt.Errorf("no frames were captured")
	}
	video := filepath.Join(artifacts, "timelapse.webm")
	cmd := exec.Command(ffmpeg, "-y", "-loglevel", "error",
		"-framerate", "10", "-i", filepath.Join(frames, "f%06d.png"),
		// yuv420p needs even dimensions, and a guest framebuffer is not
		// guaranteed to have them.
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2",
		"-c:v", "libvpx-vp9", "-b:v", "1M", "-pix_fmt", "yuv420p", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg: %s", commandOutput(out, err))
	}
	return video, nil
}
