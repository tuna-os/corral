package launcher

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ReadinessOpts bounds the probes. Zero values take safe defaults.
type ReadinessOpts struct {
	// Timeout bounds each wait. Default 5 minutes for the VM, 30 seconds
	// for the web server.
	Timeout time.Duration
	// Interval between probes. Default 2 seconds (VM), 250 ms (web).
	Interval time.Duration
	// Ctx cancels the wait (launcher shutdown). Nil uses
	// context.Background.
	Ctx context.Context
	// IsRunning reports VM liveness. Nil uses the real qemu check.
	IsRunning func(name string) bool
	// DialVNC connects to the VM's loopback VNC listener. Nil uses the
	// real TCP dial, clipped to the remaining deadline.
	DialVNC func(name string) error
	// GetWeb performs the web readiness GET. Nil uses a per-probe client
	// whose timeout is clipped to the remaining deadline.
	GetWeb func(url string) (*http.Response, error)
}

func readinessCtx(opts ReadinessOpts) context.Context {
	if opts.Ctx != nil {
		return opts.Ctx
	}
	return context.Background()
}

// sleepCtx sleeps d (clipped to the deadline) unless ctx is cancelled first.
// It reports whether the caller should probe again.
func sleepCtx(ctx context.Context, d time.Duration, deadline time.Time) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if rem := time.Until(deadline); rem < d {
		d = rem
	}
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// WaitVMReady blocks until the VM is genuinely reachable: the QEMU process
// is alive (systemd or owned direct child) and its loopback VNC listener
// accepts TCP and speaks the RFB greeting. Acceptance plus the greeting
// proves a VNC server is listening with a framebuffer path behind it — the
// noVNC console the launcher is about to open has something real to bridge
// to — rather than something else that happened to bind the port. The
// greeting identifies VNC protocol shape, not the intended VM, guest
// health, or pixels; serial/SSH markers are guest-specific and are not
// claimed here.
//
// The wait is absolutely bounded by ONE deadline fixed at entry: liveness,
// dial and greeting-read all share it (no per-phase reset that could double
// the budget), a probe that succeeds after the deadline does not count, and
// the context cancels the wait — including a dial or greeting read already
// in flight.
func WaitVMReady(name string, opts ReadinessOpts) error {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ctx := readinessCtx(opts)
	deadline := time.Now().Add(timeout)
	isRunning := opts.IsRunning
	if isRunning == nil {
		isRunning = func(name string) bool { return defaultIsRunningCtx(ctx, name, deadline) }
	}
	dialVNC := opts.DialVNC
	if dialVNC == nil {
		dialVNC = func(name string) error { return defaultDialVNCDeadline(ctx, name, deadline) }
	}
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("VM %q readiness interrupted: %w", name, err)
		}
		if !time.Now().Before(deadline) {
			break
		}
		if !isRunning(name) {
			lastErr = fmt.Errorf("VM %q is not running", name)
			if !sleepCtx(ctx, interval, deadline) {
				break
			}
			continue
		}
		if err := dialVNC(name); err != nil {
			lastErr = fmt.Errorf("VNC not yet accepting: %w", err)
			if !sleepCtx(ctx, interval, deadline) {
				break
			}
			continue
		}
		if !time.Now().Before(deadline) {
			lastErr = fmt.Errorf("VNC accepted after the %s deadline", timeout)
			break
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("VM %q readiness interrupted: %w", name, err)
	}
	return fmt.Errorf("VM %q not ready within %s (last: %v)", name, timeout, lastErr)
}

// maxWebProbe bounds any single web readiness probe even when the overall
// deadline is generous: a connected handler that never replies must fail
// the probe, not the wait.
const maxWebProbe = 10 * time.Second

// WaitWebReady blocks until GET url returns 200. The launcher probes its own
// loopback /healthz — a 200 proves the owned server is serving, not merely
// bound. Like WaitVMReady it is absolutely bounded: per-probe client
// timeouts clipped to the remaining deadline, no post-deadline success,
// cancellable sleeps.
func WaitWebReady(url string, opts ReadinessOpts) error {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	ctx := readinessCtx(opts)
	get := opts.GetWeb
	deadline := time.Now().Add(timeout)
	if get == nil {
		get = func(url string) (*http.Response, error) {
			perProbe := maxWebProbe
			if rem := time.Until(deadline); rem < perProbe {
				perProbe = rem
			}
			if perProbe <= 0 {
				return nil, fmt.Errorf("deadline exceeded")
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			return (&http.Client{Timeout: perProbe}).Do(req)
		}
	}
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("web server %s readiness interrupted: %w", url, err)
		}
		if !time.Now().Before(deadline) {
			break
		}
		resp, err := get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if !time.Now().Before(deadline) {
					lastErr = fmt.Errorf("HTTP 200 arrived after the %s deadline", timeout)
					break
				}
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if !sleepCtx(ctx, interval, deadline) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("web server %s readiness interrupted: %w", url, err)
	}
	return fmt.Errorf("web server %s not ready within %s (last: %v)", url, timeout, lastErr)
}

// DialLoopbackVNC is the default VNC dial: TCP to the VM's recorded VNC port
// on 127.0.0.1 with a 5-second budget, plus the RFB greeting check. Exported
// so the composed launcher and tests share one dial path.
func DialLoopbackVNC(addr string) error {
	return DialLoopbackVNCWithTimeout(addr, 5*time.Second)
}

// DialLoopbackVNCWithTimeout dials addr (host:port) and verifies the peer
// speaks RFB: the 12-byte server greeting starting with "RFB ". TCP
// acceptance alone would let any listener on the port pass; the greeting
// proves VNC protocol shape on the port (not the intended VM or pixels).
// The probe opens its own connection and closes it — the VNC server greets
// every connection, so probing never consumes bytes the console bridge
// needs. The whole probe — dial plus greeting read — shares one absolute
// deadline: a slow connect cannot extend the read budget.
func DialLoopbackVNCWithTimeout(addr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return dialLoopbackVNC(context.Background(), addr, time.Now().Add(timeout))
}

// dialLoopbackVNC is the cancellable VNC probe core: dial addr and verify
// the RFB greeting, all within the absolute deadline. Cancellation closes
// the connection out from under a dial or greeting read already in flight,
// and a greeting that arrives after cancellation does not count.
func dialLoopbackVNC(ctx context.Context, addr string, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("VNC %s probe interrupted: %w", addr, err)
	}
	dialer := net.Dialer{Deadline: deadline}
	c, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if cErr := ctx.Err(); cErr != nil {
			return fmt.Errorf("VNC %s probe interrupted: %w", addr, cErr)
		}
		return err
	}
	defer func() { _ = c.Close() }()
	// Cancellation must reach a stalled greeting read, not just the gap
	// between probes: closing the connection fails the ReadFull below.
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if err := c.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("VNC %s: %w", addr, err)
	}
	var greeting [12]byte
	if _, err := io.ReadFull(c, greeting[:]); err != nil {
		if cErr := ctx.Err(); cErr != nil {
			return fmt.Errorf("VNC %s probe interrupted: %w", addr, cErr)
		}
		return fmt.Errorf("VNC %s accepted TCP but sent no RFB greeting: %w", addr, err)
	}
	if !strings.HasPrefix(string(greeting[:]), "RFB ") {
		return fmt.Errorf("VNC %s sent a non-RFB greeting %q — wrong service on the VNC port?", addr, greeting)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("VNC %s probe interrupted: %w", addr, err)
	}
	return nil
}
