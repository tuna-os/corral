//go:build !unix

package macos

import (
	"context"
	"errors"
)

func lockMediaCache(context.Context, string) (func(), error) {
	return nil, errors.New("macOS media preparation requires a Unix host (Linux or macOS) with filesystem locking")
}
