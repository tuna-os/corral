// Package logging configures Corral's structured logs (log/slog) and carries
// a request ID through a context.Context, so one web request can be followed
// through its backend calls and the commands they run.
//
// Logs are diagnostics, not output: they go to stderr (or a file) and never
// to stdout, where command output and --json documents go.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Environment variables that set the log configuration when the matching
// flag is not given.
const (
	EnvLevel  = "CORRAL_LOG_LEVEL"
	EnvFormat = "CORRAL_LOG_FORMAT"
	EnvFile   = "CORRAL_LOG_FILE"
)

// Options selects the log level, the record format, and the destination.
type Options struct {
	// Level is debug, info, warn, or error.
	Level string
	// Format is text (key=value) or json (one object per line).
	Format string
	// File, when set, receives the log instead of the default writer.
	File string
}

// ParseLevel accepts the level names that --log-level documents.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn, or error)", s)
}

// NewHandler returns a handler that writes records of at least level to w in
// format, and that adds the request ID of the record's context.
func NewHandler(w io.Writer, level slog.Level, format string) (slog.Handler, error) {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text", "":
		h = slog.NewTextHandler(w, opts)
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		return nil, fmt.Errorf("unknown log format %q (want text or json)", format)
	}
	return contextHandler{h}, nil
}

// Configure installs the default slog logger from opts. Records go to
// opts.File when it is set, and to fallback otherwise. The returned closer
// releases the file; call it when the process exits.
func Configure(opts Options, fallback io.Writer) (io.Closer, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	w, closer := fallback, io.Closer(nopCloser{})
	if opts.File != "" {
		f, err := OpenFile(opts.File)
		if err != nil {
			return nil, err
		}
		w, closer = f, f
	}
	h, err := NewHandler(w, level, opts.Format)
	if err != nil {
		_ = closer.Close()
		return nil, err
	}
	slog.SetDefault(slog.New(h))
	return closer, nil
}

// OpenFile opens path for appending, owner-readable only: the log names
// hosts, VMs, and commands even with secrets redacted.
func OpenFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}

// DefaultTUIFile is where the TUI logs when CORRAL_LOG_FILE is not set: the
// TUI owns the terminal, so a record on stderr would corrupt its frame.
func DefaultTUIFile() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "corral", "tui.log")
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// ── Request IDs ─────────────────────────────────────────────────────

// RequestIDHeader carries a request ID between a client and Corral, and
// between Corral and its peers.
const RequestIDHeader = "X-Request-ID"

// RequestIDKey is the attribute name of the request ID in every record.
const RequestIDKey = "request_id"

type requestIDKey struct{}

// NewRequestID returns a random 16-hex-digit ID.
func NewRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b[:])
}

// WithRequestID returns a context that carries id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID that ctx carries, or "".
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// ValidRequestID reports whether a client-supplied ID is safe to adopt:
// 1 to 64 characters from [A-Za-z0-9._-]. Anything else could forge log
// lines or bloat every record, so the server mints its own instead.
func ValidRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// contextHandler adds the request ID of a record's context, so a call site
// only has to pass ctx (slog.InfoContext) to be correlated.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String(RequestIDKey, id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
