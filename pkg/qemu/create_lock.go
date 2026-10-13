package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// createSlot serializes VM creation inside this process. The flock file below
// serializes it across processes — flock locks are per open file description,
// so two goroutines here must not rely on the file alone. A channel (not a
// mutex) so acquisition can fail on context cancellation instead of blocking
// past it; see lifecycle_lock.go for the same pattern per VM.
var createSlot = func() chan struct{} {
	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	return ch
}()

// lockCreate takes the VM-home-wide creation lock: no two creates (same name
// or different names) run their existence-check → port-allocate → record
// critical section concurrently. Same-name double creates become an honest
// already-exists error for the loser instead of interleaved overwrites, and
// port allocation cannot hand one display to two VMs. Creation is rare and
// slow (image conversion), so serializing it costs nothing measurable.
//
// Callers must hold the returned unlock until creation is fully recorded.
// There is exactly one lock site (Create); RecordCreating stays unlocked — it
// is a best-effort progress marker, and nesting a second acquisition here
// would deadlock on the slot.
//
// Lock ordering is fixed: this creation lock first, then at most one
// per-VM lifecycle lock (see lifecycle_lock.go), released in reverse.
func lockCreate() (func(), error) {
	return lockCreateCtx(context.Background())
}

// lockCreateCtx is lockCreate bounded by ctx: a second creation waits for
// the first only until ctx expires. Create passes its own Ctx, so Ctrl-C
// during a queued create fails the wait instead of burying the signal.
func lockCreateCtx(ctx context.Context) (func(), error) {
	select {
	case <-createSlot:
	case <-ctx.Done():
		return nil, fmt.Errorf("VM creation is busy (another create holds it): %w", ctx.Err())
	}
	fail := func(err error) (func(), error) {
		createSlot <- struct{}{}
		return nil, err
	}
	if err := os.MkdirAll(VMHome(), 0o755); err != nil {
		return fail(err)
	}
	f, err := os.OpenFile(filepath.Join(VMHome(), ".create.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fail(err)
	}
	if err := lockFlockCtx(ctx, f, "VM creation is busy"); err != nil {
		_ = f.Close()
		return fail(err)
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
		createSlot <- struct{}{}
	}, nil
}
