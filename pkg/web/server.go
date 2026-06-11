// Package web serves the Corral web UI: a Proxmox-style dashboard for the
// KubeVirt backend, with in-browser VNC (noVNC) and serial TTY (xterm.js)
// consoles. It shares the registry and cluster state with the CLI/TUI, so
// both can be used in tandem.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/hanthor/corral/pkg/config"
	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/registry"
	"github.com/hanthor/corral/pkg/types"
)

//go:embed static
var staticFS embed.FS

var store *registry.Store

// Serve starts the web UI on addr and blocks.
func Serve(addr string) error {
	if s, err := registry.NewStore(); err == nil {
		store = s
	}
	mux, err := newMux()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Corral web UI listening on http://%s\n", addr)
	return http.ListenAndServe(addr, mux)
}

// newMux builds the HTTP router. Split out from Serve so tests can exercise the
// full route table with httptest.
func newMux() (*http.ServeMux, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("GET /api/vms", handleListVMs)
	mux.HandleFunc("POST /api/vms", handleCreateVM)
	mux.HandleFunc("GET /api/nodes", handleNodes)
	mux.HandleFunc("GET /api/capabilities", handleCapabilities)
	mux.HandleFunc("GET /api/instancetypes", handleInstanceTypes)
	mux.HandleFunc("GET /api/nads", handleNADs)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/nics", handleAddNIC)
	mux.HandleFunc("GET /api/datavolumes", handleListDataVolumes)
	mux.HandleFunc("POST /api/datavolumes", handleImportDataVolume)
	mux.HandleFunc("DELETE /api/datavolumes/{ns}/{name}", handleDeleteDataVolume)
	mux.HandleFunc("GET /api/tasks/{id}", handleTaskStatus)
	mux.HandleFunc("GET /api/vms/{ns}/{name}", handleVMInfo)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/{action}", handleVMAction)
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}", handleDeleteVM)

	// Advanced operations — more specific patterns win over {action} above.
	mux.HandleFunc("POST /api/vms/{ns}/{name}/scale", handleScale)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/expand", handleExpand)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/clone", handleClone)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/template", handleMarkTemplate)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/guestinfo", handleGuestInfo)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/events", handleEvents)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/metrics", handleMetrics)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/export", handleExport)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/volumes", handleAddVolume)
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}/volumes/{vol}", handleRemoveVolume)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/snapshots", handleListSnapshots)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/snapshots", handleCreateSnapshot)
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}/snapshots/{snap}", handleDeleteSnapshot)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/snapshots/{snap}/restore", handleRestoreSnapshot)

	wsServer := func(h websocket.Handler) http.Handler {
		return websocket.Server{
			Handler: h,
			// Accept any origin: the UI is meant for localhost/tailnet use.
			Handshake: func(cfg *websocket.Config, r *http.Request) error { return nil },
		}
	}
	mux.Handle("GET /api/vnc/{ns}/{name}", wsServer(vncBridge))
	mux.Handle("GET /api/tty/{ns}/{name}", wsServer(ttyBridge))

	return mux, nil
}

func jsonResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func errResp(w http.ResponseWriter, code int, err error) {
	jsonResp(w, code, map[string]string{"error": err.Error()})
}

// ── VM list ───────────────────────────────────────────────────────

func handleListVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := kubevirt.NewClient("").ListVMs()
	if err != nil {
		errResp(w, http.StatusBadGateway, fmt.Errorf("listing VMs (is kubectl configured?): %w", err))
		return
	}

	// Merge live VMI data (IP, node) for running VMs.
	for key, vmi := range vmiIndex() {
		for i := range vms {
			if vms[i].Namespace+"/"+vms[i].Name == key {
				if vmi.IP != "" {
					vms[i].IP = vmi.IP
				}
				if vmi.Node != "" {
					vms[i].Node = vmi.Node
				}
			}
		}
	}
	if vms == nil {
		vms = []types.VM{}
	}
	jsonResp(w, http.StatusOK, vms)
}

type vmiInfo struct {
	IP   string
	Node string
}

func vmiIndex() map[string]vmiInfo {
	out, err := exec.Command("kubectl", "get", "vmis", "-A", "-o", "json").Output()
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

// ── Create / delete / actions ─────────────────────────────────────

type createRequest struct {
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	CPU           int    `json:"cpu"`
	Mem           string `json:"mem"`
	Disk          string `json:"disk"`
	ContainerDisk string `json:"containerDisk"`
	ISO           string `json:"iso"`
	PVC           string `json:"pvc"`
	Bootc         string `json:"bootc"`
	SSHKey        string `json:"sshKey"`
	Node          string `json:"node"`
	CloudInit     string `json:"cloudInit"`
	InstanceType  string `json:"instancetype"`
	Preference    string `json:"preference"`
}

// buildTask tracks a long-running bootc build kicked off from the UI.
type buildTask struct {
	mu     sync.Mutex
	log    strings.Builder
	status string // "running", "done", "error"
	errMsg string
}

func (t *buildTask) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.log.Write(p)
}

func (t *buildTask) finish(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.status = "error"
		t.errMsg = err.Error()
	} else {
		t.status = "done"
	}
}

func (t *buildTask) snapshot() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]string{"status": t.status, "log": t.log.String(), "error": t.errMsg}
}

var tasks sync.Map // id → *buildTask

func handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	v, ok := tasks.Load(r.PathValue("id"))
	if !ok {
		errResp(w, http.StatusNotFound, fmt.Errorf("no such task"))
		return
	}
	jsonResp(w, http.StatusOK, v.(*buildTask).snapshot())
}

func handleCreateVM(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errResp(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("name is required"))
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}

	// Bootc builds run as background tasks — the disk build takes minutes.
	if req.Bootc != "" {
		if !kubevirt.BootcAvailable() {
			errResp(w, http.StatusBadRequest,
				fmt.Errorf("bootc support is not enabled on this server (optional plugin — run the corral:bootc image)"))
			return
		}
		sshKey := strings.TrimSpace(req.SSHKey)
		if sshKey == "" {
			sshKey = kubevirt.LoadSSHPublicKey()
		}
		if sshKey == "" {
			errResp(w, http.StatusBadRequest,
				fmt.Errorf("sshKey is required for bootc VMs (no key on the server)"))
			return
		}
		id := fmt.Sprintf("bootc-%s-%d", req.Name, time.Now().UnixNano())
		task := &buildTask{status: "running"}
		tasks.Store(id, task)

		go func() {
			build, err := kubevirt.BootcBuildDisk(req.Name, ns, req.Bootc, sshKey, req.Disk, task)
			if err == nil {
				vm := kubevirt.GenerateBootcVM(req.Name, ns, build.PVCName, req.Bootc,
					build.RootUUID, build.KernelVersion, req.Mem, req.CPU, req.Node)
				err = kubevirt.Apply(vm)
			}
			if err == nil && store != nil {
				store.Set(req.Name, types.RegistryEntry{
					Backend:   "kubevirt",
					Namespace: ns,
					Extra:     map[string]string{"bootc_image": req.Bootc},
				})
			}
			task.finish(err)
		}()
		jsonResp(w, http.StatusAccepted, map[string]string{"task": id})
		return
	}

	opts := types.CreateOpts{
		Name:             req.Name,
		Namespace:        ns,
		CPU:              req.CPU,
		Mem:              req.Mem,
		Disk:             req.Disk,
		ContainerDisk:    req.ContainerDisk,
		ISO:              req.ISO,
		PVC:              req.PVC,
		Node:             req.Node,
		CloudInitExtra:   req.CloudInit,
		InstanceType:     req.InstanceType,
		Preference:       req.Preference,
		SSHPublicKey:     kubevirt.LoadSSHPublicKey(),
		TailscaleAuthKey: config.AuthKey(),
	}
	if err := kubevirt.CreateVM(opts); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	if store != nil {
		store.Set(req.Name, types.RegistryEntry{
			Backend:   "kubevirt",
			Namespace: ns,
			Password:  kubevirt.LastPassword,
		})
	}
	jsonResp(w, http.StatusCreated, map[string]string{"name": req.Name, "namespace": ns})
}

func handleVMAction(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	action := r.PathValue("action")

	c := kubevirt.NewClient(ns)
	var err error
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
		json.NewDecoder(r.Body).Decode(&b) // empty body is fine
		err = c.Migrate(name, b.TargetNode)
	default:
		errResp(w, http.StatusBadRequest, fmt.Errorf("unknown action %q", action))
		return
	}
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleExport streams a VM disk backup (gzip) to the browser. The VM must be
// stopped (its RWO disk can't be read while running). The disk is exported to a
// pod-local temp file via virtctl, then streamed and removed.
func handleExport(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if exec.Command("kubectl", "get", "vmi", name, "-n", ns).Run() == nil {
		errResp(w, http.StatusConflict, fmt.Errorf("stop %s before exporting (its disk is in use while running)", name))
		return
	}
	tmp, err := os.CreateTemp("", name+"-*.img.gz")
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	if _, err := kubevirt.NewClient(ns).Export(name, "", tmpName); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	f, err := os.Open(tmpName)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".img.gz"))
	if st, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	io.Copy(w, f)
}

func handleDeleteVM(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if err := kubevirt.NewClient(ns).DeleteVM(name); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	if store != nil {
		store.Remove(name)
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func handleVMInfo(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	data, err := kubevirt.NewClient(ns).VMInfo(name)
	if err != nil {
		errResp(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// ── Nodes ─────────────────────────────────────────────────────────

type nodeResp struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Roles   string `json:"roles"`
	Kubelet string `json:"kubelet"`
	Arch    string `json:"arch"`
}

func handleNodes(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("kubectl", "get", "nodes", "-o", "json").Output()
	if err != nil {
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
	jsonResp(w, http.StatusOK, nodes)
}

// ── Consoles ──────────────────────────────────────────────────────

// vncBridge proxies a binary websocket (noVNC) to `virtctl vnc --proxy-only`.
func vncBridge(ws *websocket.Conn) {
	defer ws.Close()
	ns, name := ws.Request().PathValue("ns"), ws.Request().PathValue("name")
	if ns == "" || name == "" {
		return
	}

	port, err := freePort()
	if err != nil {
		return
	}
	proxy := exec.Command("virtctl", "vnc", name, "-n", ns,
		"--proxy-only", "--port", strconv.Itoa(port))
	proxy.Stdout = io.Discard
	proxy.Stderr = io.Discard
	if err := proxy.Start(); err != nil {
		return
	}
	defer func() {
		proxy.Process.Kill()
		proxy.Wait()
	}()

	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if conn == nil {
		return
	}
	defer conn.Close()

	ws.PayloadType = websocket.BinaryFrame
	done := make(chan struct{}, 2)
	go func() { io.Copy(conn, ws); done <- struct{}{} }()
	go func() { io.Copy(ws, conn); done <- struct{}{} }()
	<-done
}

// ttyBridge proxies a binary websocket (xterm.js) to `virtctl console`,
// giving a serial TTY in the browser.
func ttyBridge(ws *websocket.Conn) {
	defer ws.Close()
	ns, name := ws.Request().PathValue("ns"), ws.Request().PathValue("name")
	if ns == "" || name == "" {
		return
	}

	console := exec.Command("virtctl", "console", name, "-n", ns)
	stdin, err := console.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := console.StdoutPipe()
	if err != nil {
		return
	}
	console.Stderr = console.Stdout
	if err := console.Start(); err != nil {
		return
	}
	defer func() {
		console.Process.Kill()
		console.Wait()
	}()

	ws.PayloadType = websocket.BinaryFrame
	done := make(chan struct{}, 2)
	go func() { io.Copy(stdin, ws); done <- struct{}{} }()
	go func() { io.Copy(ws, stdout); done <- struct{}{} }()
	<-done
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
