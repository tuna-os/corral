package web

import (
	"fmt"
	"net/http"

	"github.com/tuna-os/corral/pkg/config"
)

// handleContexts lists the targets a create request can name, and the default.
func handleContexts(w http.ResponseWriter, _ *http.Request) {
	jsonResp(w, http.StatusOK, map[string]any{"default": config.DefaultContext().Name, "contexts": config.Contexts()})
}

// resolveCreateTarget maps a create request's target name to the context it
// is created in. The empty name and the legacy "local" and "cluster" aliases
// predate named contexts and keep their meaning.
func resolveCreateTarget(name string, req createRequest) (config.ContextConfig, error) {
	switch name {
	case "":
		// Backward compatibility for API clients predating named targets. The
		// default context is the right answer for a request the local backend
		// can actually serve; a KubeVirt-shaped one still means the cluster,
		// because createLocalVM silently ignores those fields and would fail
		// with an unrelated "needs an ISO" message.
		target := config.DefaultContext()
		if target.Backend != "qemu" || !needsCluster(req) {
			return target, nil
		}
		return clusterContext()
	case "local":
		return config.ContextConfig{Name: "local", Backend: "qemu"}, nil
	case "cluster":
		// Legacy alias, and the fallback the dashboard hardcodes when
		// /api/contexts is unreachable: it has always meant the KubeVirt
		// cluster and must never resolve to the local backend.
		return clusterContext()
	default:
		if target, ok := config.FindContext(name); ok {
			return target, nil
		}
		return config.ContextConfig{}, fmt.Errorf("unknown context %q", name)
	}
}

// needsCluster reports whether a request uses fields only the KubeVirt backend
// implements. createLocalVM reads ISO and Import and nothing else, so anything
// listed here would otherwise be dropped on the floor.
func needsCluster(req createRequest) bool {
	return req.ContainerDisk != "" || req.PVC != "" || req.Bootc != "" ||
		req.Image != "" || req.Windows
}

// clusterContext returns the KubeVirt context to create in.
func clusterContext() (config.ContextConfig, error) {
	if target, ok := config.FindContext("kubevirt"); ok && target.Backend == "kubevirt" {
		return target, nil
	}
	for _, c := range config.Contexts() {
		if c.Backend == "kubevirt" {
			return c, nil
		}
	}
	return config.ContextConfig{}, fmt.Errorf("no KubeVirt context is configured — name a target explicitly (see corral context ls)")
}
