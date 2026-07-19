package web

// Local (QEMU) backend support in the dashboard — issue #91, Phase 1.
// Local VMs are addressed under the reserved namespace "local" in the
// /api/vms/{ns}/{name} routes (the qemu backend has no real namespaces),
// and group under a synthetic "local" node in the Server View tree.

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// dialLocalVNC connects to a local VM's QEMU VNC listener for the browser
// console bridge (#91 Phase 2).
func dialLocalVNC(name string) (net.Conn, error) {
	addr, err := qemu.VNCAddr(name)
	if err != nil {
		return nil, err
	}
	return net.DialTimeout("tcp", addr, 5*time.Second)
}

const localNS = "local"

// localVMs returns the host's QEMU VMs shaped for the dashboard. Empty (not
// an error) when the web server runs somewhere with no local VM home — e.g.
// the in-cluster deployment.
func localVMs() []types.VM {
	vms, _ := qemu.List()
	for i := range vms {
		vms[i].Namespace = localNS
		vms[i].Node = "local"
	}
	return vms
}

// localVMAction handles start/stop/restart/delete for a local VM. Anything
// hypervisor-cluster-specific (migrate, pause, snapshot, scale…) is not a
// qemu concept here and 400s with a clear message.
func localVMAction(w http.ResponseWriter, name, action string) {
	done := taskBegin(action, localNS+"/"+name)
	var err error
	switch action {
	case "start":
		err = qemu.Start(name)
	case "stop":
		err = qemu.Stop(name)
	case "restart":
		if err = qemu.Stop(name); err == nil {
			err = qemu.Start(name)
		}
	default:
		err = fmt.Errorf("%q is not supported for local QEMU VMs", action)
		done(err)
		errResp(w, http.StatusBadRequest, err)
		return
	}
	done(err)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": action})
}
