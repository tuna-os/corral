//go:build !linux && !darwin

package qemu

import "fmt"

// Non-Linux/macOS fallback: no native identity source exists here, so every
// probe fails and every live pid classifies as unknown (see direct.go) —
// never owned, never signalled. The only such build is the js/wasm demo,
// which never starts a real VM.

func nativeExeID(pid int) (string, error) {
	return "", fmt.Errorf("no executable probe on this platform")
}

func nativeStartID(pid int) (string, error) {
	return "", fmt.Errorf("no start-identity probe on this platform")
}

func nativeIsZombie(pid int) bool {
	return false
}
