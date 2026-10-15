package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tuna-os/corral/pkg/config"
)

type nodeResp struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Roles   string `json:"roles"`
	Kubelet string `json:"kubelet"`
	Arch    string `json:"arch"`
}

func handleNodes(w http.ResponseWriter, r *http.Request) {
	// This host appears as a synthetic node whenever it has local QEMU VMs,
	// so they group under one tree entry alongside any cluster nodes.
	var localNode []nodeResp
	if len(localVMs()) > 0 {
		localNode = []nodeResp{{Name: "local", Ready: true, Roles: "this host"}}
	}

	// No KubeVirt context is configured, so there is no cluster to query and a
	// failed kubectl would only produce a misleading "no cluster connected"
	// error for a host that only runs local VMs (#91). Present the local host as
	// a node (even before its first VM exists) and skip kubectl entirely.
	if !config.HasBackend("kubevirt") {
		if localNode == nil {
			localNode = []nodeResp{{Name: "local", Ready: true, Roles: "this host"}}
		}
		jsonResp(w, http.StatusOK, localNode)
		return
	}

	out, err := requestRunner(r).Run("kubectl", "get", "nodes", "-o", "json")
	if err != nil {
		if localNode != nil {
			jsonResp(w, http.StatusOK, localNode)
			return
		}
		errResp(w, http.StatusBadGateway, fmt.Errorf("listing nodes: %w", err))
		return
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
					Architecture   string `json:"architecture"`
				} `json:"nodeInfo"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}

	nodes := []nodeResp{}
	for _, it := range res.Items {
		n := nodeResp{
			Name:    it.Metadata.Name,
			Kubelet: it.Status.NodeInfo.KubeletVersion,
			Arch:    it.Status.NodeInfo.Architecture,
		}
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				n.Ready = true
			}
		}
		var roles []string
		for label := range it.Metadata.Labels {
			if rest, ok := strings.CutPrefix(label, "node-role.kubernetes.io/"); ok {
				roles = append(roles, rest)
			}
		}
		n.Roles = strings.Join(roles, ",")
		nodes = append(nodes, n)
	}
	nodes = append(nodes, localNode...)
	jsonResp(w, http.StatusOK, nodes)
}
