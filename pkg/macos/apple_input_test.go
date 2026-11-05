//go:build linux || darwin

package macos

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAppleInputRejectsWholeNonUSFieldBeforeAnyBytes(t *testing.T) {
	for _, value := range []string{"", "good\nbad", "prefixé", "prefix\x7f", "prefix\x00"} {
		client, server := net.Pipe()
		if err := typeAppleValue(context.Background(), client, value, true); err == nil {
			t.Errorf("accepted invalid US input")
		}
		_ = server.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
		var report [8]byte
		if count, _ := server.Read(report[:]); count != 0 {
			t.Fatal("sent partial invalid field")
		}
		_ = client.Close()
		_ = server.Close()
	}
}

func TestAppleInputExplicitShiftPacingAndDrain(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	type keyReport struct {
		down   bool
		symbol uint32
	}
	expected := []keyReport{{true, 0xffe1}, {true, 'A'}, {false, 'A'}, {false, 0xffe1}, {true, 0xffe1}, {true, '!'}, {false, '!'}, {false, 0xffe1}, {true, 'z'}, {false, 'z'}, {true, 0xff0d}, {false, 0xff0d}}
	result := make(chan error, 1)
	go func() { result <- typeAppleValue(context.Background(), client, "A!z", true) }()
	var reports []keyReport
	var lastCharacter time.Time
	for range len(expected) {
		var body [8]byte
		if _, err := io.ReadFull(server, body[:]); err != nil {
			t.Fatal(err)
		}
		if body[0] != 4 || body[2] != 0 || body[3] != 0 {
			t.Fatal("invalid RFB key encoding")
		}
		report := keyReport{body[1] == 1, binary.BigEndian.Uint32(body[4:])}
		if report.down && (report.symbol == 'A' || report.symbol == '!' || report.symbol == 'z') {
			if !lastCharacter.IsZero() && time.Since(lastCharacter) < 30*time.Millisecond {
				t.Fatal("USB reports were not paced")
			}
			lastCharacter = time.Now()
		}
		reports = append(reports, report)
	}
	drainStart := time.Now()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if time.Since(drainStart) < 180*time.Millisecond {
		t.Fatal("proxy closed before keyboard drain")
	}
	if !reflect.DeepEqual(reports, expected) {
		t.Fatalf("wrong key sequence: %#v", reports)
	}
}

func TestAppleInputConfirmationGatesPrecedeExternalCommands(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, option := range []struct {
		field       string
		focused, us bool
	}{{"username", false, true}, {"password", true, false}, {"other", true, true}} {
		err := TypeAppleField(context.Background(), GuestTarget{}, option.field, "synthetic", option.focused, option.us, false)
		if err == nil || strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "inspect") {
			t.Fatalf("confirmation gate failed: %v", err)
		}
	}
}

// This smoke exercises the exported helper with synthetic kubectl/virtctl/bw
// processes. It never reaches a real cluster or vault and is runnable alone.
func TestAppleFieldSyntheticTransport(t *testing.T) {
	for _, mode := range []string{"success", "password", "early-exit", "unowned", "security-error", "oversized-name"} {
		t.Run(mode, func(t *testing.T) {
			directory := authTestDirectory(t)
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"kubectl", "virtctl", "bw"} {
				wrapper := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestAppleSyntheticCommand$ -- %s \"$@\"\n", strings.ReplaceAll(binary, "'", "'\\''"), name)
				if err := os.WriteFile(filepath.Join(directory, name), []byte(wrapper), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(directory, "vault-read")
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CORRAL_APPLE_SYNTHETIC", "1")
			t.Setenv("CORRAL_APPLE_SYNTHETIC_MODE", mode)
			t.Setenv("CORRAL_APPLE_SYNTHETIC_MARKER", marker)
			t.Setenv("BW_SESSION", "synthetic-session")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = TypeAppleField(ctx, GuestTarget{Name: "synthetic", Namespace: "synthetic-ns", KubeContext: "synthetic-context"}, "password", "synthetic-item", true, true, true)
			_, readErr := os.Stat(marker)
			if mode == "success" {
				if err != nil || readErr != nil {
					t.Fatalf("synthetic input failed: %v, vault=%v", err, readErr)
				}
			} else if err == nil || !os.IsNotExist(readErr) {
				t.Fatalf("unsafe transport reached vault: mode=%s error=%v vault=%v", mode, err, readErr)
			}
		})
	}
}

func TestAppleSyntheticCommand(t *testing.T) {
	if os.Getenv("CORRAL_APPLE_SYNTHETIC") != "1" {
		return
	}
	arguments := os.Args
	for len(arguments) > 0 && arguments[0] != "--" {
		arguments = arguments[1:]
	}
	if len(arguments) < 2 {
		os.Exit(2)
	}
	arguments = arguments[1:]
	switch arguments[0] {
	case "kubectl":
		if !reflect.DeepEqual(arguments[1:], []string{"--context", "synthetic-context", "-n", "synthetic-ns", "get", "vmi", "synthetic", "-o", "json"}) {
			os.Exit(3)
		}
		if os.Getenv("CORRAL_APPLE_SYNTHETIC_MODE") == "unowned" {
			fmt.Print(`{"metadata":{"name":"synthetic","labels":{"corral.dev/macos":"synthetic","app.kubernetes.io/managed-by":"other-plugin"}},"status":{"phase":"Running"}}`)
			os.Exit(0)
		}
		fmt.Print(`{"metadata":{"name":"synthetic","labels":{"corral.dev/macos":"synthetic","app.kubernetes.io/managed-by":"corral-macos"}},"status":{"phase":"Running"}}`)
	case "bw":
		if len(arguments) > 1 && arguments[1] == "get" {
			if os.Getenv("BW_SESSION") != "synthetic-session" {
				os.Exit(4)
			}
			if os.WriteFile(os.Getenv("CORRAL_APPLE_SYNTHETIC_MARKER"), []byte("read"), 0o600) != nil {
				os.Exit(5)
			}
			fmt.Print(`{"login":{"username":"synthetic-user","password":"Synthetic!"}}`)
		}
	case "virtctl":
		if !reflect.DeepEqual(arguments[1:], []string{"--context", "synthetic-context", "-n", "synthetic-ns", "vnc", "synthetic", "--proxy-only", "--address=127.0.0.1", "--port=0"}) {
			os.Exit(6)
		}
		if os.Getenv("CORRAL_APPLE_SYNTHETIC_MODE") == "early-exit" {
			fmt.Println(`{"port":1}`)
			os.Exit(0)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(7)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]int{"port": listener.Addr().(*net.TCPAddr).Port})
		connection, err := listener.Accept()
		if err != nil {
			os.Exit(8)
		}
		_ = listener.Close()
		_ = connection.SetDeadline(time.Now().Add(8 * time.Second))
		if err := syntheticRFBServer(connection, os.Getenv("CORRAL_APPLE_SYNTHETIC_MODE")); err != nil {
			_ = connection.Close()
			os.Exit(9)
		}
		_, _ = io.Copy(io.Discard, connection)
		_ = connection.Close()
	default:
		os.Exit(10)
	}
	os.Exit(0)
}

func syntheticRFBServer(connection net.Conn, mode string) error {
	if _, err := connection.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	var version [12]byte
	if _, err := io.ReadFull(connection, version[:]); err != nil {
		return err
	}
	if !bytes.Equal(version[:], []byte("RFB 003.008\n")) {
		return fmt.Errorf("wrong protocol")
	}
	kind := byte(1)
	if mode == "password" {
		kind = 2
	}
	if _, err := connection.Write([]byte{1, kind}); err != nil {
		return err
	}
	var selection [1]byte
	if _, err := io.ReadFull(connection, selection[:]); err != nil {
		return err
	}
	status := []byte{0, 0, 0, 0}
	if mode == "security-error" {
		status[3] = 1
	}
	if _, err := connection.Write(status); err != nil {
		return err
	}
	if _, err := io.ReadFull(connection, selection[:]); err != nil {
		return err
	}
	header := make([]byte, 24)
	if mode == "oversized-name" {
		binary.BigEndian.PutUint32(header[20:], 4097)
	}
	_, err := connection.Write(header)
	return err
}
