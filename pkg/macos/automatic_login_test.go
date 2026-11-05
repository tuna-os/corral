package macos

import (
	"encoding/hex"
	"testing"
)

func TestAutomaticLoginCredentialFormat(t *testing.T) {
	for _, tc := range []struct{ name, password, encoded string }{
		{"published format", "password", "0de82150a5d3af8ea3b91f7d"},
		{"terminator fills block", "AAAAAAAAAAA", "3cc8136293fd9cabe2f85e7d"},
		{"terminator needs next block", "AAAAAAAAAAAA", "3cc8136293fd9cabe2f85e3c895223d2bcddeaa3b91f7d89"},
		{"UTF-8 bytes", "å", "be2c5223d2bcddeaa3b91f7d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hex.EncodeToString(automaticLoginPassword(tc.password)); got != tc.encoded {
				t.Fatalf("incorrect macOS automatic-login credential: got %s, want %s", got, tc.encoded)
			}
		})
	}
}
