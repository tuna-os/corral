package proxmox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// server translates Proxmox-shaped requests onto the corral KubeVirt backend.
type Server struct {
	ns     string
	token  string // shared API secret; "" = open (tailnet-is-auth)
	runner shell.Runner
}

func NewServer(ns string) *Server {
	return &Server{ns: ns, token: os.Getenv("CORRAL_PROXMOX_TOKEN"), runner: shell.Real{}}
}

// WithToken sets the shared API secret, overriding CORRAL_PROXMOX_TOKEN.
// An empty value is ignored (keeps the env-derived secret).
func (s *Server) WithToken(token string) *Server {
	if token != "" {
		s.token = token
	}
	return s
}

// NewHandler returns the Proxmox API handler suitable for mounting
// in an existing HTTP server (e.g. corral web).
func NewHandler(ns string) http.Handler {
	return NewServer(ns).Mux()
}

func (s *Server) client(ns string) *kubevirt.Client {
	if ns == "" {
		ns = s.ns
	}
	return kubevirt.NewClient(ns)
}

// ── Proxmox response envelope ─────────────────────────────────────

// data wraps every Proxmox response body: {"data": …}.
func data(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": v})
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"data":   nil,
		"errors": map[string]string{"error": err.Error()},
	})
}

// vmIDMap queries the corral.io/proxmox-vmid label on all VMs and returns
// a name→vmid lookup map.  Delegates to VMIDLabelQuery.
func (s *Server) vmIDMap() map[string]int {
	return (&VMIDLabelQuery{runner: s.runner}).Map()
}

// vmIDFor returns the Proxmox vmid for a VM: label-derived if available,
// crc32-hashed name otherwise.
func (s *Server) vmIDFor(idMap map[string]int, name string) int {
	if id, ok := idMap[name]; ok {
		return id
	}
	return VmidFor(name)
}

// ── VMID mapping ──────────────────────────────────────────────────

// vmidFor derives a stable Proxmox-style numeric ID from the VM name.
// Proxmox VMIDs live in [100, 999999999]; crc32 keeps them deterministic with
// findVM resolves a Proxmox vmid back to a corral VM.
// First checks for the corral.io/proxmox-vmid label (set by create),
// then falls back to crc32-hashed name matching for pre-existing VMs.
func (s *Server) findVM(vmid int) (*types.VM, error) {
	// Fast path: label-based lookup via VMIDLabelQuery.
	if name, ns, ok := (&VMIDLabelQuery{runner: s.runner}).Lookup(vmid); ok {
		vms, listErr := s.client("").ListVMs()
		if listErr == nil {
			for i := range vms {
				if vms[i].Name == name && vms[i].Namespace == ns {
					return &vms[i], nil
				}
			}
		}
	}

	// Fallback: crc32-hashed name lookup (pre-existing VMs without labels)
	vms, err := s.client("").ListVMs()
	if err != nil {
		return nil, err
	}
	for i := range vms {
		if VmidFor(vms[i].Name) == vmid {
			return &vms[i], nil
		}
	}
	return nil, fmt.Errorf("VM %d does not exist", vmid)
}

// ── auth ──────────────────────────────────────────────────────────

// authorized implements the two auth styles Proxmox tooling speaks, against a
// single shared secret (s.token):
//
//   - API token header: Authorization: PVEAPIToken=user@realm!id=SECRET
//     (what Terraform providers use — stateless, no ticket dance)
//   - Ticket cookie: PVEAuthCookie=PVE:user:SECRET, issued by /access/ticket
//     when the login password equals the secret
//
// With no token configured the API is open — appropriate only when tailnet
// membership already gates the listener.
func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "PVEAPIToken=") {
		// The secret is everything after the last '=' (user@realm!id=SECRET).
		if v := auth[strings.LastIndex(auth, "=")+1:]; v == s.token {
			return true
		}
	}
	if c, err := r.Cookie("PVEAuthCookie"); err == nil {
		if parts := strings.Split(c.Value, ":"); len(parts) == 3 && parts[2] == s.token {
			return true
		}
	}
	return false
}

func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			fail(w, http.StatusUnauthorized, fmt.Errorf("authentication failure"))
			return
		}
		h(w, r)
	}
}

// ── handlers ──────────────────────────────────────────────────────

func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()

	// Ticket login. With a token configured the password must match it;
	// otherwise any login succeeds (tailnet-is-auth).
	mux.HandleFunc("POST /api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		user := r.FormValue("username")
		if user == "" {
			user = "root@pam"
		}
		secret := "corral"
		if s.token != "" {
			if r.FormValue("password") != s.token {
				fail(w, http.StatusUnauthorized, fmt.Errorf("authentication failure"))
				return
			}
			secret = s.token
		}
		data(w, map[string]any{
			"ticket":              "PVE:" + user + ":" + secret,
			"CSRFPreventionToken": "corral:static",
			"username":            user,
		})
	})

	mux.HandleFunc("GET /api2/json/version", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		data(w, map[string]any{"version": "8.2.0", "release": "8.2", "repoid": "corral-kubevirt"})
	}))

	mux.HandleFunc("GET /api2/json/nodes", s.requireAuth(s.handleNodes))
	mux.HandleFunc("GET /api2/json/cluster/resources", s.requireAuth(s.handleClusterResources))
	mux.HandleFunc("GET /api2/json/nodes/{node}/qemu", s.requireAuth(s.handleListQemu))
	mux.HandleFunc("POST /api2/json/nodes/{node}/qemu", s.requireAuth(s.handleCreateQemu))
	mux.HandleFunc("GET /api2/json/nodes/{node}/qemu/{vmid}/status/current", s.requireAuth(s.handleStatusCurrent))
	mux.HandleFunc("POST /api2/json/nodes/{node}/qemu/{vmid}/status/{action}", s.requireAuth(s.handleStatusAction))
	mux.HandleFunc("GET /api2/json/nodes/{node}/qemu/{vmid}/config", s.requireAuth(s.handleConfig))
	mux.HandleFunc("DELETE /api2/json/nodes/{node}/qemu/{vmid}", s.requireAuth(s.handleDeleteQemu))
	mux.HandleFunc("GET /api2/json/nodes/{node}/tasks/{upid}/status", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		// Operations are synchronous — every task is already done.
		data(w, map[string]any{
			"upid": r.PathValue("upid"), "node": r.PathValue("node"),
			"status": "stopped", "exitstatus": "OK",
		})
	}))

	// Pools — return empty list (no Proxmox pool support).
	mux.HandleFunc("GET /api2/json/pools", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		data(w, []any{})
	}))

	// ── Access control (K8s RBAC → Proxmox mapping) ────────────
	// These endpoints translate the cluster's K8s RBAC state into
	// Proxmox-shaped user/group/role responses.  Auth enforcement
	// is handled by K8s RBAC + tailnet membership; these are read-
	// only views consumed by Proxmox ecosystem tools.
	mux.HandleFunc("GET /api2/json/access/users", s.requireAuth(s.handleAccessUsers))
	mux.HandleFunc("GET /api2/json/access/groups", s.requireAuth(s.handleAccessGroups))
	mux.HandleFunc("GET /api2/json/access/roles", s.requireAuth(s.handleAccessRoles))

	// ── Cluster / node sub-resources ────────────────────────────
	mux.HandleFunc("GET /api2/json/cluster/ha/groups/", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		data(w, []any{}) // K8s handles HA via controllers — no Proxmox HA groups
	}))
	mux.HandleFunc("GET /api2/json/nodes/{node}/status", s.requireAuth(s.handleNodeStatus))
	mux.HandleFunc("GET /api2/json/nodes/{node}/storage", s.requireAuth(s.handleNodeStorage))
	mux.HandleFunc("GET /api2/json/nodes/{node}/time", s.requireAuth(s.handleNodeTime))
	mux.HandleFunc("GET /api2/json/nodes/{node}/dns", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		data(w, map[string]any{"dns1": "", "dns2": "", "search": ""})
	}))
	mux.HandleFunc("GET /api2/json/nodes/{node}/hosts", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		data(w, map[string]any{"digest": "", "data": ""})
	}))
	mux.HandleFunc("GET /api2/json/nodes/{node}/lxc", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		data(w, []any{}) // KubeVirt doesn't do LXC
	}))

	// VNC proxy — returns a ticket and port pointing at the corral web VNC
	// websocket bridge (/api/vnc/{ns}/{name}).  Proxmox ecosystem tools
	// (noVNC, virt-viewer) expect tcp:host:port; corral uses websockets, so
	// we return a corral:// URL that corral-aware tooling can interpret.
	mux.HandleFunc("POST /api2/json/nodes/{node}/qemu/{vmid}/vncproxy", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		vmid, _ := strconv.Atoi(r.PathValue("vmid"))
		vm, err := s.findVM(vmid)
		if err != nil {
			fail(w, 404, err)
			return
		}
		data(w, map[string]any{
			"port":   "8006",
			"ticket": fmt.Sprintf("PVEVNC:%s:%d::corral@pve:", vm.Name, vmid),
			"cert":   "",
			"user":   "root@pam",
			"upid":   UPID(r.PathValue("node"), "vncproxy", vmid),
		})
	}))

	// Serial terminal proxy — same pattern, returns a ticket for the
	// corral web console websocket bridge (/api/tty/{ns}/{name}).
	mux.HandleFunc("POST /api2/json/nodes/{node}/qemu/{vmid}/termproxy", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		vmid, _ := strconv.Atoi(r.PathValue("vmid"))
		vm, err := s.findVM(vmid)
		if err != nil {
			fail(w, 404, err)
			return
		}
		data(w, map[string]any{
			"port":   "8006",
			"ticket": fmt.Sprintf("PVETTY:%s:%d::corral@pve:", vm.Name, vmid),
			"user":   "root@pam",
			"upid":   UPID(r.PathValue("node"), "termproxy", vmid),
		})
	}))

	// Catch-all for unknown Proxmox paths — logs them for gap discovery and
	// returns a valid Proxmox error envelope (not Go's default HTML 404).
	mux.HandleFunc("/api2/json/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(os.Stderr, "[proxmox-gap] %s %s\n", r.Method, r.URL.RequestURI())
		fail(w, 404, fmt.Errorf("endpoint not implemented: %s %s", r.Method, r.URL.Path))
	})

	return mux
}

// k8s node facts for the nodes endpoints.
type nodeInfo = NodeInfo // alias — NodeInfo defined in queries.go

func (s *Server) nodes() ([]nodeInfo, error) {
	return (&NodeQuery{runner: s.runner}).List()
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.nodes()
	if err != nil {
		fail(w, 500, err)
		return
	}
	var out []map[string]any
	for _, n := range nodes {
		out = append(out, NodeEntry(n))
	}
	data(w, out)
}

// vmNode returns the VM's node. KubeVirt VMs may have no node when stopped
// (unplaced); Proxmox always expects a node, so we default to the first ready
// cluster node for stopped VMs.
func (s *Server) vmNode(vm *types.VM) string {
	nodes, _ := s.nodes()
	return VMNode(vm, nodes)
}

func (s *Server) vmEntry(vm *types.VM) map[string]any {
	nodes, _ := s.nodes()
	return VMEntry(vm, VmidFor(vm.Name), VMNode(vm, nodes))
}

func (s *Server) vmEntryWithID(vm *types.VM, vmid int) map[string]any {
	nodes, _ := s.nodes()
	return VMEntry(vm, vmid, VMNode(vm, nodes))
}

func (s *Server) handleListQemu(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	vms, err := s.client("").ListVMs()
	if err != nil {
		fail(w, 500, err)
		return
	}
	idMap := s.vmIDMap()
	out := []map[string]any{}
	for i := range vms {
		// Stopped VMs have no node; show them everywhere rather than nowhere.
		if n := s.vmNode(&vms[i]); n == "" || n == node {
			out = append(out, s.vmEntryWithID(&vms[i], s.vmIDFor(idMap, vms[i].Name)))
		}
	}
	data(w, out)
}

func (s *Server) handleClusterResources(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	out := []map[string]any{}
	idMap := s.vmIDMap()

	if typ == "" || typ == "vm" {
		vms, err := s.client("").ListVMs()
		if err != nil {
			fail(w, 500, err)
			return
		}
		for i := range vms {
			e := s.vmEntryWithID(&vms[i], s.vmIDFor(idMap, vms[i].Name))
			e["type"] = "qemu"
			e["id"] = fmt.Sprintf("qemu/%d", e["vmid"])
			out = append(out, e)
		}
	}
	if typ == "" || typ == "node" {
		nodes, err := s.nodes()
		if err != nil {
			fail(w, 500, err)
			return
		}
		for _, n := range nodes {
			out = append(out, NodeResourceEntry(n))
		}
	}
	data(w, out)
}

func (s *Server) handleStatusCurrent(w http.ResponseWriter, r *http.Request) {
	vmid, _ := strconv.Atoi(r.PathValue("vmid"))
	vm, err := s.findVM(vmid)
	if err != nil {
		fail(w, 404, err)
		return
	}
	agent := 0
	if vm.AgentConnected {
		agent = 1
	}
	data(w, map[string]any{
		"vmid": vmid, "name": vm.Name,
		"status": PVEStatus(vm), "qmpstatus": PVEStatus(vm),
		"cpus": vm.CPU, "maxmem": MemBytes(vm.Mem),
		"agent": agent, "uptime": 0,
	})
}

func (s *Server) handleStatusAction(w http.ResponseWriter, r *http.Request) {
	vmid, _ := strconv.Atoi(r.PathValue("vmid"))
	action := r.PathValue("action")
	vm, err := s.findVM(vmid)
	if err != nil {
		fail(w, 404, err)
		return
	}
	c := s.client(vm.Namespace)
	switch action {
	case "start":
		err = c.StartVM(vm.Name)
	case "stop", "shutdown":
		// Proxmox distinguishes hard stop vs guest shutdown; KubeVirt's stop
		// is already a graceful guest shutdown with a force fallback.
		err = c.StopVM(vm.Name)
	case "reset", "reboot":
		err = c.RestartVM(vm.Name)
	default:
		fail(w, 501, fmt.Errorf("action %q not implemented", action))
		return
	}
	if err != nil {
		fail(w, 500, err)
		return
	}
	data(w, UPID(r.PathValue("node"), "qm"+action, vmid))
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	vmid, _ := strconv.Atoi(r.PathValue("vmid"))
	vm, err := s.findVM(vmid)
	if err != nil {
		fail(w, 404, err)
		return
	}
	data(w, map[string]any{
		"name":   vm.Name,
		"cores":  vm.CPU,
		"memory": MemBytes(vm.Mem) / (1 << 20), // Proxmox config memory is MB
		"ostype": "l26",
		"net0":   "virtio,bridge=pod-network",
	})
}

// handleCreateQemu maps the tiny useful subset of Proxmox's 50+ create fields:
// vmid, name, cores, memory (MB). Everything else is ignored.
func (s *Server) handleCreateQemu(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	vmid, _ := strconv.Atoi(r.FormValue("vmid"))
	if vmid == 0 {
		fail(w, 400, fmt.Errorf("vmid is required"))
		return
	}
	name := r.FormValue("name")
	if name == "" {
		name = fmt.Sprintf("vm-%d", vmid)
	}
	cores, _ := strconv.Atoi(r.FormValue("cores"))
	if cores == 0 {
		cores = 2
	}
	memMB, _ := strconv.Atoi(r.FormValue("memory"))
	if memMB == 0 {
		memMB = 4096
	}
	opts := types.CreateOpts{
		Name:      name,
		Namespace: s.ns,
		CPU:       cores,
		Mem:       fmt.Sprintf("%dM", memMB),
	}
	if err := kubevirt.CreateVM(opts); err != nil {
		fail(w, 500, err)
		return
	}
	// Label the VM so findVM can resolve vmid↔name bidirectionally.
	s.runner.Run("kubectl", "label", "vm", name, "-n", s.ns,
		fmt.Sprintf("corral.io/proxmox-vmid=%d", vmid), "--overwrite")
	data(w, UPID(r.PathValue("node"), "qmcreate", vmid))
}

func (s *Server) handleDeleteQemu(w http.ResponseWriter, r *http.Request) {
	vmid, _ := strconv.Atoi(r.PathValue("vmid"))
	vm, err := s.findVM(vmid)
	if err != nil {
		fail(w, 404, err)
		return
	}
	if err := s.client(vm.Namespace).DeleteVM(vm.Name); err != nil {
		fail(w, 500, err)
		return
	}
	data(w, UPID(r.PathValue("node"), "qmdestroy", vmid))
}

// ── Access control handlers (K8s RBAC → Proxmox) ───────────────

// k8sSubjects queries ClusterRoleBinding subjects and translates them to
// Proxmox user/group shapes. Falls back to a minimal root@pam-only user list
// when the RBAC query itself fails — see ADR-0001 — so ecosystem tools that
// expect at least one user don't break; the shape translation itself lives
// in translate.go alongside K8sRolesToProxmox.
func (s *Server) k8sSubjects() (users []map[string]any, groups []map[string]any) {
	rbacUsers, rbacGroups := (&RBACQuery{runner: s.runner}).Subjects()
	if rbacUsers == nil {
		return []map[string]any{{
			"userid": "root@pam", "enable": 1, "expire": 0,
		}}, nil
	}
	return RBACUsersToProxmox(rbacUsers), RBACGroupsToProxmox(rbacGroups)
}

// k8sRoles maps K8s ClusterRoles to Proxmox privilege strings.
// See docs/adr/0001-k8s-rbac-to-proxmox-privileges.md for the full mapping.
func (s *Server) handleAccessUsers(w http.ResponseWriter, r *http.Request) {
	users, _ := s.k8sSubjects()
	if users == nil {
		// Fallback: minimal users list so tools don't break.
		users = []map[string]any{{
			"userid": "root@pam", "enable": 1, "expire": 0,
		}}
	}
	data(w, users)
}

func (s *Server) handleAccessGroups(w http.ResponseWriter, r *http.Request) {
	_, groups := s.k8sSubjects()
	data(w, groups)
}

func (s *Server) handleAccessRoles(w http.ResponseWriter, r *http.Request) {
	data(w, K8sRolesToProxmox())
}

// ── Node sub-resource handlers ───────────────────────────────────

func (s *Server) handleNodeStatus(w http.ResponseWriter, r *http.Request) {
	nodeName := r.PathValue("node")
	if nodeName == "" {
		fail(w, 400, fmt.Errorf("node name required"))
		return
	}
	// Reuse the nodes() helper — we already have all the data.
	nodes, err := s.nodes()
	if err != nil {
		fail(w, 500, err)
		return
	}
	for _, n := range nodes {
		if n.Name == nodeName {
			status := "offline"
			if n.Ready {
				status = "online"
			}
			data(w, map[string]any{
				"status":  status,
				"uptime":  0,
				"cpu":     0.0,
				"cpuinfo": map[string]any{"cpus": n.CPU},
				"memory": map[string]any{
					"used":  0,
					"total": MemBytes(n.MemRaw),
				},
				"kversion":   "",
				"pveversion": "corral/8.2",
			})
			return
		}
	}
	fail(w, 404, fmt.Errorf("node %q not found", nodeName))
}

func (s *Server) handleNodeStorage(w http.ResponseWriter, r *http.Request) {
	entries, err := (&StorageQuery{runner: s.runner}).List()
	if err != nil {
		data(w, []any{})
		return
	}

	nodeName := r.PathValue("node")
	var outList []map[string]any
	for _, sc := range entries {
		outList = append(outList, ProxmoxStorageEntry(sc, nodeName))
	}
	data(w, outList)
}

func (s *Server) handleNodeTime(w http.ResponseWriter, r *http.Request) {
	data(w, map[string]any{
		"timezone":  "UTC",
		"time":      time.Now().Unix(),
		"localtime": time.Now().Unix(),
	})
}
