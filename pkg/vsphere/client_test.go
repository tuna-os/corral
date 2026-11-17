package vsphere

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vmware/govmomi/vim25/soap"
)

func TestNewRejectsUnusableConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"empty host", Config{Username: "u", Password: "p"}, "host is required"},
		{"empty username", Config{Host: "vc.example.com", Password: "p"}, "username is required"},
		{"empty password", Config{Host: "vc.example.com", Username: "u"}, "password is required"},
		{"unusable host", Config{Host: "https://", Username: "u", Password: "p"}, "not usable"},
		{"junk fingerprint", Config{Host: "vc.example.com", Username: "u", Password: "p", Fingerprint: ":::"}, "fingerprint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New(%+v) = %v, want error containing %q", tc.cfg, err, tc.want)
			}
		})
	}
}

func TestNewAcceptsEndpointShapes(t *testing.T) {
	for _, host := range []string{
		"vc.example.com",
		"vc.example.com:443",
		"https://vc.example.com",
		"https://vc.example.com:443",
	} {
		client, err := New(Config{Host: host, Username: "administrator@vsphere.local", Password: "secret"})
		if err != nil {
			t.Fatalf("New(%q): %v", host, err)
		}
		if client.Host() == "" {
			t.Fatalf("New(%q) lost the host", host)
		}
	}
}

func TestConnectIsANoOpWithAnInjectedSession(t *testing.T) {
	client, _ := simClient(t)
	// simClient injects the simulator session; Connect must not dial past it.
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

// govmomi's dialer compares the stored thumbprint for string equality with
// soap.ThumbprintSHA256 (upper-case hex pairs joined by colons), so every
// accepted spelling of a SHA-256 pin must be handed to it in that form.
func TestFingerprintIsHandedToGovmomiInItsOwnForm(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	cert := srv.Certificate()
	want := soap.ThumbprintSHA256(cert)
	sum := sha256.Sum256(cert.Raw)
	for _, in := range []string{want, strings.ToLower(want), hex.EncodeToString(sum[:]), " " + strings.ReplaceAll(want, ":", "-") + " "} {
		if got := normaliseFingerprint(in); got != want {
			t.Errorf("normaliseFingerprint(%q) = %q; govmomi compares against %q", in, got, want)
		}
	}
	for _, junk := range []string{"AA:BB cc-dd", strings.Repeat("zz", 32), hex.EncodeToString(sum[:20])} {
		if got := normaliseFingerprint(junk); got != "" {
			t.Errorf("normaliseFingerprint(%q) = %q, want \"\" (not a SHA-256 digest)", junk, got)
		}
	}
}

// Connect against a self-signed server: the pinned certificate must get the
// SOAP request through the TLS handshake, and a different pin, with or
// without insecure, must stop it before any request (and so the login) is
// sent.
func TestConnectHonoursThePinnedFingerprint(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "not a vCenter", http.StatusNotFound)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are the point
	srv.StartTLS()
	defer srv.Close()
	pin := soap.ThumbprintSHA256(srv.Certificate())
	other := strings.Repeat("AB:", 31) + "AB"

	for _, tc := range []struct {
		name     string
		pin      string
		insecure bool
		reached  bool
	}{
		{"pinned certificate", strings.ToLower(strings.ReplaceAll(pin, ":", "")), false, true},
		{"different pin", other, false, false},
		{"different pin with insecure", other, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests.Store(0)
			c, err := New(Config{Host: srv.URL, Username: "u", Password: "p", Fingerprint: tc.pin, Insecure: tc.insecure})
			if err != nil {
				t.Fatal(err)
			}
			err = c.Connect(context.Background())
			if err == nil {
				t.Fatal("Connect succeeded against a server that is not a vCenter")
			}
			if got := requests.Load() > 0; got != tc.reached {
				t.Fatalf("request reached the server = %v, want %v (Connect error: %v)", got, tc.reached, err)
			}
		})
	}
}

func TestPinIsRefusedOverPlainHTTP(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	if _, err := New(Config{Host: "http://vc.example.com", Username: "u", Password: "p", Fingerprint: pin}); err == nil {
		t.Fatal("a fingerprint was accepted for an http:// endpoint, where it can never be checked")
	}
}
