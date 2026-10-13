package launcher

import (
	"context"
	"errors"
	"io"
	"testing"
)

// Final-chunk cancellation: a cancellation landing in the last read or
// write fails the copy — it must never return a successful hash over bytes
// the caller no longer wants. (The existing slowChunks test cancels with
// many chunks remaining; these stage the cancel inside the final reads.)

// eofCancellingReader serves its payload once, then cancels the context
// inside the final EOF read.
type eofCancellingReader struct {
	payload []byte
	cancel  context.CancelFunc
	reads   int
}

func (r *eofCancellingReader) Read(p []byte) (int, error) {
	if r.reads == 0 {
		r.reads++
		return copy(p, r.payload), nil
	}
	r.cancel()
	return 0, io.EOF
}

func TestCopyCtx_CancelDuringFinalEOFRefuses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &eofCancellingReader{payload: []byte("final-bytes"), cancel: cancel}
	n, err := copyCtx(ctx, io.Discard, r, 1<<20)
	if err == nil {
		t.Errorf("cancel during the final EOF read admitted %d bytes — must fail", n)
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// dataCancellingReader serves (payload, EOF) together — legal for a
// Reader — cancelling inside that final read.
type dataCancellingReader struct {
	payload []byte
	cancel  context.CancelFunc
	done    bool
}

func (r *dataCancellingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	r.cancel()
	return copy(p, r.payload), io.EOF
}

func TestCopyCtx_CancelDuringFinalDataRefuses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &dataCancellingReader{payload: []byte("final-bytes"), cancel: cancel}
	n, err := copyCtx(ctx, io.Discard, r, 1<<20)
	if err == nil {
		t.Errorf("cancel during the final data read admitted %d bytes — must fail", n)
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if n != 0 {
		t.Errorf("cancelled bytes must be discarded unwritten, got n=%d", n)
	}
}
