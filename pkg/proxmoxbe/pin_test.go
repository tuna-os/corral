package proxmoxbe

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// pinSelfSigned mints a throwaway self-signed server certificate, the shape a
// PVE node ships with.
func pinSelfSigned(t *testing.T, cn string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

// pinServer serves over TLS with the given certificate list and counts the
// requests that got past the handshake: every one of them carries the
// credential.
func pinServer(t *testing.T, key *ecdsa.PrivateKey, chain ...[]byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"version":"8.2.4","release":"8.2"}}`)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are the point
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: chain, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &requests
}

func pinOf(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// The pin names the certificate whose private key the server must hold. A
// certificate that merely travels in the handshake proves nothing: it is
// public, and anyone can append it behind a leaf of their own.
func TestPinnedFingerprintMustMatchTheLeaf(t *testing.T) {
	genuine, genuineKey := pinSelfSigned(t, "genuine")
	other, otherKey := pinSelfSigned(t, "other")
	pin := pinOf(genuine)

	t.Run("the pinned certificate is the leaf", func(t *testing.T) {
		srv, requests := pinServer(t, genuineKey, genuine)
		c, err := New(Config{Host: srv.URL, Token: testToken, Fingerprint: pin})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Version(); err != nil {
			t.Fatalf("against the pinned server: %v", err)
		}
		if requests.Load() == 0 {
			t.Fatal("no request reached the pinned server")
		}
	})

	t.Run("the pinned certificate is appended behind another leaf", func(t *testing.T) {
		srv, requests := pinServer(t, otherKey, other, genuine)
		c, err := New(Config{Host: srv.URL, Token: testToken, Fingerprint: pin})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Version(); err == nil {
			t.Error("a server that does not hold the pinned key was accepted")
		}
		if n := requests.Load(); n != 0 {
			t.Errorf("the credential reached that server in %d request(s)", n)
		}
	})
}

func TestPinIsRefusedOverPlainHTTP(t *testing.T) {
	pin := pinOf([]byte("any certificate"))
	// The same Config as above, with the host written as http://.
	srv := struct{ URL string }{URL: "http://pve.example.com:8006"}
	if _, err := New(Config{Host: srv.URL, Token: testToken, Fingerprint: pin}); err == nil {
		t.Fatal("a fingerprint was accepted for an http:// endpoint, where it can never be checked")
	}
}
