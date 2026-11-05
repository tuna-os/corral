//go:build unix

package macos

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMediaCacheLockWaitIsCancellableAndReleased(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockMediaCache(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if release, err := lockMediaCache(ctx, dir); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("competing prepare did not cancel: %v", err)
	}
	unlock()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := lockMediaCache(ctx, dir)
	if err != nil {
		t.Fatalf("cache lock not released: %v", err)
	}
	release()
}
