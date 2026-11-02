//go:build linux || darwin

package kubevirt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/types"
	"golang.org/x/crypto/ssh"
)

// This runner changes only the API tunnel into a loopback TCP connection.
// Client.Copy, the real OpenSSH batch parser, SFTP protocol and scoped host-key
// policy all remain in the consumer path. No shell interprets guest filenames.
type loopbackCopyRunner struct {
	port string
}

func (r loopbackCopyRunner) LookPath(name string) (string, error) {
	if name == "virtctl" {
		return "/unused/virtctl", nil
	}
	return exec.LookPath(name)
}

func (loopbackCopyRunner) Run(string, ...string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected command outside SFTP")
}

func (r loopbackCopyRunner) RunStdin(input, name string, args ...string) ([]byte, error) {
	arguments := append([]string{"-P", r.port}, args...)
	for i, arg := range arguments {
		if strings.HasPrefix(arg, "ProxyCommand=") {
			arguments[i] = "ProxyCommand=none"
		}
	}
	arguments[len(arguments)-1] = "127.0.0.1"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdin = strings.NewReader(input)
	return command.CombinedOutput()
}

func nativeSFTPServer(t *testing.T) string {
	t.Helper()
	for _, path := range []string{"sftp-server", "/usr/lib/openssh/sftp-server", "/usr/lib/ssh/sftp-server", "/usr/libexec/sftp-server", "/usr/libexec/openssh/sftp-server"} {
		if binary, err := exec.LookPath(path); err == nil {
			return binary
		}
	}
	t.Skip("OpenSSH sftp-server required for real filename and recursive-copy coverage")
	return ""
}

func copyTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func serveCopySession(connection net.Conn, signer ssh.Signer, serverBinary, root string) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	server, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		for request := range requests {
			var subsystem struct{ Name string }
			if request.Type != "subsystem" || ssh.Unmarshal(request.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			command := exec.CommandContext(ctx, serverBinary)
			command.Dir = root
			command.Stdin, command.Stdout, command.Stderr = channel, channel, io.Discard
			status := uint32(0)
			if err := command.Run(); err != nil {
				status = 1
			}
			cancel()
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			_ = channel.Close()
			_, _ = io.Copy(io.Discard, channel)
			_ = server.Wait()
			return
		}
		_ = channel.Close()
	}
}

func TestCopyOpenSSHRoundTripLiteralPathsRecursiveAndChangedKey(t *testing.T) {
	if _, err := exec.LookPath("sftp"); err != nil {
		t.Skip("OpenSSH sftp required")
	}
	serverBinary := nativeSFTPServer(t)
	guestRoot := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	signer := copyTestSigner(t)
	var signerMu sync.Mutex
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			signerMu.Lock()
			current := signer
			signerMu.Unlock()
			go serveCopySession(connection, current, serverBinary, guestRoot)
		}
	}()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".ssh", "known_hosts")
	globalContents := "do-not-read-or-modify global trust sentinel\n"
	if err := os.WriteFile(global, []byte(globalContents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(copyTrustRoot(t), "cache %h %p ' \" $()"))
	client := NewClientWithRunner("guests", loopbackCopyRunner{port: port})
	client.Context = "production"
	name := "-literal [x]*? ' \" \\ $(); & : %h ${HOME}"
	local := filepath.Join(t.TempDir(), name)
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte(i)
	}
	if err := os.WriteFile(local, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := types.CopyOptions{LocalPath: local, GuestPath: name, User: "test"}
	if err := client.Copy("web", opts); err != nil {
		t.Fatalf("upload metacharacter filename: %v", err)
	}
	assertCopyFile(t, filepath.Join(guestRoot, name), payload)
	opts.LocalPath, opts.FromGuest = filepath.Join(t.TempDir(), name), true
	if err := client.Copy("web", opts); err != nil {
		t.Fatalf("download metacharacter filename: %v", err)
	}
	assertCopyFile(t, opts.LocalPath, payload)

	tree := filepath.Join(t.TempDir(), "tree [x]*? ' \" \\ $;:")
	if err := os.MkdirAll(filepath.Join(tree, "nested ; $()", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join("nested ; $()", name)
	if err := os.WriteFile(filepath.Join(tree, nested), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".hidden"), payload[:16], 0o600); err != nil {
		t.Fatal(err)
	}
	withoutRecursive := types.CopyOptions{LocalPath: tree, GuestPath: "not-recursive", User: "test"}
	if err := client.Copy("web", withoutRecursive); err == nil {
		t.Fatal("directory copied without --recursive")
	}
	treeOptions := types.CopyOptions{LocalPath: tree, GuestPath: "copied tree [x]*?", User: "test", Recursive: true}
	if err := client.Copy("web", treeOptions); err != nil {
		t.Fatalf("recursive upload: %v", err)
	}
	assertCopyFile(t, filepath.Join(guestRoot, treeOptions.GuestPath, nested), payload)
	treeOptions.LocalPath, treeOptions.FromGuest = filepath.Join(t.TempDir(), "downloaded tree"), true
	if err := client.Copy("web", treeOptions); err != nil {
		t.Fatalf("recursive download: %v", err)
	}
	assertCopyFile(t, filepath.Join(treeOptions.LocalPath, nested), payload)
	assertCopyFile(t, filepath.Join(treeOptions.LocalPath, ".hidden"), payload[:16])
	if info, err := os.Stat(filepath.Join(treeOptions.LocalPath, "nested ; $()", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty nested directory lost: %v", err)
	}

	pins, err := filepath.Glob(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "corral", "guest-ssh", "*", "known_hosts"))
	if err != nil || len(pins) != 1 {
		t.Fatalf("expected one scoped host-key database: %v, %v", pins, err)
	}
	pinned, err := os.ReadFile(pins[0])
	if err != nil || len(pinned) == 0 {
		t.Fatalf("first-use key was not pinned: %v", err)
	}
	changed := copyTestSigner(t)
	signerMu.Lock()
	signer = changed
	signerMu.Unlock()
	opts.FromGuest, opts.GuestPath = false, "must-not-exist"
	if err := client.Copy("web", opts); err == nil {
		t.Fatal("changed host key was silently accepted")
	}
	if _, err := os.Stat(filepath.Join(guestRoot, opts.GuestPath)); !os.IsNotExist(err) {
		t.Fatalf("changed-key refusal still wrote the destination: %v", err)
	}
	assertCopyFile(t, pins[0], pinned)
	assertCopyFile(t, global, []byte(globalContents))
	// The same guest name in another context has its own first-use decision.
	client.Context = "another-context"
	if err := client.Copy("web", opts); err != nil {
		t.Fatalf("independent context reused the old host-key pin: %v", err)
	}
}

func assertCopyFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("roundtrip bytes differ for %q: %v", path, err)
	}
}
