package web

// Local (QEMU) backend support in the dashboard — issue #91, Phase 1.
// Local VMs are addressed under the reserved namespace "local" in the
// /api/vms/{ns}/{name} routes (the qemu backend has no real namespaces),
// and group under a synthetic "local" node in the Server View tree.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// localCreates tracks the download-then-create goroutines that createLocalVM
// starts. They read qemu.VMHome, which demo mode and tests repoint, so a test
// must wait for its own goroutine before the next test moves the state dir.
var localCreates sync.WaitGroup

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
		vms[i].SetIdentity()
	}
	return vms
}

// localVMAction handles start/stop/restart/delete for a local VM. Anything
// hypervisor-cluster-specific (migrate, pause, snapshot, scale…) is not a
// qemu concept here and 400s with a clear message.
func localVMAction(w http.ResponseWriter, user, name, action string) {
	done := taskBegin(user, action, localNS+"/"+name)
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
		errResp(w, localErrStatus(err), err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": action})
}

// createLocalVM handles POST /api/vms with target "local" (#91 Phase 3):
// a QEMU VM on the web server's host. Source is an installer ISO or a
// prepared qcow2 — a path on this host, or a URL downloaded once into
// VMHome/cache. qemu has no cloud-init path, so no key/user-data fields.
func createLocalVM(w http.ResponseWriter, req createRequest) {
	// The failure paths below remove VMHome/<name>: the name must be a
	// plain new VM name, and must not be a VM this request did not create.
	if err := qemu.ValidateVMName(req.Name); err != nil {
		errResp(w, http.StatusBadRequest, err)
		return
	}
	if qemu.Exists(req.Name) {
		errResp(w, http.StatusConflict, fmt.Errorf("local VM %q already exists", req.Name))
		return
	}
	src, qcow := req.ISO, false
	if src == "" {
		src, qcow = req.Import, true
	}
	if src == "" {
		errResp(w, http.StatusBadRequest,
			fmt.Errorf("local VMs need an installer ISO or a qcow2 image — a path on this host, or a URL"))
		return
	}
	// Force: the "already exists" check above is the real one. After it,
	// VMHome/<name> holds only this request's RecordCreating placeholder,
	// which qemu.Create would otherwise refuse as an existing VM (#501) —
	// the same pattern as `corral create --bootc` (cmd/create.go).
	opts := types.CreateOpts{
		Name: req.Name, Backend: "qemu",
		CPU: req.CPU, Mem: req.Mem, Disk: req.Disk,
		UEFI:  req.Firmware == "uefi" || req.UEFI,
		Force: true,
	}
	assign := func(path string) {
		if qcow {
			opts.QCOW = path
		} else {
			opts.ISO = path
		}
	}

	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		// Download (cached) then create — async, visible in the task panel and VM inventory.
		_ = qemu.RecordCreating(req.Name, opts)
		done := taskBegin(req.user, "create local (download)", localNS+"/"+req.Name)
		localCreates.Add(1)
		go func() {
			defer localCreates.Done()
			path, err := downloadToCache(src)
			if err == nil {
				assign(path)
				err = qemu.Create(opts)
			}
			if err != nil {
				_ = os.RemoveAll(filepath.Join(qemu.VMHome(), req.Name))
			}
			done(err)
		}()
		jsonResp(w, http.StatusAccepted, map[string]string{"name": req.Name, "namespace": localNS, "status": "downloading"})
		return
	}

	assign(src)
	_ = qemu.RecordCreating(req.Name, opts)
	done := taskBegin(req.user, "create local", localNS+"/"+req.Name)
	err := qemu.Create(opts)
	if err != nil {
		_ = os.RemoveAll(filepath.Join(qemu.VMHome(), req.Name))
	}
	done(err)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusCreated, map[string]string{"name": req.Name, "namespace": localNS})
}

// localErrStatus is the HTTP status for a failed local VM operation: a name
// the qemu package refused is the client's error, anything else the host's.
func localErrStatus(err error) int {
	if errors.Is(err, qemu.ErrInvalidVMName) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// downloadToCache fetches url into VMHome/cache once; later creates from the
// same image are instant. Streams via a temp file so a torn download never
// poisons the cache.
func downloadToCache(url string) (string, error) {
	cacheDir := filepath.Join(qemu.VMHome(), "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	name := path.Base(url)
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("cannot derive a filename from %q", url)
	}
	dest := filepath.Join(cacheDir, name)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil // cache hit
	}
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(cacheDir, name+".part-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return dest, os.Rename(tmp.Name(), dest)
}
