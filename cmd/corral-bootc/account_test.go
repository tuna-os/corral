//go:build bootc

package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

// scriptHash pulls the crypt hash out of a rendered account script.
func scriptHash(t *testing.T, script string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^hash='([^']*)'$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("script has no hash= line:\n%s", script)
	}
	return m[1]
}

func fakeTerminal(t *testing.T, tty bool, reads ...string) *int {
	t.Helper()
	oldRead, oldTerm := readPassword, isTerminal
	calls := 0
	readPassword = func(int) ([]byte, error) {
		v := reads[calls%len(reads)]
		calls++
		return []byte(v), nil
	}
	isTerminal = func(int) bool { return tty }
	t.Cleanup(func() { readPassword, isTerminal = oldRead, oldTerm })
	return &calls
}

func TestLoginProvisionScript_PromptHashesWithoutLeakingPlaintext(t *testing.T) {
	calls := fakeTerminal(t, true, "hunter2")

	var out bytes.Buffer
	script, err := loginProvisionScript(loginOptions{user: "tuna", passwordPrompt: true}, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("password reads = %d, want prompt and confirmation", *calls)
	}
	if err := sha512_crypt.New().Verify(scriptHash(t, script), []byte("hunter2")); err != nil {
		t.Fatalf("script hash does not verify the prompted password: %v", err)
	}
	if strings.Contains(script, "hunter2") {
		t.Fatal("plaintext password reached the provisioning script")
	}
	if !strings.Contains(out.String(), "Password:") || !strings.Contains(out.String(), "Confirm password:") {
		t.Fatalf("prompt output = %q", out.String())
	}
}

func TestLoginProvisionScript_PromptRejectsMismatch(t *testing.T) {
	fakeTerminal(t, true, "one", "two")
	_, err := loginProvisionScript(loginOptions{user: "tuna", passwordPrompt: true}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("error = %v, want mismatch", err)
	}
}

func TestLoginProvisionScript_PromptNeedsTerminal(t *testing.T) {
	calls := fakeTerminal(t, false, "never")
	_, err := loginProvisionScript(loginOptions{user: "tuna", passwordPrompt: true}, strings.NewReader("tuna\n"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("error = %v, want a pointer to --password-stdin", err)
	}
	if *calls != 0 {
		t.Fatalf("read %d passwords from a non-terminal", *calls)
	}
}

func TestLoginProvisionScript_Stdin(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{name: "printf without newline", in: "tuna", want: "tuna"},
		{name: "echo newline", in: "tuna\n", want: "tuna"},
		{name: "crlf", in: "tuna\r\n", want: "tuna"},
		{name: "first line only", in: "s3cret pass\nignored\n", want: "s3cret pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			script, err := loginProvisionScript(loginOptions{user: "tuna-test", passwordStdin: true}, strings.NewReader(tc.in), &out)
			if err != nil {
				t.Fatal(err)
			}
			if err := sha512_crypt.New().Verify(scriptHash(t, script), []byte(tc.want)); err != nil {
				t.Fatalf("hash does not verify %q: %v", tc.want, err)
			}
			if strings.Contains(script, "s3cret") {
				t.Fatal("plaintext password reached the provisioning script")
			}
			if out.Len() != 0 {
				t.Fatalf("--password-stdin printed a prompt: %q", out.String())
			}
		})
	}
}

func TestLoginProvisionScript_StdinRejectsEmpty(t *testing.T) {
	for _, in := range []string{"", "\n"} {
		_, err := loginProvisionScript(loginOptions{user: "tuna", passwordStdin: true}, strings.NewReader(in), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
			t.Fatalf("stdin %q: error = %v, want empty-password refusal", in, err)
		}
	}
}

func TestLoginProvisionScript_RejectsUnsafePasswordModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts loginOptions
		want string
	}{
		{name: "hash without user", opts: loginOptions{passwordHash: "$6$hash"}, want: "require --user"},
		{name: "stdin without user", opts: loginOptions{passwordStdin: true}, want: "require --user"},
		{name: "prompt and hash", opts: loginOptions{user: "tuna", passwordHash: "$6$hash", passwordPrompt: true}, want: "mutually exclusive"},
		{name: "prompt and stdin", opts: loginOptions{user: "tuna", passwordPrompt: true, passwordStdin: true}, want: "mutually exclusive"},
		{name: "stdin and hash", opts: loginOptions{user: "tuna", passwordHash: "$6$hash", passwordStdin: true}, want: "mutually exclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loginProvisionScript(tc.opts, strings.NewReader("x\n"), &bytes.Buffer{})
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
		for _, flag := range []string{"user", "password-prompt", "password-stdin", "password-hash"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("%s is missing --%s", name, flag)
			}
		}
	}
}
