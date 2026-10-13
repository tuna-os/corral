package qemu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StartToken captures the exact running state one start call owns. A bare
// "started" boolean cannot survive a replacement: another actor may stop and
// restart the VM between our start and our failure cleanup, and a stop by
// name would then kill somebody else's VM. The token binds the cleanup stop
// to the captured identity — direct child (pid + lifetime token + record
// bytes) or systemd activation (unit + main PID + activation timestamp) —
// and StopOwned refuses anything that no longer matches.
//
// A token is valid only when its start reported started=true. Callers must
// treat an unresolved token (Resolved=false) as unowned: nothing proven,
// nothing stoppable.
type StartToken struct {
	// Direct selects the identity half: direct child versus systemd unit.
	Direct bool
	// PID/Start/Raw pin the direct child: its pid, its lifetime token,
	// and the pid file's exact bytes as captured at start.
	PID   int
	Start string
	Raw   []byte
	// Unit/MainPID/ActiveSince pin the systemd activation: the unit name,
	// its main PID, and its activation timestamp. The timestamp changes on
	// every activation; the PID disambiguates two activations within the
	// timestamp's one-second granularity.
	Unit        string
	MainPID     string
	ActiveSince string
	// Resolved reports whether the token was captured completely. An
	// unresolved token refuses owned stops.
	Resolved bool
}

// StartOwnedToken starts a QEMU VM like StartOwned and additionally captures
// the ownership token for the running state this call started. When this
// call did not start anything (already running) the token is invalid and
// must not be used. The whole check → start → capture sequence holds the
// lifecycle lock for name, so concurrent starts serialize instead of racing.
func StartOwnedToken(name string) (StartToken, bool, error) {
	var tok StartToken
	if err := ValidateVMName(name); err != nil {
		return tok, false, err
	}
	unlock, err := lockLifecycle(name)
	if err != nil {
		return tok, false, err
	}
	defer unlock()
	return startOwnedTokenLocked(name)
}

// StartOwnedTokenCtx is StartOwnedToken with the caller's context bounding
// the lifecycle-lock wait: a cancelled caller fails the wait instead of
// queueing past it. In-operation subcalls keep their own default bounds
// (see bounded.go) — the context governs admission to the critical
// section, not each bounded step inside it.
func StartOwnedTokenCtx(ctx context.Context, name string) (StartToken, bool, error) {
	var tok StartToken
	if err := ValidateVMName(name); err != nil {
		return tok, false, err
	}
	unlock, err := lockLifecycleCtx(ctx, name)
	if err != nil {
		return tok, false, err
	}
	defer unlock()
	return startOwnedTokenLocked(name)
}

// startOwnedTokenLocked is StartOwnedToken under the caller's lifecycle lock.
func startOwnedTokenLocked(name string) (StartToken, bool, error) {
	var tok StartToken
	// TPM daemon must be up before QEMU tries to connect to its socket.
	if meta, err := readMetadata(name); err == nil && meta.TPM {
		if err := EnsureTPMDir(name, true); err != nil {
			return tok, false, err
		}
		if err := StartSwtpm(name); err != nil {
			return tok, false, err
		}
	}
	if !userSystemd() {
		// No unit can run here, so the VM's own state directory is what says
		// whether it exists.
		if !Exists(name) {
			return tok, false, fmt.Errorf("VM %q does not exist", name)
		}
		// The token is captured at launch, inside startDirectLocked: it
		// pins what that call wrote and verified, never a re-read of the
		// record after the fact. A launch whose record cannot be
		// verified fails here with its child reaped — started=true with
		// an unverifiable record cannot happen on this path.
		return startDirectLocked(name)
	}
	svc := "corral-" + name
	unitPath := filepath.Join(systemdUserDir(), svc+".service")
	if _, err := os.Stat(unitPath); err != nil {
		return tok, false, fmt.Errorf("VM %q does not exist", name)
	}
	// A direct child already holds the disk: starting the unit now would
	// run a second QEMU on it (see deftoken.go). Refuse, never double-start.
	if err := refuseDirectOwnerForUnit(name); err != nil {
		return tok, false, err
	}

	out, _ := systemctlRun(context.Background(), "is-active", svc)
	if strings.TrimSpace(string(out)) == "active" {
		fmt.Fprintf(os.Stderr, "VM %q is already running.\n", name)
		return tok, false, nil
	}

	if out, err := systemctlRun(context.Background(), "start", svc); err != nil {
		return tok, false, fmt.Errorf("starting VM: %w: %s", err, strings.TrimSpace(string(out)))
	}

	fmt.Fprintf(os.Stderr, "VM %q started.\n", name)

	// Show VNC info
	if meta, err := readMetadata(name); err == nil {
		fmt.Fprintf(os.Stderr, "  VNC: vnc://%s:%d\n", meta.Tailscale, meta.VncPort)
	}
	// systemctl start waits for the start job, so the unit is active now
	// and its activation identity is readable. If it is not, the start
	// reports started=true WITH an error and an unresolved token: the
	// unit may be running, but nothing is proven owned — owned cleanup
	// refuses loudly instead of stopping by name, and the caller must
	// propagate that uncertainty rather than treating this as a clean
	// reusable admission.
	if mainPID, activeSince, ok := unitActivation(svc); ok {
		tok = StartToken{Unit: svc, MainPID: mainPID, ActiveSince: activeSince, Resolved: true}
		return tok, true, nil
	}
	return tok, true, fmt.Errorf("VM %q started but its activation cannot be verified — refusing to claim ownership of an unverifiable unit (stop it by name explicitly if it is yours: corral stop %s)", name, name)
}

// unitActivation captures the identity of the unit's current activation:
// its main PID plus its activation timestamp. It reports ok=false when the
// activation cannot be read or the unit has none (never activated, still
// activating, or failed) — uncertainty, never an identity.
func unitActivation(svc string) (mainPID, activeSince string, ok bool) {
	out, err := systemctlRun(context.Background(), "show", "-p", "MainPID", "-p", "ActiveEnterTimestamp", svc)
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, found := strings.CutPrefix(line, "MainPID="); found {
			mainPID = strings.TrimSpace(v)
		}
		if v, found := strings.CutPrefix(line, "ActiveEnterTimestamp="); found {
			activeSince = strings.TrimSpace(v)
		}
	}
	if mainPID == "" || mainPID == "0" || activeSince == "" || activeSince == "n/a" {
		return "", "", false
	}
	return mainPID, activeSince, true
}

// StopOwned stops the running state tok owns, and nothing else: the current
// pid record (direct) or unit activation (systemd) must still equal the
// token, or the stop is refused and the stranger is left running. Automatic
// failure cleanup uses this; an explicit user stop uses Stop (by name).
func StopOwned(name string, tok StartToken) error {
	if err := ValidateVMName(name); err != nil {
		return err
	}
	unlock, err := lockLifecycle(name)
	if err != nil {
		return err
	}
	defer unlock()
	return stopOwnedLocked(name, tok)
}

// StopOwnedCtx is StopOwned with the caller's context bounding the
// lifecycle-lock wait: a cancelled caller fails the wait instead of
// queueing past it. In-operation subcalls keep their own default bounds
// (see bounded.go).
func StopOwnedCtx(ctx context.Context, name string, tok StartToken) error {
	if err := ValidateVMName(name); err != nil {
		return err
	}
	unlock, err := lockLifecycleCtx(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	return stopOwnedLocked(name, tok)
}

// stopOwnedLocked is StopOwned under the caller's lifecycle lock.
func stopOwnedLocked(name string, tok StartToken) error {
	if !tok.Resolved {
		return fmt.Errorf("VM %q: owned stop refused — the start token was never resolved, so nothing is proven owned (stop it by name explicitly if it is yours: corral stop %s)", name, name)
	}
	if tok.Direct {
		return stopOwnedDirectLocked(name, tok)
	}
	return stopOwnedUnitLocked(name, tok)
}

// stopOwnedDirectLocked stops the direct child tok pins. Every success path
// preserves the ownership-record discipline: the pid file is removed only
// when it still holds the token's bytes.
func stopOwnedDirectLocked(name string, tok StartToken) error {
	path := filepath.Join(VMHome(), name, pidFile)
	cur, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("VM %q: owned stop refused — the pid record cannot be read (%v); the child is unverifiable (pid file kept)", name, err)
		}
		// Our record is gone: whoever removed it did so before this lock.
		// Prove the token child dead before agreeing — a live token child
		// with a deleted record is still ours to stop.
		return stopOwnedDirectMissingLocked(name, path, tok)
	}
	if !bytes.Equal(cur, tok.Raw) {
		return fmt.Errorf("VM %q: ownership changed since this run started it (pid record replaced) — refusing to stop another invocation's VM (stop it by name explicitly if it is yours: corral stop %s)", name, name)
	}
	var recorded pidFileData
	if err := json.Unmarshal(cur, &recorded); err != nil {
		return fmt.Errorf("VM %q: owned stop refused — the pid record became unreadable; the child is unverifiable (pid file kept)", name)
	}
	launchBinary := recorded.Binary
	if launchBinary == "" {
		if cmd, err := readLaunchCommand(name); err == nil {
			launchBinary = cmd.Binary
		}
	}
	// The lifetime handle is acquired before verification (see
	// pidhandle.go) and retained across the escalation: signals go
	// through the pinned process, never a re-looked-up pid. Failure to
	// acquire refuses — there is nothing proven-safe to signal.
	h, err := procAcquire(recorded.PID)
	if err != nil {
		return fmt.Errorf("VM %q (pid %d): cannot bind a signal target for the recorded process (%v) — refusing to stop an unverified process (pid file kept)", name, recorded.PID, err)
	}
	defer h.Release()
	pinned := func() pidState { return classifyPID(recorded.PID, recorded, launchBinary) }
	switch pinned() {
	case pidAbsent, pidAlien:
		// Died on its own; its record is stale.
		removePIDRecord(path, tok.Raw)
		_ = StopSwtpm(name)
		return nil
	case pidUnknown:
		return fmt.Errorf("VM %q (pid %d) can no longer be proven to be this run's QEMU — refusing to stop an unverified process (pid file kept)", name, recorded.PID)
	case pidOwned:
	}
	if err := stopPinnedLocked(name, path, recorded.PID, pinned, h.Signal, tok.Raw); err != nil {
		return err
	}
	_ = StopSwtpm(name)
	return nil
}

// stopOwnedDirectMissingLocked settles an owned stop whose pid record is
// already gone: only a provably dead (or provably replaced-away) token
// child agrees silently; a live token child is stopped pinned to the token.
func stopOwnedDirectMissingLocked(name, path string, tok StartToken) error {
	launchBinary := ""
	if cmd, err := readLaunchCommand(name); err == nil {
		launchBinary = cmd.Binary
	}
	recorded := pidFileData{Version: 2, PID: tok.PID, Start: tok.Start}
	if raw := tok.Raw; len(raw) > 0 {
		// The token's own bytes, never a fresh name lookup.
		_ = json.Unmarshal(raw, &recorded)
		recorded.PID, recorded.Start = tok.PID, tok.Start
	}
	// The lifetime handle is acquired before verification (see
	// pidhandle.go) and retained across the escalation. Failure to
	// acquire refuses — there is nothing proven-safe to signal.
	h, err := procAcquire(tok.PID)
	if err != nil {
		return fmt.Errorf("VM %q (pid %d): owned stop refused — cannot bind a signal target for the token process (%v)", name, tok.PID, err)
	}
	defer h.Release()
	switch classifyPID(tok.PID, recorded, launchBinary) {
	case pidAbsent, pidAlien:
		// Ours is dead (alien proves the current occupant is not ours).
		// The TPM daemon is left alone: without the record its ownership
		// is unverifiable, and a replacement VM may own it now.
		return nil
	case pidUnknown:
		return fmt.Errorf("VM %q (pid %d): owned stop refused — the pid record is gone and the process can no longer be proven to be this run's QEMU (inspect pid %d before stopping anything by hand)", name, tok.PID, tok.PID)
	case pidOwned:
	}
	pinned := func() pidState { return classifyPID(tok.PID, recorded, launchBinary) }
	// wantRaw matches nothing on disk (the record is gone), so the
	// escalation removes nothing — it only signals the proven child.
	return stopPinnedLocked(name, path, tok.PID, pinned, h.Signal, tok.Raw)
}

// stopOwnedUnitLocked stops the systemd activation tok pins. A direct child
// alongside the unit refuses the stop: the two would share one disk, and
// stopping the unit while an unknown direct QEMU holds it resolves nothing.
func stopOwnedUnitLocked(name string, tok StartToken) error {
	svc := "corral-" + name
	if tok.Unit != svc {
		return fmt.Errorf("VM %q: ownership changed since this run started it (token is for %q) — refusing to stop another invocation's VM", name, tok.Unit)
	}
	switch _, _, st := directState(name); st {
	case pidOwned, pidUnknown:
		return fmt.Errorf("VM %q: owned stop refused — a direct QEMU child (state %s) exists alongside the systemd unit; resolve it explicitly first (corral stop %s may itself refuse until the direct child is resolved)", name, st, name)
	case pidAbsent, pidAlien:
	}
	mainPID, activeSince, ok := unitActivation(svc)
	if !ok {
		return fmt.Errorf("VM %q: owned stop refused — the unit's current activation cannot be read, so nothing is proven owned (stop it by name explicitly if it is yours: corral stop %s)", name, name)
	}
	if mainPID != tok.MainPID || activeSince != tok.ActiveSince {
		return fmt.Errorf("VM %q: ownership changed since this run started it (unit re-activated) — refusing to stop another invocation's VM (stop it by name explicitly if it is yours: corral stop %s)", name, name)
	}
	if out, err := systemctlRun(context.Background(), "stop", svc); err != nil {
		return fmt.Errorf("stopping VM: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_ = StopSwtpm(name)
	fmt.Fprintf(os.Stderr, "VM %q stopped.\n", name)
	return nil
}
