//go:build !unix

package config

// lockConfigDir is a no-op where flock is unavailable (the js/wasm browser
// demo has no filesystem to share). store.mu still serializes writers
// within the process.
func lockConfigDir(string) (func(), error) { return func() {}, nil }
