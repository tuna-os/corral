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
	okDone := taskBegin("start", "tailvm/vm1")
	okDone(nil)
	errDone := taskBegin("stop", "tailvm/vm2")
	errDone(fmt.Errorf("boom"))
	running := taskBegin("clone", "tailvm/vm3 → vm4")
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
	resp, _ := http.Post(fx.Server.URL+"/api/vms/tailvm/logvm/start", "application/json", nil)
	resp.Body.Close()

	r2, err := http.Get(fx.Server.URL + "/api/tasklog")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var entries []TaskEntry
	json.NewDecoder(r2.Body).Decode(&entries)

	found := false
	for _, e := range entries {
		if e.Action == "start" && e.Target == "tailvm/logvm" {
			found = true
		}
	}
	if !found {
		t.Errorf("start of tailvm/logvm not recorded in task log: %+v", entries)
	}
}

func TestTaskLog_RingCap(t *testing.T) {
	for i := 0; i < taskLogMax+50; i++ {
		taskBegin("noop", "x")(nil)
	}
	activity.mu.Lock()
	n := len(activity.entries)
	activity.mu.Unlock()
	if n > taskLogMax {
		t.Errorf("task log grew to %d entries, cap is %d", n, taskLogMax)
	}
}

func TestTaskLog_UserAndCancel(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	cancelled := false
	done := taskBeginCancelable("migrate", "tailvm/vm-mig", "alice@tailscale", func() {
		cancelled = true
	})
	defer done(nil)

	resp, err := http.Get(fx.Server.URL + "/api/tasklog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var entries []TaskEntry
	json.NewDecoder(resp.Body).Decode(&entries)

	if len(entries) == 0 {
		t.Fatal("expected at least 1 task")
	}
	first := entries[0]
	if first.User != "alice@tailscale" {
		t.Errorf("got user %q, want alice@tailscale", first.User)
	}
	if !first.Cancelable {
		t.Errorf("got cancelable=%v, want true", first.Cancelable)
	}

	// Cancel via HTTP
	cResp, err := http.Post(fmt.Sprintf("%s/api/tasks/%d/cancel", fx.Server.URL, first.ID), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cResp.Body.Close()
	if cResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel returned status %d, want 200", cResp.StatusCode)
	}
	if !cancelled {
		t.Errorf("cancel func was not invoked")
	}

	// Read log again to confirm status is error/cancelled
	r2, err := http.Get(fx.Server.URL + "/api/tasklog")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var entries2 []TaskEntry
	json.NewDecoder(r2.Body).Decode(&entries2)
	if entries2[0].Status != "error" || entries2[0].Error != "cancelled" {
		t.Errorf("task status after cancel: %+v, want status=error, error=cancelled", entries2[0])
	}
}
