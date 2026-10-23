//go:build bootc

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoginProvisionScript_PromptHashesWithoutLeakingPlaintext(t *testing.T) {
	oldReadPassword := readPassword
	calls := 0
	readPassword = func(int) ([]byte, error) {
		calls++
		return []byte("hunter2"), nil
	}
	t.Cleanup(func() { readPassword = oldReadPassword })

	var out bytes.Buffer
	script, err := loginProvisionScript("tuna", "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("password reads = %d, want prompt and confirmation", calls)
	}
	if !strings.Contains(script, "hash='$6$") {
		t.Fatalf("script does not contain a SHA-512 crypt hash:\n%s", script)
	}
	if strings.Contains(script, "hunter2") {
		t.Fatal("plaintext password reached the provisioning script")
	}
	if !strings.Contains(out.String(), "Password:") || !strings.Contains(out.String(), "Confirm password:") {
		t.Fatalf("prompt output = %q", out.String())
	}
}

func TestLoginProvisionScript_RejectsUnsafePasswordModes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		user   string
		hash   string
		prompt bool
		want   string
	}{
		{name: "hash without user", hash: "$6$hash", want: "require --user"},
		{name: "both password modes", user: "tuna", hash: "$6$hash", prompt: true, want: "mutually exclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loginProvisionScript(tc.user, tc.hash, tc.prompt, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoginFlagsOnBootcLifecycleCommands(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"create", "rebuild", "upgrade", "switch"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		for _, flag := range []string{"user", "password-prompt", "password-hash"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("%s is missing --%s", name, flag)
			}
		}
	}
}
