package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Per-VM lifecycle lock: the start → stop → force critical section for one
// VM name. Creation alone used to be locked, so two starts could both observe
// "absent" and exec QEMU against one disk, a start could slip between
// --force's ProvenStopped check and its disk conversion, and a stop by name
// could land on a replacement VM started after the stopper's probe. Every
// lifecycle entry — Start, Stop, token stops, Create, Delete — holds this
// lock for its whole check → act → record sequence, so same-VM lifecycle
// operations are serialized across goroutines (in-process slot) and across
// processes (flock on a stable home-level lock file).
//
// The flock file lives OUTSIDE the VM's own directory, at
// <VMHome>/locks/<name>.lock. A lock inside the VM dir dies with the VM:
// Delete removes the whole directory while holding the lock, so a
// cross-process waiter blocked on the old inode and every entrant after a
// recreate would lock DIFFERENT inodes and run their critical sections
// concurrently. The home-level file is never deleted (lock files are not
// reaped — reaping would reintroduce the inode race), so one name always
// means one inode, through delete and recreate.
//
// Lock ordering is fixed: the VM-home creation lock first, then at most one
// lifecycle lock, released in reverse. Lifecycle locks are never nested —
// Create and Delete call the *Locked cores, never the public Start/Stop —
// so the ordering cannot deadlock. Different VM names never contend.
//
// Residual: the flock is advisory within one VM-home filesystem. Two hosts
// sharing state over a network filesystem that does not honor flock would
// not exclude each other; that topology is unsupported.

// lifecycleChans holds one token channel per VM name: a full channel is an
// unlocked VM, an empty one a held lock. A channel (not a mutex) so acquisition
// can fail on context cancellation instead of blocking past it.
var (
	lifecycleMu    sync.Mutex
	lifecycleChans = map[string]chan struct{}{}
)

func lifecycleChan(name string) chan struct{} {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	if ch, ok := lifecycleChans[name]; ok {
		return ch
	}
	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	lifecycleChans[name] = ch
	return ch
}

// takeLifecycleProc takes the in-process lifecycle slot for name, failing on
// ctx rather than waiting past it.
func takeLifecycleProc(ctx context.Context, name string) (func(), error) {
	ch := lifecycleChan(name)
	select {
	case <-ch:
		return func() { ch <- struct{}{} }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("VM %q lifecycle is busy (another start/stop/create holds it): %w", name, ctx.Err())
	}
}

// lifecycleWaitBound bounds lock acquisition where no caller context exists
// (Start/Stop/Delete take no ctx): a lock held past this is reported, never
// waited out forever.
const lifecycleWaitBound = 60 * time.Second

// lockLifecycle takes the per-VM lifecycle lock, bounded by lifecycleWaitBound.
// See lockLifecycleCtx for the ordering and residual notes.
func lockLifecycle(name string) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleWaitBound)
	defer cancel()
	return lockLifecycleCtx(ctx, name)
}

// lifecycleLockPath is the stable cross-process lock file for name. It is a
// function of the name alone (never of VM state that delete/recreate can
// remove), so every process that ever locks this VM shares one inode.
func lifecycleLockPath(name string) string {
	return filepath.Join(VMHome(), "locks", name+".lock")
}

// lockLifecycleCtx takes the per-VM lifecycle lock for name's whole
// check → act → record sequence. Callers must hold the returned unlock
// until the lifecycle operation is fully recorded.
//
// The cross-process half is unconditional: the lock file is always taken,
// whether or not the VM exists yet. Skipping it on a stat failure would
// silently discard cross-process exclusion on permission or I/O errors —
// uncertainty about the filesystem must fail loudly, never degrade to a
// process-local lock.
func lockLifecycleCtx(ctx context.Context, name string) (func(), error) {
	if err := ValidateVMName(name); err != nil {
		return nil, err
	}
	releaseProc, err := takeLifecycleProc(ctx, name)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) {
		releaseProc()
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(VMHome(), "locks"), 0o755); err != nil {
		return fail(fmt.Errorf("VM %q lifecycle lock: %w", name, err))
	}
	f, err := os.OpenFile(lifecycleLockPath(name), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fail(fmt.Errorf("VM %q lifecycle lock: %w", name, err))
	}
	if err := lockFlockCtx(ctx, f, fmt.Sprintf("VM %q lifecycle is busy", name)); err != nil {
		_ = f.Close()
		return fail(err)
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
		releaseProc()
	}, nil
}

// lockFlockCtx takes an exclusive flock, polling a non-blocking attempt so
// ctx bounds the wait. A genuinely contended lock retries; any other error
// fails immediately.
func lockFlockCtx(ctx context.Context, f *os.File, what string) error {
	if err := tryLockFile(f); err == nil {
		return nil
	} else if !lockContended(err) {
		return fmt.Errorf("%s: %w", what, err)
	}
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, ctx.Err())
		case <-t.C:
			if err := tryLockFile(f); err == nil {
				return nil
			} else if !lockContended(err) {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
	}
}
