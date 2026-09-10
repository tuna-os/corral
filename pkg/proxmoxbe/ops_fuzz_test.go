package proxmoxbe

import "testing"

// memoryMiB converts Corral's memory strings into the integer PVE wants, and
// PVE acts on the number: a wrong one resizes somebody's VM. Anything it does
// not understand must be an error rather than a guess.
//
// Run longer with: go test -run xxx -fuzz FuzzMemoryMiB ./pkg/proxmoxbe/
func FuzzMemoryMiB(f *testing.F) {
	for _, seed := range []string{
		"", "0", "2048", "4Gi", "4G", "4GB", "512Mi", "512m", "1.5Gi",
		"-4Gi", "Gi", "4Gi4Gi", "9999999999999999999Gi", "0.0001Gi", " 4Gi ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := memoryMiB(s)
		if err != nil {
			return
		}
		// PVE rejects a non-positive memory outright, so producing one here
		// would turn a bad input into a failed API call an operator has to
		// decode, instead of the parse error they should have seen.
		if got <= 0 {
			t.Errorf("memoryMiB(%q) = %d with no error, want a positive MiB count", s, got)
		}
	})
}
