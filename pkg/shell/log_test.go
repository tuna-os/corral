package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/logging"
)

// captureLogs routes the default logger into a buffer at debug level for the
// rest of the test and returns a function that decodes the JSON records.
func captureLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	h, err := logging.NewHandler(lockedWriter{&mu, &buf}, slog.LevelDebug, "json")
	if err != nil {
		t.Fatal(err)
	}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("bad record %q: %v", line, err)
			}
			records = append(records, rec)
		}
		return records
	}
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// A bound Real logs each command with the request ID of its context and
// with secrets redacted.
func TestBoundRealLogsRequestIDAndRedacts(t *testing.T) {
	records := captureLogs(t)
	ctx := logging.WithRequestID(context.Background(), "req-42")
	r := Bind(ctx, WithKubeContext(Real{}, "lab"))

	out, err := r.Run("sh", "-c", "echo ok", "TS_AUTHKEY=tskey-auth-secret1", "--password", "hunter2")
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	if _, err := r.RunStdin("token: s3cr3t", "sh", "-c", "cat >/dev/null; exit 3"); err == nil {
		t.Fatal("exit 3 succeeded")
	}

	recs := records()
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2: %v", len(recs), recs)
	}
	for _, rec := range recs {
		if rec["msg"] != "exec" || rec[logging.RequestIDKey] != "req-42" || rec["level"] != "DEBUG" {
			t.Errorf("record = %v, want a DEBUG exec record with request_id=req-42", rec)
		}
	}
	cmd, _ := recs[0]["cmd"].(string)
	if strings.Contains(cmd, "tskey-auth-secret1") || strings.Contains(cmd, "hunter2") {
		t.Errorf("cmd leaks a secret: %q", cmd)
	}
	if want := "sh -c echo ok TS_AUTHKEY=[REDACTED] --password [REDACTED]"; cmd != want {
		t.Errorf("cmd = %q, want %q", cmd, want)
	}
	if recs[1]["exit_code"] != float64(3) || recs[1]["stdin_bytes"] != float64(len("token: s3cr3t")) {
		t.Errorf("failed record = %v, want exit_code=3 and stdin_bytes", recs[1])
	}
	for _, rec := range recs {
		for _, v := range rec {
			if s, ok := v.(string); ok && strings.Contains(s, "s3cr3t") {
				t.Errorf("record leaks stdin: %v", rec)
			}
		}
	}
}

// Unbound runners still log, just without a request ID.
func TestUnboundRealLogsWithoutRequestID(t *testing.T) {
	records := captureLogs(t)
	if _, err := (Real{}).Run("true"); err != nil {
		t.Fatal(err)
	}
	recs := records()
	if len(recs) != 1 || recs[0]["cmd"] != "true" {
		t.Fatalf("records = %v, want one exec record for true", recs)
	}
	if _, ok := recs[0][logging.RequestIDKey]; ok {
		t.Errorf("unbound record carries a request ID: %v", recs[0])
	}
}

// Binding the kubectl runner keeps its timeout injection, its hard deadline
// path, and above all its breaker: a bound copy that trips it must trip it
// for every other caller too.
func TestBindKubectlTimeoutRunnerSharesBreaker(t *testing.T) {
	records := captureLogs(t)
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\necho \"$@\"\ncase \"$*\" in *dead*) echo 'Unable to connect to the server'; exit 1;; esac\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	base := WithKubectlTimeout(Real{}, "5s")
	bound := Bind(logging.WithRequestID(context.Background(), "req-7"), base)

	out, err := bound.Run(kubectl, "get", "vms")
	if err != nil || !strings.Contains(string(out), "--request-timeout=5s get vms") {
		t.Fatalf("bound Run = %q, %v; want the injected request timeout", out, err)
	}
	if _, err := bound.Run(kubectl, "get", "dead"); err == nil {
		t.Fatal("dead cluster call succeeded")
	}
	if _, err := base.Run(kubectl, "get", "vms"); err == nil || !strings.Contains(err.Error(), "cluster unreachable") {
		t.Fatalf("unbound runner after a bound trip = %v; want the shared breaker open", err)
	}
	recs := records()
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (the breaker short-circuits the third): %v", len(recs), recs)
	}
	for _, rec := range recs {
		if rec[logging.RequestIDKey] != "req-7" {
			t.Errorf("record = %v, want request_id=req-7", rec)
		}
	}
}

// Bind strips cancellation: the commands of a request whose client went away
// still run, so a mutation is never killed halfway.
func TestBindIgnoresCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(logging.WithRequestID(context.Background(), "gone"))
	cancel()
	if _, err := Bind(ctx, Real{}).Run("true"); err != nil {
		t.Fatalf("Run after cancel = %v", err)
	}
}

func TestBindFakeRecordsRequestID(t *testing.T) {
	f := NewFake()
	f.AddResponse("kubectl get vms", "[]", nil)
	ctx := logging.WithRequestID(context.Background(), "req-9")
	bound := Bind(ctx, WithKubeContext(f, ""))
	if _, err := bound.Run("kubectl", "get", "vms"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Run("kubectl", "get", "vms"); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 2 || calls[0].RequestID != "req-9" || calls[1].RequestID != "" {
		t.Fatalf("calls = %+v, want the bound call tagged req-9 and the direct call untagged", calls)
	}
}

type foreignRunner struct{ Runner }

func TestBindLeavesForeignRunners(t *testing.T) {
	r := foreignRunner{NewFake()}
	if got := Bind(context.Background(), r); got != Runner(r) {
		t.Errorf("Bind(foreign) = %#v, want it unchanged", got)
	}
}
