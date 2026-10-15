package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/fleet"
	"github.com/tuna-os/corral/pkg/types"
)

func handleListVMs(w http.ResponseWriter, r *http.Request) {
	result := fleet.List(r.Context())
	vms := append(result.VMs, peerVMs()...)
	// The full-page "No cluster connected" state is only appropriate when the
	// user's PRIMARY backend is the one that failed and there is nothing to
	// show. A qemu-default host with a stray kubeconfig (which makes KubeVirt a
	// configured-but-unused context, see config.Contexts) must never get a
	// cluster nag over a backend it doesn't drive — its default backend listed
	// fine, just empty. So only surface offline when the default context itself
	// errored; otherwise a failing non-default backend is a silent partial.
	if len(vms) == 0 && len(config.Peers()) == 0 {
		if _, primaryFailed := result.Errors[config.DefaultContext().Name]; primaryFailed {
			if bad := remoteErrors(result.Errors); bad != "" {
				errResp(w, http.StatusBadGateway, fmt.Errorf("listing VMs: %s", bad))
				return
			}
		}
	}
	if vms == nil {
		vms = []types.VM{}
	}
	if len(result.Errors) > 0 {
		w.Header().Set("X-Corral-Partial", "true")
	}
	jsonResp(w, http.StatusOK, vms)
}

// remoteErrors renders the listing errors that came from a remote (non-local)
// context as a stable, sorted "name: message" list, or "" when every failure
// was local. Sorted so the message doesn't churn across map iterations.
func remoteErrors(errs map[string]string) string {
	if len(errs) == 0 {
		return ""
	}
	var parts []string
	for _, c := range config.Contexts() {
		if c.Backend == "qemu" {
			continue
		}
		if msg, ok := errs[c.Name]; ok {
			parts = append(parts, c.Name+": "+msg)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func handleInventory(w http.ResponseWriter, r *http.Request) {
	result := fleet.List(r.Context())
	result.VMs = append(result.VMs, peerVMs()...)
	jsonResp(w, http.StatusOK, result)
}

func handleAPIMeta(w http.ResponseWriter, _ *http.Request) {
	jsonResp(w, http.StatusOK, map[string]any{
		"protocol": types.PeerProtocol,
		"features": []string{"inventory", "peer-relay", "service-token", "instance-capabilities"},
	})
}

type vmiInfo struct {
	IP   string
	Node string
}

func vmiIndex() map[string]vmiInfo {
	out, err := defaultRunner.Run("kubectl", "get", "vmis", "-A", "-o", "json")
	if err != nil {
		return nil
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				NodeName   string `json:"nodeName"`
				Interfaces []struct {
					IPAddress string `json:"ipAddress"`
				} `json:"interfaces"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil {
		return nil
	}
	idx := make(map[string]vmiInfo, len(res.Items))
	for _, it := range res.Items {
		info := vmiInfo{Node: it.Status.NodeName}
		if len(it.Status.Interfaces) > 0 {
			info.IP = it.Status.Interfaces[0].IPAddress
		}
		idx[it.Metadata.Namespace+"/"+it.Metadata.Name] = info
	}
	return idx
}
