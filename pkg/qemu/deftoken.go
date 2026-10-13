package qemu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefinitionToken is the admission/start identity that binds what was
// validated to what is started.
//
// EnsureVM/checkExistingVM admit a definition outside the lifecycle lock, and
// StartAndWaitCtx starts later under it. Neither start path revalidated the
// admitted snapshot, so a legitimate force-create landing between validation
// and start made the invocation start the replacement image while claiming
// the original manifest was verified. A per-operation lock alone cannot
// preserve admission identity across separate calls.
//
// The token closes that gap. AdmitDefinition captures the admitted
// definition's identity — digests of the exact metadata, command-record and
// unit-file bytes the admission validated — atomically with that validation
// under the lifecycle lock. StartOwnedTokenAdmitted compares the token at
// actual start, still under the lock, before consulting liveness: anything
// that changed since admission (replaced metadata, replaced command,
// replaced or appeared/vanished unit file) refuses, including on the
// already-running reuse path, which never validated at all before.
//
// The token pins the DEFINITION (what would run), never liveness: a start
// timestamp or pid would refuse every legitimate concurrent reuse. Running
// state stays pinned by StartToken. Firmware bytes are covered twice —
// their digest lives inside the pinned metadata, and the start-time
// re-validation re-hashes them. The disk image is runtime state (the guest
// writes it), not definition, and is deliberately excluded.
//
// Backend switches refuse in the shared start core, so every entry —
// launcher, CLI, adapter — gets them: the systemd branch refuses beside a
// live or unverifiable direct child (a manager that became available must
// not stack a unit on an existing direct QEMU), and the direct branch
// refuses beside a positively active unit (probe disagreement must not
// stack a direct QEMU on a live unit). Refusal, never a second launch.
type DefinitionToken struct {
	// MetaSHA/CmdSHA/UnitSHA are the hex SHA256 of the exact
	// metadata.json, command.json and unit-file bytes at admission.
	MetaSHA string
	CmdSHA  string
	UnitSHA string
	// UnitPresent records whether a unit file existed at admission. A
	// direct-only VM has none; a unit that appears or vanishes
	// afterwards is a changed definition.
	UnitPresent bool
}

// AdmitDefinition captures name's admitted definition identity. Under one
// lifecycle-lock hold it reads the metadata once, runs the caller's
// manifest binding against those exact bytes, re-validates the current
// definition (validity plus loaded-unit/drop-in proof), and digests the
// snapshot. A force-create racing admission blocks on the lock and lands
// either fully before (admitted as the current definition) or fully after
// (refused at start as changed) — never between validation and capture.
//
// bind checks the manifest dimension (creation source, arch, firmware,
// transport) on the bytes it receives; it must check what it is given,
// never re-read. A nil bind refuses: validity without a manifest binding
// is not an admission.
func AdmitDefinition(name string, bind func(metadata []byte) error) (DefinitionToken, error) {
	return AdmitDefinitionCtx(context.Background(), name, bind)
}

// AdmitDefinitionCtx is AdmitDefinition with caller-bounded lifecycle-lock
// admission. Existing callers retain the historical bounded wait through
// AdmitDefinition.
func AdmitDefinitionCtx(ctx context.Context, name string, bind func(metadata []byte) error) (DefinitionToken, error) {
	var tok DefinitionToken
	if ctx == nil {
		ctx = context.Background()
	}
	if bind == nil {
		return tok, fmt.Errorf("VM %q not admitted: no manifest binding was supplied", name)
	}
	unlock, err := lockLifecycleCtx(ctx, name)
	if err != nil {
		return tok, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return tok, fmt.Errorf("VM %q admission canceled: %w", name, err)
	}
	metaBytes, err := os.ReadFile(filepath.Join(VMHome(), name, "metadata.json"))
	if err != nil {
		return tok, fmt.Errorf("VM %q not admitted: its metadata cannot be read: %w", name, err)
	}
	if err := bind(metaBytes); err != nil {
		return tok, fmt.Errorf("VM %q not admitted: %v", name, err)
	}
	if err := ValidateCommand(name); err != nil {
		return tok, fmt.Errorf("VM %q not admitted: %v", name, err)
	}
	cmdBytes, err := os.ReadFile(filepath.Join(VMHome(), name, commandFile))
	if err != nil {
		return tok, fmt.Errorf("VM %q not admitted: its recorded QEMU command cannot be read: %w", name, err)
	}
	tok = DefinitionToken{
		MetaSHA: sha256Hex(metaBytes),
		CmdSHA:  sha256Hex(cmdBytes),
	}
	unitBytes, err := os.ReadFile(filepath.Join(systemdUserDir(), "corral-"+name+".service"))
	if err != nil {
		if !os.IsNotExist(err) {
			return DefinitionToken{}, fmt.Errorf("VM %q not admitted: its systemd unit cannot be read: %w", name, err)
		}
		return tok, nil
	}
	tok.UnitSHA, tok.UnitPresent = sha256Hex(unitBytes), true
	return tok, nil
}

// Verify proves the live definition still equals the admitted one: the same
// metadata, command record and unit file, byte for byte. It takes no lock;
// the admitted start calls it under the lifecycle lock so the
// compare → validate → start sequence is atomic. A zero token admits
// nothing: every digest comparison fails against any real definition.
func (t DefinitionToken) Verify(name string) error {
	metaBytes, err := os.ReadFile(filepath.Join(VMHome(), name, "metadata.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("VM %q was removed after admission (its metadata is gone) — refusing to start what this invocation did not verify (re-run so admission sees the current definition)", name)
		}
		return fmt.Errorf("VM %q cannot be re-verified after admission (its metadata is unreadable: %v) — refusing to start an unverifiable VM", name, err)
	}
	if sha256Hex(metaBytes) != t.MetaSHA {
		return fmt.Errorf("VM %q changed since admission (its metadata no longer matches the admitted definition) — refusing to start what this invocation did not verify (re-run so admission sees the current definition)", name)
	}
	cmdBytes, err := os.ReadFile(filepath.Join(VMHome(), name, commandFile))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("VM %q was removed after admission (its recorded QEMU command is gone) — refusing to start what this invocation did not verify (re-run so admission sees the current definition)", name)
		}
		return fmt.Errorf("VM %q cannot be re-verified after admission (its recorded QEMU command is unreadable: %v) — refusing to start an unverifiable VM", name, err)
	}
	if sha256Hex(cmdBytes) != t.CmdSHA {
		return fmt.Errorf("VM %q changed since admission (its recorded QEMU command no longer matches the admitted definition) — refusing to start what this invocation did not verify (re-run so admission sees the current definition)", name)
	}
	unitBytes, err := os.ReadFile(filepath.Join(systemdUserDir(), "corral-"+name+".service"))
	switch {
	case err == nil && !t.UnitPresent:
		return fmt.Errorf("VM %q changed since admission (a systemd unit file appeared after admission) — refusing to start what this invocation did not verify (re-run so admission sees the current definition)", name)
	case err == nil && sha256Hex(unitBytes) != t.UnitSHA:
		return fmt.Errorf("VM %q changed since admission (its systemd unit no longer matches the admitted definition) — refusing to start what this invocation did not verify (re-run so admission sees the current definition)", name)
	case err == nil:
		return nil
	case !os.IsNotExist(err):
		return fmt.Errorf("VM %q cannot be re-verified after admission (its systemd unit is unreadable: %v) — refusing to start an unverifiable VM", name, err)
	case t.UnitPresent:
		return fmt.Errorf("VM %q changed since admission (its systemd unit file was removed after admission) — refusing to start what this invocation did not verify (re-run so admission sees the current definition)", name)
	default:
		return nil
	}
}

// StartOwnedTokenAdmitted starts like StartOwnedToken, but only the admitted
// definition: the token comparison and the current-definition validation
// (validity plus loaded-unit/drop-in proof) run under the lifecycle lock
// before liveness is consulted, so a replacement — running or not —
// refuses instead of starting or being silently reused. A VM this call did
// not start reports started=false with an invalid token, exactly like the
// plain entry.
func StartOwnedTokenAdmitted(name string, admitted DefinitionToken) (StartToken, bool, error) {
	return StartOwnedTokenAdmittedCtx(context.Background(), name, admitted)
}

// StartOwnedTokenAdmittedCtx is StartOwnedTokenAdmitted with caller-bounded
// lifecycle-lock admission. A canceled wait returns without validating or
// starting a VM.
func StartOwnedTokenAdmittedCtx(ctx context.Context, name string, admitted DefinitionToken) (StartToken, bool, error) {
	var tok StartToken
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ValidateVMName(name); err != nil {
		return tok, false, err
	}
	unlock, err := lockLifecycleCtx(ctx, name)
	if err != nil {
		return tok, false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return tok, false, fmt.Errorf("VM %q lifecycle admission canceled: %w", name, err)
	}
	if err := admitted.Verify(name); err != nil {
		return tok, false, err
	}
	if err := ValidateCommand(name); err != nil {
		return tok, false, err
	}
	return startOwnedTokenLocked(name)
}

// refuseDirectOwnerForUnit refuses a systemd start beside a direct QEMU:
// an owned or unverifiable direct child already holds the disk, and a unit
// started now would run a second QEMU on it. Absent or alien records
// proceed — a dead or foreign pid file is the direct path's to overwrite,
// and stopping it is not this start's job. This mirrors the owned unit
// stop's treatment (stopOwnedUnitLocked): the same states refuse there.
func refuseDirectOwnerForUnit(name string) error {
	switch _, _, st := directState(name); st {
	case pidOwned, pidUnknown:
		return fmt.Errorf("VM %q has a direct QEMU child (state %s) alongside its systemd unit — refusing to start a second QEMU on the same disk (stop the direct child first: corral stop %s)", name, st, name)
	case pidAbsent, pidAlien:
		return nil
	}
	return nil
}

// positivelyActiveUnitStates are the is-active answers that prove a unit is
// live: active plus every transitional state. Anything else — stopped
// states, unknown, unparsable output, a query failure — is not proof, and
// on a direct-only host the query failure is the expected case.
var positivelyActiveUnitStates = map[string]bool{
	"active":       true,
	"activating":   true,
	"deactivating": true,
	"reload":       true,
	"reloading":    true,
}

// refuseActiveUnitForDirect refuses a direct start beside a live unit. Where
// no unit file exists there is nothing to ask — the manager cannot be
// running a unit that was never installed. Where one exists, a positively
// active answer refuses; a failed or negative query proceeds, because on a
// direct-only host the failure is the ordinary case (no bus to answer).
// The check is text-based, not error-based: only the manager positively
// reporting a live state refuses.
func refuseActiveUnitForDirect(name string) error {
	unitPath := filepath.Join(systemdUserDir(), "corral-"+name+".service")
	if _, err := os.Stat(unitPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("VM %q cannot prove its unit file absent (%v) — refusing to start directly over an unverifiable unit", name, err)
	}
	out, _ := systemctlRun(context.Background(), "is-active", "corral-"+name)
	if positivelyActiveUnitStates[strings.TrimSpace(string(out))] {
		return fmt.Errorf("VM %q reports its systemd unit %q while this host starts QEMU directly — refusing to start a second QEMU on the same disk (resolve the unit activation before starting directly)", name, strings.TrimSpace(string(out)))
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
