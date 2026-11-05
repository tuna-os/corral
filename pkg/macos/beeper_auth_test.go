//go:build linux || darwin

package macos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func authTestDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBeeperIdentityRequiresRawAccount(t *testing.T) {
	for _, body := range []string{`{}`, `{"error":"expired"}`, `{"user":{},"userInfo":{"username":""}}`, `{"user":null,"userInfo":{"username":"account"}}`, `{"user":[],"userInfo":{"username":"account"}}`, `{"user":{},"userInfo":{"username":true}}`, "not JSON"} {
		if validBeeperIdentity([]byte(body)) {
			t.Errorf("accepted non-identity %s", body)
		}
	}
	if !validBeeperIdentity([]byte(`{"user":{},"userInfo":{"username":"account"}}`)) {
		t.Fatal("rejected actual raw identity shape")
	}
}

func TestBeeperExistingAccountNeverConsultsSelectedVaultItem(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale", true: "valid"}[valid], func(t *testing.T) {
			cache := authTestDirectory(t)
			directory := filepath.Join(cache, "beeper-auth")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(directory, "config.json")
			original := []byte(`{"native":"existing-account-token"}`)
			if err := os.WriteFile(config, original, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BEEPER_USERNAME", "inherited-other-account")
			t.Setenv("BEEPER_PASSWORD", "inherited-password")
			t.Setenv("BW_SESSION", "inherited-session")
			calls := 0
			client := beeperAuthClient{
				binary: func(context.Context, string) (string, error) { return "bbctl", nil },
				login: func(context.Context, string) (string, string, error) {
					t.Fatal("existing state consulted vault")
					return "", "", nil
				},
				run: func(_ context.Context, environment []string, binary string, arguments ...string) ([]byte, error) {
					calls++
					if !reflect.DeepEqual(arguments, []string{"--config", config, "whoami", "--raw"}) {
						t.Fatalf("unexpected auth mutation: %v", arguments)
					}
					for _, entry := range environment {
						if strings.HasPrefix(entry, "BEEPER_") || strings.HasPrefix(entry, "BW_SESSION=") {
							t.Fatalf("credential override inherited")
						}
					}
					if valid {
						return []byte(`{"user":{},"userInfo":{"username":"original-account"}}`), nil
					}
					return []byte(`{"error":"expired"}`), nil // bbctl can exit zero on an error.
				},
			}
			result, err := prepareBeeperAuth(context.Background(), cache, "different-vault-account", client)
			if valid && (err != nil || result != config) {
				t.Fatalf("existing auth: %s %v", result, err)
			}
			if !valid && (err == nil || !strings.Contains(err.Error(), beeperAuthRepair)) {
				t.Fatalf("stale auth was not refused: %v", err)
			}
			body, readErr := os.ReadFile(config)
			if readErr != nil || !reflect.DeepEqual(body, original) || calls != 1 {
				t.Fatal("existing state was modified or retried")
			}
		})
	}
}

func TestBeeperNewNativeStateUsesPrivateEnvironment(t *testing.T) {
	cache := authTestDirectory(t)
	config := filepath.Join(cache, "beeper-auth", "config.json")
	calls := 0
	client := beeperAuthClient{
		binary: func(context.Context, string) (string, error) { return "bbctl", nil },
		login: func(_ context.Context, item string) (string, string, error) {
			if item != "selected" {
				t.Fatal("wrong selected item")
			}
			return "dummy-user", "dummy-password", nil
		},
		run: func(_ context.Context, environment []string, binary string, arguments ...string) ([]byte, error) {
			calls++
			for _, argument := range arguments {
				if strings.Contains(argument, "dummy-") {
					t.Fatal("credential in argv")
				}
			}
			joined := strings.Join(environment, "\n")
			if arguments[2] == "login-password" {
				if !strings.Contains(joined, "BEEPER_USERNAME=dummy-user") || !strings.Contains(joined, "BEEPER_PASSWORD=dummy-password") {
					t.Fatal("private credentials missing")
				}
				return nil, os.WriteFile(config, []byte(`{"native":"token"}`), 0o600)
			}
			if strings.Contains(joined, "dummy-") {
				t.Fatal("credential leaked into identity check")
			}
			return []byte(`{"user":{},"userInfo":{"username":"dummy-user"}}`), nil
		},
	}
	path, err := prepareBeeperAuth(context.Background(), cache, "selected", client)
	if err != nil || path != config || calls != 2 {
		t.Fatalf("native auth: path=%s calls=%d error=%v", path, calls, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("native state is not private")
	}
}

func TestBeeperFailedLoginRetainsPartialStateWithoutRetry(t *testing.T) {
	cache := authTestDirectory(t)
	config := filepath.Join(cache, "beeper-auth", "config.json")
	calls := 0
	client := beeperAuthClient{
		binary: func(context.Context, string) (string, error) { return "bbctl", nil },
		login:  func(context.Context, string) (string, string, error) { return "dummy-user", "dummy-password", nil },
		run: func(context.Context, []string, string, ...string) ([]byte, error) {
			calls++
			if err := os.WriteFile(config, []byte("partial-native-state"), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil, errors.New("private raw error")
		},
	}
	_, err := prepareBeeperAuth(context.Background(), cache, "selected", client)
	if err == nil || strings.Contains(err.Error(), "private raw error") || calls != 1 {
		t.Fatalf("failed auth: %v calls=%d", err, calls)
	}
	body, err := os.ReadFile(config)
	if err != nil || string(body) != "partial-native-state" {
		t.Fatal("partial auth was deleted or replaced")
	}
}

func TestBeeperArtifactPinsSupportedClients(t *testing.T) {
	for _, system := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			artifact, err := beeperArtifact(system, arch)
			if err != nil || len(beeperDigests[artifact]) != 64 {
				t.Fatalf("missing pin for %s/%s", system, arch)
			}
		}
	}
	for _, pair := range [][2]string{{"windows", "amd64"}, {"linux", "386"}, {"freebsd", "arm64"}} {
		if _, err := beeperArtifact(pair[0], pair[1]); err == nil {
			t.Fatalf("accepted unsupported platform %v", pair)
		}
	}
}

func TestPrivateCommandCreatesStatePrivatelyAndSanitizesErrors(t *testing.T) {
	path := filepath.Join(authTestDirectory(t), "native.json")
	_, err := privateCommand(context.Background(), os.Environ(), "/bin/sh", "-c", `printf native > "$1"`, "test", path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("native state not private at creation")
	}
	body, err := privateCommand(context.Background(), os.Environ(), "/bin/sh", "-c", "printf secret-error >&2; exit 1")
	if err == nil || len(body) != 0 || strings.Contains(err.Error(), "secret-error") {
		t.Fatal("private execution published process output")
	}
}
