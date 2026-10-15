package web

import (
	"strings"
	"sync"
)

// buildTask tracks a long-running bootc build kicked off from the UI.
type buildTask struct {
	mu     sync.Mutex
	log    strings.Builder
	status string // "running", "done", "error"
	errMsg string
	done   chan struct{}
}

// newBuildTask creates a running task. Always use this over a bare struct
// literal — done must be initialized or wait() blocks forever.
func newBuildTask() *buildTask {
	return &buildTask{status: "running", done: make(chan struct{})}
}

func (t *buildTask) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.log.Write(p)
}

func (t *buildTask) finish(err error) {
	t.mu.Lock()
	if err != nil {
		t.status = "error"
		t.errMsg = err.Error()
	} else {
		t.status = "done"
	}
	t.mu.Unlock()
	close(t.done)
}

// wait blocks until finish has been called — lets tests synchronize on a
// background task instead of racing its goroutine.
func (t *buildTask) wait() {
	<-t.done
}

func (t *buildTask) snapshot() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]string{"status": t.status, "log": t.log.String(), "error": t.errMsg}
}

var tasks sync.Map // id → *buildTask
