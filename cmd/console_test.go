package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/types"
)

// serveFirstLine runs serve with its stdout on a pipe, returns the first
// line it prints, and a stop function that ends the server and returns its
// error.
func serveFirstLine(t *testing.T, serve func(ctx context.Context, out io.Writer) error) (string, func() error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := serve(ctx, w)
		_ = w.CloseWithError(io.EOF)
		done <- err
	}()
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		lines <- line
		_, _ = io.Copy(io.Discard, r)
	}()
	var line string
	select {
	case line = <-lines:
	case err := <-done:
		cancel()
		t.Fatalf("server exited before printing its URL: %v", err)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("no URL printed within 30s")
	}
	stop := func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("server did not stop within 10s of cancel")
			return nil
		}
	}
	return line, stop
}

func withJSONOutput(t *testing.T, on bool) {
	t.Helper()
	prev := jsonOutput
	jsonOutput = on
	t.Cleanup(func() { jsonOutput = prev })
}

// assertOSPort checks that u is on 127.0.0.1 with the port the OS chose, not 0.
func assertOSPort(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host != "127.0.0.1" || port == "" || port == "0" {
		t.Fatalf("URL %q: want 127.0.0.1 and the port the OS chose", raw)
	}
	return u
}

func TestConsole_JSONLineWithPortZero(t *testing.T) {
	withJSONOutput(t, true)
	vm := types.VM{Name: "desk", Namespace: "corral-vms", Context: "lab"}
	line, stop := serveFirstLine(t, func(ctx context.Context, out io.Writer) error {
		return runConsole(ctx, out, vm, "127.0.0.1:0")
	})
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "}\n") {
		t.Fatalf("want one JSON object line, got %q", line)
	}
	var got consoleEndpoint
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("not JSON: %v: %q", err, line)
	}
	if got.VM != "desk" || got.Namespace != "corral-vms" || got.Context != "lab" {
		t.Errorf("identity = %+v", got)
	}
	u := assertOSPort(t, got.URL)
	if u.Path != "/console/corral-vms/desk" || u.Query().Get("context") != "lab" {
		t.Errorf("url = %q, want /console/corral-vms/desk?context=lab", got.URL)
	}
	if want := "ws://" + u.Host + "/api/vnc/corral-vms/desk?context=lab"; got.WebSocket != want {
		t.Errorf("websocket = %q, want %q", got.WebSocket, want)
	}

	resp, err := http.Get(got.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", got.URL, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `data-vm="desk"`) {
		t.Errorf("GET %s = %d, want the console page:\n%s", got.URL, resp.StatusCode, body)
	}
	if err := stop(); err != nil {
		t.Errorf("serve returned %v after cancel, want nil", err)
	}
}

func TestConsole_HumanLine(t *testing.T) {
	withJSONOutput(t, false)
	line, stop := serveFirstLine(t, func(ctx context.Context, out io.Writer) error {
		return runConsole(ctx, out, types.VM{Name: "desk", Namespace: "local"}, "127.0.0.1:0")
	})
	defer func() { _ = stop() }()
	prefix := "Console of desk: "
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("line = %q, want prefix %q", line, prefix)
	}
	u := assertOSPort(t, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
	if u.Path != "/console/local/desk" || u.RawQuery != "" {
		t.Errorf("url = %q, want /console/local/desk without a query", u)
	}
}

func TestWeb_JSONLineWithPortZero(t *testing.T) {
	withJSONOutput(t, true)
	line, stop := serveFirstLine(t, func(ctx context.Context, out io.Writer) error {
		return runWeb(ctx, out, io.Discard, "127.0.0.1:0", nil)
	})
	var got webListening
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("not JSON: %v: %q", err, line)
	}
	assertOSPort(t, got.URL)
	resp, err := http.Get(got.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", resp.StatusCode)
	}
	if err := stop(); err != nil {
		t.Errorf("serve returned %v after cancel, want nil", err)
	}
}

func TestWeb_HumanLineNamesTheChosenPort(t *testing.T) {
	withJSONOutput(t, false)
	// The human line goes to errOut, so errOut is the stream read here.
	line, stop := serveFirstLine(t, func(ctx context.Context, out io.Writer) error {
		return runWeb(ctx, io.Discard, out, "127.0.0.1:0", nil)
	})
	defer func() { _ = stop() }()
	prefix := "Corral web UI listening on "
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("line = %q, want prefix %q", line, prefix)
	}
	assertOSPort(t, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
}

func TestConsoleEndpoint_EscapesPath(t *testing.T) {
	got := newConsoleEndpoint("http://127.0.0.1:9", types.VM{Name: "a b", Namespace: "ns", Context: "x&y"})
	if got.URL != "http://127.0.0.1:9/console/ns/a%20b?context=x%26y" {
		t.Errorf("url = %q", got.URL)
	}
	if got.WebSocket != "ws://127.0.0.1:9/api/vnc/ns/a%20b?context=x%26y" {
		t.Errorf("websocket = %q", got.WebSocket)
	}
}
