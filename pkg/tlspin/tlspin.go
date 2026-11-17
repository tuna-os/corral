// Package tlspin checks a server certificate against a SHA-256 pin. It is the
// one implementation the hypervisor clients (Proxmox, XCP-ng, Nutanix,
// vSphere) share, so the check cannot drift between copies.
//
// A pin names the server's own (leaf) certificate. Only the leaf counts: it
// is the one certificate whose private key the TLS handshake proves the
// server holds. Any later certificate in the list is bytes the peer chose to
// send, and the genuine certificate is public, so accepting a match anywhere
// in the list would let a man in the middle append it behind a leaf of its
// own.
package tlspin

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Pin is a parsed SHA-256 certificate fingerprint.
type Pin [sha256.Size]byte

// Parse reads a SHA-256 fingerprint in any of the usual spellings: hex in
// either case, optionally separated by colons, spaces or dashes (the form
// `openssl x509 -fingerprint -sha256` prints, or bare hex).
func Parse(s string) (Pin, error) {
	var p Pin
	digits := strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(s))
	raw, err := hex.DecodeString(digits)
	if err != nil || len(raw) != sha256.Size {
		return p, fmt.Errorf("fingerprint must be a SHA-256 hex digest (64 hex characters), got %d characters", len(digits))
	}
	copy(p[:], raw)
	return p, nil
}

// MatchesLeaf reports whether the first certificate in rawCerts (the leaf)
// has this fingerprint. The comparison is constant time.
func (p Pin) MatchesLeaf(rawCerts [][]byte) bool {
	if len(rawCerts) == 0 {
		return false
	}
	sum := sha256.Sum256(rawCerts[0])
	return subtle.ConstantTimeCompare(sum[:], p[:]) == 1
}

// ErrMismatch is returned when the server's leaf certificate does not match
// the pin.
var ErrMismatch = errors.New("server certificate does not match the pinned fingerprint")

// Verify is a tls.Config.VerifyPeerCertificate callback that accepts only a
// leaf matching the pin.
func (p Pin) Verify(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if p.MatchesLeaf(rawCerts) {
		return nil
	}
	return ErrMismatch
}

// ClientConfig is a TLS client configuration that trusts exactly the pinned
// certificate. Chain verification is replaced by the pin, not skipped: the
// handshake fails unless the leaf matches, whatever the system trust store
// says about it.
func (p Pin) ClientConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify:    true, //nolint:gosec // replaced by the pin check in VerifyPeerCertificate
		VerifyPeerCertificate: p.Verify,
	}
}
