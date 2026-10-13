//go:build !unix

package qemu

// Non-Unix fallback: free space is unverifiable here, so capacity checks
// cannot refuse — they see an effectively unbounded disk. The only such
// build is the js/wasm demo, which never creates a real VM; every real
// host (Linux/macOS) measures honestly.
func freeBytesOnDir(dir string) (uint64, error) {
	return 1 << 62, nil
}
