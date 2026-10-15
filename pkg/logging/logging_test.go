package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "": slog.LevelInfo, "INFO": slog.LevelInfo,
		"warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
	} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel(loud) succeeded")
	}
}

func TestNewHandlerRejectsUnknownFormat(t *testing.T) {
	if _, err := NewHandler(&bytes.Buffer{}, slog.LevelInfo, "xml"); err == nil {
		t.Fatal("NewHandler accepted format xml")
	}
}

// The handler adds the request ID of the record's context, so call sites
// only pass ctx to be correlated.
func TestHandlerAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	h, err := NewHandler(&buf, slog.LevelDebug, "json")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(h).With("component", "test")
	ctx := WithRequestID(context.Background(), "abc123")
	logger.InfoContext(ctx, "hello")
	logger.Info("no request")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d records, want 2:\n%s", len(lines), buf.String())
	}
	var first, second map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if first[RequestIDKey] != "abc123" || first["component"] != "test" {
		t.Errorf("first record = %v, want request_id=abc123 and component=test", first)
	}
	if _, ok := second[RequestIDKey]; ok {
		t.Errorf("second record carries a request ID without one in its context: %v", second)
	}
}

func TestConfigureLevelAndFile(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })

	path := filepath.Join(t.TempDir(), "sub", "corral.log")
	var fallback bytes.Buffer
	closer, err := Configure(Options{Level: "warn", Format: "text", File: path}, &fallback)
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("dropped")
	slog.Warn("kept")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "msg=kept") || strings.Contains(string(data), "dropped") {
		t.Errorf("log file = %q, want only the warn record", data)
	}
	if fallback.Len() != 0 {
		t.Errorf("fallback writer got %q although a file was set", fallback.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("log file mode = %v, want 0600", perm)
	}
}

func TestConfigureRejectsBadOptions(t *testing.T) {
	if _, err := Configure(Options{Level: "chatty"}, &bytes.Buffer{}); err == nil {
		t.Error("Configure accepted level chatty")
	}
	if _, err := Configure(Options{Format: "yaml"}, &bytes.Buffer{}); err == nil {
		t.Error("Configure accepted format yaml")
	}
}

func TestRequestIDs(t *testing.T) {
	a, b := NewRequestID(), NewRequestID()
	if len(a) != 16 || a == b || !ValidRequestID(a) {
		t.Errorf("NewRequestID() = %q, %q; want two distinct valid 16-char IDs", a, b)
	}
	if got := RequestID(context.Background()); got != "" {
		t.Errorf("RequestID(empty) = %q", got)
	}
	if got := RequestID(WithRequestID(context.Background(), a)); got != a {
		t.Errorf("RequestID round trip = %q, want %q", got, a)
	}
	for _, bad := range []string{"", "a b", "x\ny", strings.Repeat("a", 65), `"quoted"`} {
		if ValidRequestID(bad) {
			t.Errorf("ValidRequestID(%q) = true", bad)
		}
	}
	for _, good := range []string{"abc", "req-1.2_3", strings.Repeat("a", 64)} {
		if !ValidRequestID(good) {
			t.Errorf("ValidRequestID(%q) = false", good)
		}
	}
}
