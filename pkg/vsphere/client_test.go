package vsphere

import (
	"context"
	"strings"
	"testing"
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

func TestFingerprintNormalisation(t *testing.T) {
	if got := normaliseFingerprint("AA:BB cc-dd"); got != "aabbccdd" {
		t.Fatalf("normaliseFingerprint = %q", got)
	}
}
