package qemu

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DiagnosticBundle collects evidence for a VM into dir. Mirrors the
// artifact layout of tuna-os/tunaos scripts/iso-e2e.sh and
// scripts/evidence-bundle.sh so that a corral VM can be used wherever
// that harness currently drives QEMU directly.
func DiagnosticBundle(name, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	meta, _ := readMetadata(name)
	// serial.log
	if data, err := SerialLog(name); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "serial.log"), data, 0o644)
	}
	// screenshot
	screenPath := filepath.Join(dir, "screenshot.png")
	if frame, err := Capture(name, screenPath); err == nil {
		summary, _ := json.MarshalIndent(frame, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "screenshot.json"), summary, 0o644)
	}
	// journal
	if entries, err := Events(name); err == nil {
		var lines []string
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf("%s [%s] %s", e.Time, e.Kind, e.Message))
		}
		_ = os.WriteFile(filepath.Join(dir, "journal.log"), []byte(strings.Join(lines, "\n")), 0o644)
	}
	// QMP status
	if qmp, err := QMPStatus(name); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "qmp-status.json"), qmp, 0o644)
	}
	// vm metadata
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "metadata.json"), metaBytes, 0o644)
	// vsock / tpm notes
	var notes []string
	if meta.Vsock {
		notes = append(notes, fmt.Sprintf("vsock: enabled cid=%d", meta.VsockCID))
		if ok, reason := VsockHostAvailable(); ok {
			notes = append(notes, "vsock host: available")
		} else {
			notes = append(notes, "vsock host: unavailable: "+reason)
		}
	} else {
		notes = append(notes, "vsock: disabled")
	}
	if meta.TPM {
		notes = append(notes, "tpm: enabled")
		if ok, reason := TPMHostAvailable(); ok {
			notes = append(notes, "tpm host: available")
		} else {
			notes = append(notes, "tpm host: unavailable: "+reason)
		}
	} else {
		notes = append(notes, "tpm: disabled")
	}
	notes = append(notes, fmt.Sprintf("firmware: %s", meta.Firmware))
	notes = append(notes, fmt.Sprintf("qemu: %s", qemuVersion()))
	_ = os.WriteFile(filepath.Join(dir, "diagnostics.txt"), []byte(strings.Join(notes, "\n")+"\n"), 0o644)
	return nil
}

// qemuVersion returns qemu --version first line.
func qemuVersion() string {
	for _, bin := range []string{"qemu-system-x86_64", "/usr/bin/qemu-system-x86_64"} {
		if path, err := exec.LookPath(bin); err == nil {
			if out, err := exec.Command(path, "--version").Output(); err == nil {
				if line := strings.Split(strings.TrimSpace(string(out)), "\n")[0]; line != "" {
					return line
				}
			}
		}
	}
	return "unknown"
}

// QMPStatus queries QMP query-status and returns JSON.
func QMPStatus(name string) ([]byte, error) {
	sockPath := filepath.Join(VMHome(), name, "qmp.sock")
	if _, err := os.Stat(sockPath); err != nil {
		return nil, fmt.Errorf("no QMP socket for %q — is it running?", name)
	}
	conn, reader, err := qmpDial(sockPath)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	raw, err := qmpExecute(conn, reader, "query-status", nil)
	if err != nil {
		return nil, err
	}
	out, _ := json.MarshalIndent(json.RawMessage(raw), "", "  ")
	return out, nil
}

// BootFailedOnSerial reports whether the serial log contains signatures of a
// failed boot (emergency shell, dracut, kernel panic). Mirrors
// tuna-os/tunaos scripts/iso-e2e.sh boot_failed_on_serial().
func BootFailedOnSerial(name string) (bool, string) {
	data, err := SerialLog(name)
	if err != nil {
		return false, ""
	}
	text := string(data)
	sigs := []string{
		"Entering emergency mode",
		"Dracut Emergency Shell",
		"Warning: Could not boot",
		"Kernel panic",
		"You are in emergency mode",
		"dracut: FATAL",
		"rdsosreport.txt",
	}
	for _, sig := range sigs {
		if strings.Contains(text, sig) {
			return true, sig
		}
	}
	return false, ""
}

// SerialTail returns last n lines of serial log for diagnostics.
func SerialTail(name string, n int) string {
	data, err := SerialLog(name)
	if err != nil {
		return fmt.Sprintf("no serial log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
