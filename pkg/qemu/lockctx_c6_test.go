package qemu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Lock waits honor the caller: the Ctx lifecycle entries fail a queued
// lock wait on caller cancellation instead of queueing past it. Each
// test holds the respective lock and calls with an already-cancelled
// context — deterministic (no timing), prompt by construction. Old code
// has no Ctx entries (the old-fails direction is the API itself, the
// signature-direction precedent); the non-Ctx entries keep their
// historical waits byte-identical.

func TestDeleteCtx_CancelledLockWaitFailsFast(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	unlockCreate, err := lockCreate()
	if err != nil {
		t.Fatalf("holding the creation lock: %v", err)
	}
	defer unlockCreate()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err = DeleteCtx(ctx, "gate")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancelled delete waited %s for the creation lock", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "VM creation is busy") || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled delete must fail naming the busy creation lock and the cancellation, got %v", err)
	}
}

func TestStartOwnedTokenCtx_CancelledLockWaitFailsFast(t *testing.T) {
	directVM(t, "gate")
	unlock, err := lockLifecycle("gate")
	if err != nil {
		t.Fatalf("holding the lifecycle lock: %v", err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, started, err := StartOwnedTokenCtx(ctx, "gate")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancelled start waited %s for the lifecycle lock", elapsed)
	}
	if started {
		t.Error("cancelled start must not report started")
	}
	if err == nil || !strings.Contains(err.Error(), "lifecycle is busy") || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled start must fail naming the busy lifecycle lock and the cancellation, got %v", err)
	}
}

func TestStopCtx_CancelledLockWaitFailsFast(t *testing.T) {
	directVM(t, "gate")
	unlock, err := lockLifecycle("gate")
	if err != nil {
		t.Fatalf("holding the lifecycle lock: %v", err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err = StopCtx(ctx, "gate")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancelled stop waited %s for the lifecycle lock", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "lifecycle is busy") || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled stop must fail naming the busy lifecycle lock and the cancellation, got %v", err)
	}
}

func TestStopOwnedCtx_CancelledLockWaitFailsFast(t *testing.T) {
	directVM(t, "gate")
	unlock, err := lockLifecycle("gate")
	if err != nil {
		t.Fatalf("holding the lifecycle lock: %v", err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err = StopOwnedCtx(ctx, "gate", StartToken{})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancelled owned stop waited %s for the lifecycle lock", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "lifecycle is busy") || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled owned stop must fail naming the busy lifecycle lock and the cancellation, got %v", err)
	}
}
