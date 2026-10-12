package qemu

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

// journalViaFake wires the journal seam to a scriptable shell.Fake — the
// seam-first order (shell tested in slice 3a) means these tests stand on a
// tested fake, and the exact journalctl argv is pinned by Fake matching.
func journalViaFake(t *testing.T, fake *shell.Fake) {
	t.Helper()
	SetJournalctl(func(args ...string) ([]byte, error) {
		return fake.Run("journalctl", args...)
	})
	t.Cleanup(func() { SetJournalctl(nil) })
}

func TestBackfill_Events_ViaShellFake(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("journalctl",
		[]string{"--user", "-u", "corral-dev", "-n", "50", "--no-pager", "-o", "short-iso"},
		strings.Join([]string{
			"2026-08-05T10:11:12+0000 host corral-vm[1]: Started corral-dev.",
			"2026-08-05T10:11:20+0000 host corral-vm[1]: qemu: could not open disk image",
		}, "\n")+"\n", nil)
	journalViaFake(t, fake)

	events, err := Events("dev")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	if events[0].Message != "Started corral-dev." {
		t.Errorf("message = %q", events[0].Message)
	}
	if events[1].Kind != "Warning" {
		t.Errorf("error line classed %q, want Warning", events[1].Kind)
	}

	// The fake recorded the exact invocation.
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "journalctl" {
		t.Fatalf("want one journalctl call, got %+v", calls)
	}
}

func TestBackfill_Events_ErrorPropagates_ViaShellFake(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponse("journalctl --user -u corral-dev -n 50 --no-pager -o short-iso",
		"", errors.New("journalctl failed: exit status 1"))
	journalViaFake(t, fake)

	if _, err := Events("dev"); err == nil || !strings.Contains(err.Error(), "reading the journal") {
		t.Fatalf("journal failure should propagate, got %v", err)
	}
}

func TestBackfill_Events_SkipsSeparators_ViaShellFake(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("journalctl",
		[]string{"--user", "-u", "corral-dev", "-n", "50", "--no-pager", "-o", "short-iso"},
		strings.Join([]string{
			"-- Boot 1234 --",
			"",
			"-- No entries --",
			"2026-08-05T10:11:12+0000 host corral-vm[1]: Started corral-dev.",
			"",
		}, "\n"), nil)
	journalViaFake(t, fake)

	events, err := Events("dev")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("separators and blanks are not events: %+v", events)
	}
}

func TestBackfill_SetJournalctl_NilRestores(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponse("journalctl --user -u corral-dev -n 50 --no-pager -o short-iso",
		"sentinel\n", nil)
	journalViaFake(t, fake)

	if events, err := Events("dev"); err != nil || len(events) != 1 || events[0].Message != "sentinel" {
		t.Fatalf("fake should serve sentinel: %+v %v", events, err)
	}
	// Restore the real reader: the sentinel must no longer be served.
	// (The real journalctl runs read-only; whatever it returns, it is not
	// the sentinel.)
	SetJournalctl(nil)
	out, err := journalRun("--user", "-u", "corral-dev", "-n", "50", "--no-pager", "-o", "short-iso")
	if string(out) == "sentinel\n" && err == nil {
		t.Fatal("nil should restore the real journal reader, sentinel still served")
	}
}

func TestBackfill_Capture_DefaultOutputName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	origWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	vmDir := filepath.Join(VMHome(), "capdef")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeQMPServer(t, filepath.Join(vmDir, "qmp.sock"))

	frame, err := Capture("capdef", "")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if frame.Path != "capdef-screenshot.png" {
		t.Errorf("default path = %q", frame.Path)
	}
	if info, err := os.Stat("capdef-screenshot.png"); err != nil || info.Size() == 0 {
		t.Errorf("expected default-named PNG: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vmDir, "screenshot.ppm")); !os.IsNotExist(err) {
		t.Errorf("screendump temp should be removed, stat err = %v", err)
	}
}

// serveQMPOnce answers one QMP session: greeting, capabilities, then a single
// canned reply to every execute command.
func serveQMPOnce(t *testing.T, sockPath, reply string, writeFile func(filename string)) {
	t.Helper()
	if len(sockPath) > 100 {
		t.Skipf("socket path too long (%d bytes)", len(sockPath))
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listening on fake QMP socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		greeting := map[string]any{"QMP": map[string]any{"version": map[string]any{}, "capabilities": []string{}}}
		gb, _ := json.Marshal(greeting)
		_, _ = conn.Write(append(gb, '\n'))
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req map[string]any
			if err := json.Unmarshal(line, &req); err != nil {
				continue
			}
			if req["execute"] == "qmp_capabilities" {
				_, _ = conn.Write([]byte(`{"return": {}}` + "\n"))
				continue
			}
			if writeFile != nil {
				if args, _ := req["arguments"].(map[string]any); args != nil {
					if fn, _ := args["filename"].(string); fn != "" {
						writeFile(fn)
					}
				}
			}
			_, _ = conn.Write([]byte(reply + "\n"))
		}
	}()
}

func TestBackfill_Capture_ScreendumpError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	vmDir := filepath.Join(VMHome(), "caperr")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serveQMPOnce(t, filepath.Join(vmDir, "qmp.sock"),
		`{"error": {"class": "GenericError", "desc": "device has no display"}}`, nil)

	if _, err := Capture("caperr", filepath.Join(tmp, "out.png")); err == nil ||
		!strings.Contains(err.Error(), "screendump") {
		t.Fatalf("screendump failure should surface, got %v", err)
	}
}

func TestBackfill_Capture_DecodeError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	vmDir := filepath.Join(VMHome(), "capbad")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serveQMPOnce(t, filepath.Join(vmDir, "qmp.sock"), `{"return": {}}`,
		func(fn string) { _ = os.WriteFile(fn, []byte("not a ppm at all"), 0o644) })

	if _, err := Capture("capbad", filepath.Join(tmp, "out.png")); err == nil ||
		!strings.Contains(err.Error(), "decoding") {
		t.Fatalf("garbage screendump should fail decoding, got %v", err)
	}
}

func TestBackfill_QMPStatus(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Missing socket errors before dialing.
	if _, err := QMPStatus("never-started"); err == nil || !strings.Contains(err.Error(), "QMP socket") {
		t.Fatalf("missing socket should error, got %v", err)
	}

	vmDir := filepath.Join(VMHome(), "qstat")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serveQMPOnce(t, filepath.Join(vmDir, "qmp.sock"), `{"return": {"status": "running"}}`, nil)

	raw, err := QMPStatus("qstat")
	if err != nil {
		t.Fatalf("QMPStatus: %v", err)
	}
	if !strings.Contains(string(raw), "running") {
		t.Errorf("status should mention running: %s", raw)
	}
}

func TestBackfill_SerialTail(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// No serial log yet: a helpful message, not an empty string.
	if got := SerialTail("never-started", 10); !strings.Contains(got, "no serial log") {
		t.Errorf("missing log should explain, got %q", got)
	}

	vmDir := filepath.Join(VMHome(), "tailed")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{"one", "two", "three", "four"}
	if err := os.WriteFile(filepath.Join(vmDir, "serial.log"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SerialTail("tailed", 2); got != "three\nfour" {
		t.Errorf("tail = %q, want %q", got, "three\\nfour")
	}
	if got := SerialTail("tailed", 10); got != strings.Join(lines, "\n") {
		t.Errorf("short log returns whole: %q", got)
	}
}
