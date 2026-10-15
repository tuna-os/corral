package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Characterization tests for the async task tracker (#503): the log-tailing
// task handle that bootc builds, imports and migrations report through, and
// GET /api/tasks/{id}, which the dashboard polls.

func TestBuildTask_Lifecycle(t *testing.T) {
	task := newBuildTask()
	if got := task.snapshot(); got["status"] != "running" || got["log"] != "" || got["error"] != "" || len(got) != 3 {
		t.Fatalf("new task snapshot = %v", got)
	}

	fmt.Fprintf(task, "step %d\n", 1)
	if _, err := task.Write([]byte("step 2\n")); err != nil {
		t.Fatal(err)
	}
	if got := task.snapshot()["log"]; got != "step 1\nstep 2\n" {
		t.Fatalf("log = %q", got)
	}

	select {
	case <-task.done:
		t.Fatal("done closed before finish")
	default:
	}
	task.finish(nil)
	task.wait()
	if got := task.snapshot(); got["status"] != "done" || got["error"] != "" {
		t.Fatalf("finished task snapshot = %v", got)
	}

	// The log stays writable after finish; the status does not change.
	fmt.Fprint(task, "late\n")
	if got := task.snapshot(); got["log"] != "step 1\nstep 2\nlate\n" || got["status"] != "done" {
		t.Fatalf("after late write = %v", got)
	}
}

func TestBuildTask_FinishWithError(t *testing.T) {
	task := newBuildTask()
	fmt.Fprint(task, "pulling\n")
	task.finish(errors.New("pull failed"))
	got := task.snapshot()
	if got["status"] != "error" || got["error"] != "pull failed" || got["log"] != "pulling\n" {
		t.Fatalf("snapshot = %v", got)
	}
}

func TestBuildTask_WaitBlocksUntilFinish(t *testing.T) {
	task := newBuildTask()
	waited := make(chan struct{})
	go func() {
		task.wait()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("wait returned before finish")
	case <-time.After(20 * time.Millisecond):
	}
	task.finish(nil)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after finish")
	}
}

func TestBuildTask_ConcurrentWritesAreSafe(t *testing.T) {
	task := newBuildTask()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				fmt.Fprint(task, "x")
				_ = task.snapshot()
			}
		}()
	}
	wg.Wait()
	if n := len(task.snapshot()["log"]); n != 800 {
		t.Fatalf("log length = %d, want 800", n)
	}
}

func TestHandleTaskStatus_TracksLifecycle(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	task := newBuildTask()
	tasks.Store("char-task", task)
	defer tasks.Delete("char-task")

	get := func() map[string]string {
		t.Helper()
		resp := mustGet(t, fx.Server.URL+"/api/tasks/char-task")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status code = %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q", ct)
		}
		var body map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	if got := get(); got["status"] != "running" || got["log"] != "" || got["error"] != "" {
		t.Fatalf("running: %v", got)
	}
	fmt.Fprint(task, "building\n")
	if got := get(); got["log"] != "building\n" {
		t.Fatalf("log tail: %v", got)
	}
	task.finish(errors.New("boom"))
	if got := get(); got["status"] != "error" || got["error"] != "boom" || got["log"] != "building\n" {
		t.Fatalf("finished: %v", got)
	}
}

func TestHandleTaskStatus_Unknown(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	resp := mustGet(t, fx.Server.URL+"/api/tasks/no-such-task")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound || body != "{\"error\":\"no such task\"}\n" {
		t.Fatalf("unknown task: %d %q", resp.StatusCode, body)
	}
}
