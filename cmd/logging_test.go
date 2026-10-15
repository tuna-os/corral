package cmd

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/logging"
)

func resetLogging(t *testing.T) {
	t.Helper()
	old, oldOpts := slog.Default(), logOptions
	t.Cleanup(func() {
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
		slog.SetDefault(old)
		logOptions = oldOpts
	})
	logOptions = logging.Options{}
	t.Setenv(logging.EnvLevel, "")
	t.Setenv(logging.EnvFormat, "")
	t.Setenv(logging.EnvFile, "")
}

// The TUI draws on the terminal, so with no log file named it must still log
// to a file, never to stderr.
func TestConfigureLoggingTUIWritesToFile(t *testing.T) {
	resetLogging(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	if err := configureLogging(true); err != nil {
		t.Fatal(err)
	}
	slog.Warn("tui record")
	data, err := os.ReadFile(logging.DefaultTUIFile())
	if err != nil {
		t.Fatalf("TUI log file: %v", err)
	}
	if !strings.Contains(string(data), "tui record") {
		t.Errorf("TUI log file = %q, want the record", data)
	}
}

// Flags win over the environment, and the environment fills what flags omit.
func TestConfigureLoggingFlagsAndEnv(t *testing.T) {
	resetLogging(t)
	path := filepath.Join(t.TempDir(), "corral.log")
	t.Setenv(logging.EnvLevel, "error")
	t.Setenv(logging.EnvFormat, "json")
	t.Setenv(logging.EnvFile, path)
	logOptions.Level = "debug"

	if err := configureLogging(false); err != nil {
		t.Fatal(err)
	}
	slog.Debug("flag level wins")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"msg":"flag level wins"`) {
		t.Errorf("log = %q, want a JSON debug record", data)
	}

	logOptions.Level = "shout"
	if err := configureLogging(false); err == nil {
		t.Error("configureLogging accepted --log-level shout")
	}
}
