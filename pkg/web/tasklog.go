package web

import (
	"net/http"
	"sync"
	"time"
)

// The task log is corral's answer to Proxmox's bottom task panel: every
// mutating operation the server performs is recorded with status and
// duration, newest first, queryable at GET /api/tasklog. In-memory ring —
// it documents this server's activity, not durable cluster history (the
// per-VM Events tab covers cluster-side events).

// TaskEntry is one row in the task log.
type TaskEntry struct {
	ID       int64  `json:"id"`
	Action   string `json:"action"` // "create", "start", "snapshot", …
	Target   string `json:"target"` // "corral-ns/myvm", "datavolume ns/iso", …
	Status   string `json:"status"` // "running", "ok", "error"
	Error    string `json:"error,omitempty"`
	Started  string `json:"started"`            // RFC3339
	Duration string `json:"duration,omitempty"` // set when finished
}

const taskLogMax = 200

type taskLog struct {
	mu      sync.Mutex
	entries []*TaskEntry // newest last; served newest first
	nextID  int64
	started map[int64]time.Time
}

var activity = &taskLog{started: map[int64]time.Time{}}

// begin records a running task and returns a finish func to call with the
// outcome. Usage: done := taskBegin("start", ns+"/"+name); …; done(err)
func taskBegin(action, target string) func(error) {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	activity.nextID++
	id := activity.nextID
	e := &TaskEntry{
		ID: id, Action: action, Target: target,
		Status: "running", Started: time.Now().Format(time.RFC3339),
	}
	activity.entries = append(activity.entries, e)
	activity.started[id] = time.Now()
	if len(activity.entries) > taskLogMax {
		drop := activity.entries[0]
		delete(activity.started, drop.ID)
		activity.entries = activity.entries[1:]
	}
	return func(err error) {
		activity.mu.Lock()
		defer activity.mu.Unlock()
		if t, ok := activity.started[id]; ok {
			e.Duration = time.Since(t).Round(10 * time.Millisecond).String()
			delete(activity.started, id)
		}
		if err != nil {
			e.Status = "error"
			e.Error = err.Error()
		} else {
			e.Status = "ok"
		}
	}
}

// GET /api/tasklog — recent server-side tasks, newest first.
func handleTaskLog(w http.ResponseWriter, r *http.Request) {
	activity.mu.Lock()
	out := make([]TaskEntry, 0, len(activity.entries))
	for i := len(activity.entries) - 1; i >= 0; i-- {
		out = append(out, *activity.entries[i])
	}
	activity.mu.Unlock()
	jsonResp(w, http.StatusOK, out)
}
