package nutanix

// Tests against a fake Prism Central.
//
// There is no shell.Runner seam here because there is no command to fake —
// the seam is HTTP, so these run an httptest server that answers with
// recorded v4 payload shapes (list envelope with data+metadata, 202 task
// envelope, prism task status) and records what was asked of it. The
// assertions are about the request Corral makes (path, method, auth,
// idempotency header) and what it does with the answer.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// listFixture is a recorded v4 GET /ahv/config/vms shape: three AHV VMs —
// running, stopped, paused — with the config fields the inventory maps.
const listFixture = `{
  "data": [
    {
      "extId": "11111111-1111-1111-1111-111111111111",
      "name": "web-prod",
      "description": "frontend",
      "powerState": "ON",
      "numSockets": 2,
      "numCoresPerSocket": 2,
      "memorySizeBytes": 8589934592,
      "nics": [{"networkInfo": {"ipv4Config": {"ipAddress": {"value": "10.8.23.6"}}}}],
      "cluster": {"extId": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
      "host": {"extId": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
    },
    {
      "extId": "22222222-2222-2222-2222-222222222222",
      "name": "db-prod",
      "powerState": "OFF",
      "numSockets": 4,
      "numCoresPerSocket": 2,
      "memorySizeBytes": 17179869184,
      "cluster": {"extId": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
    },
    {
      "extId": "33333333-3333-3333-3333-333333333333",
      "name": "paused-box",
      "powerState": "PAUSED",
      "numSockets": 1,
      "numCoresPerSocket": 1,
      "memorySizeBytes": 2147483648,
      "cluster": {"extId": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
    }
  ],
  "metadata": {"totalAvailableResults": 3}
}`

// fakePC is a scriptable Prism Central. The vms handler answers the list
// shape honouring $filter=name eq '…'; the actions handler mints one task
// per verb and answers 202 envelopes; the task handler answers the scripted
// status sequence for that task (taskOutcomes[verb], default SUCCEEDED); the
// recovery-points handler answers the snapshot fixture. Raw bodies are
// recorded for exact-request assertions.
type fakePC struct {
	t            *testing.T
	server       *httptest.Server
	mu           sync.Mutex
	requests     []recordedRequest
	taskOutcomes map[string][]string
	tasks        map[string][]string
	taskHits     map[string]int
	taskCount    int
}

type recordedRequest struct {
	Method    string
	Path      string
	Query     url.Values
	AuthUser  string
	RequestID string
	Body      string
}

func newFakePC(t *testing.T) *fakePC {
	t.Helper()
	f := &fakePC{tasks: map[string][]string{}, taskHits: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePC) serve(w http.ResponseWriter, r *http.Request) {
	user, _, _ := r.BasicAuth()
	var rawBody []byte
	if r.Body != nil {
		rawBody, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		Method:    r.Method,
		Path:      r.URL.Path,
		Query:     r.URL.Query(),
		AuthUser:  user,
		RequestID: r.Header.Get("Ntnx-Request-Id"),
		Body:      string(rawBody),
	})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/ahv/config/vms"):
		f.serveList(w, r)
	case r.Method == http.MethodGet && strings.Contains(path, "/ahv/config/vms/"):
		f.serveGet(w, path)
	case r.Method == http.MethodPost && strings.Contains(path, "/$actions/"):
		f.serveAction(w, path)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/recovery-points"):
		f.serveRecoveryPoints(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/recovery-points"):
		f.serveAction(w, path+"/create")
	case r.Method == http.MethodDelete && strings.Contains(path, "/recovery-points/"):
		f.serveAction(w, path+"/delete")
	case r.Method == http.MethodGet && strings.Contains(path, "/config/tasks/"):
		f.serveTask(w, path)
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "no fake handler for " + r.Method + " " + path})
	}
}

// serveAction mints one task per verb invocation and answers the 202
// envelope. Tests pre-seed f.taskOutcomes[verb] to script that task's
// status sequence (e.g. shutdown → FAILED for the fallback test).
func (f *fakePC) serveAction(w http.ResponseWriter, path string) {
	verb := path
	if i := strings.LastIndex(path, "/$actions/"); i >= 0 {
		verb = path[i+len("/$actions/"):]
	} else if i := strings.LastIndex(path, "/"); i >= 0 {
		verb = path[i+1:]
	}
	f.mu.Lock()
	f.taskCount++
	taskID := fmt.Sprintf("task-%s-%d", verb, f.taskCount)
	seq := f.taskOutcomes[verb]
	if len(seq) == 0 {
		seq = []string{"SUCCEEDED"}
	}
	f.tasks[taskID] = seq
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{"extId": taskID},
		"metadata": map[string]any{
			"flags": []any{map[string]any{"name": "hasError", "value": false}},
		},
	})
}

func (f *fakePC) serveTask(w http.ResponseWriter, path string) {
	taskID := path[strings.LastIndex(path, "/")+1:]
	f.mu.Lock()
	seq := f.tasks[taskID]
	if len(seq) == 0 {
		seq = []string{"SUCCEEDED"}
	}
	i := f.taskHits[taskID]
	if i >= len(seq) {
		i = len(seq) - 1
	}
	f.taskHits[taskID]++
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{"extId": taskID, "status": seq[i]},
	})
}

func (f *fakePC) serveList(w http.ResponseWriter, r *http.Request) {
	var resp listResponse
	if err := json.Unmarshal([]byte(listFixture), &resp); err != nil {
		f.t.Fatalf("bad fixture: %v", err)
	}
	if filter := r.URL.Query().Get("$filter"); filter != "" {
		name, ok := parseNameFilter(filter)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "bad filter"})
			return
		}
		var kept []VM
		for _, vm := range resp.Data {
			if vm.Name == name {
				kept = append(kept, vm)
			}
		}
		resp.Data = kept
		resp.Meta.TotalAvailableResults = len(kept)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakePC) serveGet(w http.ResponseWriter, path string) {
	var resp listResponse
	_ = json.Unmarshal([]byte(listFixture), &resp)
	extID := path[strings.LastIndex(path, "/")+1:]
	for _, vm := range resp.Data {
		if vm.ExtID == extID {
			_ = json.NewEncoder(w).Encode(getResponse{Data: vm})
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{"message": "vm not found"})
}

// recoveryPointsFixture is a recorded v4 dataprotection list shape: two
// crash-consistent points on web-prod, one on db-prod. The consistency
// value is the fixture's — the client overwrites it with CrashConsistent
// regardless, because the server must not be trusted on this field.
const recoveryPointsFixture = `{
  "data": [
    {
      "extId": "rp-uuid-1",
      "name": "snap1",
      "creationTime": "2026-10-01T00:00:00Z",
      "consistency": "CRASH",
      "vmExtId": "11111111-1111-1111-1111-111111111111"
    },
    {
      "extId": "rp-uuid-2",
      "name": "snap1",
      "creationTime": "2026-10-02T00:00:00Z",
      "consistency": "CRASH",
      "vmExtId": "22222222-2222-2222-2222-222222222222"
    },
    {
      "extId": "rp-uuid-3",
      "name": "nightly",
      "creationTime": "2026-10-03T00:00:00Z",
      "consistency": "CRASH",
      "vmExtId": "11111111-1111-1111-1111-111111111111"
    }
  ],
  "metadata": {"totalAvailableResults": 3}
}`

func (f *fakePC) serveRecoveryPoints(w http.ResponseWriter, r *http.Request) {
	var resp recoveryPointsResponse
	if err := json.Unmarshal([]byte(recoveryPointsFixture), &resp); err != nil {
		f.t.Fatalf("bad snapshot fixture: %v", err)
	}
	if filter := r.URL.Query().Get("$filter"); filter != "" {
		const prefix = "vmExtId eq '"
		if !strings.HasPrefix(filter, prefix) || !strings.HasSuffix(filter, "'") {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "bad filter"})
			return
		}
		want := filter[len(prefix) : len(filter)-1]
		var kept []SnapshotInfo
		for _, p := range resp.Data {
			if p.VMExtID == want {
				kept = append(kept, p)
			}
		}
		resp.Data = kept
		resp.Meta.TotalAvailableResults = len(kept)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// parseNameFilter accepts exactly the filter Resolve sends.
func parseNameFilter(filter string) (string, bool) {
	const prefix = "name eq '"
	if !strings.HasPrefix(filter, prefix) || !strings.HasSuffix(filter, "'") {
		return "", false
	}
	inner := filter[len(prefix) : len(filter)-1]
	name := strings.ReplaceAll(inner, "''", "'")
	return name, true
}

func (f *fakePC) lastRequest() recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func testClient(t *testing.T, f *fakePC) *Client {
	t.Helper()
	c, err := New(Config{Host: "pc.example.com", Username: "admin", Secret: "s3cret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// In-package seam: keep the validated config but aim the base URL at
	// the fake. Production never does this — it has a real PC.
	c.base = f.server.URL + "/api"
	return c.WithHTTPClient(f.server.Client())
}

func TestNewValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"empty host", Config{Username: "u", Secret: "s"}},
		{"empty username", Config{Host: "pc", Secret: "s"}},
		{"empty secret", Config{Host: "pc", Username: "u"}},
		{"bad fingerprint", Config{Host: "pc", Username: "u", Secret: "s", Fingerprint: "zz"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil {
				t.Fatal("expected an error")
			}
		})
	}

	c, err := New(Config{Host: "pc.example.com", Username: "u", Secret: "s"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.base; got != "https://pc.example.com:9440/api" {
		t.Fatalf("base = %q, want default 9440/api", got)
	}
	if c.APIVersion() != DefaultAPIVersion {
		t.Fatalf("version = %q, want default %q", c.APIVersion(), DefaultAPIVersion)
	}

	if _, err := New(Config{Host: "pc", Username: "u", Secret: "s", APIVersion: "v4.0.b1"}); err != nil {
		t.Fatalf("explicit version rejected: %v", err)
	}
}

func TestList(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)

	vms, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 3 {
		t.Fatalf("got %d VMs, want 3", len(vms))
	}
	byName := map[string]int{}
	for _, vm := range vms {
		byName[vm.Name]++
		if vm.Backend != "nutanix" {
			t.Errorf("%s: backend = %q", vm.Name, vm.Backend)
		}
		if vm.ID == "" {
			t.Errorf("%s: empty identity", vm.Name)
		}
	}
	web := vms[0]
	if !web.Running || !web.Ready || web.Status != "Running" {
		t.Errorf("web-prod: running=%v ready=%v status=%q", web.Running, web.Ready, web.Status)
	}
	if web.CPU != 4 {
		t.Errorf("web-prod: CPU = %d, want 4 (2 sockets × 2 cores)", web.CPU)
	}
	if web.Mem != "8.0 GiB" {
		t.Errorf("web-prod: Mem = %q, want 8.0 GiB", web.Mem)
	}
	if web.IP != "10.8.23.6" {
		t.Errorf("web-prod: IP = %q", web.IP)
	}
	db := vms[1]
	if db.Running || db.Ready || db.Status != "Stopped" {
		t.Errorf("db-prod: running=%v ready=%v status=%q", db.Running, db.Ready, db.Status)
	}
	paused := vms[2]
	if paused.Running || paused.Status != "Paused" {
		t.Errorf("paused-box: running=%v status=%q", paused.Running, paused.Status)
	}

	req := f.lastRequest()
	if req.AuthUser != "admin" {
		t.Errorf("auth user = %q, want basic auth as admin", req.AuthUser)
	}
	if !strings.HasPrefix(req.Path, "/api/vmm/v4.0/ahv/config/vms") {
		t.Errorf("path = %q, want versioned vmm list path", req.Path)
	}
}

func TestResolve(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)

	vm, err := c.Resolve("db-prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if vm.ExtID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("extId = %q", vm.ExtID)
	}
	req := f.lastRequest()
	if filter := req.Query.Get("$filter"); !strings.Contains(filter, "db-prod") {
		t.Errorf("$filter = %q, want server-side name match", filter)
	}

	if _, err := c.Resolve("nope"); err == nil {
		t.Error("expected an error for an unknown name")
	}
	if c.Exists("web-prod") != true || c.Exists("nope") != false {
		t.Error("Exists mismatch")
	}
}

// findRequest returns the most recent request whose path contains substr.
func findRequest(f *fakePC, substr string) (recordedRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if strings.Contains(f.requests[i].Path, substr) {
			return f.requests[i], true
		}
	}
	return recordedRequest{}, false
}

func TestPowerOps(t *testing.T) {
	ops := []struct {
		name string
		verb string
		call func(c *Client, name string) (Task, error)
	}{
		{"Start", "power-on", (*Client).Start},
		{"Stop", "shutdown", (*Client).Stop},
		{"Kill", "power-off", (*Client).Kill},
		{"Restart", "reboot", (*Client).Restart},
		{"Reset", "power-cycle", (*Client).Reset},
		{"Pause", "suspend", (*Client).Pause},
		{"Resume", "resume", (*Client).Resume},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			f := newFakePC(t)
			c := testClient(t, f)
			task, err := op.call(c, "web-prod")
			if err != nil {
				t.Fatalf("%s: %v", op.name, err)
			}
			// Stop/Restart wait on the graceful task and return it on
			// success, so the action POST is found by verb, not recency.
			if !task.Valid() || !strings.HasPrefix(task.ExtID, "task-"+op.verb+"-") {
				t.Fatalf("%s: task = %+v", op.name, task)
			}
			req, ok := findRequest(f, "/$actions/"+op.verb)
			if !ok {
				t.Fatalf("%s: no $actions/%s request recorded", op.name, op.verb)
			}
			if req.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", req.Method)
			}
			want := "/api/vmm/v4.0/ahv/config/vms/11111111-1111-1111-1111-111111111111/$actions/" + op.verb
			if req.Path != want {
				t.Errorf("path = %q, want %q", req.Path, want)
			}
			if req.AuthUser != "admin" {
				t.Errorf("auth user = %q", req.AuthUser)
			}
			if req.RequestID == "" {
				t.Error("missing Ntnx-Request-Id idempotency header")
			}
		})
	}
}

func TestPowerOpUnknownVM(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	if _, err := c.Start("nope"); err == nil {
		t.Error("expected an error for an unknown name")
	}
}

func TestStopFallback(t *testing.T) {
	// The guest refuses the graceful shutdown (task FAILED): Stop must
	// escalate to the hard power-off and return the fallback's task.
	f := newFakePC(t)
	f.taskOutcomes = map[string][]string{"shutdown": {"FAILED"}}
	c := testClient(t, f)
	task, err := c.Stop("web-prod")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !strings.HasPrefix(task.ExtID, "task-power-off-") {
		t.Fatalf("task = %q, want the power-off fallback's task", task.ExtID)
	}
	if _, ok := findRequest(f, "/$actions/shutdown"); !ok {
		t.Error("graceful shutdown was never attempted")
	}
	req, ok := findRequest(f, "/$actions/power-off")
	if !ok {
		t.Fatal("hard power-off fallback was never issued")
	}
	if req.RequestID == "" {
		t.Error("fallback missing Ntnx-Request-Id idempotency header")
	}
}

func TestRestartFallback(t *testing.T) {
	// The guest refuses the reboot: Restart must fall back to power-cycle.
	f := newFakePC(t)
	f.taskOutcomes = map[string][]string{"reboot": {"FAILED"}}
	c := testClient(t, f)
	task, err := c.Restart("web-prod")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !strings.HasPrefix(task.ExtID, "task-power-cycle-") {
		t.Fatalf("task = %q, want the power-cycle fallback's task", task.ExtID)
	}
	if _, ok := findRequest(f, "/$actions/power-cycle"); !ok {
		t.Fatal("hard power-cycle fallback was never issued")
	}
}

func TestMigrate(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	task, err := c.Migrate("web-prod", "host-ext-9")
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !task.Valid() {
		t.Fatal("no task returned")
	}
	req, ok := findRequest(f, "/$actions/migrate")
	if !ok {
		t.Fatal("no migrate action recorded")
	}
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	wantPath := "/api/vmm/v4.0/ahv/config/vms/11111111-1111-1111-1111-111111111111/$actions/migrate"
	if req.Path != wantPath {
		t.Errorf("path = %q, want %q", req.Path, wantPath)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	host, _ := body["destinationHost"].(map[string]any)
	if host["extId"] != "host-ext-9" {
		t.Errorf("body = %s, want destinationHost host-ext-9", req.Body)
	}
	if req.RequestID == "" {
		t.Error("missing Ntnx-Request-Id idempotency header")
	}

	if _, err := c.Migrate("web-prod", ""); err == nil {
		t.Error("empty target should be refused, not guessed")
	}
}

func TestClone(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	task, err := c.Clone("web-prod", "web-prod-copy")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if !task.Valid() {
		t.Fatal("no task returned")
	}
	req, ok := findRequest(f, "/$actions/clone")
	if !ok {
		t.Fatal("no clone action recorded")
	}
	wantPath := "/api/vmm/v4.0/ahv/config/vms/11111111-1111-1111-1111-111111111111/$actions/clone"
	if req.Path != wantPath {
		t.Errorf("path = %q, want %q", req.Path, wantPath)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["name"] != "web-prod-copy" {
		t.Errorf("body = %s, want clone name web-prod-copy", req.Body)
	}
}

func TestSnapshots(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)

	if c.SupportsQuiesce() {
		t.Error("quiesce must report unsupported: the spike proved no v4 quiesce flag")
	}

	points, err := c.ListSnapshots("web-prod")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("got %d points, want 2 on web-prod", len(points))
	}
	for _, p := range points {
		if p.Consistency != CrashConsistent {
			t.Errorf("%s: consistency = %q, want crash only", p.Name, p.Consistency)
		}
	}
	req, ok := findRequest(f, "/recovery-points")
	if !ok {
		t.Fatal("no recovery-points request recorded")
	}
	if !strings.HasPrefix(req.Path, "/api/dataprotection/v4.0/recovery-points") {
		t.Errorf("path = %q, want dataprotection namespace", req.Path)
	}
	if filter := req.Query.Get("$filter"); !strings.Contains(filter, "11111111-1111-1111-1111-111111111111") {
		t.Errorf("$filter = %q, want server-side VM match", filter)
	}

	task, err := c.CreateSnapshot("web-prod", "pre-upgrade")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if !task.Valid() {
		t.Fatal("no task returned")
	}
	postReq, ok := findRequest(f, "/recovery-points")
	if !ok || postReq.Method != http.MethodPost {
		t.Fatal("no POST recovery-points recorded")
	}
	var cbody map[string]any
	if err := json.Unmarshal([]byte(postReq.Body), &cbody); err != nil {
		t.Fatalf("create body is not JSON: %v", err)
	}
	if cbody["name"] != "pre-upgrade" {
		t.Errorf("create body = %s", postReq.Body)
	}
	if _, hasQuiesce := cbody["quiesce"]; hasQuiesce {
		t.Errorf("create body = %s: must never carry a quiesce flag", postReq.Body)
	}

	dtask, err := c.DeleteSnapshot("web-prod", "nightly")
	if err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if !dtask.Valid() {
		t.Fatal("no task returned")
	}
	dreq, ok := findRequest(f, "/recovery-points/rp-uuid-3")
	if !ok || dreq.Method != http.MethodDelete {
		t.Fatalf("no DELETE of rp-uuid-3 recorded (ok=%v req=%+v)", ok, dreq)
	}

	rtask, err := c.RevertSnapshot("web-prod", "nightly")
	if err != nil {
		t.Fatalf("RevertSnapshot: %v", err)
	}
	if !rtask.Valid() {
		t.Fatal("no task returned")
	}
	rreq, ok := findRequest(f, "/$actions/revert")
	if !ok {
		t.Fatal("no revert action recorded")
	}
	var rbody map[string]any
	if err := json.Unmarshal([]byte(rreq.Body), &rbody); err != nil {
		t.Fatalf("revert body is not JSON: %v", err)
	}
	if rbody["recoveryPointExtId"] != "rp-uuid-3" {
		t.Errorf("revert body = %s, want recoveryPointExtId rp-uuid-3", rreq.Body)
	}

	if _, err := c.DeleteSnapshot("web-prod", "nope"); err == nil {
		t.Error("expected an error for an unknown snapshot")
	}
}

func TestWaitTask(t *testing.T) {
	f := newFakePC(t)
	f.tasks["task-uuid-1"] = []string{"PENDING", "RUNNING", "SUCCEEDED"}
	c := testClient(t, f)
	if err := c.WaitTask(Task{ExtID: "task-uuid-1"}, 30*time.Second); err != nil {
		t.Fatalf("WaitTask: %v", err)
	}
	req := f.lastRequest()
	if !strings.HasPrefix(req.Path, "/api/prism/v4.0/config/tasks/task-uuid-1") {
		t.Errorf("task path = %q", req.Path)
	}

	f2 := newFakePC(t)
	f2.tasks["task-uuid-1"] = []string{"FAILED"}
	c2 := testClient(t, f2)
	if err := c2.WaitTask(Task{ExtID: "task-uuid-1"}, 30*time.Second); err == nil {
		t.Error("expected an error for a failed task")
	}

	if err := c2.WaitTask(Task{}, time.Second); err != nil {
		t.Errorf("waiting on an invalid task should be a no-op: %v", err)
	}
}

func TestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"authentication failed"}`)
	}))
	t.Cleanup(srv.Close)
	c, _ := New(Config{Host: "pc.example.com", Username: "u", Secret: "bad"})
	c.base = srv.URL + "/api"
	c.WithHTTPClient(srv.Client())
	_, err := c.List()
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err type = %T, want *APIError", err)
	}
	if !apiErr.Unauthorized() {
		t.Errorf("status %d should report Unauthorized", apiErr.Status)
	}
}

func TestPasswordFromEnv(t *testing.T) {
	t.Setenv(PasswordEnv, "base")
	t.Setenv(PasswordEnv+"_LAB", "lab-secret")
	if got := passwordFromEnv("lab"); got != "lab-secret" {
		t.Errorf("per-context override = %q", got)
	}
	if got := passwordFromEnv("other"); got != "base" {
		t.Errorf("base = %q", got)
	}
	t.Setenv(PasswordEnv+"_MY_PC", "dash")
	if got := passwordFromEnv("my-pc"); got != "dash" {
		t.Errorf("suffix normalisation = %q", got)
	}
}
