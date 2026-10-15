package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestTaskLog_RecordsLifecycle(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	// A successful action and a failed one.
	okDone := taskBegin("", "start", "tailvm/vm1")
	okDone(nil)
	errDone := taskBegin("", "stop", "tailvm/vm2")
	errDone(fmt.Errorf("boom"))
	running := taskBegin("", "clone", "tailvm/vm3 → vm4")
	_ = running // still running

	resp, err := http.Get(fx.Server.URL + "/api/tasklog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var entries []TaskEntry
	json.NewDecoder(resp.Body).Decode(&entries)

	if len(entries) < 3 {
		t.Fatalf("got %d entries, want >= 3", len(entries))
	}
	// Newest first.
	if entries[0].Action != "clone" || entries[0].Status != "running" {
		t.Errorf("entries[0] = %+v, want running clone", entries[0])
	}
	if entries[1].Action != "stop" || entries[1].Status != "error" || entries[1].Error != "boom" {
		t.Errorf("entries[1] = %+v, want failed stop with error", entries[1])
	}
	if entries[2].Action != "start" || entries[2].Status != "ok" || entries[2].Duration == "" {
		t.Errorf("entries[2] = %+v, want ok start with duration", entries[2])
	}
	running(nil) // tidy up
}

func TestTaskLog_VMActionsRecorded(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	fx.Runner.AddPrefixResponse("virtctl start", "started", nil)
	// The task log names who asked, so the dock's User column can show it.
	req, _ := http.NewRequest(http.MethodPost, fx.Server.URL+"/api/vms/tailvm/logvm/start", nil)
	req.Header.Set("Tailscale-User-Login", "alice@example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	r2, err := http.Get(fx.Server.URL + "/api/tasklog")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var entries []TaskEntry
	json.NewDecoder(r2.Body).Decode(&entries)

	var found *TaskEntry
	for i, e := range entries {
		if e.Action == "start" && e.Target == "tailvm/logvm" {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("start of tailvm/logvm not recorded in task log: %+v", entries)
	}
	if found.User != "alice@example.com" {
		t.Errorf("task user = %q, want the caller's login alice@example.com", found.User)
	}
}

func TestTaskLog_RingCap(t *testing.T) {
	for i := 0; i < taskLogMax+50; i++ {
		taskBegin("", "noop", "x")(nil)
	}
	activity.mu.Lock()
	n := len(activity.entries)
	activity.mu.Unlock()
	if n > taskLogMax {
		t.Errorf("task log grew to %d entries, cap is %d", n, taskLogMax)
	}
}
