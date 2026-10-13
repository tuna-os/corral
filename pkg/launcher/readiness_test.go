package launcher

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaitVMReady_OK(t *testing.T) {
	err := WaitVMReady("vm1", ReadinessOpts{
		Timeout:   5 * time.Second,
		Interval:  10 * time.Millisecond,
		IsRunning: func(name string) bool { return true },
		DialVNC:   func(name string) error { return nil },
	})
	if err != nil {
		t.Fatalf("WaitVMReady: %v", err)
	}
}

func TestWaitVMReady_WaitsForBoth(t *testing.T) {
	calls := 0
	err := WaitVMReady("vm1", ReadinessOpts{
		Timeout:  5 * time.Second,
		Interval: 10 * time.Millisecond,
		IsRunning: func(name string) bool {
			calls++
			return calls > 2
		},
		DialVNC: func(name string) error { return nil },
	})
	if err != nil {
		t.Fatalf("should wait for running: %v", err)
	}
	if err := WaitVMReady("vm1", ReadinessOpts{
		Timeout:   100 * time.Millisecond,
		Interval:  10 * time.Millisecond,
		IsRunning: func(name string) bool { return true },
		DialVNC:   func(name string) error { return fmt.Errorf("refused") },
	}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("VNC refusal must fail readiness: %v", err)
	}
	if err := WaitVMReady("vm1", ReadinessOpts{
		Timeout:   100 * time.Millisecond,
		Interval:  10 * time.Millisecond,
		IsRunning: func(name string) bool { return false },
		DialVNC:   func(name string) error { return nil },
	}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("stopped VM must fail readiness: %v", err)
	}
}

func TestWaitWebReady_OK(t *testing.T) {
	err := WaitWebReady("http://127.0.0.1:1/healthz", ReadinessOpts{
		Timeout:  5 * time.Second,
		Interval: 10 * time.Millisecond,
		GetWeb: func(url string) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		},
	})
	if err != nil {
		t.Fatalf("WaitWebReady: %v", err)
	}
}

func TestWaitWebReady_Non200Fails(t *testing.T) {
	err := WaitWebReady("http://127.0.0.1:1/healthz", ReadinessOpts{
		Timeout:  100 * time.Millisecond,
		Interval: 10 * time.Millisecond,
		GetWeb: func(url string) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("x"))}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("503 must fail web readiness: %v", err)
	}
}

func TestWaitVMReady_PostDeadlineSuccessRejected(t *testing.T) {
	// A probe that succeeds only after the deadline must not count.
	err := WaitVMReady("vm1", ReadinessOpts{
		Timeout:   60 * time.Millisecond,
		Interval:  10 * time.Millisecond,
		IsRunning: func(name string) bool { return true },
		DialVNC: func(name string) error {
			time.Sleep(120 * time.Millisecond)
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("post-deadline success must fail readiness, got %v", err)
	}
}

func TestWaitVMReady_ContextCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := WaitVMReady("vm1", ReadinessOpts{
		Timeout:   5 * time.Second,
		Interval:  10 * time.Millisecond,
		Ctx:       ctx,
		IsRunning: func(name string) bool { return true },
		DialVNC:   func(name string) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("cancelled wait must report interruption, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("cancelled wait did not return promptly")
	}
}

func TestWaitWebReady_DefaultClientIsBounded(t *testing.T) {
	// A connected handler that never replies must fail the probe on the
	// per-probe timeout, not hang the wait past its deadline. The handler
	// hijacks and holds (a Close-proof hang); the client timeout ends it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		c, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var b [1]byte
		_, _ = c.Read(b[:])
	}))
	defer srv.Close()
	start := time.Now()
	err := WaitWebReady(srv.URL+"/healthz", ReadinessOpts{
		Timeout:  300 * time.Millisecond,
		Interval: 50 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("silent handler must fail readiness, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("bounded wait took %s — per-probe timeout is missing", elapsed)
	}
}

func TestDialLoopbackVNCWithTimeout_RFBIdentity(t *testing.T) {
	serve := func(t *testing.T, write func(net.Conn)) string {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer func() { _ = c.Close() }()
					write(c)
				}()
			}
		}()
		return ln.Addr().String()
	}
	// A real greeting passes.
	good := serve(t, func(c net.Conn) { _, _ = c.Write([]byte("RFB 003.008\n")) })
	if err := DialLoopbackVNCWithTimeout(good, time.Second); err != nil {
		t.Errorf("RFB greeting must pass: %v", err)
	}
	// TCP acceptance with no greeting fails (bounded by the timeout).
	silent := serve(t, func(c net.Conn) { time.Sleep(5 * time.Second) })
	if err := DialLoopbackVNCWithTimeout(silent, 100*time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "no RFB greeting") {
		t.Errorf("silent listener must fail greeting check, got %v", err)
	}
	// A wrong service on the port fails loudly.
	wrong := serve(t, func(c net.Conn) { _, _ = c.Write([]byte("SSH-2.0-fake!\n")) })
	if err := DialLoopbackVNCWithTimeout(wrong, time.Second); err == nil ||
		!strings.Contains(err.Error(), "non-RFB greeting") {
		t.Errorf("non-RFB greeting must fail, got %v", err)
	}
}

func TestDialLoopbackVNC_CancelMidRead(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				time.Sleep(30 * time.Second) // silent past any test budget
			}()
		}
	}()
	// Cancellation reaches a greeting read already in flight: the probe
	// fails promptly as interrupted, not after the 30s deadline.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err = dialLoopbackVNC(ctx, ln.Addr().String(), time.Now().Add(30*time.Second))
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("cancelled probe must report interruption, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancelled probe took %s — cancellation did not reach the read", elapsed)
	}
}

func TestDialLoopbackVNC_PreCancelled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("RFB 003.008\n"))
			_ = c.Close()
		}
	}()
	// A cancelled context fails even against a healthy server: no
	// post-cancellation success is ever reported.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := dialLoopbackVNC(ctx, ln.Addr().String(), time.Now().Add(time.Minute)); err == nil ||
		!strings.Contains(err.Error(), "interrupted") {
		t.Errorf("pre-cancelled probe must report interruption, got %v", err)
	}
}

func TestDialLoopbackVNC_PastDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// A spent deadline fails the dial itself: the budget is one absolute
	// deadline, not a per-phase reset.
	start := time.Now()
	if err := dialLoopbackVNC(context.Background(), ln.Addr().String(), time.Now().Add(-time.Second)); err == nil {
		t.Error("past-deadline dial must fail")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("past-deadline dial took %s", elapsed)
	}
}
