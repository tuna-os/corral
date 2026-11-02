//go:build !linux && !darwin

package guestssh

import "errors"

func privateDirectory(string) error {
	return errors.New("private guest SSH trust storage is supported only on Linux and macOS")
}

func privateFile(string, bool) error {
	return errors.New("private guest SSH trust storage is supported only on Linux and macOS")
}
