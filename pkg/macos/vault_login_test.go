//go:build linux || darwin

package macos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVaultLoginValidatesPrivateResponseWithoutPublishingIt(t *testing.T) {
	directory := authTestDirectory(t)
	script := "#!/bin/sh\nif [ \"$BW_SESSION\" != synthetic-session ]; then exit 4; fi\nif [ \"$1\" = unlock ]; then exit 0; fi\nprintf '%s' \"$CORRAL_SYNTHETIC_VAULT_JSON\"\n"
	if err := os.WriteFile(filepath.Join(directory, "bw"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("BW_SESSION", "synthetic-session")
	for _, fixture := range []struct {
		body  string
		valid bool
	}{
		{`{"login":{"username":"synthetic-user","password":"synthetic-private-password"}}`, true},
		{`private-non-JSON-response`, false},
		{`{"login":{"username":"synthetic-user"}}`, false},
		{`{"login":{"username":"synthetic-user","password":null}}`, false},
		{`{"login":{"username":5,"password":"synthetic-private-password"}}`, false},
		{`{"login":{"username":"synthetic-user","password":"synthetic\u0000private"}}`, false},
	} {
		t.Setenv("CORRAL_SYNTHETIC_VAULT_JSON", fixture.body)
		username, password, err := vaultLogin(context.Background(), "synthetic-item")
		if fixture.valid {
			if err != nil || username != "synthetic-user" || password != "synthetic-private-password" {
				t.Fatal("valid private login was not read")
			}
		} else {
			if err == nil || username != "" || password != "" {
				t.Fatal("invalid vault response accepted")
			}
			if strings.Contains(err.Error(), "synthetic-private-password") || strings.Contains(err.Error(), "private-non-JSON-response") {
				t.Fatal("private vault response published")
			}
		}
	}
}
