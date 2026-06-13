package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveSSHKey_Passthrough(t *testing.T) {
	for _, s := range []string{"", "ssh-ed25519 AAAAC3Nza... me@host", "ssh-rsa AAAB3... x"} {
		got, err := resolveSSHKey(s)
		if err != nil || got != strings.TrimSpace(s) {
			t.Errorf("resolveSSHKey(%q) = %q, %v — want passthrough", s, got, err)
		}
	}
}

func TestResolveSSHKey_GitHubUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hanthor.keys" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("ssh-ed25519 AAAkey1\nssh-rsa AAAkey2\n"))
	}))
	defer srv.Close()
	orig := githubKeysBase
	githubKeysBase = srv.URL
	defer func() { githubKeysBase = orig }()

	for _, ref := range []string{"gh:hanthor", "github:hanthor", "@hanthor"} {
		got, err := resolveSSHKey(ref)
		if err != nil {
			t.Fatalf("resolveSSHKey(%q): %v", ref, err)
		}
		if !strings.Contains(got, "AAAkey1") || !strings.Contains(got, "AAAkey2") {
			t.Errorf("resolveSSHKey(%q) = %q, want both keys", ref, got)
		}
	}

	// Unknown user → error, not a silent empty key.
	if _, err := resolveSSHKey("gh:nonexistent"); err == nil {
		t.Error("expected error for unknown GitHub user")
	}
}

func TestResolveSSHKey_InvalidUsername(t *testing.T) {
	for _, ref := range []string{"gh:", "gh:has space", "gh:way-too-long-" + strings.Repeat("x", 50)} {
		if _, err := resolveSSHKey(ref); err == nil {
			t.Errorf("resolveSSHKey(%q) should reject the username", ref)
		}
	}
}
