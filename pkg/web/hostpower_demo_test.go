package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// resetDemoHostPower puts the fixture back to its declared starting state and
// restores it when the test ends, so one test's power change cannot decide
// another's result.
func resetDemoHostPower(t *testing.T) {
	t.Helper()
	demoHostPowerMu.Lock()
	saved := make([]demoHost, len(demoHostPowerHosts))
	for i, h := range demoHostPowerHosts {
		saved[i] = *h
	}
	demoHostPowerMu.Unlock()
	t.Cleanup(func() {
		demoHostPowerMu.Lock()
		for i := range demoHostPowerHosts {
			*demoHostPowerHosts[i] = saved[i]
		}
		demoHostPowerMu.Unlock()
	})
}

func TestDemoHostPowerListReportsOnlyAllowedActions(t *testing.T) {
	resetDemoHostPower(t)
	hosts := demoHostPowerList()
	if len(hosts) == 0 {
		t.Fatal("demo host power reported no hosts")
	}
	for _, h := range hosts {
		if h.ID == "" || h.Name == "" || h.State == "" {
			t.Errorf("host %+v is missing an identifier, a name or a state", h)
		}
		switch h.State {
		case "running":
			if !slices.Equal(h.Actions, []string{"stop"}) {
				t.Errorf("running host %s offers %v, want [stop]", h.Name, h.Actions)
			}
		case "stopped":
			if !slices.Equal(h.Actions, []string{"start"}) {
				t.Errorf("stopped host %s offers %v, want [start]", h.Name, h.Actions)
			}
		default:
			if len(h.Actions) != 0 {
				t.Errorf("host %s in state %q offers %v, want nothing while it changes",
					h.Name, h.State, h.Actions)
			}
		}
	}
}

func TestDemoHostPowerStartPassesThroughStarting(t *testing.T) {
	resetDemoHostPower(t)
	settle := demoHostPowerSettle
	demoHostPowerSettle = 40 * time.Millisecond
	t.Cleanup(func() { demoHostPowerSettle = settle })

	stopped := ""
	for _, h := range demoHostPowerList() {
		if h.State == "stopped" {
			stopped = h.ID
			break
		}
	}
	if stopped == "" {
		t.Fatal("the fixture reports no stopped host to start")
	}

	if !demoHostPowerAct(stopped, "start") {
		t.Fatalf("start %s reported the host as unknown", stopped)
	}
	state := func() string {
		for _, h := range demoHostPowerList() {
			if h.ID == stopped {
				return h.State
			}
		}
		return ""
	}
	if got := state(); got != "starting" {
		t.Fatalf("state right after start is %q, want starting", got)
	}
	time.Sleep(demoHostPowerSettle * 3)
	if got := state(); got != "running" {
		t.Fatalf("state after the settle window is %q, want running", got)
	}
}

func TestDemoHostPowerIgnoresARedundantAction(t *testing.T) {
	resetDemoHostPower(t)
	running := ""
	for _, h := range demoHostPowerList() {
		if h.State == "running" {
			running = h.ID
			break
		}
	}
	if running == "" {
		t.Fatal("the fixture reports no running host")
	}
	// Starting a running machine must leave it alone: the providers behind
	// this hook are idempotent, and a check that starts one twice must not
	// see it drop into an intermediate state.
	if !demoHostPowerAct(running, "start") {
		t.Fatalf("start %s reported the host as unknown", running)
	}
	for _, h := range demoHostPowerList() {
		if h.ID == running && h.State != "running" {
			t.Fatalf("a redundant start moved %s to %q", running, h.State)
		}
	}
}

func TestDemoHostPowerRejectsAnUnknownHost(t *testing.T) {
	resetDemoHostPower(t)
	if demoHostPowerAct("i-nosuchhost", "start") {
		t.Error("an unknown host id was accepted")
	}
}

// The fixture must reach the API only in demo mode: a real deployment finds
// providers through the plugin handshake and nowhere else.
func TestHostPowerEndpointServesTheFixtureOnlyInDemoMode(t *testing.T) {
	resetDemoHostPower(t)
	saved := demoMode
	t.Cleanup(func() { demoMode = saved })

	read := func() hostPowerResp {
		t.Helper()
		rec := httptest.NewRecorder()
		handleHostPower(rec, httptest.NewRequest(http.MethodGet, "/api/hostpower", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/hostpower answered %d", rec.Code)
		}
		var got hostPowerResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("response is not valid JSON: %v", err)
		}
		return got
	}

	demoMode = true
	withDemo := read()
	if len(withDemo.Hosts) == 0 {
		t.Fatal("demo mode reported no hosts, so the host-power UI has nothing to draw")
	}
	for _, h := range withDemo.Hosts {
		if h.Plugin != demoHostPowerPlugin {
			t.Errorf("host %s names plugin %q, want %q", h.Name, h.Plugin, demoHostPowerPlugin)
		}
	}
	if slices.Contains(hostPowerPlugins(), demoHostPowerPlugin) {
		t.Error("the fixture appears among the installed plugins")
	}

	demoMode = false
	for _, h := range read().Hosts {
		if h.Plugin == demoHostPowerPlugin {
			t.Error("the fixture served a host outside demo mode")
		}
	}
}

func TestHostPowerActionRejectsAnUnknownFixtureHost(t *testing.T) {
	resetDemoHostPower(t)
	saved := demoMode
	demoMode = true
	t.Cleanup(func() { demoMode = saved })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/hostpower/"+demoHostPowerPlugin+"/start?id=i-nosuchhost", nil)
	req.SetPathValue("plugin", demoHostPowerPlugin)
	req.SetPathValue("action", "start")
	handleHostPowerAction(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown host id answered %d, want 404", rec.Code)
	}
}
