package web

// A host-power source for demo mode.
//
// The host-power hook belongs to plugins (ADR-0007), and core knows no
// provider. That left the capability with no coverage: `corral web --demo`
// installs no plugins, so GET /api/hostpower answered with an empty list, and
// every screen the hook feeds — the tree rows, the context menu and the
// host-power page — never appeared in the browser suite.
//
// This is a fixture, not a provider. It lives behind demoMode, it is not a
// plugin and it does not enter hostPowerPlugins(), so a real deployment still
// reaches providers only through the metadata handshake. It reports machines
// that carry the demo nodes, and start and stop move them through the
// intermediate state a cloud API would report, so the UI has something to
// render while a machine changes state.

import (
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/plugin/sdk"
)

// demoHostPowerPlugin is the name the API reports as the source of these
// hosts. It is not an installed plugin; the action handler matches on it
// before it consults the installed set.
const demoHostPowerPlugin = "demo-power"

// demoHostPowerSettle is how long a machine stays in its intermediate state.
// It is longer than the page's 5-second poll on purpose: a browser check has
// to be able to see "starting" before it becomes "running", and a window
// shorter than one poll would turn that check into a race.
var demoHostPowerSettle = 6 * time.Second

type demoHost struct {
	id, name, node, detail string
	state                  string
	// at records when the current intermediate state began.
	at time.Time
}

var (
	demoHostPowerMu    sync.Mutex
	demoHostPowerHosts = []*demoHost{
		{id: "i-0demo1", name: "corral-1", node: "corral-1", state: "running", detail: "on-premise, always on"},
		{id: "i-0demo2", name: "corral-2", node: "corral-2", state: "running", detail: "$0.096/hour"},
		{id: "i-0demo3", name: "corral-3", node: "corral-3", state: "stopped", detail: "idle since 14:02"},
	}
)

// demoHostPowerSettleNow moves any machine whose intermediate state has run
// its course to the state it was heading for. Both the list and the action
// handler call this first, so a settled machine never has to wait for a poll.
func demoHostPowerSettleNow() {
	now := time.Now()
	for _, h := range demoHostPowerHosts {
		switch h.state {
		case "starting":
			if now.Sub(h.at) >= demoHostPowerSettle {
				h.state = "running"
			}
		case "stopping":
			if now.Sub(h.at) >= demoHostPowerSettle {
				h.state = "stopped"
			}
		}
	}
}

// demoHostPowerList reports the fixture's machines in the shape a plugin's
// `host-power list` would return.
func demoHostPowerList() []sdk.Host {
	demoHostPowerMu.Lock()
	defer demoHostPowerMu.Unlock()
	demoHostPowerSettleNow()
	out := make([]sdk.Host, 0, len(demoHostPowerHosts))
	for _, h := range demoHostPowerHosts {
		var actions []string
		switch h.state {
		case "running":
			actions = []string{"stop"}
		case "stopped":
			actions = []string{"start"}
		}
		detail := h.detail
		if h.state == "starting" || h.state == "stopping" {
			detail = strings.ToUpper(h.state[:1]) + h.state[1:] + "…"
		}
		out = append(out, sdk.Host{
			ID: h.id, Name: h.name, Node: h.node,
			State: h.state, Actions: actions, Detail: detail,
		})
	}
	return out
}

// demoHostPowerAct applies start or stop to one machine. It reports whether
// the machine exists, so the handler can answer 404 for an unknown id rather
// than pretend to accept it.
func demoHostPowerAct(id, action string) bool {
	demoHostPowerMu.Lock()
	defer demoHostPowerMu.Unlock()
	demoHostPowerSettleNow()
	for _, h := range demoHostPowerHosts {
		if h.id != id {
			continue
		}
		// A machine already in the state the caller asked for stays put. The
		// real providers are idempotent here, and a check that stops a
		// stopped machine must not see it start.
		switch {
		case action == "start" && h.state == "stopped":
			h.state, h.at = "starting", time.Now()
		case action == "stop" && h.state == "running":
			h.state, h.at = "stopping", time.Now()
		}
		return true
	}
	return false
}
