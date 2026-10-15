package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/shell"
)

// testRunner is shell.Fake with deniable binaries: Fake.LookPath always
// succeeds, so absence is simulated here. Everything else delegates.
type testRunner struct {
	*shell.Fake
	missing map[string]bool
}

func (t *testRunner) LookPath(name string) (string, error) {
	if t.missing[name] {
		return "", errors.New("executable not found in $PATH")
	}
	return "/fake/bin/" + name, nil
}

func newTestRunner() *testRunner {
	return &testRunner{Fake: shell.NewFake(), missing: map[string]bool{}}
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "no-kubeconfig"))
	t.Setenv("CORRAL_PROXMOX_TOKEN", "")
	oldRunner, oldKVM, oldReach := runner, kvmStat, proxmoxReachable
	t.Cleanup(func() { runner, kvmStat, proxmoxReachable = oldRunner, oldKVM, oldReach })
	kvmStat = func() error { return nil }
	proxmoxReachable = func(host string) error { return errors.New("unreachable in tests: " + host) }
}

func verdict(t *testing.T, r Report, backend string) BackendResult {
	t.Helper()
	got := r.ByBackend(backend)
	if got == nil {
		t.Fatalf("no result for %s", backend)
	}
	return *got
}

func TestScanQemuReady(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.AddResponse("qemu-system-x86_64 --version", "QEMU emulator version 9.1.0", nil)
	SetRunner(f)
	if got := verdict(t, Scan(), "qemu"); got.Verdict != VerdictReady {
		t.Fatalf("qemu = %v (%s)", got.Verdict, got.Detail)
	}
}

func TestScanQemuAbsent(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.missing["qemu-system-x86_64"] = true
	SetRunner(f)
	if got := verdict(t, Scan(), "qemu"); got.Verdict != VerdictAbsent {
		t.Fatalf("qemu = %v, want ABSENT", got.Verdict)
	}
}

func TestScanQemuNoKVM(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.AddResponse("qemu-system-x86_64 --version", "QEMU emulator version 9.1.0", nil)
	SetRunner(f)
	kvmStat = func() error { return errors.New("no such file") }
	got := verdict(t, Scan(), "qemu")
	if got.Verdict != VerdictDegraded {
		t.Fatalf("qemu = %v, want DEGRADED", got.Verdict)
	}
}

func TestScanQemuKVMPermission(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	SetRunner(f)
	kvmStat = func() error { return errors.New("open /dev/kvm: permission denied") }
	got := verdict(t, Scan(), "qemu")
	if got.Verdict != VerdictDegraded {
		t.Fatalf("qemu = %v, want DEGRADED", got.Verdict)
	}
	if !strings.Contains(got.Remedy, "usermod -aG kvm") {
		t.Fatalf("remedy = %q, want usermod kvm guidance", got.Remedy)
	}
}

func TestScanIncusReady(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.AddResponse("incus query local:/1.0", `{"auth":"trusted"}`, nil)
	f.AddResponse("incus info local:", "driver: qemu\ninstance_types: container, virtual-machine", nil)
	SetRunner(f)
	if got := verdict(t, Scan(), "incus"); got.Verdict != VerdictReady {
		t.Fatalf("incus = %v (%s)", got.Verdict, got.Detail)
	}
}

func TestScanIncusNoVMSupport(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.AddResponse("incus query local:/1.0", `{"auth":"trusted"}`, nil)
	f.AddResponse("incus info local:", "driver: dir\ninstance_types: container", nil)
	SetRunner(f)
	if got := verdict(t, Scan(), "incus"); got.Verdict != VerdictDegraded {
		t.Fatalf("incus = %v, want DEGRADED", got.Verdict)
	}
}

func TestScanIncusAbsent(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.missing["incus"] = true
	SetRunner(f)
	if got := verdict(t, Scan(), "incus"); got.Verdict != VerdictAbsent {
		t.Fatalf("incus = %v, want ABSENT", got.Verdict)
	}
}

func TestScanLibvirtReady(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.AddResponse("virsh -c qemu:///system connect", "", nil)
	f.AddResponse("virsh -c qemu:///system nodeinfo", "CPU model: x86_64", nil)
	SetRunner(f)
	if got := verdict(t, Scan(), "libvirt"); got.Verdict != VerdictReady {
		t.Fatalf("libvirt = %v (%s)", got.Verdict, got.Detail)
	}
}

func TestScanLibvirtPermissionRemedy(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.AddResponse("virsh -c qemu:///system connect", "", errors.New("error: failed to connect: permission denied"))
	SetRunner(f)
	got := verdict(t, Scan(), "libvirt")
	if got.Verdict != VerdictDegraded {
		t.Fatalf("libvirt = %v, want DEGRADED", got.Verdict)
	}
	if !strings.Contains(got.Remedy, "usermod -aG libvirt") {
		t.Fatalf("remedy = %q, want usermod libvirt guidance", got.Remedy)
	}
	assertNoSudoInstruction(t, got.Remedy)
}

func TestScanLibvirtProbesConfiguredURIsOnly(t *testing.T) {
	isolate(t)
	if err := config.AddContext(config.ContextConfig{Name: "lab", Backend: "libvirt", Context: "qemu+ssh://lab/system"}); err != nil {
		t.Fatal(err)
	}
	f := newTestRunner()
	f.AddResponse("virsh -c qemu:///system connect", "", errors.New("no daemon"))
	f.AddResponse("virsh -c qemu+ssh://lab/system connect", "", nil)
	f.AddResponse("virsh -c qemu+ssh://lab/system nodeinfo", "CPU model: x86_64", nil)
	SetRunner(f)
	got := verdict(t, Scan(), "libvirt")
	if got.Verdict != VerdictReady || !strings.Contains(got.Detail, "qemu+ssh://lab/system") {
		t.Fatalf("libvirt = %v (%s), want READY via configured URI", got.Verdict, got.Detail)
	}
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if strings.Contains(a, "ssh://") && a != "qemu+ssh://lab/system" {
				t.Fatalf("probed unconfigured host %q", a)
			}
		}
	}
}

func TestScanKubevirtNotConfigured(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	SetRunner(f)
	got := verdict(t, Scan(), "kubevirt")
	if got.Verdict != VerdictNotConfigured {
		t.Fatalf("kubevirt = %v, want NOT_CONFIGURED", got.Verdict)
	}
	for _, c := range f.Calls() {
		if c.Name == "kubectl" {
			t.Fatal("unconfigured kubevirt probe ran kubectl")
		}
	}
}

func TestScanKubevirtReady(t *testing.T) {
	isolate(t)
	kc := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kc, []byte("clusters: []"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kc)
	f := newTestRunner()
	f.AddResponse("kubectl get --raw /livez --request-timeout=3s", "ok", nil)
	SetRunner(f)
	if got := verdict(t, Scan(), "kubevirt"); got.Verdict != VerdictReady {
		t.Fatalf("kubevirt = %v (%s)", got.Verdict, got.Detail)
	}
}

func TestScanProxmoxNeverDialsUnconfigured(t *testing.T) {
	isolate(t)
	called := false
	proxmoxReachable = func(host string) error { called = true; return nil }
	f := newTestRunner()
	SetRunner(f)
	got := verdict(t, Scan(), "proxmox")
	if got.Verdict != VerdictNotConfigured {
		t.Fatalf("proxmox = %v, want NOT_CONFIGURED", got.Verdict)
	}
	if called {
		t.Fatal("scan dialed a PVE host with no proxmox context configured")
	}
}

func TestScanProxmoxReadyWithToken(t *testing.T) {
	isolate(t)
	if err := config.AddContext(config.ContextConfig{Name: "pve", Backend: "proxmox", Context: "pve.example.com"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORRAL_PROXMOX_TOKEN", "root@pam!corral=secret")
	proxmoxReachable = func(host string) error { return nil }
	SetRunner(newTestRunner())
	if got := verdict(t, Scan(), "proxmox"); got.Verdict != VerdictReady {
		t.Fatalf("proxmox = %v (%s)", got.Verdict, got.Detail)
	}
}

func TestScanProxmoxDegradedWithoutToken(t *testing.T) {
	isolate(t)
	if err := config.AddContext(config.ContextConfig{Name: "pve", Backend: "proxmox", Context: "pve.example.com"}); err != nil {
		t.Fatal(err)
	}
	proxmoxReachable = func(host string) error { return nil }
	SetRunner(newTestRunner())
	got := verdict(t, Scan(), "proxmox")
	if got.Verdict != VerdictDegraded {
		t.Fatalf("proxmox = %v, want DEGRADED without token", got.Verdict)
	}
	if !strings.Contains(got.Remedy, "CORRAL_PROXMOX_TOKEN") {
		t.Fatalf("remedy = %q, want token env hint", got.Remedy)
	}
}

func TestScanUnprobedBackendsNotConfigured(t *testing.T) {
	isolate(t)
	SetRunner(newTestRunner())
	r := Scan()
	for _, b := range []string{"virtualbox", "vsphere", "xen", "nutanix"} {
		if got := verdict(t, r, b); got.Verdict != VerdictNotConfigured {
			t.Fatalf("%s = %v, want NOT_CONFIGURED", b, got.Verdict)
		}
	}
}

// assertNoSudoInstruction fails on sudo-as-remedy ("sudo dnf install …")
// while allowing "do not run with sudo" guidance.
func assertNoSudoInstruction(t *testing.T, s string) {
	t.Helper()
	for _, pat := range []string{"sudo dnf", "sudo apt", "sudo apk", "sudo yum", "sudo install", "sudo virsh", "sudo corral", "run with sudo", "run as root"} {
		if strings.Contains(strings.ToLower(s), pat) {
			t.Fatalf("remedy suggests sudo/root: %q", s)
		}
	}
}

func TestScanNeverSuggestsSudo(t *testing.T) {
	isolate(t)
	f := newTestRunner()
	f.missing["qemu-system-x86_64"] = true
	f.missing["incus"] = true
	f.missing["virsh"] = true
	SetRunner(f)
	kvmStat = func() error { return errors.New("permission denied") }
	for _, res := range Scan().Results {
		assertNoSudoInstruction(t, res.Detail+" "+res.Remedy)
	}
}

func TestRankedSingleDefault(t *testing.T) {
	r := Report{Results: []BackendResult{
		{Backend: "qemu", Verdict: VerdictAbsent},
		{Backend: "proxmox", Verdict: VerdictNotConfigured},
		{Backend: "incus", Verdict: VerdictDegraded},
		{Backend: "libvirt", Verdict: VerdictReady},
	}}
	ranked := r.Ranked()
	if ranked[0].Backend != "libvirt" || ranked[1].Backend != "incus" {
		t.Fatalf("ranked order = %v, want READY then DEGRADED first", ranked)
	}
	ready := 0
	for _, res := range ranked {
		if res.Verdict == VerdictReady {
			ready++
		}
	}
	if ready != 1 {
		t.Fatalf("ranked has %d READY, want exactly one default candidate", ready)
	}
}

// The scan, not the wizard, says which backends get a context offer: incus,
// libvirt, and kubevirt do; local QEMU needs no context, a Proxmox host comes
// from flags, and the rest are not probed at all (#214).
func TestScanMarksWhichBackendsAreOffered(t *testing.T) {
	isolate(t)
	SetRunner(newTestRunner())
	r := Scan()
	want := map[string]bool{
		"qemu": false, "incus": true, "libvirt": true, "kubevirt": true, "proxmox": false,
		"virtualbox": false, "vsphere": false, "xen": false, "nutanix": false,
	}
	for b, offer := range want {
		if got := verdict(t, r, b); got.Offerable != offer {
			t.Errorf("%s Offerable = %v, want %v", b, got.Offerable, offer)
		}
	}
}
