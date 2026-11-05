//go:build linux || darwin

package macos

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapStateKeepsAutologinSecretOffClientDisk(t *testing.T) {
	const password = "A dedicated local guest password"
	account, err := newBootstrapAccount("beeper", password, true)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(account.Record)
	defer clear(account.KCPassword)
	path := filepath.Join(authTestDirectory(t), "account.json")
	if err := bootstrapSave(context.Background(), path, &bootstrapState{Version: 1, Account: account}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential state is not owner-private")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(body)
	var stored struct{ Account map[string]json.RawMessage }
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	if _, present := stored.Account["KCPassword"]; present || bytes.Contains(body, []byte(password)) {
		t.Fatal("reversible or plaintext local password persisted on client")
	}
	loaded, err := bootstrapLoad(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(loaded.Account.Record)
	if !bootstrapPasswordMatches(loaded.Account, password) || bootstrapPasswordMatches(loaded.Account, "wrong password") {
		t.Fatal("private retry state cannot verify the original credential")
	}
}
