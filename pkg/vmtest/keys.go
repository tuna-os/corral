package vmtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The run's own SSH key.
//
// A fresh CI runner has no ~/.ssh, and the probe cannot answer a password
// prompt, so a run that depends on the caller having a key fails on the machine
// it matters most on. Every run therefore generates its own keypair into the
// artifact directory and injects the public half at install time. The private
// half is the run's, expires with it, and never has to be a repository secret.
//
// A key the caller already has is added as well, so a human can log into the
// VM the run leaves behind with the key they normally use.

// Keypair is the run's identity.
type Keypair struct {
	PrivatePath string
	PublicKey   string
	// Extra are other public keys authorised as well, e.g. the operator's own.
	Extra []string
}

// AuthorizedKeys returns every key to inject, the run's own first.
func (k Keypair) AuthorizedKeys() string {
	keys := append([]string{k.PublicKey}, k.Extra...)
	var kept []string
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			kept = append(kept, key)
		}
	}
	return strings.Join(kept, "\n")
}

// EnsureKeypair returns the run's keypair, generating one in dir if it is not
// there already. Reused on a second run against the same artifact directory,
// so a re-run does not invalidate a VM the first one left behind.
func EnsureKeypair(dir string, extra ...string) (Keypair, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Keypair{}, err
	}
	priv := filepath.Join(dir, "id_ed25519")
	pub := priv + ".pub"

	if _, err := os.Stat(priv); err == nil {
		data, err := os.ReadFile(pub)
		if err != nil {
			return Keypair{}, fmt.Errorf("reading %s: %w", pub, err)
		}
		return Keypair{PrivatePath: priv, PublicKey: strings.TrimSpace(string(data)), Extra: extra}, nil
	}

	// ssh-keygen rather than a Go implementation: the run already needs the
	// OpenSSH client for every probe and check, so this adds no requirement
	// that was not there, and the key is in exactly the format the client
	// expects with no format questions of our own.
	if _, err := lookPath("ssh-keygen"); err != nil {
		return Keypair{}, fmt.Errorf("ssh-keygen is not installed, and the run needs a keypair to log in with — install openssh-client")
	}
	out, err := runner.Run("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "corral-vmtest", "-f", priv)
	if err != nil {
		return Keypair{}, fmt.Errorf("generating a keypair: %s", commandOutput(out, err))
	}
	data, err := os.ReadFile(pub)
	if err != nil {
		return Keypair{}, fmt.Errorf("ssh-keygen reported success but %s is missing: %w", pub, err)
	}
	return Keypair{PrivatePath: priv, PublicKey: strings.TrimSpace(string(data)), Extra: extra}, nil
}
