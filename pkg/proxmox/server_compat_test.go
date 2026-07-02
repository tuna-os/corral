package proxmox

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

// ── Proxmox API compatibility tests ───────────────────────────────
//
// These tests validate that every endpoint produces responses whose shape
// matches what real Proxmox ecosystem tools (Terraform provider, proxmoxer,
// qm CLI, etc.) expect.  Every test name starts with "Compat_" so they can
// be run selectively with `go test -run Compat`.

// ── helpers ───────────────────────────────────────────────────────

// getJSON fetches a URL, asserts 200, and returns the unmarshalled body.
func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s = %d: %s", url, resp.StatusCode, body)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	return body
}

// requireKeys asserts that every key in `want` exists in `m`.
func requireKeys(t *testing.T, path string, m map[string]any, want ...string) {
	t.Helper()
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("%s: missing required key %q (have: %v)", path, k, keysOf(m))
		}
	}
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── envelope tests ────────────────────────────────────────────────

func TestCompat_Envelope_Success(t *testing.T) {
	c := newCompatClient(t)
	body := getJSON(t, c.url("/api2/json/version"))
	if _, ok := body["data"]; !ok {
		t.Errorf("success response must have a 'data' key: %v", body)
	}
}

func TestCompat_Envelope_Error(t *testing.T) {
	c := newCompatClient(t)
	resp, err := http.Get(c.url("/api2/json/nodes/bihar/qemu/99999/status/current"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("error Content-Type = %q", resp.Header.Get("Content-Type"))
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["data"] != nil {
		t.Errorf("error response data must be null, got %v", body["data"])
	}
	errs, ok := body["errors"]
	if !ok {
		t.Errorf("error response must have 'errors' key: %v", body)
	}
	if em, ok := errs.(map[string]any); !ok || em["error"] == nil {
		t.Errorf("errors must have an 'error' key: %v", errs)
	}
}

// ── access/ticket ─────────────────────────────────────────────────

func TestCompat_AccessTicket_Shape(t *testing.T) {
	c := newCompatClient(t)

	resp, err := http.PostForm(c.url("/api2/json/access/ticket"),
		url.Values{"username": {"terraform@pve"}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	d, _ := body["data"].(map[string]any)
	if d == nil {
		t.Fatal("access/ticket data is missing")
	}
	requireKeys(t, "access/ticket", d,
		"ticket", "CSRFPreventionToken", "username")

	// The ticket must have the PVE: prefix that real Proxmox uses.
	ticket, _ := d["ticket"].(string)
	if !strings.HasPrefix(ticket, "PVE:") {
		t.Errorf("ticket must start with 'PVE:': got %q", ticket)
	}
	parts := strings.Split(ticket, ":")
	if len(parts) != 3 {
		t.Errorf("ticket must be PVE:user:secret: got %q", ticket)
	}

	if d["CSRFPreventionToken"].(string) == "" {
		t.Error("CSRFPreventionToken must not be empty")
	}
}

func TestCompat_AccessTicket_AuthWithToken(t *testing.T) {
	// Server with a shared token — login requires it, and the returned
	// ticket + cookie can then be used for subsequent requests.
	s := NewServer("tailvm")
	s.token = "test-secret-123"
	ts := newCompatServer(t, s)

	// Bad password must be rejected.
	resp, _ := http.PostForm(ts.URL+"/api2/json/access/ticket",
		url.Values{"username": {"user@pve"}, "password": {"wrong"}})
	if resp.StatusCode != 401 {
		t.Errorf("wrong password = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Correct password must succeed.
	resp, err := http.PostForm(ts.URL+"/api2/json/access/ticket",
		url.Values{"username": {"user@pve"}, "password": {"test-secret-123"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	ticket, _ := body["data"].(map[string]any)["ticket"].(string)
	if !strings.Contains(ticket, "test-secret-123") {
		t.Errorf("ticket should contain the secret: %q", ticket)
	}
}

// ── version ───────────────────────────────────────────────────────

func TestCompat_Version_Shape(t *testing.T) {
	c := newCompatClient(t)
	body := getJSON(t, c.url("/api2/json/version"))
	d, _ := body["data"].(map[string]any)
	requireKeys(t, "version", d, "version", "release", "repoid")
}

// ── nodes ─────────────────────────────────────────────────────────

func TestCompat_Nodes_Shape(t *testing.T) {
	c := newCompatClient(t)

	body := getJSON(t, c.url("/api2/json/nodes"))
	arr, _ := body["data"].([]any)
	if len(arr) == 0 {
		t.Fatal("nodes list is empty")
	}
	n := arr[0].(map[string]any)
	requireKeys(t, "node[0]", n,
		"node", "id", "type", "status", "maxcpu", "maxmem")
	if n["type"] != "node" {
		t.Errorf("node type = %q, want 'node'", n["type"])
	}
	if !strings.HasPrefix(n["id"].(string), "node/") {
		t.Errorf("node id must be 'node/<name>': got %q", n["id"])
	}
	status := n["status"].(string)
	if status != "online" && status != "offline" {
		t.Errorf("node status = %q, want 'online' or 'offline'", status)
	}
}

// ── nodes/{node}/qemu (VM list) ───────────────────────────────────

func TestCompat_ListQemu_Shape(t *testing.T) {
	c := newCompatClient(t)

	body := getJSON(t, c.url("/api2/json/nodes/bihar/qemu"))
	arr, _ := body["data"].([]any)
	if len(arr) == 0 {
		t.Fatal("qemu list is empty")
	}
	vm := arr[0].(map[string]any)
	// Terraform provider reads: vmid, name, status, node, template, maxmem, cpus, uptime.
	requireKeys(t, "qemu[0]", vm,
		"vmid", "name", "status", "node", "cpus", "maxmem", "uptime")

	// vmid must be in Proxmox range [100, 999999999].
	vmid := int(vm["vmid"].(float64))
	if vmid < 100 || vmid > 999999999 {
		t.Errorf("vmid %d out of Proxmox range", vmid)
	}
	// Status must use Proxmox vocabulary.
	switch vm["status"].(string) {
	case "running", "stopped":
	default:
		t.Errorf("status = %q, want 'running' or 'stopped'", vm["status"])
	}
}

// ── status/current ────────────────────────────────────────────────

func TestCompat_StatusCurrent_Shape(t *testing.T) {
	c := newCompatClient(t)
	vmid := VmidFor("web")

	body := getJSON(t, c.url(fmt.Sprintf("/api2/json/nodes/bihar/qemu/%d/status/current", vmid)))
	d, _ := body["data"].(map[string]any)
	// Terraform reads: vmid, status, qmpstatus, cpus, maxmem, agent, uptime.
	requireKeys(t, "status/current", d,
		"vmid", "name", "status", "qmpstatus", "cpus", "maxmem", "agent", "uptime")

	// status and qmpstatus must use the same Proxmox vocabulary.
	if d["status"] != d["qmpstatus"] {
		t.Errorf("status=%q != qmpstatus=%q (should match for KubeVirt VMs)",
			d["status"], d["qmpstatus"])
	}

	// agent is 0 or 1 (Proxmox integer boolean).
	agent := int(d["agent"].(float64))
	if agent != 0 && agent != 1 {
		t.Errorf("agent = %d, want 0 or 1", agent)
	}
}

// ── config ────────────────────────────────────────────────────────

func TestCompat_Config_Shape(t *testing.T) {
	c := newCompatClient(t)
	vmid := VmidFor("web")

	body := getJSON(t, c.url(fmt.Sprintf("/api2/json/nodes/bihar/qemu/%d/config", vmid)))
	d, _ := body["data"].(map[string]any)
	// Terraform reads: name, cores, memory, ostype.
	requireKeys(t, "config", d, "name", "cores", "memory", "ostype")

	// memory must be in MB (Proxmox convention, not GiB/bytes).
	mem := d["memory"].(float64)
	if mem != 4096 {
		t.Errorf("memory = %v, want 4096 (MB for 4GiB VM)", mem)
	}
}

// ── cluster/resources ─────────────────────────────────────────────

func TestCompat_ClusterResources_Shape(t *testing.T) {
	c := newCompatClient(t)

	// Full list (both types).
	body := getJSON(t, c.url("/api2/json/cluster/resources"))
	arr, _ := body["data"].([]any)
	if len(arr) == 0 {
		t.Fatal("cluster/resources is empty")
	}
	foundTypes := map[string]bool{}
	for _, e := range arr {
		m := e.(map[string]any)
		requireKeys(t, "resources[]", m, "id", "type")
		foundTypes[m["type"].(string)] = true
		switch m["type"] {
		case "qemu":
			requireKeys(t, "qemu resource", m, "vmid", "name", "status")
		case "node":
			requireKeys(t, "node resource", m, "node", "status")
		}
	}
	if !foundTypes["qemu"] || !foundTypes["node"] {
		t.Errorf("cluster/resources must contain both qemu and node: %v", foundTypes)
	}

	// Filtered: ?type=vm
	body2 := getJSON(t, c.url("/api2/json/cluster/resources?type=vm"))
	for _, e := range body2["data"].([]any) {
		if e.(map[string]any)["type"] != "qemu" {
			t.Errorf("?type=vm should only return qemu types: %v", e)
		}
	}

	// Filtered: ?type=node
	body3 := getJSON(t, c.url("/api2/json/cluster/resources?type=node"))
	for _, e := range body3["data"].([]any) {
		if e.(map[string]any)["type"] != "node" {
			t.Errorf("?type=node should only return node types: %v", e)
		}
	}
}

// ── status actions (start/stop/shutdown/reset) ────────────────────

func TestCompat_StatusActions_ReturnUPID(t *testing.T) {
	c := newCompatClient(t)

	for _, tc := range []struct{ vm, action string }{
		{"db", "start"}, {"web", "stop"}, {"web", "shutdown"}, {"db", "reset"},
	} {
		t.Run(tc.action+"/"+tc.vm, func(t *testing.T) {
			vmid := strconv.Itoa(VmidFor(tc.vm))
			resp, err := http.Post(
				c.url(fmt.Sprintf("/api2/json/nodes/bihar/qemu/%s/status/%s", vmid, tc.action)),
				"application/x-www-form-urlencoded", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == 501 {
				t.Skipf("action %q not implemented", tc.action)
			}
			if resp.StatusCode != 200 {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("%s %s = %d: %s", tc.action, tc.vm, resp.StatusCode, body)
			}
			var body map[string]any
			json.NewDecoder(resp.Body).Decode(&body)
			upid, _ := body["data"].(string)
			if !strings.HasPrefix(upid, "UPID:") {
				t.Errorf("action response must be a UPID string: %q", upid)
			}
		})
	}
}

func TestCompat_StatusAction_UnknownAction(t *testing.T) {
	c := newCompatClient(t)
	vmid := strconv.Itoa(VmidFor("web"))

	resp, err := http.Post(
		c.url(fmt.Sprintf("/api2/json/nodes/bihar/qemu/%s/status/hibernate", vmid)),
		"", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 501 {
		t.Errorf("unknown action = %d, want 501", resp.StatusCode)
	}
}

// ── tasks/{upid}/status ───────────────────────────────────────────

func TestCompat_TaskStatus_Shape(t *testing.T) {
	c := newCompatClient(t)

	body := getJSON(t, c.url("/api2/json/nodes/bihar/tasks/UPID:bihar:0:0:0:qmstart:100:corral@pve:/status"))
	d, _ := body["data"].(map[string]any)
	requireKeys(t, "task status", d, "upid", "node", "status", "exitstatus")
	if d["status"] != "stopped" {
		t.Errorf("task status = %q, want 'stopped'", d["status"])
	}
	if d["exitstatus"] != "OK" {
		t.Errorf("task exitstatus = %q, want 'OK'", d["exitstatus"])
	}
}

// ── create ────────────────────────────────────────────────────────

func TestCompat_Create_ReturnsUPID(t *testing.T) {
	c := newCompatClient(t)

	resp, err := http.PostForm(c.url("/api2/json/nodes/bihar/qemu"),
		url.Values{"vmid": {"200"}, "name": {"tfvm"}, "cores": {"4"}, "memory": {"8192"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("create = %d: %s", resp.StatusCode, body)
	}
	var env map[string]any
	json.Unmarshal(body, &env)
	upid, _ := env["data"].(string)
	if !strings.HasPrefix(upid, "UPID:") {
		t.Errorf("create must return a UPID: %q", upid)
	}
}

func TestCompat_Create_DefaultValues(t *testing.T) {
	c := newCompatClient(t)

	// Only vmid — name, cores, memory defaulted.
	resp, err := http.PostForm(c.url("/api2/json/nodes/bihar/qemu"),
		url.Values{"vmid": {"300"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create defaults = %d: %s", resp.StatusCode, body)
	}
}

func TestCompat_Create_MissingVMID(t *testing.T) {
	c := newCompatClient(t)

	resp, err := http.PostForm(c.url("/api2/json/nodes/bihar/qemu"), url.Values{"name": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("create without vmid = %d, want 400", resp.StatusCode)
	}
}

// ── delete ────────────────────────────────────────────────────────

func TestCompat_Delete_ReturnsUPID(t *testing.T) {
	c := newCompatClient(t)
	vmid := strconv.Itoa(VmidFor("db"))

	req, _ := http.NewRequest(http.MethodDelete,
		c.url(fmt.Sprintf("/api2/json/nodes/bihar/qemu/%s", vmid)), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete = %d: %s", resp.StatusCode, body)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	upid, _ := body["data"].(string)
	if !strings.HasPrefix(upid, "UPID:") {
		t.Errorf("delete must return a UPID: %q", upid)
	}
}

// ── UPID format ───────────────────────────────────────────────────

func TestCompat_UPID_Format(t *testing.T) {
	c := newCompatClient(t)

	// Do a start to get a UPID, then check its format.
	vmid := strconv.Itoa(VmidFor("db"))
	resp, err := http.Post(
		c.url(fmt.Sprintf("/api2/json/nodes/bihar/qemu/%s/status/start", vmid)),
		"", nil)
	if err != nil {
		t.Fatalf("POST start: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	upid, _ := body["data"].(string)

	// Real Proxmox UPID: UPID:node:pid:pstart:cstart:type:id:user@realm:
	// We're lenient — at least UPID prefix, colon-separated, with node.
	if !strings.HasPrefix(upid, "UPID:") {
		t.Fatalf("not a UPID: %q", upid)
	}
	parts := strings.Split(upid, ":")
	if len(parts) < 8 {
		t.Errorf("UPID should have 8+ colon-separated fields: %q (%d parts)", upid, len(parts))
	}
	// Field 1 = node, field 5 = type, field 6 = vmid.
	if parts[1] != "bihar" {
		t.Errorf("UPID node = %q, want 'bihar'", parts[1])
	}
	if !strings.HasPrefix(parts[5], "qm") {
		t.Errorf("UPID type must start with 'qm': %q", parts[5])
	}
}

// ── auth: API token header ────────────────────────────────────────

func TestCompat_Auth_APITokenHeader(t *testing.T) {
	// Server with a token — unauthenticated requests must fail.
	s := NewServer("tailvm")
	s.token = "my-api-secret"
	ts := newCompatServer(t, s)

	// Without auth header — must fail.
	resp, _ := http.Get(ts.URL + "/api2/json/version")
	if resp.StatusCode != 401 {
		t.Errorf("unauthenticated = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// With correct API token header — must succeed.
	req, _ := http.NewRequest("GET", ts.URL+"/api2/json/version", nil)
	req.Header.Set("Authorization", "PVEAPIToken=terraform@pve!token=my-api-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("API token auth = %d: %s", resp.StatusCode, body)
	}
}

func TestCompat_Auth_TicketCookie(t *testing.T) {
	// Server with a token.
	s := NewServer("tailvm")
	s.token = "cookie-secret"
	ts := newCompatServer(t, s)

	// Login to get the cookie, then use it.
	resp, err := http.PostForm(ts.URL+"/api2/json/access/ticket",
		url.Values{"username": {"user@pve"}, "password": {"cookie-secret"}})
	if err != nil {
		t.Fatalf("POST ticket: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	ticket, _ := body["data"].(map[string]any)["ticket"].(string)

	req, _ := http.NewRequest("GET", ts.URL+"/api2/json/version", nil)
	req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: ticket})
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		body, _ := io.ReadAll(resp2.Body)
		t.Errorf("cookie auth = %d: %s", resp2.StatusCode, body)
	}
}

// ── 404 handling ─────────────────────────────────────────────────

func TestCompat_404_IsProxmoxError(t *testing.T) {
	c := newCompatClient(t)

	// Unknown VMID.
	resp, err := http.Get(c.url("/api2/json/nodes/bihar/qemu/99999/status/current"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown vmid = %d, want 404", resp.StatusCode)
	}
	// Should still be JSON with the Proxmox error envelope.
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("404 must be JSON")
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["data"] != nil {
		t.Errorf("404 data must be null")
	}
}

// ── VMID stability ────────────────────────────────────────────────

func TestCompat_VMID_DeterministicAcrossCalls(t *testing.T) {
	// vmidFor should produce the same result every time.
	names := []string{"web", "db", "long-vm-name-with-dashes", "a", "zzz"}
	first := map[string]int{}
	for _, n := range names {
		first[n] = VmidFor(n)
	}
	for i := 0; i < 100; i++ {
		for _, n := range names {
			if VmidFor(n) != first[n] {
				t.Errorf("VmidFor(%q) changed from %d to %d", n, first[n], VmidFor(n))
			}
		}
	}
}

// ── fixtures (shared with server_test.go via newTestServer/scriptCluster) ──

// compatClient bundles a test server with a pre-scripted cluster.
type compatClient struct {
	t  *testing.T
	ts *httptest.Server
}

func newCompatClient(t *testing.T) *compatClient {
	t.Helper()
	ts, fake := newTestServer(t)
	scriptCluster(fake)
	// Add responses for the compat tests that need virtctl calls beyond
	// the basic ones already covered by scriptCluster.
	fake.AddResponseKV("/fake/bin/virtctl", []string{"start", "db", "-n", "tailvm"}, "", nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"stop", "web", "-n", "tailvm"}, "", nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"restart", "db", "-n", "tailvm"}, "", nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"shutdown", "web", "-n", "tailvm"}, "", nil)
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	fake.AddPrefixResponse("kubectl delete vm db -n tailvm", "deleted", nil)
	fake.AddPrefixResponse("kubectl delete pvc", "", nil)
	fake.AddPrefixResponse("kubectl delete svc", "", nil)
	fake.AddPrefixResponse("kubectl delete deploy", "", nil)
	fake.AddPrefixResponse("kubectl delete sa", "", nil)
	fake.AddPrefixResponse("kubectl delete role", "", nil)
	fake.AddPrefixResponse("kubectl delete rolebinding", "", nil)
	t.Cleanup(ts.Close)
	t.Setenv("HOME", t.TempDir())
	return &compatClient{t: t, ts: ts}
}

func (c *compatClient) url(path string) string {
	return c.ts.URL + path
}

func newCompatServer(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	fake := shell.NewFake()
	fake.AddResponseKV("virtctl", nil, "", nil)
	kubevirt.SetDefaultRunner(fake)
	kubevirt.SetPackageRunner(fake)
	kubevirt.SetApplyRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetDefaultRunner(nil)
		kubevirt.SetPackageRunner(shell.Real{})
		kubevirt.SetApplyRunner(shell.Real{})
	})
	s.runner = fake
	scriptCluster(fake)
	fake.AddPrefixResponse("kubectl delete vm db -n tailvm", "deleted", nil)
	fake.AddPrefixResponse("kubectl delete pvc", "", nil)
	fake.AddPrefixResponse("kubectl delete svc", "", nil)
	fake.AddPrefixResponse("kubectl delete deploy", "", nil)
	fake.AddPrefixResponse("kubectl delete sa", "", nil)
	fake.AddPrefixResponse("kubectl delete role", "", nil)
	fake.AddPrefixResponse("kubectl delete rolebinding", "", nil)
	t.Setenv("HOME", t.TempDir())
	ts := httptest.NewServer(s.Mux())
	t.Cleanup(ts.Close)
	return ts
}
