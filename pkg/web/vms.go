package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/qemu"
)

func kubeClient(r *http.Request, ns string) *kubevirt.Client {
	return kubevirt.NewClientForContext(ns, r.URL.Query().Get("context"))
}

func handleVMInfo(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if peer, remoteNS, ok := parsePeerNamespace(ns); ok {
		proxyPeerVM(w, r, peer, remoteNS)
		return
	}
	if ns == "incus" {
		data, err := incus.NewClient(r.URL.Query().Get("context")).Info(name)
		if err != nil {
			errResp(w, 404, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(data)
		return
	}
	if ns == "libvirt" {
		data, err := libvirt.NewClient(r.URL.Query().Get("context")).Info(name)
		if err != nil {
			errResp(w, 404, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(data)
		return
	}
	var data []byte
	var err error
	if ns == localNS {
		data, err = qemu.Info(name)
	} else {
		data, err = kubeClient(r, ns).VMInfo(name)
	}
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, qemu.ErrInvalidVMName) {
			status = http.StatusBadRequest
		}
		errResp(w, status, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func handleVMAction(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if peer, remoteNS, ok := parsePeerNamespace(ns); ok {
		proxyPeerVM(w, r, peer, remoteNS)
		return
	}
	action := r.PathValue("action")
	if ns == "incus" {
		c := incus.NewClient(r.URL.Query().Get("context"))
		var err error
		switch action {
		case "start":
			err = c.Start(name)
		case "stop":
			err = c.Stop(name)
		case "restart":
			if err = c.Stop(name); err == nil {
				err = c.Start(name)
			}
		default:
			errResp(w, 400, fmt.Errorf("%q is not supported for Incus instances", action))
			return
		}
		if err != nil {
			errResp(w, 500, err)
			return
		}
		jsonResp(w, 200, map[string]string{"status": "ok"})
		return
	}
	if ns == "libvirt" {
		c := libvirt.NewClient(r.URL.Query().Get("context"))
		var err error
		switch action {
		case "start":
			err = c.Start(name)
		case "stop":
			err = c.Stop(name)
		case "restart":
			if err = c.Stop(name); err == nil {
				err = c.Start(name)
			}
		default:
			errResp(w, 400, fmt.Errorf("%q is not supported for libvirt domains", action))
			return
		}
		if err != nil {
			errResp(w, 500, err)
			return
		}
		jsonResp(w, 200, map[string]string{"status": "ok"})
		return
	}
	if ns == localNS {
		localVMAction(w, requestUser(r), name, action)
		return
	}

	c := kubeClient(r, ns)
	var err error
	done := taskBegin(requestUser(r), action, ns+"/"+name)
	switch action {
	case "start":
		err = c.StartVM(name)
	case "stop":
		err = c.StopVM(name)
	case "restart":
		err = c.RestartVM(name)
	case "pause":
		err = c.PauseVM(name)
	case "unpause":
		err = c.UnpauseVM(name)
	case "migrate":
		var b struct {
			TargetNode string `json:"targetNode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b) // empty body is fine
		err = c.Migrate(name, b.TargetNode)
	default:
		done(fmt.Errorf("unknown action"))
		errResp(w, http.StatusBadRequest, fmt.Errorf("unknown action %q", action))
		return
	}
	done(err)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleDeleteVM(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if peer, remoteNS, ok := parsePeerNamespace(ns); ok {
		proxyPeerVM(w, r, peer, remoteNS)
		return
	}
	if ns == "incus" {
		if err := incus.NewClient(r.URL.Query().Get("context")).Delete(name); err != nil {
			errResp(w, 500, err)
			return
		}
		jsonResp(w, 200, map[string]string{"status": "deleted"})
		return
	}
	if ns == "libvirt" {
		if r.URL.Query().Get("destroyStorage") != "true" {
			errResp(w, http.StatusBadRequest, fmt.Errorf("libvirt deletion requires destroyStorage=true because it permanently removes domain disks"))
			return
		}
		if err := libvirt.NewClient(r.URL.Query().Get("context")).Delete(name); err != nil {
			errResp(w, 500, err)
			return
		}
		jsonResp(w, 200, map[string]string{"status": "deleted"})
		return
	}
	done := taskBegin(requestUser(r), "delete", ns+"/"+name)
	if ns == localNS {
		if err := qemu.Delete(name); err != nil {
			done(err)
			errResp(w, localErrStatus(err), err)
			return
		}
		done(nil)
		if store != nil {
			_ = store.Remove(name)
		}
		jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
		return
	}
	if err := kubeClient(r, ns).DeleteVM(name); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	if store != nil {
		_ = store.Remove(name)
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}
