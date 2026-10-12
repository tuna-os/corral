package xen

// Fixture-driven tests against a fake XenAPI pool: the fake speaks the real
// XenAPI envelope ({Status, Value, ErrorDescription}) with hand-written
// XML-RPC responses, so what is verified here is the client's reading of
// the protocol, not its agreement with itself.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
)

const testSession = "OpaqueRef:session-test"

// fakePool answers XML-RPC method calls from per-test handlers and records
// every method invoked, in order.
type fakePool struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	bodies   []string
	handlers map[string]func(body string) string
	export   string
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
	method := ""
	if i := strings.Index(body, "<methodName>"); i >= 0 {
		rest := body[i+len("<methodName>"):]
		if j := strings.Index(rest, "</methodName>"); j >= 0 {
			method = rest[:j]
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.bodies = append(f.bodies, body)
	h := f.handlers[method]
	f.mu.Unlock()
	if h == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("no handler for " + method))
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

// vmRecordXML is a realistic VM.get_record body for one guest.
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
		member("memory_static_max", fmt.Sprintf(`<int>%d</int>`, mem)) +
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
	if !strings.Contains(f.bodies[0], "session.login_with_password") {
		t.Fatalf("request does not name the method: %s", f.bodies[0])
	}
	for _, want := range []string{"root", "secret"} {
		if !strings.Contains(f.bodies[0], want) {
			t.Fatalf("request missing %q: %s", want, f.bodies[0])
		}
	}
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
}

// resolveHandlers answers get_by_uuid with UUID_INVALID (names are not
// UUIDs) and get_by_name_label with a single ref, exercising VMRef's
// fallback path.
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
}

// TestStopCleanNeverForcesPowerOff proves a successful graceful shutdown
// issues no hard_shutdown: the pool answers METHOD_UNKNOWN to the
// nonexistent VM.shutdown, so sending anything but VM.clean_shutdown turns
// every normal stop into a forced power-off.
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
	defer rc.Close()
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
}
