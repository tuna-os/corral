package proxmox

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

const vmsJSON = `{
  "items": [
    {
      "metadata": {"name": "web", "namespace": "tailvm"},
      "spec": {"runStrategy": "Always",
        "template": {"spec": {"domain": {
          "cpu": {"sockets": 2, "cores": 1, "threads": 1},
          "memory": {"guest": "4Gi"}}}}},
      "status": {"printableStatus": "Running", "ready": true}
    },
    {
      "metadata": {"name": "db", "namespace": "tailvm"},
      "spec": {"runStrategy": "Halted",
        "template": {"spec": {"domain": {
          "cpu": {"sockets": 1, "cores": 1, "threads": 1},
          "memory": {"guest": "2Gi"}}}}},
      "status": {"printableStatus": "Stopped"}
    }
  ]
}`

const nodesJSON = `{
  "items": [{
    "metadata": {"name": "bihar"},
    "status": {
      "capacity": {"cpu": "8", "memory": "32Gi"},
      "conditions": [{"type": "Ready", "status": "True"}]
    }
  }]
}`

// newTestServer wires a fake runner into every seam and returns the test
// HTTP server plus the fake for response scripting.
func newTestServer(t *testing.T) (*httptest.Server, *shell.Fake) {
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

	s := NewServer("tailvm")
	s.runner = fake
	ts := httptest.NewServer(s.Mux())
	t.Cleanup(ts.Close)
	return ts, fake
}

func scriptCluster(fake *shell.Fake) {
	fake.AddResponseKV("kubectl", []string{"get", "vms", "-A", "-o", "json"}, vmsJSON, nil)
	fake.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, `{"items":[]}`, nil)
	fake.AddResponseKV("kubectl", []string{"get", "nodes", "-o", "json"}, nodesJSON, nil)
	fake.AddResponseKV("kubectl", []string{"get", "datavolumes", "-A", "-o", "json"}, `{"items":[]}`, nil)
	fake.AddPrefixResponse("kubectl get datavolume", "", nil)
	fake.AddPrefixResponse("kubectl get svc", "", nil)
}

// getData asserts 200 and unwraps the Proxmox {"data": …} envelope.
func getData(t *testing.T, url string) any {
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
	var envelope map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	d, ok := envelope["data"]
	if !ok {
		t.Fatalf("response lacks the Proxmox data envelope: %v", envelope)
	}
	return d
}

func TestAccessTicket(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, err := http.PostForm(ts.URL+"/api2/json/access/ticket",
		url.Values{"username": {"terraform@pve"}, "password": {"anything"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Data struct {
			Ticket string `json:"ticket"`
			CSRF   string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&envelope)
	if envelope.Data.Ticket == "" || envelope.Data.CSRF == "" {
		t.Errorf("ticket login must return ticket + CSRF token: %+v", envelope.Data)
	}
}

func TestVersion(t *testing.T) {
	ts, _ := newTestServer(t)
	d := getData(t, ts.URL+"/api2/json/version").(map[string]any)
	if d["version"] == "" {
		t.Errorf("version = %v", d)
	}
}

func TestPools_OnePoolPerNamespaceWithMembers(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)

	d := getData(t, ts.URL+"/api2/json/pools").([]any)
	if len(d) != 1 { // both fixture VMs are in namespace "tailvm"
		t.Fatalf("pools = %v", d)
	}
	pool := d[0].(map[string]any)
	if pool["poolid"] != "tailvm" {
		t.Errorf("poolid = %v, want tailvm", pool["poolid"])
	}
	members, ok := pool["members"].([]any)
	if !ok || len(members) != 2 {
		t.Errorf("members = %v, want 2 vmids", pool["members"])
	}
}

func TestPoolsFromNamespaces(t *testing.T) {
	vms := []types.VM{
		{Name: "web", Namespace: "tailvm"},
		{Name: "db", Namespace: "tailvm"},
		{Name: "orphan", Namespace: ""}, // no namespace — excluded, not its own pool
		{Name: "ci-runner", Namespace: "ci"},
	}
	vmidFor := func(name string) int {
		switch name {
		case "web":
			return 101
		case "db":
			return 102
		case "ci-runner":
			return 201
		}
		return 0
	}

	pools := PoolsFromNamespaces(vms, vmidFor)
	if len(pools) != 2 {
		t.Fatalf("pools = %v, want 2 (tailvm, ci)", pools)
	}
	// Sorted alphabetically: "ci" before "tailvm".
	if pools[0]["poolid"] != "ci" || pools[1]["poolid"] != "tailvm" {
		t.Errorf("pool order = %v", pools)
	}
	tailvmMembers := pools[1]["members"].([]int)
	if len(tailvmMembers) != 2 || tailvmMembers[0] != 101 || tailvmMembers[1] != 102 {
		t.Errorf("tailvm members = %v, want [101 102]", tailvmMembers)
	}
}

func TestNodes(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)

	d := getData(t, ts.URL+"/api2/json/nodes").([]any)
	if len(d) != 1 {
		t.Fatalf("nodes = %v", d)
	}
	n := d[0].(map[string]any)
	if n["node"] != "bihar" || n["status"] != "online" || n["maxcpu"].(float64) != 8 {
		t.Errorf("node entry = %v", n)
	}
}

func TestListQemu(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)

	d := getData(t, ts.URL+"/api2/json/nodes/bihar/qemu").([]any)
	if len(d) != 2 {
		t.Fatalf("qemu list = %v", d)
	}
	byName := map[string]map[string]any{}
	for _, e := range d {
		m := e.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if byName["web"]["status"] != "running" || byName["db"]["status"] != "stopped" {
		t.Errorf("statuses = %v", byName)
	}
	if byName["web"]["maxmem"].(float64) != float64(4<<30) {
		t.Errorf("web maxmem = %v, want 4GiB in bytes", byName["web"]["maxmem"])
	}
}

func TestClusterResources(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)

	d := getData(t, ts.URL+"/api2/json/cluster/resources").([]any)
	var qemu, nodes int
	for _, e := range d {
		switch e.(map[string]any)["type"] {
		case "qemu":
			qemu++
		case "node":
			nodes++
		}
	}
	if qemu != 2 || nodes != 1 {
		t.Errorf("cluster/resources: %d qemu + %d nodes, want 2 + 1", qemu, nodes)
	}
}

func TestStatusCurrent(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)

	vmid := VmidFor("web")
	d := getData(t, ts.URL+"/api2/json/nodes/bihar/qemu/"+strconv.Itoa(vmid)+"/status/current").(map[string]any)
	if d["status"] != "running" || d["name"] != "web" {
		t.Errorf("status/current = %v", d)
	}
}

func TestStatusCurrent_UnknownVMID(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)

	resp, err := http.Get(ts.URL + "/api2/json/nodes/bihar/qemu/123/status/current")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown vmid = %d, want 404", resp.StatusCode)
	}
}

func TestStartStop(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"start", "db", "-n", "tailvm"}, "", nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"stop", "web", "-n", "tailvm"}, "", nil)

	for _, tc := range []struct{ vm, action string }{{"db", "start"}, {"web", "stop"}} {
		vmid := strconv.Itoa(VmidFor(tc.vm))
		resp, err := http.Post(ts.URL+"/api2/json/nodes/bihar/qemu/"+vmid+"/status/"+tc.action, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s %s = %d: %s", tc.action, tc.vm, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "UPID:bihar") {
			t.Errorf("%s should return a UPID, got %s", tc.action, body)
		}
	}
}

func TestTaskStatus_AlwaysDone(t *testing.T) {
	ts, _ := newTestServer(t)

	d := getData(t, ts.URL+"/api2/json/nodes/bihar/tasks/UPID:bihar:0:0:0:qmstart:100:corral@pve:/status").(map[string]any)
	if d["status"] != "stopped" || d["exitstatus"] != "OK" {
		t.Errorf("task status = %v", d)
	}
}

func TestConfig(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)

	vmid := VmidFor("web")
	d := getData(t, ts.URL+"/api2/json/nodes/bihar/qemu/"+strconv.Itoa(vmid)+"/config").(map[string]any)
	if d["name"] != "web" || d["memory"].(float64) != 4096 {
		t.Errorf("config = %v (memory must be MB)", d)
	}
}

func TestCreateQemu(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	t.Setenv("HOME", t.TempDir())

	resp, err := http.PostForm(ts.URL+"/api2/json/nodes/bihar/qemu",
		url.Values{"vmid": {"100"}, "name": {"tfvm"}, "cores": {"2"}, "memory": {"2048"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("create = %d: %s", resp.StatusCode, body)
	}
	applies := 0
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
		}
	}
	if applies == 0 {
		t.Error("create should have applied manifests")
	}
}

func TestCreateQemu_MissingVMID(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, err := http.PostForm(ts.URL+"/api2/json/nodes/bihar/qemu", url.Values{"name": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("create without vmid = %d, want 400", resp.StatusCode)
	}
}

func TestDeleteQemu(t *testing.T) {
	ts, fake := newTestServer(t)
	scriptCluster(fake)
	fake.AddPrefixResponse("kubectl delete vm db -n tailvm", "deleted", nil)
	fake.AddPrefixResponse("kubectl delete pvc", "", nil)
	fake.AddPrefixResponse("kubectl delete svc", "", nil)
	fake.AddPrefixResponse("kubectl delete deploy", "", nil)
	fake.AddPrefixResponse("kubectl delete sa", "", nil)
	fake.AddPrefixResponse("kubectl delete role", "", nil)
	fake.AddPrefixResponse("kubectl delete rolebinding", "", nil)

	req, _ := http.NewRequest(http.MethodDelete,
		ts.URL+"/api2/json/nodes/bihar/qemu/"+strconv.Itoa(VmidFor("db")), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete = %d: %s", resp.StatusCode, body)
	}
}

func TestVmidFor_StableAndInRange(t *testing.T) {
	a, b := VmidFor("web"), VmidFor("web")
	if a != b {
		t.Error("vmid must be deterministic")
	}
	if a < 100 || a > 999999999 {
		t.Errorf("vmid %d out of Proxmox range", a)
	}
	if VmidFor("web") == VmidFor("db") {
		t.Error("different names should give different vmids")
	}
}

// MemBytes coverage lives in translate_test.go's TestMemBytes — a superset
// of what was here (whitespace, fractional GiB, no-unit default, invalid
// input, in addition to the same suffix cases).
