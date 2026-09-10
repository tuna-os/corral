package backend

// Adapter behaviour, one table over every backend that drives a command line.
//
// The adapters are the only place a backend's own signature is translated into
// the contract, and they were the least-tested file in the repo: a typo in an
// argument list, a method wired to the wrong client call, or a family quietly
// dropped from an adapter all reach an operator's fleet before anything fails.
//
// Each case installs a fake runner in the backend's own seam, builds the
// adapter through the registry — so For() and the factories are covered too —
// and asserts the command that reached the seam. Nothing here dials anything.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/proxmoxbe"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// seam installs fake into the package the adapter delegates to, and restores
// the real runner when the test ends.
func seam(t *testing.T, backend string, fake *shell.Fake) {
	t.Helper()
	switch backend {
	case "kubevirt":
		// Three seams, because manifests go out through Apply (its own runner)
		// while lifecycle goes through the client's.
		kubevirt.SetDefaultRunner(fake)
		kubevirt.SetPackageRunner(fake)
		kubevirt.SetApplyRunner(fake)
		t.Cleanup(func() {
			kubevirt.SetDefaultRunner(shell.Real{})
			kubevirt.SetPackageRunner(shell.Real{})
			kubevirt.SetApplyRunner(shell.Real{})
		})
	case "incus":
		incus.SetRunner(fake)
		t.Cleanup(func() { incus.SetRunner(shell.Real{}) })
	case "libvirt":
		libvirt.SetRunner(fake)
		t.Cleanup(func() { libvirt.SetRunner(shell.Real{}) })
	default:
		t.Fatalf("no runner seam known for backend %q", backend)
	}
}

// newFake returns a runner that answers anything with success. The assertions
// here are about which command was issued, not what it printed; a case that
// needs output registers its own prefix response.
func newFake() *shell.Fake {
	f := shell.NewFake()
	for _, bin := range []string{"kubectl", "virsh", "incus", "/fake/bin/virtctl", "systemctl"} {
		f.AddPrefixResponse(bin, "", nil)
	}
	return f
}

// sawCommand reports whether the fake recorded a call whose command line
// contains want. Matching the line rather than argument-by-argument keeps the
// expectations readable — and they read as the command an operator would run.
func sawCommand(calls []shell.Call, want string) bool {
	for _, c := range calls {
		if strings.Contains(commandLine(c), want) {
			return true
		}
	}
	return false
}

func commandLine(c shell.Call) string {
	// virtctl resolves through LookPath, so it arrives as /fake/bin/virtctl.
	name := c.Name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSpace(name + " " + strings.Join(c.Args, " "))
}

func describeCalls(calls []shell.Call) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString("\n  " + commandLine(c))
	}
	if b.Len() == 0 {
		return " (no commands ran)"
	}
	return b.String()
}

// The Power family, which every backend implements, plus Restarter and
// Suspender where the adapter has them. One case per backend rather than one
// per method: what is being pinned is the translation, and a table keeps a new
// backend to a few lines here.
func TestAdapters_PowerCommands(t *testing.T) {
	cases := []struct {
		backend          string
		ref              types.InstanceRef
		start, stop, del string
	}{
		{
			backend: "kubevirt",
			ref:     types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"},
			start:   "virtctl start web-1 -n corral-vms",
			stop:    "virtctl stop web-1 -n corral-vms",
			del:     "kubectl delete vm web-1 -n corral-vms",
		},
		{
			backend: "incus",
			ref:     types.InstanceRef{Backend: "incus", Name: "ct-1"},
			start:   "incus start local:ct-1",
			stop:    "incus stop local:ct-1",
			del:     "incus delete local:ct-1 --force",
		},
		{
			backend: "libvirt",
			ref:     types.InstanceRef{Backend: "libvirt", Name: "dom-1"},
			start:   "virsh -c qemu:///system start dom-1",
			stop:    "virsh -c qemu:///system shutdown dom-1",
			del:     "virsh -c qemu:///system undefine dom-1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.backend, func(t *testing.T) {
			for _, op := range []struct {
				verb string
				want string
				call func(Adapter) error
			}{
				{"start", tc.start, func(a Adapter) error { return a.(Power).Start(tc.ref.Name) }},
				{"stop", tc.stop, func(a Adapter) error { return a.(Power).Stop(tc.ref.Name) }},
				{"delete", tc.del, func(a Adapter) error { return a.(Power).Delete(tc.ref.Name) }},
			} {
				fake := newFake()
				seam(t, tc.backend, fake)

				adapter, err := For(tc.ref)
				if err != nil {
					t.Fatalf("For(%s): %v", tc.backend, err)
				}
				if adapter.Backend() != tc.backend {
					t.Errorf("Backend() = %q, want %q", adapter.Backend(), tc.backend)
				}
				if _, ok := adapter.(Power); !ok {
					t.Fatalf("%s adapter does not implement Power", tc.backend)
				}

				if err := op.call(adapter); err != nil {
					t.Fatalf("%s %s: %v", tc.backend, op.verb, err)
				}
				if !sawCommand(fake.Calls(), op.want) {
					t.Errorf("%s %s did not run `%s`; commands were:%s",
						tc.backend, op.verb, op.want, describeCalls(fake.Calls()))
				}
			}
		})
	}
}

// Restart is its own family because "the backend has a reboot" and "we fake one
// with stop+start" are different facts. qemu's adapter is deliberately the
// second kind; the others must reach a native verb.
func TestAdapters_RestartUsesTheBackendsOwnVerb(t *testing.T) {
	cases := []struct {
		backend string
		ref     types.InstanceRef
		want    string
	}{
		{"kubevirt", types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"}, "virtctl restart web-1 -n corral-vms"},
		{"incus", types.InstanceRef{Backend: "incus", Name: "ct-1"}, "incus restart local:ct-1"},
		{"libvirt", types.InstanceRef{Backend: "libvirt", Name: "dom-1"}, "virsh -c qemu:///system reboot dom-1"},
	}
	for _, tc := range cases {
		t.Run(tc.backend, func(t *testing.T) {
			fake := newFake()
			seam(t, tc.backend, fake)

			adapter, err := For(tc.ref)
			if err != nil {
				t.Fatalf("For(%s): %v", tc.backend, err)
			}
			restarter, ok := adapter.(Restarter)
			if !ok {
				t.Fatalf("%s adapter does not implement Restarter", tc.backend)
			}
			if err := restarter.Restart(tc.ref.Name); err != nil {
				t.Fatalf("Restart: %v", err)
			}
			if !sawCommand(fake.Calls(), tc.want) {
				t.Errorf("Restart did not run `%s`; commands were:%s",
					tc.want, describeCalls(fake.Calls()))
			}
		})
	}
}

func TestAdapters_SuspendCommands(t *testing.T) {
	cases := []struct {
		backend       string
		ref           types.InstanceRef
		pause, resume string
	}{
		{"kubevirt", types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"},
			"virtctl pause vm web-1 -n corral-vms", "virtctl unpause vm web-1 -n corral-vms"},
		// Incus has no "resume": the adapter's Resume is a start, which is the
		// backend's own way back from a paused instance.
		{"incus", types.InstanceRef{Backend: "incus", Name: "ct-1"},
			"incus pause local:ct-1", "incus start local:ct-1"},
		{"libvirt", types.InstanceRef{Backend: "libvirt", Name: "dom-1"},
			"virsh -c qemu:///system suspend dom-1", "virsh -c qemu:///system resume dom-1"},
	}
	for _, tc := range cases {
		t.Run(tc.backend, func(t *testing.T) {
			fake := newFake()
			seam(t, tc.backend, fake)

			adapter, err := For(tc.ref)
			if err != nil {
				t.Fatalf("For(%s): %v", tc.backend, err)
			}
			susp, ok := adapter.(Suspender)
			if !ok {
				t.Fatalf("%s adapter does not implement Suspender", tc.backend)
			}
			if err := susp.Pause(tc.ref.Name); err != nil {
				t.Fatalf("Pause: %v", err)
			}
			if !sawCommand(fake.Calls(), tc.pause) {
				t.Errorf("Pause did not run `%s`; commands were:%s",
					tc.pause, describeCalls(fake.Calls()))
			}

			fake2 := newFake()
			seam(t, tc.backend, fake2)
			adapter2, err := For(tc.ref)
			if err != nil {
				t.Fatalf("For(%s): %v", tc.backend, err)
			}
			if err := adapter2.(Suspender).Resume(tc.ref.Name); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if !sawCommand(fake2.Calls(), tc.resume) {
				t.Errorf("Resume did not run `%s`; commands were:%s",
					tc.resume, describeCalls(fake2.Calls()))
			}
		})
	}
}

// A failing command must come back as an error rather than a silent success:
// the fleet surfaces report what these return.
func TestAdapters_PropagateBackendFailures(t *testing.T) {
	for _, tc := range []struct {
		backend string
		ref     types.InstanceRef
		bin     string
	}{
		{"incus", types.InstanceRef{Backend: "incus", Name: "ct-1"}, "incus"},
		{"libvirt", types.InstanceRef{Backend: "libvirt", Name: "dom-1"}, "virsh"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			fake := shell.NewFake()
			fake.AddPrefixResponse(tc.bin, "boom", errString("exit status 1"))
			seam(t, tc.backend, fake)

			adapter, err := For(tc.ref)
			if err != nil {
				t.Fatalf("For(%s): %v", tc.backend, err)
			}
			if err := adapter.(Power).Stop(tc.ref.Name); err == nil {
				t.Error("a failing backend command must surface as an error")
			}
		})
	}
}

// KubeVirt ties hotplug to the VMI's live-migratable condition, and CanMigrate
// reads the same fact. Both answers come from one VM listing, so one scripted
// listing pins both — including the refusal string an operator sees.
func TestKubevirtAdapter_HotplugAndMigrationFollowLiveMigratable(t *testing.T) {
	ref := types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"}

	for _, tc := range []struct {
		name       string
		migratable bool
	}{
		{"live-migratable", true},
		{"not live-migratable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake()
			fake.AddPrefixResponse("kubectl get vms", vmListJSON("web-1", "corral-vms"), nil)
			fake.AddPrefixResponse("kubectl get vmis", vmiListJSON("web-1", "corral-vms", tc.migratable), nil)
			fake.AddPrefixResponse("kubectl get pods", `{"items":[]}`, nil)
			fake.AddPrefixResponse("kubectl get nodes", nodeListJSON, nil)
			seam(t, "kubevirt", fake)

			adapter, err := For(ref)
			if err != nil {
				t.Fatalf("For(kubevirt): %v", err)
			}
			sizer, ok := adapter.(Sizer)
			if !ok {
				t.Fatal("the kubevirt adapter must implement Sizer")
			}
			if got := sizer.HotplugsLive("web-1"); got != tc.migratable {
				t.Errorf("HotplugsLive = %v, want %v", got, tc.migratable)
			}

			mover, ok := adapter.(Mover)
			if !ok {
				t.Fatal("the kubevirt adapter must implement Mover")
			}
			can, why := mover.CanMigrate("web-1")
			if can != tc.migratable {
				t.Errorf("CanMigrate = %v, want %v", can, tc.migratable)
			}
			if !can && why == "" {
				t.Error("a refusal must carry a reason — the UI shows it")
			}
			if can && why != "" {
				t.Errorf("CanMigrate returned a reason %q while allowing the move", why)
			}
		})
	}
}

// An unknown instance is not a hotplug candidate, and must not be reported as
// one just because the listing came back empty.
func TestKubevirtAdapter_HotplugIsFalseForAnUnknownInstance(t *testing.T) {
	fake := newFake()
	fake.AddPrefixResponse("kubectl get vms", `{"items":[]}`, nil)
	fake.AddPrefixResponse("kubectl get vmis", `{"items":[]}`, nil)
	fake.AddPrefixResponse("kubectl get pods", `{"items":[]}`, nil)
	fake.AddPrefixResponse("kubectl get nodes", nodeListJSON, nil)
	seam(t, "kubevirt", fake)

	adapter, err := For(types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "ghost"})
	if err != nil {
		t.Fatalf("For(kubevirt): %v", err)
	}
	if adapter.(Sizer).HotplugsLive("ghost") {
		t.Error("HotplugsLive must be false for an instance the cluster does not have")
	}
}

// The registry is what every fleet surface goes through. A backend that stops
// being constructible from a bare ref breaks capability probing, which is how
// the UI decides what to even offer.
func TestFor_ConstructsEveryRegisteredBackend(t *testing.T) {
	for _, b := range Backends {
		if !Registered(b) {
			t.Errorf("backend %q is in Backends but has no registered factory", b)
			continue
		}
		adapter, err := For(types.InstanceRef{Backend: b, Name: "x"})
		if err != nil {
			t.Errorf("For(%s) from a bare ref: %v", b, err)
			continue
		}
		if adapter.Backend() != b {
			t.Errorf("For(%s).Backend() = %q", b, adapter.Backend())
		}
	}
}

func TestFor_UnknownBackend(t *testing.T) {
	if _, err := For(types.InstanceRef{Backend: "hyperv", Name: "x"}); err == nil {
		t.Error("For() must reject a backend nothing registered")
	}
}

// The kubevirt factory fills in the default namespace, because a ref carrying
// none is the common case from the CLI.
func TestFor_KubevirtDefaultsTheNamespace(t *testing.T) {
	fake := newFake()
	seam(t, "kubevirt", fake)

	adapter, err := For(types.InstanceRef{Backend: "kubevirt", Name: "web-1"})
	if err != nil {
		t.Fatalf("For(kubevirt): %v", err)
	}
	if err := adapter.(Power).Start("web-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !sawCommand(fake.Calls(), "virtctl start web-1 -n "+kubevirt.DefaultNamespace) {
		t.Errorf("a ref with no namespace should use %q; commands were:%s",
			kubevirt.DefaultNamespace, describeCalls(fake.Calls()))
	}
}

func vmListJSON(name, namespace string) string {
	return `{"items":[{"metadata":{"name":"` + name + `","namespace":"` + namespace + `"},` +
		`"spec":{"template":{"spec":{"domain":{"cpu":{"cores":2},"memory":{"guest":"4Gi"}}}}},` +
		`"status":{"printableStatus":"Running","ready":true}}]}`
}

// A VMI carrying KubeVirt's own LiveMigratable condition, on a node the node
// listing below gives a same-vendor peer for. Both halves are needed: Corral
// reports live migration as viable only when the cluster could actually run
// it, which is the fact HotplugsLive and CanMigrate both read.
func vmiListJSON(name, namespace string, liveMigratable bool) string {
	status := "False"
	if liveMigratable {
		status = "True"
	}
	return `{"items":[{"metadata":{"name":"` + name + `","namespace":"` + namespace + `"},` +
		`"status":{"nodeName":"node-a","interfaces":[{"ipAddress":"10.42.0.9"}],` +
		`"conditions":[{"type":"LiveMigratable","status":"` + status + `"},` +
		`{"type":"AgentConnected","status":"True"}]}}]}`
}

// Two schedulable nodes of the same CPU vendor: a migration target exists.
const nodeListJSON = `{"items":[
	{"metadata":{"name":"node-a","labels":{"kubevirt.io/schedulable":"true","cpu-vendor.node.kubevirt.io/Intel":"true"}}},
	{"metadata":{"name":"node-b","labels":{"kubevirt.io/schedulable":"true","cpu-vendor.node.kubevirt.io/Intel":"true"}}}
]}`

// ── local QEMU ────────────────────────────────────────────────────
//
// qemu drives systemd --user rather than a CLI, so it has its own seam and its
// own case. Its Restart is deliberately a stop+start: the family exists to say
// whether the backend has its own reboot, and systemd restarting the unit is
// that mechanism.

func qemuSeam(t *testing.T) *[]string {
	t.Helper()
	home := t.TempDir()
	units := t.TempDir()
	qemu.SetStateDirs(home, units)
	// Start refuses a VM with no unit file, which is the check that makes
	// "does not exist" a clear error rather than a systemd one.
	if err := os.WriteFile(filepath.Join(units, "corral-vm-1.service"), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatalf("writing unit: %v", err)
	}

	var calls []string
	qemu.SetSystemctl(func(args ...string) ([]byte, error) {
		calls = append(calls, "systemctl "+strings.Join(args, " "))
		return nil, nil
	})
	t.Cleanup(func() {
		qemu.SetStateDirs("", "")
		qemu.SetSystemctl(nil)
	})
	return &calls
}

func TestQemuAdapter_DrivesTheUnit(t *testing.T) {
	ref := types.InstanceRef{Backend: "qemu", Name: "vm-1"}

	t.Run("stop", func(t *testing.T) {
		calls := qemuSeam(t)
		adapter, err := For(ref)
		if err != nil {
			t.Fatalf("For(qemu): %v", err)
		}
		if err := adapter.(Power).Stop("vm-1"); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if !containsCall(*calls, "systemctl stop corral-vm-1") {
			t.Errorf("Stop did not stop the unit; calls were %v", *calls)
		}
	})

	t.Run("restart is a stop then a start", func(t *testing.T) {
		calls := qemuSeam(t)
		adapter, err := For(ref)
		if err != nil {
			t.Fatalf("For(qemu): %v", err)
		}
		if err := adapter.(Restarter).Restart("vm-1"); err != nil {
			t.Fatalf("Restart: %v", err)
		}
		if !containsCall(*calls, "systemctl stop corral-vm-1") {
			t.Errorf("Restart did not stop the unit; calls were %v", *calls)
		}
		if !containsCall(*calls, "systemctl start corral-vm-1") {
			t.Errorf("Restart did not start the unit again; calls were %v", *calls)
		}
	})

	t.Run("start refuses a VM that does not exist", func(t *testing.T) {
		qemuSeam(t)
		adapter, err := For(ref)
		if err != nil {
			t.Fatalf("For(qemu): %v", err)
		}
		err = adapter.(Power).Start("no-such-vm")
		if err == nil {
			t.Fatal("starting a VM with no unit file must fail")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("error = %v, want it to say the VM does not exist", err)
		}
	})
}

// Pause needs the VM's QMP socket. With no socket the operator gets an
// explanation and a way forward, not a dial error.
func TestQemuAdapter_PauseWithoutQMPSocketExplainsItself(t *testing.T) {
	qemuSeam(t)
	adapter, err := For(types.InstanceRef{Backend: "qemu", Name: "vm-1"})
	if err != nil {
		t.Fatalf("For(qemu): %v", err)
	}
	err = adapter.(Suspender).Pause("vm-1")
	if err == nil {
		t.Fatal("pausing a VM with no QMP socket must fail")
	}
	if !strings.Contains(err.Error(), "QMP socket") {
		t.Errorf("error = %v, want it to name the missing QMP socket", err)
	}
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}

// ── Incus addressing ──────────────────────────────────────────────

func TestIncusAdapter_AddressComesFromTheInstanceListing(t *testing.T) {
	fake := newFake()
	fake.AddPrefixResponse("incus list", `[{"name":"ct-1","status":"Running","state":{"network":{"eth0":{"addresses":[{"family":"inet","address":"10.60.0.22","scope":"global"}]}}}}]`, nil)
	seam(t, "incus", fake)

	adapter, err := For(types.InstanceRef{Backend: "incus", Name: "ct-1"})
	if err != nil {
		t.Fatalf("For(incus): %v", err)
	}
	addresser, ok := adapter.(Addresser)
	if !ok {
		t.Fatal("the incus adapter must implement Addresser")
	}
	got, err := addresser.Address("ct-1")
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	if got != "10.60.0.22" {
		t.Errorf("Address = %q, want 10.60.0.22", got)
	}

	// An instance the remote does not have is an error, not an empty string a
	// caller would go on to use as a host.
	if _, err := addresser.Address("ghost"); err == nil {
		t.Error("Address on an unknown instance must fail")
	}
}

// ── Proxmox ───────────────────────────────────────────────────────
//
// Every PVE operation returns a task id, and the adapter's job is to wait for
// it: a fleet surface that fired and forgot would report success for a
// migration that failed thirty seconds later. These tests are about that wait,
// so the fake cluster answers with real UPIDs and a task status the adapter has
// to read before it can return.

// fakePVE is the smallest Proxmox that can answer an adapter: resolve a name to
// a guest, accept a status action, and report how the resulting task ended.
type fakePVE struct {
	server   *httptest.Server
	exit     string // task exitstatus: "OK" or a failure description
	mu       sync.Mutex
	requests []string
}

func newFakePVE(t *testing.T) *fakePVE {
	t.Helper()
	f := &fakePVE{exit: "OK"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+path)
		exit := f.exit
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		var data any
		switch {
		case path == "/version":
			data = map[string]any{"version": "8.3.0"}
		case path == "/cluster/resources":
			data = []map[string]any{{
				"vmid": 100, "name": "web-prod", "node": "pve1", "type": "qemu",
				"status": "running", "maxcpu": 4, "maxmem": 8589934592,
			}}
		case strings.HasSuffix(path, "/status"):
			data = map[string]any{"status": "stopped", "exitstatus": exit, "type": "qmstart", "node": "pve1"}
		case strings.Contains(path, "/tasks/") && strings.HasSuffix(path, "/log"):
			data = []map[string]any{{"n": 1, "t": "task failed"}}
		default:
			data = "UPID:pve1:00001234:0000ABCD:65000000:qmstart:100:corral@pve!ci:"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePVE) saw(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if strings.Contains(r, want) {
			return true
		}
	}
	return false
}

// proxmoxSeam points a context at the fake cluster. SetClientForContext exists
// for exactly this, so no configuration file or token is involved.
func proxmoxSeam(t *testing.T, f *fakePVE) string {
	t.Helper()
	client, err := proxmoxbe.New(proxmoxbe.Config{
		Host:  f.server.URL,
		Token: "corral@pve!ci=00000000-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatalf("proxmoxbe.New: %v", err)
	}
	const context = "test-pve"
	proxmoxbe.SetClientForContext(context, client)
	t.Cleanup(proxmoxbe.ResetClients)
	return context
}

func TestProxmoxAdapter_PowerWaitsForTheTask(t *testing.T) {
	for _, tc := range []struct {
		verb string
		want string
		call func(Adapter) error
	}{
		{"start", "/status/start", func(a Adapter) error { return a.(Power).Start("web-prod") }},
		// Corral's Stop means "ask the guest", which is PVE's shutdown. Its
		// /stop is a power cut, and reaching for that quietly would kill
		// somebody's database mid-write.
		{"stop", "/status/shutdown", func(a Adapter) error { return a.(Power).Stop("web-prod") }},
		{"restart", "/status/reboot", func(a Adapter) error { return a.(Restarter).Restart("web-prod") }},
		{"pause", "/status/suspend", func(a Adapter) error { return a.(Suspender).Pause("web-prod") }},
		{"resume", "/status/resume", func(a Adapter) error { return a.(Suspender).Resume("web-prod") }},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			f := newFakePVE(t)
			context := proxmoxSeam(t, f)

			adapter, err := For(types.InstanceRef{Backend: "proxmox", Context: context, Name: "web-prod"})
			if err != nil {
				t.Fatalf("For(proxmox): %v", err)
			}
			if err := tc.call(adapter); err != nil {
				t.Fatalf("%s: %v", tc.verb, err)
			}
			if !f.saw(tc.want) {
				t.Errorf("%s did not POST %s; requests were %v", tc.verb, tc.want, f.requests)
			}
			if !f.saw("/tasks/") {
				t.Errorf("%s returned without waiting for its task; requests were %v", tc.verb, f.requests)
			}
		})
	}
}

// A task that ends badly must come back as an error. This is the whole reason
// the adapter waits, so it is the case worth pinning hardest.
func TestProxmoxAdapter_FailedTaskIsAnError(t *testing.T) {
	f := newFakePVE(t)
	f.exit = "unable to start VM: no such disk"
	context := proxmoxSeam(t, f)

	adapter, err := For(types.InstanceRef{Backend: "proxmox", Context: context, Name: "web-prod"})
	if err != nil {
		t.Fatalf("For(proxmox): %v", err)
	}
	err = adapter.(Power).Start("web-prod")
	if err == nil {
		t.Fatal("a PVE task that failed must surface as an error, not a silent success")
	}
	if !strings.Contains(err.Error(), "no such disk") {
		t.Errorf("error = %v, want PVE's own exit status in it", err)
	}
}

// An unconfigured context must fail when an operation runs, not when the
// adapter is built: probe() constructs every backend to ask what families it
// implements, and a constructor that dialled would make that a network call.
func TestProxmoxAdapter_UnconfiguredContextFailsLateNotEarly(t *testing.T) {
	proxmoxbe.ResetClients()
	t.Setenv("HOME", t.TempDir())

	adapter, err := For(types.InstanceRef{Backend: "proxmox", Context: "nowhere", Name: "web-prod"})
	if err != nil {
		t.Fatalf("For() on an unconfigured proxmox context must still build an adapter: %v", err)
	}
	if err := adapter.(Power).Start("web-prod"); err == nil {
		t.Error("Start against an unconfigured context must fail")
	}
}

// ── the rest of the KubeVirt families ─────────────────────────────
//
// Storer, Mover, Cloner, Templater, Tagger, Metricser, Eventer. Every one is a
// translation of the contract's vocabulary into a kubectl or virtctl line, and
// a translation is exactly the thing that rots silently: the method still
// compiles, the command it sends is wrong, and an operator finds out when a
// disk does not appear.
func TestKubevirtAdapter_RemainingFamilies(t *testing.T) {
	ref := types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"}

	cases := []struct {
		name string
		want string
		call func(*testing.T, Adapter)
	}{
		{"AddDisk", "virtctl addvolume web-1 --volume-name=", func(t *testing.T, a Adapter) {
			if err := a.(Storer).AddDisk("web-1", "10Gi"); err != nil {
				t.Fatalf("AddDisk: %v", err)
			}
		}},
		{"RemoveDisk", "virtctl removevolume web-1 --volume-name=web-1-hp-1 -n corral-vms", func(t *testing.T, a Adapter) {
			if err := a.(Storer).RemoveDisk("web-1", "web-1-hp-1"); err != nil {
				t.Fatalf("RemoveDisk: %v", err)
			}
		}},
		{"ExpandDisk", "kubectl patch pvc web-1-disk -n corral-vms", func(t *testing.T, a Adapter) {
			if err := a.(Storer).ExpandDisk("web-1", "web-1-disk", "40Gi"); err != nil {
				t.Fatalf("ExpandDisk: %v", err)
			}
		}},
		{"Migrate to a named node", "virtctl migrate web-1 -n corral-vms", func(t *testing.T, a Adapter) {
			if err := a.(Mover).Migrate("web-1", "node-b"); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
		}},
		{"MarkTemplate", "kubectl label vm web-1 -n corral-vms corral.dev/template=true", func(t *testing.T, a Adapter) {
			if err := a.(Templater).MarkTemplate("web-1", true); err != nil {
				t.Fatalf("MarkTemplate: %v", err)
			}
		}},
		{"SetTag", "kubectl label vm web-1 -n corral-vms", func(t *testing.T, a Adapter) {
			if err := a.(Tagger).SetTag("web-1", "prod", true); err != nil {
				t.Fatalf("SetTag: %v", err)
			}
		}},
		{"Events", "kubectl get events -n corral-vms", func(t *testing.T, a Adapter) {
			if _, err := a.(Eventer).Events("web-1"); err != nil {
				t.Fatalf("Events: %v", err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake()
			fake.AddPrefixResponse("kubectl get events", `{"items":[]}`, nil)
			fake.AddPrefixResponse("kubectl get vms", vmListJSON("web-1", "corral-vms"), nil)
			fake.AddPrefixResponse("kubectl get vmis", vmiListJSON("web-1", "corral-vms", true), nil)
			fake.AddPrefixResponse("kubectl get nodes", nodeListJSON, nil)
			fake.AddPrefixResponse("kubectl get storageclass", `{"items":[]}`, nil)
			seam(t, "kubevirt", fake)

			adapter, err := For(ref)
			if err != nil {
				t.Fatalf("For(kubevirt): %v", err)
			}
			tc.call(t, adapter)
			if !sawCommand(fake.Calls(), tc.want) {
				t.Errorf("%s did not run `%s`; commands were:%s", tc.name, tc.want, describeCalls(fake.Calls()))
			}
		})
	}
}

// Removing a tag is a different command from adding one — a trailing "-" is
// how kubectl deletes a label, and getting it wrong silently sets the label to
// the literal string instead.
func TestKubevirtAdapter_ClearingATagRemovesTheLabel(t *testing.T) {
	fake := newFake()
	seam(t, "kubevirt", fake)

	adapter, err := For(types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"})
	if err != nil {
		t.Fatalf("For(kubevirt): %v", err)
	}
	if err := adapter.(Tagger).SetTag("web-1", "prod", false); err != nil {
		t.Fatalf("SetTag: %v", err)
	}
	if sawCommand(fake.Calls(), "=true") {
		t.Errorf("clearing a tag must not set it; commands were:%s", describeCalls(fake.Calls()))
	}
	if !sawCommand(fake.Calls(), "-") {
		t.Errorf("clearing a tag should use kubectl's trailing-dash removal; commands were:%s",
			describeCalls(fake.Calls()))
	}
}

// Migrate with no target node refuses when the VM is not live-migratable,
// rather than asking KubeVirt to attempt it and reporting a cluster error.
func TestKubevirtAdapter_MigrateRefusesWhenNotMigratable(t *testing.T) {
	fake := newFake()
	fake.AddPrefixResponse("kubectl get vms", vmListJSON("web-1", "corral-vms"), nil)
	fake.AddPrefixResponse("kubectl get vmis", vmiListJSON("web-1", "corral-vms", false), nil)
	fake.AddPrefixResponse("kubectl get nodes", nodeListJSON, nil)
	seam(t, "kubevirt", fake)

	adapter, err := For(types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"})
	if err != nil {
		t.Fatalf("For(kubevirt): %v", err)
	}
	err = adapter.(Mover).Migrate("web-1", "")
	if err == nil {
		t.Fatal("an unmigratable VM must be refused before virtctl is asked")
	}
	if sawCommand(fake.Calls(), "virtctl migrate") {
		t.Errorf("refusal should not have reached virtctl; commands were:%s", describeCalls(fake.Calls()))
	}
}

// Metrics is the one family whose adapters differ in shape rather than
// vocabulary: each backend has its own source (KubeVirt's VMI, incus info,
// virsh domstats, systemd properties). What matters at this layer is that the
// call reaches that source and the map comes back.
func TestAdapters_MetricsReachTheBackend(t *testing.T) {
	for _, tc := range []struct {
		backend string
		ref     types.InstanceRef
		respond func(*shell.Fake)
		want    string
	}{
		{
			backend: "incus",
			ref:     types.InstanceRef{Backend: "incus", Name: "ct-1"},
			respond: func(f *shell.Fake) {
				f.AddPrefixResponse("incus query", `{"cpu":{"usage":1000000000},"memory":{"usage":536870912}}`, nil)
			},
			want: "incus query",
		},
		{
			backend: "libvirt",
			ref:     types.InstanceRef{Backend: "libvirt", Name: "dom-1"},
			respond: func(f *shell.Fake) {
				f.AddPrefixResponse("virsh", "Domain: 'dom-1'\n  cpu.time=1000000\n", nil)
			},
			want: "virsh",
		},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			fake := newFake()
			tc.respond(fake)
			seam(t, tc.backend, fake)

			adapter, err := For(tc.ref)
			if err != nil {
				t.Fatalf("For(%s): %v", tc.backend, err)
			}
			metricser, ok := adapter.(Metricser)
			if !ok {
				t.Fatalf("%s adapter must implement Metricser", tc.backend)
			}
			if _, err := metricser.Metrics(tc.ref.Name); err != nil {
				t.Fatalf("Metrics: %v", err)
			}
			if !sawCommand(fake.Calls(), tc.want) {
				t.Errorf("Metrics did not reach %s; commands were:%s", tc.want, describeCalls(fake.Calls()))
			}
		})
	}
}

// Clone's manifest travels on stdin, so the assertion is about what the
// VirtualMachineClone object says: source web-1, target web-2. A clone that
// names the wrong side copies over the VM it was supposed to copy from.
func TestKubevirtAdapter_CloneNamesBothSides(t *testing.T) {
	fake := newFake()
	seam(t, "kubevirt", fake)

	adapter, err := For(types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"})
	if err != nil {
		t.Fatalf("For(kubevirt): %v", err)
	}
	if err := adapter.(Cloner).Clone("web-1", "web-2"); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	var manifest string
	for _, c := range fake.Calls() {
		if c.Stdin != "" {
			manifest = c.Stdin
		}
	}
	if manifest == "" {
		t.Fatalf("Clone sent no manifest; commands were:%s", describeCalls(fake.Calls()))
	}
	for _, want := range []string{"web-1", "web-2"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("clone manifest does not mention %q: %s", want, manifest)
		}
	}
}
