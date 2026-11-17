package tlspin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestParseAcceptsTheUsualSpellings(t *testing.T) {
	leaf := []byte("leaf certificate")
	sum := sha256.Sum256(leaf)
	lower := hex.EncodeToString(sum[:])
	pairs := make([]string, 0, len(sum))
	for i := 0; i < len(lower); i += 2 {
		pairs = append(pairs, strings.ToUpper(lower[i:i+2]))
	}
	for _, in := range []string{
		lower,
		strings.ToUpper(lower),
		strings.Join(pairs, ":"), // openssl x509 -fingerprint -sha256
		" " + strings.Join(pairs, "-") + " ",
		strings.Join(pairs, " "),
	} {
		p, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if !p.MatchesLeaf([][]byte{leaf}) {
			t.Errorf("Parse(%q) does not match the certificate it was taken from", in)
		}
	}
}

func TestParseRejectsAnythingButASHA256Digest(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	full := hex.EncodeToString(sum[:])
	for _, in := range []string{
		"",
		"   ",
		":::",
		"ab:cd",
		full[:40],                // SHA-1 length
		full + "00",              // too long
		strings.Repeat("zz", 32), // right length, not hex
	} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) accepted a value that is not a SHA-256 digest", in)
		}
	}
}

// Only the leaf counts: the pinned certificate appended behind another leaf
// must not match.
func TestMatchesLeafOnly(t *testing.T) {
	genuine, other := []byte("genuine"), []byte("other")
	sum := sha256.Sum256(genuine)
	p, err := Parse(hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		certs [][]byte
		want  bool
	}{
		{"pinned leaf", [][]byte{genuine}, true},
		{"pinned leaf with a chain", [][]byte{genuine, other}, true},
		{"pinned certificate behind another leaf", [][]byte{other, genuine}, false},
		{"other leaf", [][]byte{other}, false},
		{"no certificates", nil, false},
	} {
		if got := p.MatchesLeaf(tc.certs); got != tc.want {
			t.Errorf("%s: MatchesLeaf = %v, want %v", tc.name, got, tc.want)
		}
		err := p.Verify(tc.certs, nil)
		if tc.want && err != nil {
			t.Errorf("%s: Verify = %v, want nil", tc.name, err)
		}
		if !tc.want && !errors.Is(err, ErrMismatch) {
			t.Errorf("%s: Verify = %v, want ErrMismatch", tc.name, err)
		}
	}
	if cfg := p.ClientConfig(); cfg.VerifyPeerCertificate == nil || !cfg.InsecureSkipVerify {
		t.Error("ClientConfig must replace chain verification with the pin check")
	}
}
