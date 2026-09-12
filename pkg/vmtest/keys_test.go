package vmtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

// keygenRunner stands in for ssh-keygen: it writes a keypair where it was told
// to, which is the only part of ssh-keygen this package depends on.
type keygenRunner struct{ shell.Runner }

func (keygenRunner) Run(name string, args ...string) ([]byte, error) {
	if name != "ssh-keygen" {
		return nil, os.ErrNotExist
	}
	var path string
	for i, arg := range args {
		if arg == "-f" && i+1 < len(args) {
			path = args[i+1]
		}
	}
	if path == "" {
		return nil, os.ErrInvalid
	}
	if err := os.WriteFile(path, []byte("PRIVATE KEY\n"), 0o600); err != nil {
		return nil, err
	}
	return nil, os.WriteFile(path+".pub", []byte("ssh-ed25519 AAAAgenerated corral-vmtest\n"), 0o644)
}

func (keygenRunner) RunStdin(string, string, ...string) ([]byte, error) { return nil, nil }
func (keygenRunner) LookPath(name string) (string, error)               { return "/usr/bin/" + name, nil }

func TestEnsureKeypair_GeneratesAndReuses(t *testing.T) {
	SetRunner(keygenRunner{})
	t.Cleanup(func() { SetRunner(shell.Real{}) })
	fakeLookPath(t, map[string]bool{"ssh-keygen": true})

	dir := filepath.Join(t.TempDir(), "ssh")
	keys, err := EnsureKeypair(dir, "ssh-ed25519 AAAAoperator me@host")
	if err != nil {
		t.Fatalf("EnsureKeypair: %v", err)
	}
	if keys.PrivatePath != filepath.Join(dir, "id_ed25519") {
		t.Errorf("private key = %q", keys.PrivatePath)
	}
	if keys.PublicKey != "ssh-ed25519 AAAAgenerated corral-vmtest" {
		t.Errorf("public key = %q", keys.PublicKey)
	}

	// The run's own key comes first, then the operator's — both are authorised,
	// so a human can log into the VM the run leaves behind.
	authorized := keys.AuthorizedKeys()
	lines := strings.Split(authorized, "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "AAAAgenerated") || !strings.Contains(lines[1], "AAAAoperator") {
		t.Errorf("authorized keys = %q", authorized)
	}

	// A second run against the same directory reuses the key, so it does not
	// lock itself out of a VM the first run left behind.
	SetRunner(shell.NewFake())
	again, err := EnsureKeypair(dir)
	if err != nil {
		t.Fatalf("EnsureKeypair (reuse): %v", err)
	}
	if again.PublicKey != keys.PublicKey {
		t.Errorf("the key changed on reuse: %q vs %q", again.PublicKey, keys.PublicKey)
	}
}

func TestEnsureKeypair_NoKeygen(t *testing.T) {
	fakeLookPath(t, nil)
	_, err := EnsureKeypair(filepath.Join(t.TempDir(), "ssh"))
	if err == nil || !strings.Contains(err.Error(), "ssh-keygen") {
		t.Errorf("expected an error naming ssh-keygen, got %v", err)
	}
}

func TestAuthorizedKeys_DropsEmptyEntries(t *testing.T) {
	keys := Keypair{PublicKey: "ssh-ed25519 AAAArun", Extra: []string{"", "   ", "ssh-rsa AAAAother"}}
	if got := keys.AuthorizedKeys(); got != "ssh-ed25519 AAAArun\nssh-rsa AAAAother" {
		t.Errorf("authorized keys = %q", got)
	}
}
