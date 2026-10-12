package xen

// Fixture-driven tests against a fake XenAPI pool.
//
// Provenance standard (RFC-0003): every method name, parameter order, field
// name, and response shape below is tied to the published XenAPI reference —
// method tables at https://xapi-project.github.io/xen-api/classes/vm.html
// (VM), .../classes/vdi.html (VDI.destroy/resize), .../classes/host.html,
// .../classes/vbd.html, .../classes/session.html; call semantics (session
// ref first, SESSION_INVALID, get_by_name_label returning a set, synchronous
// VM.start) at https://xapi-project.github.io/xen-api/usage.html; snapshot
// semantics at .../xen-api/snapshots.html; export at
// .../xen-api/importexport.html. Per-fixture comments cite the exact page.
//
// These are hand-written to those documented shapes, not packet captures —
// where a shape detail cannot be tied to the docs it is marked SYNTHETIC.
// The fake parses every request's full parameter list (not just the method
// name), so a test fails if the client calls the wrong object or swaps
// arguments: that is the check that would have caught VM.shutdown.

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
)

const testSession = "OpaqueRef:session-test"

// fakePool answers XML-RPC method calls from per-test handlers and records
// every method invoked, in order, with its full decoded parameter list.
type fakePool struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	bodies   []string
	args     [][]any
	handlers map[string]func(body string) string
	export   string
}

// wireCall is one XML-RPC request: the method plus its ordered parameters,
// decoded with the client's own value parser so the fake reads exactly what
// the client wrote.
type wireCall struct {
	Method string     `xml:"methodName"`
	Params []rpcValue `xml:"params>param>value"`
}

func newFakePool(t *testing.T) *fakePool {
	t.Helper()
	return &fakePool{t: t, handlers: map[string]func(string) string{}}
}

func (f *fakePool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/export" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(f.export))
		return
	}
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	var call wireCall
	if err := xml.Unmarshal([]byte(body), &call); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("malformed XML-RPC request: " + err.Error()))
		return
	}
	params := make([]any, 0, len(call.Params))
	for _, p := range call.Params {
		params = append(params, p.goValue())
	}
	f.mu.Lock()
	f.calls = append(f.calls, call.Method)
	f.bodies = append(f.bodies, body)
	f.args = append(f.args, params)
	h := f.handlers[call.Method]
	f.mu.Unlock()
	if h == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("no handler for " + call.Method))
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = w.Write([]byte(h(body)))
}

func (f *fakePool) served(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}

// lastArgs returns the decoded parameters of the most recent call to method.
func (f *fakePool) lastArgs(method string) ([]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i] == method {
			return f.args[i], true
		}
	}
	return nil, false
}

// assertWire checks the last call to method carried exactly want, in order:
// session reference first (the usage guide: every call takes the session as
// its first parameter), then the documented arguments. Numbers compare
// numerically across int encodings; anything else must match exactly. A
// client that calls the wrong object or swaps two arguments fails here even
// when the response fixture would have let it pass.
func assertWire(t *testing.T, f *fakePool, method string, want ...any) {
	t.Helper()
	got, ok := f.lastArgs(method)
	if !ok {
		t.Fatalf("%s was never called", method)
	}
	if len(got) != len(want) {
		t.Fatalf("%s called with %d params (%v), want %d (%v)",
			method, len(got), got, len(want), want)
	}
	for i := range want {
		if !wireEqual(got[i], want[i]) {
			t.Fatalf("%s param %d = %#v, want %#v (full: %#v)",
				method, i, got[i], want[i], got)
		}
	}
}

func wireEqual(got, want any) bool {
	if gf, ok := wireFloat(got); ok {
		if wf, ok := wireFloat(want); ok {
			return gf == wf
		}
		return false
	}
	if _, ok := wireFloat(want); ok {
		return false
	}
	return reflect.DeepEqual(got, want)
}

func wireFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	default:
		return 0, false
	}
}

func testClient(t *testing.T, f *fakePool) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(Config{Host: strings.TrimPrefix(srv.URL, "http://"), Username: "root", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	// The fake pool speaks plain HTTP; the client's https base (built for a
	// real pool) is swapped for the test server URL here, same-package.
	c.base = srv.URL + "/"
	httpClient := srv.Client()
	httpClient.Timeout = Timeout
	return c.WithHTTPClient(httpClient)
}

// ── envelope fixtures ─────────────────────────────────────────────
// success/failure build the XenAPI response envelope: every call returns a
// struct with Status ("Success"/"Failure"), Value on success, and
// ErrorDescription ([code, params...]) on failure — the shape the reference
// XenAPI.py binding surfaces as return values vs Failure(details), with the
// usage guide naming SESSION_INVALID and HOST_IS_SLAVE as failure codes.
// A void call answers an empty-string Value.

func success(valueXML string) string {
	return `<methodResponse><params><param><value><struct>` +
		`<member><name>Status</name><value><string>Success</string></value></member>` +
		`<member><name>Value</name><value>` + valueXML + `</value></member>` +
		`</struct></value></param></params></methodResponse>`
}

func failure(code string, params ...string) string {
	var sb strings.Builder
	sb.WriteString(`<methodResponse><params><param><value><struct>` +
		`<member><name>Status</name><value><string>Failure</string></value></member>` +
		`<member><name>ErrorDescription</name><value><array><data>`)
	sb.WriteString(`<value><string>` + code + `</string></value>`)
	for _, p := range params {
		sb.WriteString(`<value><string>` + p + `</string></value>`)
	}
	sb.WriteString(`</data></array></value></member></struct></value></param></params></methodResponse>`)
	return sb.String()
}

func str(s string) string { return `<string>` + s + `</string>` }

func arr(items ...string) string {
	return `<array><data>` + strings.Join(items, "") + `</data></array>`
}

func val(s string) string { return `<value>` + s + `</value>` }

// vmRecordXML is a VM.get_record body for one guest. Member names follow the
// VM class reference (https://xapi-project.github.io/xen-api/classes/vm.html):
// uuid, name_label, name_description, power_state (Halted/Running per the
// vm_power_state enum), is_a_template, is_control_domain, is_a_snapshot,
// VCPUs_max, memory_static_max, resident_on (a host ref, OpaqueRef:NULL when
// halted — the XenAPI null-ref convention), tags (a string set: an array on
// the wire).
// SYNTHETIC: memory_static_max here (4 GiB) is wider than a 32-bit XML-RPC
// int, so it is emitted as a string; the exact server encoding for wide
// counters is unverified from the docs, and the client accepts int, i8, and
// string alike.
func vmRecordXML(uuid, label, power string, template, control, snapshot bool, vcpus int, mem int64, resident string) string {
	b := func(v bool) string {
		if v {
			return `<boolean>1</boolean>`
		}
		return `<boolean>0</boolean>`
	}
	member := func(name, v string) string {
		return `<member><name>` + name + `</name><value>` + v + `</value></member>`
	}
	return `<struct>` +
		member("uuid", str(uuid)) +
		member("name_label", str(label)) +
		member("name_description", str(label+" description")) +
		member("power_state", str(power)) +
		member("is_a_template", b(template)) +
		member("is_control_domain", b(control)) +
		member("is_a_snapshot", b(snapshot)) +
		member("VCPUs_max", fmt.Sprintf(`<int>%d</int>`, vcpus)) +
		member("memory_static_max", str(fmt.Sprintf(`%d`, mem))) +
		member("resident_on", str(resident)) +
		member("tags", arr(val(str("web")), val(str("prod")))) +
		`</struct>`
}

func loginHandler(sess string) func(string) string {
	return func(string) string { return success(str(sess)) }
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
	if _, err := New(Config{Host: "xen.example.com"}); err == nil {
		t.Fatal("missing username accepted")
	}
	if _, err := New(Config{Host: "xen.example.com", Username: "root"}); err == nil {
		t.Fatal("missing password accepted")
	}
	if _, err := New(Config{Host: "xen.example.com", Username: "root", Password: "x", Fingerprint: "abc"}); err == nil {
		t.Fatal("short fingerprint accepted")
	}
	c, err := New(Config{Host: "xen.example.com", Username: "root", Password: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.base, "https://xen.example.com/") {
		t.Fatalf("base = %q", c.base)
	}
}

func TestLogin(t *testing.T) {
	// session.login_with_password(user, password, version, originator) takes
	// no session ref — it mints one. Signature per the usage guide
	// (https://xapi-project.github.io/xen-api/usage.html), whose example
	// logs in as ("root", "", "2.3", "My Widget v0.1").
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	c := testClient(t, f)
	sess, err := c.Login()
	if err != nil {
		t.Fatal(err)
	}
	if sess != testSession {
		t.Fatalf("session = %q", sess)
	}
	assertWire(t, f, "session.login_with_password", "root", "secret", "1.0", "corral")
}

func TestLoginFault(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = func(string) string {
		return failure("SESSION_AUTHENTICATION_FAILED")
	}
	c := testClient(t, f)
	if _, err := c.Login(); err == nil {
		t.Fatal("bad credentials accepted")
	} else if apiErr, ok := err.(*APIError); !ok || apiErr.Code != "SESSION_AUTHENTICATION_FAILED" {
		t.Fatalf("err = %v", err)
	}
}

func TestXMLRPCFault(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = func(string) string {
		return `<methodCall><garbage`
	}
	c := testClient(t, f)
	if _, err := c.Login(); err == nil {
		t.Fatal("malformed response accepted")
	}
}

// wireInventory serves List: VM.get_all (a set of refs, per the usage guide's
// implicit messages), one VM.get_record per VM, host.get_all_records (a
// ref→record map; host fields per the host class reference), and the
// VBD/VDI get_all_records join for disk sizes (VBD.VM/VBD.VDI and
// VDI.virtual_size per the VBD/VDI class references).
// SYNTHETIC: VDI.virtual_size here (20 GiB) exceeds 32-bit XML-RPC int range
// and is emitted as a string; exact server encoding unverified, client
// accepts int/i8/string.
func wireInventory(f *fakePool) {
	refs := []string{
		"OpaqueRef:vm-running", "OpaqueRef:vm-halted",
		"OpaqueRef:vm-template", "OpaqueRef:vm-dom0", "OpaqueRef:vm-snap",
	}
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	f.handlers["VM.get_all"] = func(string) string {
		items := make([]string, 0, len(refs))
		for _, r := range refs {
			items = append(items, val(str(r)))
		}
		return success(arr(items...))
	}
	records := map[string]string{
		"OpaqueRef:vm-running": vmRecordXML("uuid-running", "web-1", "Running", false, false, false, 2, 4294967296, "OpaqueRef:host-1"),
		"OpaqueRef:vm-halted":  vmRecordXML("uuid-halted", "db-1", "Halted", false, false, false, 2, 4294967296, "OpaqueRef:NULL"),
		"OpaqueRef:vm-template": vmRecordXML("uuid-tmpl", "ubuntu-2404", "Halted", true, false, false, 2, 4294967296,
			"OpaqueRef:NULL"),
		"OpaqueRef:vm-dom0": vmRecordXML("uuid-dom0", "Control domain on host", "Running", false, true, false, 2, 4294967296,
			"OpaqueRef:host-1"),
		"OpaqueRef:vm-snap": vmRecordXML("uuid-snap", "web-1-snap", "Halted", false, false, true, 2, 4294967296,
			"OpaqueRef:NULL"),
	}
	f.handlers["VM.get_record"] = func(body string) string {
		for ref, rec := range records {
			if strings.Contains(body, ref) {
				return success(rec)
			}
		}
		return failure("HANDLE_INVALID", "VM", "unknown")
	}
	f.handlers["host.get_all_records"] = func(string) string {
		return success(`<struct><member><name>OpaqueRef:host-1</name><value><struct>` +
			`<member><name>uuid</name><value><string>host-uuid-1</string></value></member>` +
			`<member><name>name_label</name><value><string>xen-1</string></value></member>` +
			`<member><name>hostname</name><value><string>xen-1.example.com</string></value></member>` +
			`</struct></value></member></struct>`)
	}
	f.handlers["VBD.get_all_records"] = func(string) string {
		return success(`<struct>` +
			`<member><name>OpaqueRef:vbd-1</name><value><struct>` +
			`<member><name>VM</name><value><string>OpaqueRef:vm-running</string></value></member>` +
			`<member><name>VDI</name><value><string>OpaqueRef:vdi-1</string></value></member>` +
			`</struct></value></member>` +
			`<member><name>OpaqueRef:vbd-cd</name><value><struct>` +
			`<member><name>VM</name><value><string>OpaqueRef:vm-running</string></value></member>` +
			`<member><name>VDI</name><value><string>OpaqueRef:NULL</string></value></member>` +
			`</struct></value></member>` +
			`</struct>`)
	}
	f.handlers["VDI.get_all_records"] = func(string) string {
		return success(`<struct>` +
			`<member><name>OpaqueRef:vdi-1</name><value><struct>` +
			`<member><name>virtual_size</name><value><string>21474836480</string></value></member>` +
			`</struct></value></member>` +
			`</struct>`)
	}
}

func TestList(t *testing.T) {
	f := newFakePool(t)
	wireInventory(f)
	c := testClient(t, f)
	vms, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 2 {
		t.Fatalf("got %d VMs, want 2 (running+halted; template/dom0/snapshot skipped)", len(vms))
	}
	byName := map[string]bool{}
	for _, vm := range vms {
		byName[vm.Name] = vm.Running
		if vm.Backend != "xen" {
			t.Fatalf("Backend = %q", vm.Backend)
		}
		if vm.Context != c.cfg.Host {
			t.Fatalf("Context = %q", vm.Context)
		}
		if vm.ID == "" {
			t.Fatalf("%s has no identity", vm.Name)
		}
		if !vm.Capabilities.Start || !vm.Capabilities.Snapshots || !vm.Capabilities.Migrate {
			t.Fatalf("%s capabilities = %+v", vm.Name, vm.Capabilities)
		}
	}
	if !byName["web-1"] {
		t.Fatal("web-1 should be running")
	}
	if byName["db-1"] {
		t.Fatal("db-1 should be halted")
	}
	for _, vm := range vms {
		if vm.Name == "web-1" {
			if vm.Node != "xen-1" {
				t.Fatalf("web-1 Node = %q, want xen-1", vm.Node)
			}
			if vm.Disk == "" {
				t.Fatal("web-1 should report disk size from the VDI join")
			}
			if vm.CPU != 2 {
				t.Fatalf("web-1 CPU = %d", vm.CPU)
			}
			if vm.Mem == "" {
				t.Fatal("web-1 should report memory")
			}
		}
	}
	// Every inventory call carries the session first — the wire contract for
	// the whole listing, not just its shape.
	assertWire(t, f, "VM.get_all", testSession)
	assertWire(t, f, "VM.get_record", testSession, "OpaqueRef:vm-snap")
	assertWire(t, f, "host.get_all_records", testSession)
	assertWire(t, f, "VBD.get_all_records", testSession)
	assertWire(t, f, "VDI.get_all_records", testSession)
}

// resolveHandlers answers get_by_uuid with UUID_INVALID (names are not
// UUIDs) and get_by_name_label with a single ref, exercising VMRef's
// fallback path. Both lookups are implicit class messages per the usage
// guide; get_by_name_label returns a set because labels are not unique.
func resolveHandlers(f *fakePool, ref string) {
	f.handlers["VM.get_by_uuid"] = func(string) string {
		return failure("UUID_INVALID", "VM", "not-a-uuid")
	}
	f.handlers["VM.get_by_name_label"] = func(string) string {
		return success(arr(val(str(ref))))
	}
}

func TestPowerOps(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.start"] = func(string) string { return success(`<string></string>`) }
	f.handlers["VM.clean_reboot"] = func(string) string { return success(`<string></string>`) }
	f.handlers["VM.suspend"] = func(string) string { return success(`<string></string>`) }
	f.handlers["VM.clean_shutdown"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	for _, op := range []func(string) error{c.Start, c.Stop, c.Reboot, c.Suspend} {
		if err := op("web-1"); err != nil {
			t.Fatal(err)
		}
	}
	// Exact wire contract per the VM class reference
	// (https://xapi-project.github.io/xen-api/classes/vm.html):
	// start(session, VM, paused, force), clean_shutdown(session, VM),
	// clean_reboot(session, VM), suspend(session, VM). A void call answers
	// an empty-string Value.
	assertWire(t, f, "VM.get_by_uuid", testSession, "web-1")
	assertWire(t, f, "VM.get_by_name_label", testSession, "web-1")
	assertWire(t, f, "VM.start", testSession, "OpaqueRef:vm-running", false, false)
	assertWire(t, f, "VM.clean_shutdown", testSession, "OpaqueRef:vm-running")
	assertWire(t, f, "VM.clean_reboot", testSession, "OpaqueRef:vm-running")
	assertWire(t, f, "VM.suspend", testSession, "OpaqueRef:vm-running")
}

// TestStopCleanNeverForcesPowerOff proves a successful graceful shutdown
// issues no hard_shutdown. The client sends only VM.clean_shutdown — never
// the bare VM.shutdown, which real pools may answer with METHOD_UNKNOWN
// (accepted blocker advisory): treating that failure as "guest uncooperative"
// would turn every normal stop into a forced power-off.
func TestStopCleanNeverForcesPowerOff(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.clean_shutdown"] = func(string) string { return success(`<string></string>`) }
	f.handlers["VM.hard_shutdown"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	if err := c.Stop("web-1"); err != nil {
		t.Fatal(err)
	}
	if f.served("VM.clean_shutdown") != 1 {
		t.Fatal("Stop did not issue VM.clean_shutdown")
	}
	if f.served("VM.hard_shutdown") != 0 {
		t.Fatal("successful clean shutdown also issued VM.hard_shutdown")
	}
	if f.served("VM.shutdown") != 0 {
		t.Fatal("Stop invoked the nonexistent VM.shutdown method")
	}
}

func TestStopFallsBackToHardShutdown(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.clean_shutdown"] = func(string) string {
		return failure("VM_REQUIRES_TOOLS", "OpaqueRef:vm-running")
	}
	f.handlers["VM.hard_shutdown"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	if err := c.Stop("web-1"); err != nil {
		t.Fatal(err)
	}
	if f.served("VM.hard_shutdown") != 1 {
		t.Fatal("clean shutdown failure did not fall back to hard shutdown")
	}
}

func TestStopBadPowerStateIsNotRetried(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-halted")
	f.handlers["VM.clean_shutdown"] = func(string) string {
		return failure("VM_BAD_POWER_STATE", "OpaqueRef:vm-halted", "halted")
	}
	f.handlers["VM.hard_shutdown"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	if err := c.Stop("db-1"); err == nil {
		t.Fatal("stopping a halted VM should fail")
	}
	if f.served("VM.hard_shutdown") != 0 {
		t.Fatal("a halted VM must not be hard-shut-down")
	}
}

func TestSnapshotCloneRevert(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.snapshot"] = func(body string) string {
		if !strings.Contains(body, "web-1-snap") {
			return failure("INVALID_VALUE", "snapshot name missing")
		}
		return success(str("OpaqueRef:snap-new"))
	}
	f.handlers["VM.clone"] = func(string) string { return success(str("OpaqueRef:clone-new")) }
	f.handlers["VM.revert"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	snap, err := c.Snapshot("web-1", "web-1-snap")
	if err != nil {
		t.Fatal(err)
	}
	if snap != "OpaqueRef:snap-new" {
		t.Fatalf("snapshot ref = %q", snap)
	}
	clone, err := c.Clone("web-1", "web-2")
	if err != nil {
		t.Fatal(err)
	}
	if clone != "OpaqueRef:clone-new" {
		t.Fatalf("clone ref = %q", clone)
	}
	if err := c.Revert(snap); err != nil {
		t.Fatal(err)
	}
	// Exact wire contract per the VM class reference:
	// snapshot(session, VM, new_name, VDI set) — the empty set means every
	// disk; clone(session, VM, new_name); revert(session, snapshot).
	assertWire(t, f, "VM.snapshot", testSession, "OpaqueRef:vm-running", "web-1-snap", []any{})
	assertWire(t, f, "VM.clone", testSession, "OpaqueRef:vm-running", "web-2")
	assertWire(t, f, "VM.revert", testSession, "OpaqueRef:snap-new")
}

func TestMigrate(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["host.get_all_records"] = func(string) string {
		return success(`<struct><member><name>OpaqueRef:host-2</name><value><struct>` +
			`<member><name>uuid</name><value><string>host-uuid-2</string></value></member>` +
			`<member><name>name_label</name><value><string>xen-2</string></value></member>` +
			`<member><name>hostname</name><value><string>xen-2.example.com</string></value></member>` +
			`</struct></value></member></struct>`)
	}
	f.handlers["VM.pool_migrate"] = func(body string) string {
		if !strings.Contains(body, "OpaqueRef:host-2") {
			return failure("INVALID_VALUE", "host ref missing")
		}
		return success(`<string></string>`)
	}
	c := testClient(t, f)
	if err := c.Migrate("web-1", "xen-2"); err != nil {
		t.Fatal(err)
	}
	// pool_migrate(session, VM, host, options) per the VM class reference;
	// the empty options map means intra-pool motion with no VDI/VIF
	// remapping (remapping is the cross-pool migrate_send path).
	assertWire(t, f, "VM.pool_migrate", testSession, "OpaqueRef:vm-running", "OpaqueRef:host-2", map[string]any{})
}

func TestSessionInvalidRetriesOnce(t *testing.T) {
	f := newFakePool(t)
	logins := 0
	f.handlers["session.login_with_password"] = func(string) string {
		logins++
		return success(str(testSession))
	}
	calls := 0
	f.handlers["VM.get_all"] = func(string) string {
		calls++
		if calls == 1 {
			return failure("SESSION_INVALID", testSession)
		}
		return success(arr())
	}
	f.handlers["host.get_all_records"] = func(string) string { return success(`<struct></struct>`) }
	f.handlers["VBD.get_all_records"] = func(string) string { return success(`<struct></struct>`) }
	f.handlers["VDI.get_all_records"] = func(string) string { return success(`<struct></struct>`) }
	c := testClient(t, f)
	if _, err := c.List(); err != nil {
		t.Fatal(err)
	}
	if logins != 2 {
		t.Fatalf("logins = %d, want 2 (initial + SESSION_INVALID retry)", logins)
	}
}

// TestExport covers the /export HTTP download (not an XML-RPC method): GET
// with session_id and ref query parameters, per the VM import/export guide
// (https://xapi-project.github.io/xen-api/importexport.html). The byte
// content is opaque to the client, so the fixture asserts streaming, not
// format.
func TestExport(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.export = "fake-xva-bytes"
	c := testClient(t, f)
	rc, err := c.Export("web-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake-xva-bytes" {
		t.Fatalf("export = %q", data)
	}
}

func TestPasswordEnv(t *testing.T) {
	t.Setenv("CORRAL_XEN_PASSWORD", "shared")
	t.Setenv("CORRAL_XEN_PASSWORD_POOL2", "specific")
	if got := PasswordFromEnv("pool1"); got != "shared" {
		t.Fatalf("shared = %q", got)
	}
	if got := PasswordFromEnv("pool2"); got != "specific" {
		t.Fatalf("per-context = %q", got)
	}
}

func TestConfigForContext(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Contexts = []config.ContextConfig{{
		Name:    "pool1",
		Backend: "xen",
		Context: "xen.example.com",
		Xen:     &config.XenContextConfig{Username: "admin"},
	}}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORRAL_XEN_PASSWORD", "secret")
	resolved, err := ConfigForContext("pool1")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Host != "xen.example.com" || resolved.Username != "admin" || resolved.Password != "secret" {
		t.Fatalf("resolved = %+v", resolved)
	}
	t.Setenv("CORRAL_XEN_PASSWORD", "")
	if _, err := ConfigForContext("pool1"); err == nil {
		t.Fatal("missing password accepted")
	}
	if _, err := ConfigForContext("no-such-pool"); err == nil {
		t.Fatal("unknown context accepted")
	}
}

func TestDestroyRemovesVMAndDisks(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-halted")
	f.handlers["VM.get_record"] = func(string) string {
		return success(`<struct>` +
			`<member><name>uuid</name><value><string>uuid-halted</string></value></member>` +
			`<member><name>name_label</name><value><string>db-1</string></value></member>` +
			`<member><name>power_state</name><value><string>Halted</string></value></member>` +
			`<member><name>is_a_template</name><value><boolean>0</boolean></value></member>` +
			`<member><name>is_control_domain</name><value><boolean>0</boolean></value></member>` +
			`<member><name>is_a_snapshot</name><value><boolean>0</boolean></value></member>` +
			`<member><name>VBDs</name><value><array><data>` +
			`<value><string>OpaqueRef:vbd-1</string></value>` +
			`</data></array></value></member>` +
			`</struct>`)
	}
	f.handlers["VBD.get_record"] = func(string) string {
		return success(`<struct>` +
			`<member><name>VM</name><value><string>OpaqueRef:vm-halted</string></value></member>` +
			`<member><name>VDI</name><value><string>OpaqueRef:vdi-1</string></value></member>` +
			`</struct>`)
	}
	f.handlers["VM.destroy"] = func(string) string { return success(`<string></string>`) }
	f.handlers["VDI.destroy"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	if err := c.Destroy("db-1"); err != nil {
		t.Fatal(err)
	}
	if f.served("VM.destroy") != 1 || f.served("VDI.destroy") != 1 {
		t.Fatalf("destroy calls: VM=%d VDI=%d, want 1 each",
			f.served("VM.destroy"), f.served("VDI.destroy"))
	}
	// destroy(session, VM) and destroy(session, VDI) per the VM/VDI class
	// references; the VDI behind each VBD goes after the VM record so no
	// attached disk outlives its guest.
	assertWire(t, f, "VBD.get_record", testSession, "OpaqueRef:vbd-1")
	assertWire(t, f, "VM.destroy", testSession, "OpaqueRef:vm-halted")
	assertWire(t, f, "VDI.destroy", testSession, "OpaqueRef:vdi-1")
}

func TestCanMigrate(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.get_record"] = func(string) string {
		return success(`<struct>` +
			`<member><name>power_state</name><value><string>Running</string></value></member>` +
			`</struct>`)
	}
	f.handlers["host.get_all_records"] = func(string) string {
		return success(`<struct>` +
			`<member><name>OpaqueRef:host-1</name><value><struct>` +
			`<member><name>name_label</name><value><string>xen-1</string></value></member>` +
			`</struct></value></member>` +
			`<member><name>OpaqueRef:host-2</name><value><struct>` +
			`<member><name>name_label</name><value><string>xen-2</string></value></member>` +
			`</struct></value></member>` +
			`</struct>`)
	}
	c := testClient(t, f)
	if ok, reason := c.CanMigrate("web-1"); !ok {
		t.Fatalf("running VM in 2-host pool refused: %s", reason)
	}
}

func TestCanMigrateRefusesHalted(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-halted")
	f.handlers["VM.get_record"] = func(string) string {
		return success(`<struct>` +
			`<member><name>power_state</name><value><string>Halted</string></value></member>` +
			`</struct>`)
	}
	c := testClient(t, f)
	if ok, _ := c.CanMigrate("db-1"); ok {
		t.Fatal("halted VM accepted for live migration")
	}
}

func TestSnapshotsListAndDelete(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.get_snapshots"] = func(string) string {
		return success(arr(val(str("OpaqueRef:snap-1"))))
	}
	f.handlers["VM.get_record"] = func(string) string {
		return success(`<struct>` +
			`<member><name>name_label</name><value><string>web-1-snap</string></value></member>` +
			`<member><name>power_state</name><value><string>Halted</string></value></member>` +
			`</struct>`)
	}
	f.handlers["VM.destroy"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	snaps, err := c.Snapshots("web-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Name != "web-1-snap" {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if err := c.DestroySnapshot("web-1", "web-1-snap"); err != nil {
		t.Fatal(err)
	}
	if f.served("VM.destroy") != 1 {
		t.Fatal("destroy was not issued for the snapshot")
	}
	if err := c.DestroySnapshot("web-1", "already-gone"); err != nil {
		t.Fatalf("deleting a missing snapshot should be a no-op: %v", err)
	}
	// get_snapshots(session, VM) per the VM class reference; the snapshot
	// goes out via destroy(session, VM) like any VM object.
	assertWire(t, f, "VM.get_snapshots", testSession, "OpaqueRef:vm-running")
	assertWire(t, f, "VM.destroy", testSession, "OpaqueRef:snap-1")
}
