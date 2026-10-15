package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/logging"
)

// captureRecords routes the default logger into a buffer at debug level for
// the rest of the test and returns a function that decodes its records.
func captureRecords(t *testing.T) func() []map[string]any {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	h, err := logging.NewHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), slog.LevelDebug, "json")
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
			var rec map[string]any
			if line != "" && json.Unmarshal([]byte(line), &rec) == nil {
				records = append(records, rec)
			}
		}
		return records
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// recordsFor returns the records of one request ID with message msg.
func recordsFor(records []map[string]any, id, msg string) []map[string]any {
	var out []map[string]any
	for _, rec := range records {
		if rec[logging.RequestIDKey] == id && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func getWithID(t *testing.T, url, id string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		req.Header.Set(logging.RequestIDHeader, id)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// The request ID a client sends reaches the backend command the request
// causes, the warning for the context that failed, and the access record —
// and comes back in the response header.
func TestRequestID_PropagatesFromRequestToBackendCommand(t *testing.T) {
	t.Setenv("CORRAL_DEFAULT_BACKEND", "kubevirt")
	records := captureRecords(t)
	fx := NewTestFixture()
	defer fx.Close()
	fx.Runner.AddResponseKV("kubectl", []string{"get", "vms", "-A", "-o", "json"}, "", errSimulated)

	resp := getWithID(t, fx.Server.URL+"/api/vms", "trace-list-1")
	_ = resp.Body.Close()
	if got := resp.Header.Get(logging.RequestIDHeader); got != "trace-list-1" {
		t.Errorf("response %s = %q, want the client's ID echoed", logging.RequestIDHeader, got)
	}

	var listed bool
	for _, call := range fx.Runner.Calls() {
		if call.Name == "kubectl" && strings.Contains(strings.Join(call.Args, " "), "get vms") {
			listed = true
			if call.RequestID != "trace-list-1" {
				t.Errorf("kubectl %v ran with request ID %q, want trace-list-1", call.Args, call.RequestID)
			}
		}
	}
	if !listed {
		t.Fatalf("the listing never reached kubectl: %+v", fx.Runner.Calls())
	}

	recs := records()
	if len(recordsFor(recs, "trace-list-1", "fleet context unavailable")) == 0 {
		t.Errorf("no fleet warning carries the request ID: %v", recs)
	}
	access := recordsFor(recs, "trace-list-1", "http request")
	if len(access) != 1 {
		t.Fatalf("access records for the request = %v, want one", access)
	}
	if access[0]["status"] != float64(http.StatusBadGateway) || access[0]["level"] != "WARN" ||
		access[0]["path"] != "/api/vms" {
		t.Errorf("access record = %v, want a WARN for the 502 on /api/vms", access[0])
	}
}

// A request without an ID, or with one that could forge log lines, gets one
// minted by the server; per-VM handlers bind it into their backend client.
func TestRequestID_MintedWhenAbsentOrMalformed(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	for _, sent := range []string{"", "bad id level=ERROR", `"quoted"`, strings.Repeat("x", 65)} {
		fx.Runner.Reset()
		resp := getWithID(t, fx.Server.URL+"/api/vms/incus/web1?context=lab", sent)
		_ = resp.Body.Close()
		id := resp.Header.Get(logging.RequestIDHeader)
		if id == sent || !logging.ValidRequestID(id) {
			t.Fatalf("sent %q, got ID %q; want a fresh valid ID", sent, id)
		}
		calls := fx.Runner.Calls()
		if len(calls) == 0 || calls[0].Name != "incus" || calls[0].RequestID != id {
			t.Fatalf("incus calls = %+v, want the first bound to %q", calls, id)
		}
	}
}

// Peer requests carry the ID, so a federated request is one ID on both
// servers.
func TestRequestID_ForwardedToPeers(t *testing.T) {
	var got []string
	var mu sync.Mutex
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get(logging.RequestIDHeader))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"vms":[]}`))
	}))
	defer peer.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := config.SetPeer("p", peer.URL); err != nil {
		t.Fatal(err)
	}

	fetchPeerVMs(logging.WithRequestID(context.Background(), "trace-peer"))
	fetchPeerVMs(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "trace-peer" || got[1] != "" {
		t.Fatalf("peer saw IDs %q, want [trace-peer, none]", got)
	}
}

// Tasks outlive their request; their records keep its ID.
func TestTaskRecordsCarryTheRequestID(t *testing.T) {
	records := captureRecords(t)
	ctx, cancel := context.WithCancel(logging.WithRequestID(context.Background(), "trace-task"))
	task := tasks.start(ctx, "trace-task-1")
	defer tasks.remove("trace-task-1")
	cancel() // the request ended; the task did not
	task.finish(errSimulated)

	recs := records()
	if len(recordsFor(recs, "trace-task", "task started")) != 1 {
		t.Errorf("no task-started record with the request ID: %v", recs)
	}
	failed := recordsFor(recs, "trace-task", "task failed")
	if len(failed) != 1 || failed[0]["task"] != "trace-task-1" || failed[0]["level"] != "WARN" {
		t.Errorf("task-failed records = %v, want one WARN for trace-task-1", failed)
	}
}

// The status recorder must not hide what console websockets and streamed
// responses need from the server's writer.
func TestRequestTracer_PassesThroughHijackAndFlush(t *testing.T) {
	srv := httptest.NewServer(requestTracer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flush" {
			_, _ = io.WriteString(w, "a")
			w.(http.Flusher).Flush()
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nhi")
		_ = rw.Flush()
	})))
	defer srv.Close()

	for _, path := range []string{"/flush", "/hijack"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(bufio.NewReader(resp.Body))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("%s = %d %q", path, resp.StatusCode, body)
		}
	}
}
