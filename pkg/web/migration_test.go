package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

// fastMigrationTimings shrinks the watch loop so tests don't sleep for seconds.
func fastMigrationTimings(t *testing.T) {
	t.Helper()
	os, op, ot := migrationSettle, migrationPollInterval, migrationTimeout
	migrationSettle = time.Millisecond
	migrationPollInterval = time.Millisecond
	migrationTimeout = 2 * time.Second
	t.Cleanup(func() {
		migrationSettle, migrationPollInterval, migrationTimeout = os, op, ot
	})
}

func TestWatchMigration_Completes(t *testing.T) {
	fastMigrationTimings(t)
	r := shell.NewFake()
	r.AddResponseKV("kubectl", []string{"get", "vmi", "web", "-n", "ns", "-o", "json"},
		`{"status":{"migrationState":{"completed":true,"sourceNode":"bihar","targetNode":"karnataka"}}}`, nil)
	c := kubevirt.NewClientWithRunner("ns", r)

	var b strings.Builder
	if err := watchMigration(c, "web", &b); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !strings.Contains(b.String(), "karnataka") || !strings.Contains(b.String(), "complete") {
		t.Errorf("log missing completion detail: %q", b.String())
	}
}

func TestWatchMigration_Fails(t *testing.T) {
	fastMigrationTimings(t)
	r := shell.NewFake()
	r.AddResponseKV("kubectl", []string{"get", "vmi", "web", "-n", "ns", "-o", "json"},
		`{"status":{"migrationState":{"failed":true,"sourceNode":"bihar","targetNode":"karnataka"}}}`, nil)
	c := kubevirt.NewClientWithRunner("ns", r)

	if err := watchMigration(c, "web", &strings.Builder{}); err == nil {
		t.Error("expected error on failed migration")
	}
}

func TestHandleMigrate_AsyncSuccess(t *testing.T) {
	fastMigrationTimings(t)
	fx := NewTestFixture()
	defer fx.Server.Close()

	// Explicit target → Migrate pins nodeSelector then triggers virtctl migrate.
	fx.Runner.AddResponseKV("kubectl", []string{"patch", "vm", "testvm", "-n", "tailvm",
		"--type", "merge", "-p", `{"spec":{"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":"karnataka"}}}}}`}, "", nil)
	fx.Runner.AddResponseKV("/fake/bin/virtctl", []string{"migrate", "testvm", "-n", "tailvm"}, "", nil)
	fx.Runner.AddResponseKV("kubectl", []string{"get", "vmi", "testvm", "-n", "tailvm", "-o", "json"},
		`{"status":{"migrationState":{"completed":true,"sourceNode":"bihar","targetNode":"karnataka"}}}`, nil)

	resp, err := http.Post(fx.Server.URL+"/api/vms/tailvm/testvm/migrate", "application/json",
		strings.NewReader(`{"targetNode":"karnataka"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	var body struct {
		Task string `json:"task"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Task == "" {
		t.Fatal("expected a task id")
	}

	// The background watch should drive the task to done quickly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r2, _ := http.Get(fx.Server.URL + "/api/tasks/" + body.Task)
		var ts struct{ Status, Log string }
		json.NewDecoder(r2.Body).Decode(&ts)
		r2.Body.Close()
		if ts.Status == "done" {
			if !strings.Contains(ts.Log, "karnataka") {
				t.Errorf("task log missing target node: %q", ts.Log)
			}
			return
		}
		if ts.Status == "error" {
			t.Fatalf("task errored: %q", ts.Log)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("migration task did not complete in time")
}

func TestOrHelper(t *testing.T) {
	if or("", "x") != "x" {
		t.Error("or should return fallback for empty")
	}
	if or("a", "x") != "a" {
		t.Error("or should return value when non-empty")
	}
}
