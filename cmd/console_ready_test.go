package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/web"
)

type consoleHandoffWriter struct {
	server       *web.Server
	lines        chan string
	writes       int
	eventStarted chan struct{}
	eventRelease <-chan struct{}
	eventError   error
}

func (w *consoleHandoffWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		if w.eventStarted != nil {
			close(w.eventStarted)
		}
		if w.eventRelease != nil {
			<-w.eventRelease
		}
		if w.eventError != nil {
			return 0, w.eventError
		}
	}
	w.lines <- string(data)
	return len(data), nil
}

func nextConsoleHandoffLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line := <-lines:
		return line
	case <-time.After(10 * time.Second):
		t.Fatal("console did not print the expected JSON line")
		return ""
	}
}

func startConsoleHandoff(t *testing.T, vm types.VM, out *consoleHandoffWriter) (consoleEndpoint, func() error, <-chan struct{}) {
	t.Helper()
	withJSONOutput(t, true)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORRAL_AUTH_REQUIRED", "")
	t.Setenv("CORRAL_ADMINS", "")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var result error
	go func() {
		defer close(finished)
		out.server, result = runConsole(ctx, out, vm, "127.0.0.1:0", true)
	}()
	stop := func() error {
		cancel()
		select {
		case <-finished:
			return result
		case <-time.After(10 * time.Second):
			t.Fatal("console did not release its listener after cancellation")
			return nil
		}
	}
	t.Cleanup(func() { _ = stop() })
	var endpoint consoleEndpoint
	if err := json.Unmarshal([]byte(nextConsoleHandoffLine(t, out.lines)), &endpoint); err != nil {
		t.Fatal(err)
	}
	return endpoint, stop, finished
}

func postConsoleHandoff(t *testing.T, endpoint consoleEndpoint) int {
	t.Helper()
	u, err := url.Parse(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"nonce": u.Query().Get("ready_nonce"), "context": endpoint.Context})
	if err != nil {
		t.Fatal(err)
	}
	origin := u.Scheme + "://" + u.Host
	path := "/api/console-ready/" + url.PathEscape(endpoint.Namespace) + "/" + url.PathEscape(endpoint.VM)
	req, err := http.NewRequest("POST", origin+path, strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func assertConsoleListenerClosed(t *testing.T, server *web.Server, endpoint consoleEndpoint) {
	t.Helper()
	if server == nil {
		t.Fatal("runConsole did not return its owned server")
	}
	u := assertOSPort(t, endpoint.URL)
	if server.URL() != u.Scheme+"://"+u.Host {
		t.Fatalf("retirement inspected a different listener: %s, endpoint %s", server.URL(), endpoint.URL)
	}
	// A different owner can bind this port as soon as our socket closes.
	// Inspect the actual listener, not the current occupant of its old address.
	if err := server.Close(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("owned console listener was not already closed: %v", err)
	}
}

func TestConsoleNotifyReady_JSONEndpointThenOneAuthenticEvent(t *testing.T) {
	out := &consoleHandoffWriter{lines: make(chan string, 4)}
	vm := types.VM{Name: "desk", Namespace: "local", Context: "lab"}
	endpoint, stop, _ := startConsoleHandoff(t, vm, out)
	u := assertOSPort(t, endpoint.URL)
	if endpoint.VM != vm.Name || endpoint.Namespace != vm.Namespace || endpoint.Context != vm.Context || u.Path != "/console/local/desk" || u.Query().Get("context") != "lab" || u.Query().Get("ready_nonce") == "" {
		t.Fatalf("readiness endpoint = %+v", endpoint)
	}
	if endpoint.WebSocket != "ws://"+u.Host+"/api/vnc/local/desk?context=lab" {
		t.Fatalf("readiness nonce leaked to or changed websocket URL: %q", endpoint.WebSocket)
	}
	resp, err := http.Get(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `data-ready-nonce="`+u.Query().Get("ready_nonce")+`"`) {
		t.Fatalf("GET readiness page = %d: %s", resp.StatusCode, body)
	}
	select {
	case line := <-out.lines:
		t.Fatalf("page opening falsely reported readiness: %s", line)
	default:
	}
	if status := postConsoleHandoff(t, endpoint); status != http.StatusNoContent {
		t.Fatalf("authentic report = %d", status)
	}
	line := nextConsoleHandoffLine(t, out.lines)
	var event map[string]string
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatal(err)
	}
	if len(event) != 3 || event["event"] != "rfb-connected" || event["vm"] != "desk" || event["namespace"] != "local" {
		t.Fatalf("readiness event = %s", line)
	}
	if status := postConsoleHandoff(t, endpoint); status != http.StatusConflict {
		t.Fatalf("duplicate report = %d, want 409", status)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-out.lines:
		t.Fatalf("duplicate event: %s", extra)
	default:
	}
	assertConsoleListenerClosed(t, out.server, endpoint)
}

func TestConsoleNotifyReady_BlockedEventOutputDoesNotBlockHTTPOrCancellation(t *testing.T) {
	release := make(chan struct{})
	// Registered first so console cancellation cleanup runs before unblocking the
	// caller-owned writer. This tests cancellation, not a cooperative writer.
	t.Cleanup(func() { close(release) })
	out := &consoleHandoffWriter{lines: make(chan string, 4), eventStarted: make(chan struct{}), eventRelease: release}
	endpoint, stop, _ := startConsoleHandoff(t, types.VM{Name: "desk", Namespace: "local"}, out)
	if status := postConsoleHandoff(t, endpoint); status != http.StatusNoContent {
		t.Fatalf("HTTP waited for blocked stdout or rejected report: %d", status)
	}
	select {
	case <-out.eventStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("CLI did not try to write the event")
	}
	if status := postConsoleHandoff(t, endpoint); status != http.StatusConflict {
		t.Fatalf("duplicate request blocked behind stdout: %d", status)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	assertConsoleListenerClosed(t, out.server, endpoint)
}

func TestConsoleNotifyReady_EventOutputFailureStopsServer(t *testing.T) {
	failure := errors.New("output closed")
	out := &consoleHandoffWriter{lines: make(chan string, 4), eventError: failure}
	endpoint, stop, finished := startConsoleHandoff(t, types.VM{Name: "desk", Namespace: "local"}, out)
	if status := postConsoleHandoff(t, endpoint); status != http.StatusNoContent {
		t.Fatalf("report = %d", status)
	}
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("console survived an event-output error")
	}
	if err := stop(); !errors.Is(err, failure) {
		t.Fatalf("output error = %v, want %v", err, failure)
	}
	assertConsoleListenerClosed(t, out.server, endpoint)
}

func TestConsoleNotifyReady_CancelBeforeRFBEmitsNoEvent(t *testing.T) {
	out := &consoleHandoffWriter{lines: make(chan string, 4)}
	endpoint, stop, _ := startConsoleHandoff(t, types.VM{Name: "desk", Namespace: "local"}, out)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-out.lines:
		t.Fatalf("cancellation emitted an event: %s", line)
	default:
	}
	assertConsoleListenerClosed(t, out.server, endpoint)
}

type failedConsoleEndpointWriter struct {
	endpoint consoleEndpoint
	err      error
	short    bool
}

func (w *failedConsoleEndpointWriter) Write(data []byte) (int, error) {
	if err := json.Unmarshal(data, &w.endpoint); err != nil {
		return 0, err
	}
	if w.short {
		return len(data) - 1, nil
	}
	return 0, w.err
}

func TestConsoleNotifyReady_EndpointWriteFailureReleasesListener(t *testing.T) {
	withJSONOutput(t, true)
	t.Setenv("HOME", t.TempDir())
	failure := errors.New("stdout failed")
	for _, tc := range []struct {
		name  string
		short bool
		want  error
	}{{"error", false, failure}, {"short write", true, io.ErrShortWrite}} {
		t.Run(tc.name, func(t *testing.T) {
			out := &failedConsoleEndpointWriter{err: failure, short: tc.short}
			server, err := runConsole(context.Background(), out, types.VM{Name: "desk", Namespace: "local"}, "127.0.0.1:0", true)
			if !errors.Is(err, tc.want) {
				t.Fatalf("endpoint output = %v, want %v", err, tc.want)
			}
			assertConsoleListenerClosed(t, server, out.endpoint)
		})
	}
}

func TestConsoleNotifyReady_RejectsNonJSONAndInvalidIdentityBeforeOutput(t *testing.T) {
	withJSONOutput(t, false)
	var out strings.Builder
	if _, err := runConsole(context.Background(), &out, types.VM{Name: "desk", Namespace: "local"}, "127.0.0.1:0", true); err == nil || !strings.Contains(err.Error(), "requires --json") {
		t.Fatalf("non-JSON notify = %v", err)
	}
	jsonOutput = true
	t.Setenv("HOME", t.TempDir())
	if _, err := runConsole(context.Background(), &out, types.VM{Name: "../desk", Namespace: "local"}, "127.0.0.1:0", true); err == nil {
		t.Fatal("invalid readiness identity was accepted")
	}
	if out.Len() != 0 {
		t.Fatalf("invalid readiness setup printed a usable-looking endpoint: %s", out.String())
	}
}

type blockedConsoleEndpointWriter struct {
	endpoint chan consoleEndpoint
	release  <-chan struct{}
	finished chan struct{}
}

func (w *blockedConsoleEndpointWriter) Write(data []byte) (int, error) {
	defer close(w.finished)
	var endpoint consoleEndpoint
	if err := json.Unmarshal(data, &endpoint); err != nil {
		return 0, err
	}
	w.endpoint <- endpoint
	<-w.release
	return len(data), nil
}

func TestConsoleNotifyReady_BlockedEndpointOutputStillReleasesListener(t *testing.T) {
	withJSONOutput(t, true)
	t.Setenv("HOME", t.TempDir())
	release := make(chan struct{})
	out := &blockedConsoleEndpointWriter{endpoint: make(chan consoleEndpoint, 1), release: release, finished: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var server *web.Server
	result := make(chan error, 1)
	go func() {
		var err error
		server, err = runConsole(ctx, out, types.VM{Name: "desk", Namespace: "local"}, "127.0.0.1:0", true)
		result <- err
	}()
	defer func() {
		close(release)
		select {
		case <-out.finished:
		case <-time.After(time.Second):
			t.Error("test output writer did not finish")
		}
	}()
	var endpoint consoleEndpoint
	select {
	case endpoint = <-out.endpoint:
	case <-time.After(10 * time.Second):
		t.Fatal("console did not attempt to print its endpoint")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked first stdout write prevented cancellation")
	}
	assertConsoleListenerClosed(t, server, endpoint)
}

func TestConsoleNotifyReady_RetirementDoesNotClaimReusedPort(t *testing.T) {
	out := &consoleHandoffWriter{lines: make(chan string, 4)}
	endpoint, stop, _ := startConsoleHandoff(t, types.VM{Name: "desk", Namespace: "local"}, out)
	u := assertOSPort(t, endpoint.URL)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	foreign, err := net.Listen("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	assertConsoleListenerClosed(t, out.server, endpoint)
	conn, err := net.DialTimeout("tcp", foreign.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("retirement check interfered with a different listener: %v", err)
	}
	_ = conn.Close()
}
