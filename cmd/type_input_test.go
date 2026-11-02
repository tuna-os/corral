package cmd

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tuna-os/corral/pkg/rfb/rfbtest"
)

func typeSourceFlags(t *testing.T, file *string, powershell bool) {
	t.Helper()
	flag := typeCmd.Flags().Lookup("file")
	oldFile, oldPowerShell, oldChanged := typeFile, typePowerShell, flag.Changed
	typeFile, typePowerShell, flag.Changed = "", powershell, file != nil
	if file != nil {
		typeFile = *file
	}
	t.Cleanup(func() {
		typeFile, typePowerShell, flag.Changed = oldFile, oldPowerShell, oldChanged
	})
}

func typeStdin(t *testing.T, reader io.Reader) {
	t.Helper()
	old := typeCmd.InOrStdin()
	typeCmd.SetIn(reader)
	t.Cleanup(func() { typeCmd.SetIn(old) })
}

func TestTypeRequiresExactlyOneSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		file bool
		path string
		ok   bool
	}{
		{"text", []string{"web", "hello"}, false, "", true},
		{"empty text", []string{"web", ""}, false, "", true},
		{"file", []string{"web"}, true, "script.ps1", true},
		{"stdin", []string{"web"}, true, "-", true},
		{"missing source", []string{"web"}, false, "", false},
		{"missing vm", nil, false, "", false},
		{"missing vm with file", nil, true, "-", false},
		{"extra text", []string{"web", "one", "two"}, false, "", false},
		{"file and text", []string{"web", "secret"}, true, "-", false},
		{"empty file path", []string{"web"}, true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var file *string
			if tc.file {
				file = &tc.path
			}
			typeSourceFlags(t, file, false)
			if err := typeCmd.Args(typeCmd, tc.args); (err == nil) != tc.ok {
				t.Fatalf("argument validation = %v, want success %v", err, tc.ok)
			}
		})
	}
}

func TestTypeFileAndStdinOverVNC(t *testing.T) {
	for _, stdin := range []bool{false, true} {
		for _, enter := range []bool{false, true} {
			t.Run(fmt.Sprintf("stdin=%v/enter=%v", stdin, enter), func(t *testing.T) {
				server, _ := kubevirtConsole(t)
				consoleFlags(t, enter, "", false)
				withJSON(t)
				source := "Secret!1\nsecond 'line'"
				path := filepath.Join(t.TempDir(), "input.txt")
				if stdin {
					path = "-"
					typeStdin(t, strings.NewReader(source))
				} else if err := os.WriteFile(path, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				typeSourceFlags(t, &path, false)
				out := captureStdout(t, func() {
					if err := typeCmd.RunE(typeCmd, []string{"web"}); err != nil {
						t.Fatal(err)
					}
				})
				want := source
				if enter {
					want += "\n"
				}
				if got := rfbtest.Typed(server.Events()); got != want {
					t.Fatalf("typed %q, want %q", got, want)
				}
				if server.Sessions() != 1 {
					t.Errorf("sessions = %d, want one", server.Sessions())
				}
				got := decodeJSON(t, out)
				if len(got) != 4 || got["vm"] != "web" || got["action"] != "type" || got["characters"] != float64(len(source)) || got["enter"] != enter {
					t.Errorf("--json = %v", got)
				}
				if strings.Contains(out, "Secret") || strings.Contains(out, path) {
					t.Errorf("output disclosed source: %s", out)
				}
			})
		}
	}
}

type failedTypeReader struct{}

func (failedTypeReader) Read(p []byte) (int, error) {
	return copy(p, "partial secret"), errors.New("input read failed")
}

func TestTypePreflightSendsNoKeys(t *testing.T) {
	for _, tc := range []struct {
		name       string
		source     string
		file       bool
		powershell bool
		missing    bool
		readError  bool
		stdin      bool
		want       string
	}{
		{name: "invalid positional UTF8", source: "valid prefix\xff", want: "valid UTF-8"},
		{name: "invalid file UTF8", source: "valid prefix\xff", file: true, want: "valid UTF-8"},
		{name: "invalid stdin UTF8", source: "valid prefix\xff", stdin: true, want: "valid UTF-8"},
		{name: "invalid PowerShell file UTF8", source: "valid prefix\xff", file: true, powershell: true, want: "valid UTF-8"},
		{name: "invalid PowerShell stdin UTF8", source: "valid prefix\xff", stdin: true, powershell: true, want: "valid UTF-8"},
		{name: "invalid PowerShell UTF8", source: "\ufeffvalid prefix\xff", powershell: true, want: "valid UTF-8"},
		{name: "ordinary unsupported character", source: "valid prefix\U0001f41f", want: "no US-keyboard mapping"},
		{name: "ordinary BOM is not stripped", source: "\ufeffhello", file: true, want: "no US-keyboard mapping"},
		{name: "missing file", file: true, missing: true, want: "read type input"},
		{name: "stdin read failure", readError: true, want: "input read failed"},
		{name: "empty PowerShell source", powershell: true, want: "must not be empty"},
		{name: "BOM-only PowerShell source", source: "\ufeff", powershell: true, want: "must not be empty"},
		{name: "oversized launcher", source: strings.Repeat("a", powerShellCommandLimit), powershell: true, want: "command-line limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := kubevirtConsole(t)
			consoleFlags(t, true, "", false)
			var file *string
			args := []string{"web", tc.source}
			if tc.file {
				path := filepath.Join(t.TempDir(), "input.txt")
				file = &path
				if !tc.missing {
					if err := os.WriteFile(path, []byte(tc.source), 0600); err != nil {
						t.Fatal(err)
					}
				}
				args = args[:1]
			} else if tc.readError || tc.stdin {
				path := "-"
				file = &path
				if tc.readError {
					typeStdin(t, failedTypeReader{})
				} else {
					typeStdin(t, strings.NewReader(tc.source))
				}
				args = args[:1]
			}
			typeSourceFlags(t, file, tc.powershell)
			out := captureStdout(t, func() {
				err := typeCmd.RunE(typeCmd, args)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("type error = %v, want %q", err, tc.want)
				}
			})
			if out != "" || server.Sessions() != 0 || len(server.Events()) != 0 {
				t.Errorf("failed preflight produced output %q, %d sessions, events %v", out, server.Sessions(), server.Events())
			}
		})
	}
}

func TestTypePowerShellStdinOverVNC(t *testing.T) {
	server, _ := kubevirtConsole(t)
	consoleFlags(t, false, "", false)
	withJSON(t)
	path := "-"
	typeSourceFlags(t, &path, true)
	typeStdin(t, strings.NewReader("\ufeffA\U0001f41f\n"))
	out := captureStdout(t, func() {
		if err := typeCmd.RunE(typeCmd, []string{"web"}); err != nil {
			t.Fatal(err)
		}
	})
	// Independent UTF-16LE vector, with neither a BOM nor an appended Return.
	want := powerShellPrefix + base64.StdEncoding.EncodeToString([]byte{0x41, 0, 0x3d, 0xd8, 0x1f, 0xdc, 0x0a, 0})
	if typed := rfbtest.Typed(server.Events()); typed != want {
		t.Fatalf("typed %q, want %q", typed, want)
	}
	got := decodeJSON(t, out)
	if len(got) != 4 || got["characters"] != float64(3) || got["enter"] != false {
		t.Errorf("--json = %v", got)
	}
	if strings.Contains(out, strings.TrimPrefix(want, powerShellPrefix)) {
		t.Errorf("--json disclosed encoded payload: %s", out)
	}
}

func TestTypePowerShellEncodingAndLimit(t *testing.T) {
	typeSourceFlags(t, nil, true)
	// UTF-16LE: A, a surrogate pair D83D DC1F, then LF; no byte-order mark.
	launcher, characters, err := prepareTypeInput(typeCmd, []string{"web", "\ufeffA\U0001f41f\n"})
	if err != nil {
		t.Fatal(err)
	}
	want := powerShellPrefix + base64.StdEncoding.EncodeToString([]byte{0x41, 0, 0x3d, 0xd8, 0x1f, 0xdc, 0x0a, 0})
	if launcher != want || characters != 3 {
		t.Fatalf("launcher = %q, characters = %d; want %q, 3", launcher, characters, want)
	}
	maxUnits := ((powerShellCommandLimit - len(powerShellPrefix)) / 4 * 3) / 2
	for _, supplementary := range []bool{false, true} {
		source := strings.Repeat("a", maxUnits)
		if supplementary {
			source = strings.Repeat("a", maxUnits-2) + "\U0001f41f"
		}
		launcher, characters, err = prepareTypeInput(typeCmd, []string{"web", source})
		if err != nil || len(launcher) > powerShellCommandLimit || characters != utf8.RuneCountInString(source) {
			t.Fatalf("largest fitting source: launcher length %d, characters %d, error %v", len(launcher), characters, err)
		}
		if _, _, err := prepareTypeInput(typeCmd, []string{"web", source + "a"}); err == nil {
			t.Fatal("accepted source one UTF-16 unit beyond the launcher bound")
		}
	}
}

func TestTypePowerShellPlainOutputIsSilent(t *testing.T) {
	server, _ := kubevirtConsole(t)
	consoleFlags(t, false, "", false)
	typeSourceFlags(t, nil, true)
	old := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = old })
	out := captureStdout(t, func() {
		if err := typeCmd.RunE(typeCmd, []string{"web", "Write-Output 'private \U0001f41f'"}); err != nil {
			t.Fatal(err)
		}
	})
	typed := rfbtest.Typed(server.Events())
	if out != "" || !strings.HasPrefix(typed, powerShellPrefix) || strings.HasSuffix(typed, "\n") {
		t.Fatalf("output %q, typed %q; want silent launcher without Return", out, typed)
	}
}

// The RFB server captures keyboard delivery, not a real Windows desktop. A
// separate real PowerShell process consumes exactly that launcher payload and
// must create its own artifact; decoding our own encoder would miss semantics.
func TestTypePowerShellRealConsumer(t *testing.T) {
	var executable string
	for _, name := range []string{"powershell.exe", "pwsh"} {
		if path, err := exec.LookPath(name); err == nil {
			executable = path
			break
		}
	}
	if executable == "" {
		t.Skip("real PowerShell consumer requires powershell.exe or pwsh on PATH")
	}
	server, _ := kubevirtConsole(t)
	consoleFlags(t, true, "", false)
	withJSON(t)
	artifact := filepath.Join(t.TempDir(), "consumer 'result'.txt")
	want := "quotes ' and \"; caf\u00e9 \U0001f41f\nsecond line"
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	source := "# Unicode \U0001f41f and CRLF source\r\n" +
		"$value = " + quote(want) + "\r\n" +
		"[System.IO.File]::WriteAllText(" + quote(artifact) + ", $value, [System.Text.UTF8Encoding]::new($false))\r\n"
	path := filepath.Join(t.TempDir(), "source.ps1")
	if err := os.WriteFile(path, []byte("\ufeff"+source), 0600); err != nil {
		t.Fatal(err)
	}
	typeSourceFlags(t, &path, true)
	out := captureStdout(t, func() {
		if err := typeCmd.RunE(typeCmd, []string{"web"}); err != nil {
			t.Fatal(err)
		}
	})
	typed := rfbtest.Typed(server.Events())
	if !strings.HasPrefix(typed, powerShellPrefix) || !strings.HasSuffix(typed, "\n") || strings.Count(typed, "\n") != 1 {
		t.Fatalf("expected one launcher and explicit Return, got %q", typed)
	}
	fields := strings.Fields(strings.TrimSuffix(typed, "\n"))
	if len(fields) != 5 || fields[0] != "powershell.exe" || fields[1] != "-NoLogo" || fields[2] != "-NoProfile" || fields[3] != "-EncodedCommand" {
		t.Fatalf("unexpected launcher fields: %v", fields)
	}
	got := decodeJSON(t, out)
	if len(got) != 4 || got["characters"] != float64(utf8.RuneCountInString(source)) || got["enter"] != true {
		t.Errorf("--json = %v", got)
	}
	if strings.Contains(out, source) || strings.Contains(out, fields[4]) || strings.Contains(out, path) {
		t.Errorf("--json disclosed source or payload: %s", out)
	}
	if result, err := exec.Command(executable, fields[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("real PowerShell consumer: %v\n%s", err, result)
	}
	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("PowerShell did not create its artifact: %v", err)
	}
	if string(data) != want {
		t.Errorf("PowerShell artifact = %q, want %q", data, want)
	}
}
