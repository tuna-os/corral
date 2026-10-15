package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tuna-os/corral/pkg/kubevirt"
)

type createRequest struct {
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	CPU           int    `json:"cpu"`
	Mem           string `json:"mem"`
	Disk          string `json:"disk"`
	ContainerDisk string `json:"containerDisk"`
	Image         string `json:"image"`  // catalog name
	Import        string `json:"import"` // qcow2/raw URL
	ISO           string `json:"iso"`
	PVC           string `json:"pvc"`
	Bootc         string `json:"bootc"`
	SSHKey        string `json:"sshKey"`
	Node          string `json:"node"`
	CloudInit     string `json:"cloudInit"`
	InstanceType  string `json:"instancetype"`
	Preference    string `json:"preference"`
	Windows       bool   `json:"windows"`      // Windows installer flow (windows plugin)
	StorageClass  string `json:"storageClass"` // overrides the cluster-preferred StorageClass
	Target        string `json:"target"`       // named Corral context; legacy "cluster" and "local" remain accepted
	Firmware      string `json:"firmware"`     // "uefi" or "bios"
	UEFI          bool   `json:"uefi"`         // UEFI boot
	// user is the caller's login, for the task log, and ctx the request, for
	// the request ID of the task it starts. The handler sets both; neither is
	// read from the request body.
	user string
	ctx  context.Context
}

// handleCreateVM dispatches to the strategy matching the request shape —
// bootc build, Windows guided install, or everything else (catalog images,
// container disks, import URLs, ISO installs, PVC-backed) — and translates
// the result into the right HTTP response. The strategies themselves
// (createBootc/createWindows/createGeneric) don't touch http.ResponseWriter,
// so tests can call them directly instead of racing a goroutine through a
// full HTTP round-trip.
func handleCreateVM(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errResp(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.user, req.ctx = requestUser(r), r.Context()
	if req.Name == "" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("name is required"))
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}

	target, err := resolveCreateTarget(req.Target, req)
	if err != nil {
		errResp(w, http.StatusBadRequest, err)
		return
	}
	if target.Backend == "qemu" {
		createLocalVM(w, req)
		return
	}
	if target.Backend != "kubevirt" {
		createBackendVM(w, req, target)
		return
	}

	switch {
	case req.Bootc != "":
		id, _, err := createBootcInContext(req, ns, target.Context)
		if err != nil {
			errResp(w, statusFor(err), err)
			return
		}
		jsonResp(w, http.StatusAccepted, map[string]string{"task": id})

	case req.Windows:
		if err := createWindowsInContext(req, ns, target.Context); err != nil {
			errResp(w, statusFor(err), err)
			return
		}
		jsonResp(w, http.StatusCreated, map[string]string{"name": req.Name, "namespace": ns})

	default:
		if err := createGenericInContext(req, ns, target.Context); err != nil {
			errResp(w, statusFor(err), err)
			return
		}
		jsonResp(w, http.StatusCreated, map[string]string{"name": req.Name, "namespace": ns})
	}
}
