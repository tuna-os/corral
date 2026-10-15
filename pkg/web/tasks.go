package web

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/logging"
)

// asyncTask is a long-running operation a request started in the background —
// a bootc build, an image import, a live migration. The operation writes its
// progress log into the task (it is an io.Writer) and calls finish when done;
// clients poll the snapshot at GET /api/tasks/{id}.
type asyncTask struct {
	mu     sync.Mutex
	log    strings.Builder
	status string // "running", "done", "error"
	errMsg string
	done   chan struct{}

	// For the log record of the outcome: the task outlives its request, so
	// it keeps the request's ID to tie the two together.
	id      string
	ctx     context.Context
	started time.Time
}

// newAsyncTask creates a running task. Always use this (or taskTracker.start)
// over a bare struct literal — done must be initialized or wait() blocks
// forever.
func newAsyncTask() *asyncTask {
	return &asyncTask{status: "running", done: make(chan struct{})}
}

func (t *asyncTask) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.log.Write(p)
}

func (t *asyncTask) finish(err error) {
	t.mu.Lock()
	if err != nil {
		t.status = "error"
		t.errMsg = err.Error()
	} else {
		t.status = "done"
	}
	t.mu.Unlock()
	close(t.done)
	if t.ctx == nil {
		return
	}
	if err != nil {
		slog.WarnContext(t.ctx, "task failed", "task", t.id,
			"duration", time.Since(t.started), "error", logging.Redact(err.Error()))
		return
	}
	slog.InfoContext(t.ctx, "task done", "task", t.id, "duration", time.Since(t.started))
}

// wait blocks until finish has been called — lets tests synchronize on a
// background task instead of racing its goroutine.
func (t *asyncTask) wait() {
	<-t.done
}

func (t *asyncTask) snapshot() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]string{"status": t.status, "log": t.log.String(), "error": t.errMsg}
}

// taskTracker indexes async tasks by ID. Tasks stay registered after they
// finish so a client can still read the outcome.
type taskTracker struct {
	m sync.Map // id → *asyncTask
}

// start registers a new running task under id and returns it. ctx is the
// request that started the task; only its values are kept, so the task does
// not stop when the request ends.
func (tr *taskTracker) start(ctx context.Context, id string) *asyncTask {
	if ctx == nil { // strategy functions called directly, as tests do
		ctx = context.Background()
	}
	t := newAsyncTask()
	t.id, t.ctx, t.started = id, context.WithoutCancel(ctx), time.Now()
	tr.m.Store(id, t)
	slog.InfoContext(t.ctx, "task started", "task", id)
	return t
}

// get returns the task registered under id.
func (tr *taskTracker) get(id string) (*asyncTask, bool) {
	v, ok := tr.m.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*asyncTask), true
}

// remove forgets the task registered under id.
func (tr *taskTracker) remove(id string) {
	tr.m.Delete(id)
}

// tasks is the process-wide tracker behind GET /api/tasks/{id}.
var tasks = &taskTracker{}
