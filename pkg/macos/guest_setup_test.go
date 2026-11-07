package macos

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGuestPreflightRequiresExplicitBoolean(t *testing.T) {
	for _, invalid := range []string{
		`{"guest":"verified"}`, `{"guest":"verified","auth_present":null}`,
		`{"guest":"verified","auth_present":"false"}`, `{"guest":"verified","auth_present":1}`,
		`{"guest":"other","auth_present":true}`, `[]`, `{"guest":"verified","auth_present":true} trailing`,
	} {
		if _, err := parseGuestPreflight([]byte(invalid)); err == nil {
			t.Fatalf("accepted unverified guest state %s", invalid)
		}
	}
	for _, present := range []bool{true, false} {
		body, _ := json.Marshal(map[string]any{"guest": "verified", "auth_present": present})
		if got, err := parseGuestPreflight(body); err != nil || got != present {
			t.Fatalf("boolean state failed: %v, %v", got, err)
		}
	}
}

func guestTestTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVirtualGuestEvidenceRejectsRegistryHeadersAndInactiveObjects(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required to exercise the guest's guard")
	}
	dir := t.TempDir()
	sysctl := guestTestTool(t, dir, "sysctl", `printf '%s\n' "$GUARD_HYPERVISOR"`)
	ioreg := guestTestTool(t, dir, "ioreg", `printf '%s\n' "$GUARD_REGISTRY"; exit "${GUARD_REGISTRY_STATUS:-0}"`)
	guards := map[string]string{"preflight": virtualGuestGuard}
	for name, script := range map[string]string{"profile": guestProfile, "bridge": beeperBridge} {
		start := strings.Index(script, "virtual_guest() {")
		if start < 0 {
			t.Fatalf("%s has no guest guard", name)
		}
		end := strings.Index(script[start:], "\n}\n")
		if end < 0 {
			t.Fatalf("%s guard is incomplete", name)
		}
		guards[name] = script[start : start+end+3]
	}
	for name, guard := range guards {
		t.Run(name, func(t *testing.T) {
			guard = strings.ReplaceAll(guard, "/usr/sbin/sysctl", guestShellQuote(sysctl))
			guard = strings.ReplaceAll(guard, "/usr/sbin/ioreg", guestShellQuote(ioreg))
			for _, tc := range []struct {
				name, hypervisor, registry, status string
				want                               bool
			}{
				{"visible hypervisor", "1", "", "1", true},
				{"empty", "0", "", "0", false},
				{"root header", "0", "+-o Root <class IORegistryEntry, id 0x100, registered, matched, active, retain 1>", "0", false},
				{"plist header", "0", "<?xml version=\"1.0\"?><plist><dict/></plist>", "0", false},
				{"wrong class", "0", "  +-o virtio <class AppleVirtIO, id 0x100, registered, matched, active, retain 1>", "0", false},
				{"inactive", "0", "  +-o virtio <class AppleVirtIOPCITransport, id 0x100, registered, matched, inactive, retain 1>", "0", false},
				{"unmatched", "0", "  +-o virtio <class AppleVirtIOPCITransport, id 0x100, registered, !matched, active, retain 1>", "0", false},
				{"live driver", "0", "  | +-o virtio <class AppleVirtIOPCITransport, id 0xAb12, registered, matched, active, retain 1>", "0", true},
				{"failed inspection", "0", "  +-o virtio <class AppleVirtIOPCITransport, id 0x100, registered, matched, active, retain 1>", "1", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cmd := exec.Command(bash, "-c", guard+"\nvirtual_guest")
					cmd.Env = append(os.Environ(), "GUARD_HYPERVISOR="+tc.hypervisor, "GUARD_REGISTRY="+tc.registry, "GUARD_REGISTRY_STATUS="+tc.status)
					if err := cmd.Run(); (err == nil) != tc.want {
						t.Fatalf("virtual guest evidence accepted=%v, want %v", err == nil, tc.want)
					}
				})
			}
		})
	}
}

func guestTestTarget(t *testing.T) GuestTarget {
	t.Helper()
	cache := t.TempDir()
	key := filepath.Join(cache, "identity")
	if err := os.WriteFile(key, []byte("test private identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	return GuestTarget{Name: "guest", Namespace: "guests", KubeContext: "explicit", User: "bridge", IdentityFile: key, CacheDir: cache}
}

func TestGuestTransportRefusesUntrustedLocalFilesAndScopesHostKeys(t *testing.T) {
	target := guestTestTarget(t)
	original := target.IdentityFile
	if err := os.Chmod(original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := guestSSHTransport(target); err == nil {
		t.Fatal("accepted a publicly readable identity")
	}
	if err := os.Chmod(original, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(target.CacheDir, "identity-link")
	if err := os.Symlink(original, link); err != nil {
		t.Fatal(err)
	}
	target.IdentityFile = link
	if _, err := guestSSHTransport(target); err == nil {
		t.Fatal("accepted a symlink identity")
	}
	target.IdentityFile = original
	if _, err := guestSSHTransport(target); err != nil {
		t.Fatal(err)
	}
	scopes, err := filepath.Glob(filepath.Join(target.CacheDir, "guest-ssh", "*", "known_hosts"))
	if err != nil || len(scopes) != 1 {
		t.Fatalf("missing scoped host pin: %v %v", scopes, err)
	}
	if err := os.Remove(scopes[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, scopes[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := guestSSHTransport(target); err == nil {
		t.Fatal("accepted a symlink host-key database")
	}
	target.KubeContext = "another-context"
	if _, err := guestSSHTransport(target); err != nil {
		t.Fatalf("context did not isolate host-key trust: %v", err)
	}
}

func TestGuestProxyPreservesLiteralPercentTokensAndShellCharacters(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH is required to exercise ProxyCommand expansion")
	}
	target := guestTestTarget(t)
	target.KubeContext = "context %h %p %n ' $(false)"
	dir := t.TempDir()
	arguments := filepath.Join(dir, "arguments")
	guestTestTool(t, dir, "virtctl", `printf '%s\000' "$@" > "$GUEST_PROXY_ARGUMENTS"; exit 1`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GUEST_PROXY_ARGUMENTS", arguments)
	transport, err := guestSSHTransport(target)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ssh, append(transport, "true")...)
	// OpenSSH runs ProxyCommand through $SHELL -c; pin POSIX sh so the test
	// is hermetic regardless of the developer's login shell (fish breaks it).
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	// The fake proxy exits before SSH negotiation; no network or guest is used.
	_ = cmd.Run()
	body, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatalf("SSH did not execute the scoped proxy: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(body), "\x00"), "\x00")
	want := []string{"--context", target.KubeContext, "-n", target.Namespace, "port-forward", "--stdio=true", "vmi/" + target.Name, "22"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxy arguments changed: got %q, want %q", got, want)
	}
}

func guestSetupFixture(t *testing.T) (GuestTarget, string) {
	t.Helper()
	target := guestTestTarget(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("GUEST_CALLS", log)
	t.Setenv("GUEST_PREFLIGHT", `{"guest":"verified","auth_present":true}`)
	t.Setenv("GUEST_VMI", `{"metadata":{"name":"guest","labels":{"corral.dev/macos":"guest","app.kubernetes.io/managed-by":"corral-macos"}},"status":{"phase":"Running"}}`)
	t.Setenv("BW_SESSION", "test-session-not-a-secret")
	guestTestTool(t, dir, "kubectl", `printf 'target\n' >> "$GUEST_CALLS"; printf '%s\n' "$GUEST_VMI"`)
	guestTestTool(t, dir, "ssh", `
for argument do command=$argument; done
# Decode only the shell-quoted argument vector. The embedded script is an
# argument to set, never executed by this fixture.
eval "set -- $command"
for argument do
  [ "$argument" != 'local-password' ] || exit 10
done
if [ "$1" = /usr/bin/sudo ]; then
  IFS= read -r password
  [ "$password" = 'local-password' ] || exit 9
  shift 6
fi
[ "$1" = /bin/bash ] && [ "$2" = -c ] || exit 7
shift 3
script=$1
shift
case "$script" in
  corral-macos-guest)
    printf 'preflight\n' >> "$GUEST_CALLS"
    printf '%s\n' "$GUEST_PREFLIGHT" ;;
  guest_profile.sh)
    printf 'profile\n' >> "$GUEST_CALLS"
    printf 'profile state\n'
    exit "${GUEST_PROFILE_STATUS:-0}" ;;
  beeper_bridge.sh)
    printf 'bridge\n' >> "$GUEST_CALLS"
    [ "$#" = 1 ] || exit 8
    printf 'bridge state\n' ;;
  *) exit 7 ;;
esac`)
	guestTestTool(t, dir, "bw", `
printf 'vault\n' >> "$GUEST_CALLS"
case "$1" in
  unlock) exit 0 ;;
  get) printf '{"login":{"username":"%s","password":"local-password"}}\n' "${GUEST_VAULT_USER:-bridge}" ;;
  *) exit 1 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return target, log
}

func TestGuestSetupTrustChecksPrecedeVaultAndCheckNeverConsumesAuth(t *testing.T) {
	for _, tc := range []struct {
		name, vmi, preflight string
		check, wantError     bool
		calls                string
	}{
		{"unowned target", `{"metadata":{"name":"guest"},"status":{"phase":"Running"}}`, "", false, true, "target\n"},
		{"malformed guest state", "", `{"guest":"verified","auth_present":"true"}`, false, true, "target\npreflight\n"},
		{"check", "", "", true, false, "target\npreflight\nprofile\nbridge\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, log := guestSetupFixture(t)
			if tc.vmi != "" {
				t.Setenv("GUEST_VMI", tc.vmi)
			}
			if tc.preflight != "" {
				t.Setenv("GUEST_PREFLIGHT", tc.preflight)
			}
			var output bytes.Buffer
			err := SetupGuest(context.Background(), GuestSetupOptions{Target: target, Check: tc.check}, &output)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected setup result: %v", err)
			}
			body, readErr := os.ReadFile(log)
			if readErr != nil || string(body) != tc.calls {
				t.Fatalf("unexpected trust/credential ordering: %q %v", body, readErr)
			}
			if tc.check && (!strings.Contains(output.String(), "profile state") || !strings.Contains(output.String(), "bridge state")) {
				t.Fatal("guest inspection output was discarded")
			}
		})
	}
}

func TestGuestSetupPreservesExistingAuthAndSendsAdminPasswordOnlyOnStdin(t *testing.T) {
	target, log := guestSetupFixture(t)
	var output bytes.Buffer
	err := SetupGuest(context.Background(), GuestSetupOptions{Target: target, AdminItem: "selected-admin"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(log)
	if err != nil || string(body) != "target\npreflight\nvault\nvault\nprofile\nbridge\n" {
		t.Fatalf("unexpected existing-auth setup: %q %v", body, err)
	}
	if strings.Contains(output.String(), "local-password") {
		t.Fatal("admin password appeared in output")
	}
	if _, err := os.Stat(filepath.Join(target.CacheDir, "beeper-auth")); !os.IsNotExist(err) {
		t.Fatal("existing guest auth accessed local native auth")
	}
}

func TestGuestSetupRejectsMismatchedAdminBeforeGuestMutation(t *testing.T) {
	target, log := guestSetupFixture(t)
	t.Setenv("GUEST_VAULT_USER", "another-account")
	if err := SetupGuest(context.Background(), GuestSetupOptions{Target: target, AdminItem: "selected-admin"}, nil); err == nil {
		t.Fatal("accepted mismatched GUI admin")
	}
	body, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(body), "profile") || strings.Contains(string(body), "bridge") {
		t.Fatalf("mismatched admin mutated guest: %q %v", body, err)
	}
}

// A loopback-only SSH server exercises OpenSSH's real TOFU behavior, without a
// cluster, sshd installation, real guest, or authentication material.
func TestGuestSSHTrustPinsFirstKeyAndRejectsChangedKey(t *testing.T) {
	sshBinary, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH is required for host-key trust behavior")
	}
	makeSigner := func() ssh.Signer {
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
	first, changed := makeSigner(), makeSigner()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for _, signer := range []ssh.Signer{first, first, changed} {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			guestTestSSHConnection(connection, signer)
		}
	}()
	target := guestTestTarget(t)
	transport, err := guestSSHTransport(target)
	if err != nil {
		t.Fatal(err)
	}
	for i, argument := range transport {
		if strings.HasPrefix(argument, "ProxyCommand=") {
			transport[i] = "ProxyCommand=none"
		}
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// The public destination changes only for this loopback test; the actual
	// context-scoped HostKeyAlias and known-hosts database are left intact.
	arguments := append(append([]string(nil), transport[:len(transport)-1]...), "-p", port, "127.0.0.1", "true")
	for attempt := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result, runErr := exec.CommandContext(ctx, sshBinary, arguments...).CombinedOutput()
		cancel()
		if attempt < 2 && runErr != nil {
			t.Fatalf("unchanged scoped host key rejected on attempt %d: %v: %s", attempt, runErr, result)
		}
		if attempt == 2 && runErr == nil {
			t.Fatal("a changed SSH host key was silently accepted")
		}
	}
}

func guestTestSSHConnection(connection net.Conn, signer ssh.Signer) {
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	server, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		return
	}
	defer func() { _ = server.Close() }()
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
			if request.Type != "exec" {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			_ = channel.Close()
			// Complete the channel close exchange before closing the TCP
			// transport; otherwise OpenSSH can lose the buffered exit status.
			_, _ = io.Copy(io.Discard, channel)
			_ = server.Wait()
			return
		}
		_ = channel.Close()
	}
}

func TestGuestSetupRejectsAmbientContextBeforeSubprocesses(t *testing.T) {
	target, log := guestSetupFixture(t)
	target.KubeContext = ""
	if err := SetupGuest(context.Background(), GuestSetupOptions{Target: target}, nil); err == nil {
		t.Fatal("accepted ambient Kubernetes context")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("an external command ran before explicit context validation")
	}
}

func TestGuestCheckReportsBothProfilesWhenOneIsUnhealthy(t *testing.T) {
	target, log := guestSetupFixture(t)
	t.Setenv("GUEST_PROFILE_STATUS", "1")
	var output bytes.Buffer
	if err := SetupGuest(context.Background(), GuestSetupOptions{Target: target, Check: true}, &output); err == nil {
		t.Fatal("unhealthy guest inspection succeeded")
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "target\npreflight\nprofile\nbridge\n" {
		t.Fatalf("inspection skipped remaining guest state or consumed auth: %q %v", calls, err)
	}
	if !strings.Contains(output.String(), "profile state") || !strings.Contains(output.String(), "bridge state") {
		t.Fatal("unhealthy inspection discarded public guest output")
	}
}
