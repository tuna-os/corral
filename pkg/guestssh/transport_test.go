//go:build linux || darwin

package guestssh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testPrivateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testTarget(t *testing.T) Target {
	t.Helper()
	return Target{Context: "production", Namespace: "guests", Name: "web", User: "ubuntu", CacheDir: filepath.Join(testPrivateDir(t), "corral")}
}

func TestTrustIsScopedAndNeverUsesGlobalFiles(t *testing.T) {
	target := testTarget(t)
	seen := map[string]bool{}
	for _, mutate := range []func(*Target){
		func(*Target) {},
		func(v *Target) { v.Context += "-other" },
		func(v *Target) { v.Namespace += "-other" },
		func(v *Target) { v.Name += "-other" },
	} {
		copy := target
		mutate(&copy)
		args, err := Options(copy)
		if err != nil {
			t.Fatal(err)
		}
		var alias string
		for _, arg := range args {
			if strings.HasPrefix(arg, "HostKeyAlias=") {
				alias = arg
			}
		}
		if alias == "" || seen[alias] {
			t.Fatalf("guest scope reused host-key alias: %q", alias)
		}
		seen[alias] = true
		joined := strings.Join(args, "\n")
		for _, want := range []string{"GlobalKnownHostsFile=/dev/null", "StrictHostKeyChecking=accept-new", "UpdateHostKeys=no", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "BatchMode=yes", "IdentityFile=none", "-F\n/dev/null"} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing trust/auth policy %q: %s", want, joined)
			}
		}
		if strings.Contains(joined, "IdentitiesOnly=yes") {
			t.Fatal("agent authentication was disabled without an explicit identity")
		}
	}
}

func TestTransportRefusesUnsafeKeysAndTrustState(t *testing.T) {
	for _, kind := range []string{"key permissions", "key symlink", "key hardlink", "known_hosts permissions", "known_hosts symlink", "known_hosts hardlink", "cache symlink", "cache permissions", "ancestor symlink"} {
		t.Run(kind, func(t *testing.T) {
			target := testTarget(t)
			identityDir := filepath.Join(testPrivateDir(t), "ssh")
			if err := os.Mkdir(identityDir, 0o700); err != nil {
				t.Fatal(err)
			}
			key := filepath.Join(identityDir, "identity")
			if err := os.WriteFile(key, []byte("test identity"), 0o600); err != nil {
				t.Fatal(err)
			}
			target.Identity = key
			if _, err := Options(target); err != nil {
				t.Fatal(err)
			}
			pins, err := filepath.Glob(filepath.Join(target.CacheDir, "*", "known_hosts"))
			if err != nil || len(pins) != 1 {
				t.Fatalf("missing scoped pin: %v, %v", pins, err)
			}
			path := key
			if strings.HasPrefix(kind, "known_hosts") {
				path = pins[0]
			}
			switch {
			case kind == "cache symlink" || kind == "ancestor symlink":
				link := filepath.Join(testPrivateDir(t), "link")
				if err := os.Symlink(target.CacheDir, link); err != nil {
					t.Fatal(err)
				}
				target.CacheDir = link
				if kind == "ancestor symlink" {
					target.CacheDir = filepath.Join(link, "child")
				}
			case kind == "cache permissions":
				if err := os.Chmod(target.CacheDir, 0o755); err != nil {
					t.Fatal(err)
				}
			case strings.HasSuffix(kind, "permissions"):
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case strings.HasSuffix(kind, "symlink"):
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
					t.Fatal(err)
				}
			case strings.HasSuffix(kind, "hardlink"):
				if err := os.Link(path, filepath.Join(t.TempDir(), "linked")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Options(target); err == nil {
				t.Fatalf("accepted unsafe %s", kind)
			}
		})
	}
}

func TestProxyPreservesPercentAndShellMetacharacters(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH required")
	}
	target := testTarget(t)
	target.Context = "context %h %p %n ' $(false) ; &"
	dir := t.TempDir()
	log := filepath.Join(dir, "arguments")
	proxy := "#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$PROXY_ARGUMENTS\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "virtctl"), []byte(proxy), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PROXY_ARGUMENTS", log)
	args, err := Options(target)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, ssh, append(args, Host, "true")...).Run()
	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(body), "\x00"), "\x00")
	want := []string{"--context", target.Context, "-n", target.Namespace, "port-forward", "--stdio=true", "vmi/" + target.Name, "22"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxy changed arguments: got %q; want %q", got, want)
	}
}

func TestTransportRefusesTrustResetThroughWritableAncestor(t *testing.T) {
	root := testPrivateDir(t)
	ancestor := filepath.Join(root, "shared")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	target := testTarget(t)
	target.CacheDir = filepath.Join(ancestor, "private")
	if _, err := Options(target); err != nil {
		t.Fatal(err)
	}
	pins, err := filepath.Glob(filepath.Join(target.CacheDir, "*", "known_hosts"))
	if err != nil || len(pins) != 1 {
		t.Fatalf("missing scoped pin: %v, %v", pins, err)
	}
	if err := os.WriteFile(pins[0], []byte("existing trusted pin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Options(target); err == nil || !strings.Contains(err.Error(), "writable ancestor") {
		t.Fatalf("accepted writable ancestor above private trust state: %v", err)
	}
	// A writer of the ancestor can move the entire private tree without
	// entering it. Corral must not recreate empty first-use trust afterward.
	moved := filepath.Join(root, "moved-pins")
	if err := os.Rename(target.CacheDir, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := Options(target); err == nil {
		t.Fatal("recreated trust after ancestor replacement")
	}
	if _, err := os.Stat(target.CacheDir); !os.IsNotExist(err) {
		t.Fatalf("unsafe ancestor caused cache mutation: %v", err)
	}
	pin := filepath.Join(moved, filepath.Base(filepath.Dir(pins[0])), "known_hosts")
	if body, err := os.ReadFile(pin); err != nil || string(body) != "existing trusted pin\n" {
		t.Fatalf("existing pin changed: %q, %v", body, err)
	}
}

func TestTransportChecksIdentityAncestorsAndAllowsTrustedStickyDirectories(t *testing.T) {
	root := testPrivateDir(t)
	ancestor := filepath.Join(root, "shared")
	keyDir := filepath.Join(ancestor, "private")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(keyDir, "identity")
	if err := os.WriteFile(key, []byte("test identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := testTarget(t)
	target.Identity = key
	if err := os.Chmod(ancestor, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Options(target); err == nil || !strings.Contains(err.Error(), "writable ancestor") {
		t.Fatalf("accepted writable identity ancestor: %v", err)
	}
	if err := os.Chmod(ancestor, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	target.CacheDir = filepath.Join(ancestor, "private-cache")
	if _, err := Options(target); err != nil {
		t.Fatalf("rejected trusted sticky ancestor: %v", err)
	}
}

func TestTransportRejectsOpenSSHEnvironmentExpansionBeforeMutation(t *testing.T) {
	t.Setenv("SSH_PATH_ESCAPE", "redirected")
	for _, kind := range []string{"identity", "cache"} {
		t.Run(kind, func(t *testing.T) {
			root := testPrivateDir(t)
			target := testTarget(t)
			target.CacheDir = filepath.Join(root, "cache")
			if kind == "cache" {
				target.CacheDir += "-${SSH_PATH_ESCAPE}"
			} else {
				target.Identity = filepath.Join(root, "identity-${SSH_PATH_ESCAPE}")
				if err := os.WriteFile(target.Identity, []byte("test identity"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Options(target); err == nil || !strings.Contains(err.Error(), "OpenSSH expands environment variables") {
				t.Fatalf("accepted environment-expanded %s path: %v", kind, err)
			}
			if _, err := os.Stat(target.CacheDir); !os.IsNotExist(err) {
				t.Fatalf("refused %s path mutated trust: %v", kind, err)
			}
		})
	}
}
