package web

import (
	"strings"
	"testing"
)

// parseQuantity reads memory strings that come from the backends, not from
// Corral: a Kubernetes quantity, an Incus state field, whatever PVE reports.
// Corral does not get to choose what arrives, and a /metrics scrape must not
// be brought down by a string a cluster made up.
//
// Run longer with: go test -run xxx -fuzz FuzzParseQuantity ./pkg/web/
func FuzzParseQuantity(f *testing.F) {
	for _, seed := range []string{
		"", " ", "0", "4Gi", "2048Mi", "1T", "1Ti", "512M", "1.5Gi", "8589934592",
		"-1", "1e9", "Gi", "..", "9223372036854775807Ti", "4 Gi", "٤Gi", "0x10",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := parseQuantity(s)
		if err != nil {
			return
		}
		// A parse that succeeds must produce a usable number. A negative one
		// would be exported as a gauge and read as a fleet with negative
		// memory; a silent zero from a non-empty input hides a parse bug.
		if got < 0 {
			t.Errorf("parseQuantity(%q) = %d, want a non-negative size", s, got)
		}
		if got == 0 && strings.ContainsAny(s, "123456789") {
			t.Errorf("parseQuantity(%q) = 0 despite naming a non-zero size", s)
		}
	})
}
