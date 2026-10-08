package web

import (
	"fmt"
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
	ID         int64  `json:"id"`
	Action     string `json:"action"` // "create", "start", "snapshot", …
	Target     string `json:"target"` // "corral-ns/myvm", "datavolume ns/iso", …
	Status     string `json:"status"` // "running", "ok", "error"
	Error      string `json:"error,omitempty"`
	User       string `json:"user,omitempty"`
	Log        string `json:"log,omitempty"`
	Cancelable bool   `json:"cancelable,omitempty"`
	Started    string `json:"started"`            // RFC3339
	Duration   string `json:"duration,omitempty"` // set when finished
}

const taskLogMax = 200

type taskLog struct {
	mu      sync.Mutex
	entries []*TaskEntry // newest last; served newest first
	nextID  int64
	started map[int64]time.Time
	cancels map[int64]func()
}

var activity = &taskLog{
	started: map[int64]time.Time{},
	cancels: map[int64]func(){},
}

// resetActivity empties the ring. The task log is process-global by design —
// it documents this server's activity — which makes it shared state between
// tests: one test's failed move is visible to the next test that reads
// /api/tasklog. Test fixtures call this so each starts from an empty log.
func resetActivity() {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	activity.entries = nil
	activity.nextID = 0
	activity.started = map[int64]time.Time{}
	activity.cancels = map[int64]func(){}
}

// begin records a running task and returns a finish func to call with the
// outcome. Usage: done := taskBegin("start", ns+"/"+name); …; done(err)
func taskBegin(action, target string, user ...string) func(error) {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	activity.nextID++
	id := activity.nextID
	u := ""
	if len(user) > 0 {
		u = user[0]
	}
	if u == "" {
		u = "root@pam"
	}
	e := &TaskEntry{
		ID: id, Action: action, Target: target, User: u,
		Status: "running", Started: time.Now().Format(time.RFC3339),
	}
	activity.entries = append(activity.entries, e)
	activity.started[id] = time.Now()
	if len(activity.entries) > taskLogMax {
		drop := activity.entries[0]
		delete(activity.started, drop.ID)
		delete(activity.cancels, drop.ID)
		activity.entries = activity.entries[1:]
	}
	return func(err error) {
		activity.mu.Lock()
		defer activity.mu.Unlock()
		if t, ok := activity.started[id]; ok {
			e.Duration = time.Since(t).Round(10 * time.Millisecond).String()
			delete(activity.started, id)
		}
		delete(activity.cancels, id)
		e.Cancelable = false
		if err != nil {
			e.Status = "error"
			e.Error = err.Error()
		} else {
			e.Status = "ok"
		}
	}
}

// taskBeginCancelable records a running task with a cancellation handler.
func taskBeginCancelable(action, target, user string, cancel func()) func(error) {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	activity.nextID++
	id := activity.nextID
	u := user
	if u == "" {
		u = "root@pam"
	}
	e := &TaskEntry{
		ID: id, Action: action, Target: target, User: u,
		Status: "running", Cancelable: cancel != nil,
		Started: time.Now().Format(time.RFC3339),
	}
	activity.entries = append(activity.entries, e)
	activity.started[id] = time.Now()
	if cancel != nil {
		if activity.cancels == nil {
			activity.cancels = map[int64]func(){}
		}
		activity.cancels[id] = cancel
	}
	if len(activity.entries) > taskLogMax {
		drop := activity.entries[0]
		delete(activity.started, drop.ID)
		delete(activity.cancels, drop.ID)
		activity.entries = activity.entries[1:]
	}
	return func(err error) {
		activity.mu.Lock()
		defer activity.mu.Unlock()
		if t, ok := activity.started[id]; ok {
			e.Duration = time.Since(t).Round(10 * time.Millisecond).String()
			delete(activity.started, id)
		}
		delete(activity.cancels, id)
		e.Cancelable = false
		if err != nil {
			e.Status = "error"
			e.Error = err.Error()
		} else {
			e.Status = "ok"
		}
	}
}

// cancelTaskByID cancels a running task if a cancel func is registered.
func cancelTaskByID(id int64) error {
	activity.mu.Lock()
	cancel, ok := activity.cancels[id]
	if !ok {
		activity.mu.Unlock()
		return fmt.Errorf("task %d not found or not cancelable", id)
	}
	delete(activity.cancels, id)
	for _, e := range activity.entries {
		if e.ID == id {
			e.Status = "error"
			e.Error = "cancelled"
			e.Cancelable = false
			if t, ok := activity.started[id]; ok {
				e.Duration = time.Since(t).Round(10 * time.Millisecond).String()
				delete(activity.started, id)
			}
			break
		}
	}
	activity.mu.Unlock()
	cancel()
	return nil
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
