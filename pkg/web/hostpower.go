package web

// Host power hook: installed plugins that declare the sdk.CapHostPower
// capability can report and change the power state of the machines that
// host VMs. Core stays provider-agnostic (ADR-0007): it discovers capable
// plugins through the metadata handshake, aggregates their `host-power list`
// output, and forwards start/stop. Starting and stopping are POSTs, so
// adminGate already limits them to CORRAL_ADMINS.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/plugin"
	"github.com/tuna-os/corral/pkg/plugin/sdk"
)

// hostPowerTimeout bounds each plugin call; a cloud API that hangs must not
// hang the UI poll.
var hostPowerTimeout = 30 * time.Second

type hostPowerHost struct {
	sdk.Host
	Plugin string `json:"plugin"`
}

type hostPowerResp struct {
	Hosts []hostPowerHost `json:"hosts"`
	// Errors reports plugins that failed, by name, so one broken provider
	// doesn't hide the others.
	Errors map[string]string `json:"errors,omitempty"`
}

var hostIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,256}$`)

// hostPowerPlugins returns installed plugins declaring CapHostPower. The
// metadata handshake result is cached per plugin path; it only changes when
// the plugin is reinstalled.
var (
	hostPowerMetaMu    sync.Mutex
	hostPowerMetaCache = map[string][]string{}
)

func hostPowerPlugins() []string {
	var names []string
	for _, p := range plugin.Installed() {
		caps := p.Capabilities
		if len(caps) == 0 {
			hostPowerMetaMu.Lock()
			cached, ok := hostPowerMetaCache[p.Path]
			hostPowerMetaMu.Unlock()
			if !ok {
				if meta, err := plugin.Inspect(p.Name); err == nil {
					cached = meta.Capabilities
				}
				hostPowerMetaMu.Lock()
				hostPowerMetaCache[p.Path] = cached
				hostPowerMetaMu.Unlock()
			}
			caps = cached
		}
		if slices.Contains(caps, sdk.CapHostPower) {
			names = append(names, p.Name)
		}
	}
	return names
}

func runHostPower(name string, args ...string) ([]byte, error) {
	bin := plugin.Resolve(name)
	if bin == "" {
		return nil, fmt.Errorf("plugin %q is not installed", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostPowerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"host-power"}, args...)...)
	cmd.Env = append(cmd.Environ(), "CORRAL_PLUGIN="+name)
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("corral-%s host-power %s: %s", name, strings.Join(args, " "), msg)
	}
	return out, nil
}

// GET /api/hostpower
func handleHostPower(w http.ResponseWriter, r *http.Request) {
	resp := hostPowerResp{Hosts: []hostPowerHost{}}
	for _, name := range hostPowerPlugins() {
		out, err := runHostPower(name, "list")
		if err != nil {
			if resp.Errors == nil {
				resp.Errors = map[string]string{}
			}
			resp.Errors[name] = err.Error()
			continue
		}
		var hosts []sdk.Host
		if err := json.Unmarshal(out, &hosts); err != nil {
			if resp.Errors == nil {
				resp.Errors = map[string]string{}
			}
			resp.Errors[name] = fmt.Sprintf("invalid host-power list output: %v", err)
			continue
		}
		for _, h := range hosts {
			resp.Hosts = append(resp.Hosts, hostPowerHost{Host: h, Plugin: name})
		}
	}
	sort.Slice(resp.Hosts, func(i, j int) bool { return resp.Hosts[i].Name < resp.Hosts[j].Name })
	jsonResp(w, http.StatusOK, resp)
}

// POST /api/hostpower/{plugin}/{action}?id=<host-id>
//
// The host ID travels as a query parameter because provider IDs may contain
// slashes (ARNs, zone/instance paths).
func handleHostPowerAction(w http.ResponseWriter, r *http.Request) {
	name, action, id := r.PathValue("plugin"), r.PathValue("action"), r.URL.Query().Get("id")
	if action != "start" && action != "stop" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("unknown action %q (want start or stop)", action))
		return
	}
	if !hostIDPattern.MatchString(id) || strings.HasPrefix(id, "-") {
		errResp(w, http.StatusBadRequest, fmt.Errorf("invalid host id"))
		return
	}
	if !slices.Contains(hostPowerPlugins(), name) {
		errResp(w, http.StatusNotFound, fmt.Errorf("no installed plugin %q provides %s", name, sdk.CapHostPower))
		return
	}
	if _, err := runHostPower(name, action, id); err != nil {
		errResp(w, http.StatusBadGateway, err)
		return
	}
	jsonResp(w, http.StatusAccepted, map[string]string{"plugin": name, "id": id, "action": action})
}
