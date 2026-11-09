package macos

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TypeAppleField types only into the user-confirmed focused US-layout control.
// Apple authentication, trusted-device approval and 2FA remain interactive.
func TypeAppleField(ctx context.Context, target GuestTarget, field, item string, confirmFocused, confirmUS, enter bool) error {
	if !confirmFocused || !confirmUS {
		return errors.New("confirm the focused credential field and plain US input layout before continuing")
	}
	if field != "username" && field != "password" {
		return errors.New("the selected field must be username or password")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return errors.New("focused credential input requires a Linux or macOS client")
	}
	if strings.TrimSpace(item) == "" {
		return errors.New("a selected Bitwarden login item is required")
	}
	if err := ValidateRunningGuest(ctx, target); err != nil {
		return err
	}
	connection, err := dialAppleVNC(ctx, target)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	if err := handshakeAppleVNC(connection); err != nil {
		return err
	}
	// Never consume the vault before the authenticated API tunnel and RFB
	// no-second-password handshake have both succeeded.
	if connection.exited() {
		return errors.New("VNC API proxy exited; no credentials sent")
	}
	username, password, err := vaultLogin(ctx, item)
	if err != nil {
		return err
	}
	value := username
	if field == "password" {
		value = password
	}
	username, password = "", ""
	if connection.exited() {
		return errors.New("VNC API proxy exited; no credentials sent")
	}
	return typeAppleValue(ctx, connection, value, enter)
}

// The general console dialer reserves then releases a port and cannot detect
// early proxy exit. Credential input instead lets virtctl bind loopback port 0
// and consumes its bound-port JSON, never racing a separately reserved port.
func dialAppleVNC(ctx context.Context, target GuestTarget) (*appleVNCConnection, error) {
	proxyCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(proxyCtx, "virtctl", "--context", target.KubeContext, "-n", target.Namespace,
		"vnc", target.Name, "--proxy-only", "--address=127.0.0.1", "--port=0")
	reader, writer := io.Pipe()
	command.Stdout = writer
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		return nil, errors.New("cannot start VNC API proxy; no credentials sent")
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); _ = writer.Close(); close(done) }()
	type portResult struct {
		port int
		err  error
	}
	published := make(chan portResult, 1)
	go func() {
		buffer := bufio.NewReaderSize(reader, 4096)
		line, err := buffer.ReadSlice('\n')
		var response struct {
			Port int `json:"port"`
		}
		if err != nil || json.Unmarshal(line, &response) != nil || response.Port < 1 || response.Port > 65535 {
			published <- portResult{err: errors.New("VNC API proxy did not publish a valid bound port; no credentials sent")}
		} else {
			published <- portResult{port: response.Port}
		}
		_, _ = io.Copy(io.Discard, buffer)
		_ = reader.Close()
	}()
	cleanup := func() { cancel(); _ = reader.Close(); _ = writer.Close(); <-done }
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var port int
	select {
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-done:
		cleanup()
		return nil, errors.New("VNC API proxy exited before connection; no credentials sent")
	case <-timer.C:
		cleanup()
		return nil, errors.New("VNC API proxy did not bind in time; no credentials sent")
	case result := <-published:
		if result.err != nil {
			cleanup()
			return nil, result.err
		}
		port = result.port
	}
	select {
	case <-done:
		cleanup()
		return nil, errors.New("VNC API proxy exited before connection; no credentials sent")
	default:
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(proxyCtx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		cleanup()
		return nil, errors.New("cannot connect to the VNC API proxy; no credentials sent")
	}
	result := &appleVNCConnection{Conn: connection, done: done, cleanup: cleanup}
	go func() { <-proxyCtx.Done(); _ = connection.Close() }()
	return result, nil
}

type appleVNCConnection struct {
	net.Conn
	done    <-chan struct{}
	cleanup func()
	once    sync.Once
}

func (connection *appleVNCConnection) exited() bool {
	select {
	case <-connection.done:
		return true
	default:
		return false
	}
}

func (connection *appleVNCConnection) Close() error {
	var err error
	connection.once.Do(func() { err = connection.Conn.Close(); connection.cleanup() })
	return err
}

func writeAppleBytes(connection net.Conn, body []byte) error {
	for len(body) > 0 {
		count, err := connection.Write(body)
		if err != nil || count == 0 {
			return errors.New("VNC input transport closed; inspect the guest UI before any further input")
		}
		body = body[count:]
	}
	return nil
}

func handshakeAppleVNC(connection net.Conn) error {
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = connection.SetDeadline(time.Time{}) }()
	receive := func(count int) ([]byte, error) {
		body := make([]byte, count)
		if _, err := io.ReadFull(connection, body); err != nil {
			return nil, errors.New("VNC connection closed during handshake; no credentials sent")
		}
		return body, nil
	}
	version, err := receive(12)
	if err != nil {
		return err
	}
	if string(version) != "RFB 003.008\n" && string(version) != "RFB 003.007\n" {
		return errors.New("unsupported VNC protocol; no credentials sent")
	}
	if err := writeAppleBytes(connection, []byte("RFB 003.008\n")); err != nil {
		return err
	}
	count, err := receive(1)
	if err != nil {
		return err
	}
	if count[0] == 0 {
		return errors.New("VNC rejected security negotiation; no credentials sent")
	}
	security, err := receive(int(count[0]))
	if err != nil {
		return err
	}
	noPassword := false
	for _, kind := range security {
		if kind == 1 {
			noPassword = true
		}
	}
	if !noPassword {
		return errors.New("expected API-tunneled VNC without a second password; no credentials sent")
	}
	if err := writeAppleBytes(connection, []byte{1}); err != nil {
		return err
	}
	status, err := receive(4)
	if err != nil {
		return err
	}
	if binary.BigEndian.Uint32(status) != 0 {
		return errors.New("VNC authentication failed; no credentials sent")
	}
	if err := writeAppleBytes(connection, []byte{1}); err != nil {
		return err
	}
	header, err := receive(24)
	if err != nil {
		return err
	}
	nameSize := binary.BigEndian.Uint32(header[20:24])
	if nameSize > 4096 {
		return errors.New("invalid VNC server name length; no credentials sent")
	}
	_, err = receive(int(nameSize))
	return err
}

func printableUS(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		if value[i] < 32 || value[i] > 126 {
			return false
		}
	}
	return true
}

func typeAppleValue(ctx context.Context, connection net.Conn, value string, enter bool) error {
	// Check the entire field before even a Shift-down can be emitted.
	if !printableUS(value) {
		return errors.New("this field is not printable US-keyboard text; use the Apple UI directly")
	}
	key := func(symbol uint32, down bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var report [8]byte
		report[0] = 4
		if down {
			report[1] = 1
		}
		binary.BigEndian.PutUint32(report[4:], symbol)
		_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return writeAppleBytes(connection, report[:])
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	pause := func(duration time.Duration) error {
		timer.Reset(duration)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	for _, character := range value {
		shift := character >= 'A' && character <= 'Z' || strings.ContainsRune("~!@#$%^&*()_+{}|:\"<>?", character)
		if shift {
			if err := key(0xffe1, true); err != nil {
				return err
			}
		}
		if err := key(uint32(character), true); err != nil {
			return err
		}
		// Span a USB poll without reaching the guest's repeat threshold.
		// A longer hold produced duplicate letters in Recovery guard paths.
		if err := pause(20 * time.Millisecond); err != nil {
			return err
		}
		if err := key(uint32(character), false); err != nil {
			return err
		}
		if shift {
			if err := key(0xffe1, false); err != nil {
				return err
			}
		}
		if err := pause(80 * time.Millisecond); err != nil {
			return err
		}
	}
	if enter {
		if err := key(0xff0d, true); err != nil {
			return err
		}
		if err := pause(20 * time.Millisecond); err != nil {
			return err
		}
		if err := key(0xff0d, false); err != nil {
			return err
		}
	}
	return pause(200 * time.Millisecond)
}
