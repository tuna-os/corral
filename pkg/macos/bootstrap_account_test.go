package macos

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"howett.net/plist"
)

func TestBootstrapNativeCredentialRecord(t *testing.T) {
	password := "A unique UTF-8 password: å🔐"
	account, err := newBootstrapAccount("beeper", password, true)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(account.Record)
	defer clear(account.KCPassword)
	var record map[string]interface{}
	if _, err := plist.Unmarshal(account.Record, &record); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"name": "beeper", "uid": "501", "gid": "20", "home": "/Users/beeper", "shell": "/bin/zsh",
		"passwd": "********", "generateduid": account.UUID,
		"authentication_authority": ";ShadowHash;HASHLIST:<SALTED-SHA512-PBKDF2>",
	} {
		values, ok := record[key].([]interface{})
		if !ok || len(values) != 1 || values[0] != want {
			t.Fatalf("incorrect native attribute %s", key)
		}
	}
	shadowValues := record["ShadowHashData"].([]interface{})
	shadowBytes := shadowValues[0].([]byte)
	if !bytes.HasPrefix(shadowBytes, []byte("bplist00")) {
		t.Fatal("ShadowHashData is not a binary plist")
	}
	var shadow struct {
		Hash struct {
			Entropy    []byte `plist:"entropy"`
			Salt       []byte `plist:"salt"`
			Iterations int    `plist:"iterations"`
		} `plist:"SALTED-SHA512-PBKDF2"`
	}
	if _, err := plist.Unmarshal(shadowBytes, &shadow); err != nil {
		t.Fatal(err)
	}
	if len(shadow.Hash.Salt) != 32 || len(shadow.Hash.Entropy) != 128 || shadow.Hash.Iterations < 100000 {
		t.Fatal("weak or malformed credential parameters")
	}
	derived, err := pbkdf2.Key(sha512.New, password, shadow.Hash.Salt, shadow.Hash.Iterations, 128)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(derived)
	if !bytes.Equal(derived, shadow.Hash.Entropy) {
		t.Fatal("credential does not derive from UTF-8 password")
	}
	if !bootstrapUUIDPattern.MatchString(account.UUID) {
		t.Fatal("invalid generated account UUID")
	}
	if !bootstrapPasswordMatches(account, password) {
		t.Fatal("correct password rejected")
	}
	if bootstrapPasswordMatches(account, password+"wrong") {
		t.Fatal("wrong password accepted")
	}
	if bytes.Contains(account.Record, []byte(password)) {
		t.Fatal("record contains plaintext password")
	}
	account.KCPassword[0] ^= 1
	if bootstrapPasswordMatches(account, password) {
		t.Fatal("changed automatic-login credential accepted")
	}
}

func TestBootstrapCredentialSaltAndIdentityAreUnique(t *testing.T) {
	first, err := newBootstrapAccount("beeper", "same password", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newBootstrapAccount("beeper", "same password", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.UUID == second.UUID || bytes.Equal(first.Record, second.Record) {
		t.Fatal("account identities or salt were reused")
	}
	a, ok := decodeBootstrapRecord(first)
	if !ok {
		t.Fatal("first record invalid")
	}
	b, ok := decodeBootstrapRecord(second)
	if !ok {
		t.Fatal("second record invalid")
	}
	if bytes.Equal(a.Hash.Salt, b.Hash.Salt) {
		t.Fatal("credential salt reused")
	}
	if len(first.KCPassword) != 0 || len(second.KCPassword) != 0 {
		t.Fatal("automatic login enabled without opt-in")
	}
}

func TestBootstrapInputBoundaries(t *testing.T) {
	for _, user := range []string{"", "root", "daemon", "nobody", "guest", "admin", "sshd", "_service", "Beeper", "../beeper", "a-b", "beeper\n", strings.Repeat("a", 32), "é"} {
		if _, err := newBootstrapAccount(user, "valid", false); err == nil {
			t.Fatalf("unsafe username accepted: %q", user)
		}
	}
	for _, password := range []string{"", "one\ntwo", "one\rtwo", "a\x00b", string([]byte{0xff}), strings.Repeat("x", 1025)} {
		if _, err := newBootstrapAccount("beeper", password, false); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	for _, password := range []string{"x", " å🔐 ", strings.Repeat("x", 1024)} {
		account, err := newBootstrapAccount(strings.Repeat("a", 31), password, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bootstrapPasswordMatches(account, password) {
			t.Fatal("valid password boundary rejected")
		}
		if bootstrapPasswordMatches(account, "") || bootstrapPasswordMatches(account, "bad\x00input") {
			t.Fatal("invalid password matched")
		}
	}
}

func TestBootstrapRejectsAlteredAccountState(t *testing.T) {
	account, err := newBootstrapAccount("beeper", "native password", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*bootstrapAccount){
		func(a *bootstrapAccount) { a.User = "other" },
		func(a *bootstrapAccount) { a.UUID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" },
		func(a *bootstrapAccount) { a.Record = []byte("invalid") },
		func(a *bootstrapAccount) {
			a.Record = bytes.Replace(a.Record, []byte("<string>501</string>"), []byte("<string>502</string>"), 1)
		},
		func(a *bootstrapAccount) {
			a.Record = bytes.Replace(a.Record, []byte("/Users/beeper"), []byte("/Users/other"), 1)
		},
	} {
		altered := account
		change(&altered)
		if bootstrapPasswordMatches(altered, "native password") {
			t.Fatal("altered account matched")
		}
	}
}

func bootstrapTestKey(t *testing.T) []byte {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return ssh.MarshalAuthorizedKey(key)
}

func TestBootstrapArchiveFixedPrivateMembers(t *testing.T) {
	public := bootstrapTestKey(t)
	for _, autologin := range []bool{false, true} {
		account, err := newBootstrapAccount("beeper", "never archive my plaintext password", autologin)
		if err != nil {
			t.Fatal(err)
		}
		var archive bytes.Buffer
		if err := writeBootstrapArchive(&archive, account, public); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(archive.Bytes(), []byte("never archive my plaintext password")) {
			t.Fatal("archive contains plaintext password")
		}
		want := map[string]int64{"user.plist": 0600, "authorized_keys": 0600, "bootstrap-firstboot.sh": 0700, "bootstrap-firstboot.plist": 0644}
		if autologin {
			want["kcpassword"] = 0600
		}
		tr := tar.NewReader(&archive)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			mode, ok := want[header.Name]
			if !ok {
				t.Fatalf("unexpected or duplicate archive member %q", header.Name)
			}
			delete(want, header.Name)
			if header.Mode != mode || header.Typeflag != tar.TypeReg || header.Uid != 0 || header.Gid != 0 || header.Linkname != "" {
				t.Fatal("unsafe archive metadata")
			}
		}
		if len(want) != 0 {
			t.Fatal("missing archive member")
		}
	}
}

type bootstrapFailWriter struct{}

func (bootstrapFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestBootstrapArchiveRejectsUnsafeKeysAndPropagatesWriterFailure(t *testing.T) {
	account, err := newBootstrapAccount("beeper", "private password", false)
	if err != nil {
		t.Fatal(err)
	}
	public := bootstrapTestKey(t)
	for _, key := range [][]byte{nil, []byte("not a public key"), append(append([]byte{}, public...), public...), append([]byte("command=\"echo unsafe\" "), public...)} {
		var archive bytes.Buffer
		if err := writeBootstrapArchive(&archive, account, key); err == nil {
			t.Fatal("unsafe public key accepted")
		}
		if archive.Len() != 0 {
			t.Fatal("private bytes streamed before key validation")
		}
	}
	if err := writeBootstrapArchive(bootstrapFailWriter{}, account, public); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("writer failure was not propagated")
	}
	account.User = "other"
	var archive bytes.Buffer
	if err := writeBootstrapArchive(&archive, account, public); err == nil || archive.Len() != 0 {
		t.Fatal("invalid state streamed")
	}
}
