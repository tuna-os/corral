//go:build !linux && !darwin

package macos

import (
	"context"
	"errors"
	"os"
)

func privateStateUnsupported() error {
	return errors.New("private macOS guest operations require a Linux or macOS client")
}
func ownedByCurrentUser(info os.FileInfo) bool { return false }
func privateDirectory(path string) error       { return privateStateUnsupported() }
func privateConfig(path string) error          { return privateStateUnsupported() }
func privateOpenFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	return nil, privateStateUnsupported()
}
func lockPrivateState(ctx context.Context, directory string) (func(), error) {
	return nil, privateStateUnsupported()
}
func openBitwardenSession() (*os.File, error) { return nil, privateStateUnsupported() }
