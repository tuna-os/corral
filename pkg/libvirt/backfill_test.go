package libvirt

import (
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

func withFake(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	SetRunner(fake)
	t.Cleanup(func() { SetRunner(shell.Real{}) })
	return fake
}

const backfillURI = "qemu:///system"

func TestBackfill_Lifecycle(t *testing.T) {
	fake := withFake(t)
	c := NewClient(backfillURI)

	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "dominfo", "web"}, "Name: web\n", nil)
	if !c.Exists("web") {
		t.Error("Exists should be true when dominfo succeeds")
	}
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "dominfo", "gone"}, "", errNotFound)
	if c.Exists("gone") {
		t.Error("Exists should be false when dominfo fails")
	}

	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "start", "web"}, "", nil)
	if err := c.Start("web"); err != nil {
		t.Errorf("Start: %v", err)
	}
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "start", "bad"}, "error: not found", errNotFound)
	if err := c.Start("bad"); err == nil {
		t.Error("Start should propagate virsh failures")
	}

	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "reboot", "web"}, "", nil)
	if err := c.Restart("web"); err != nil {
		t.Errorf("Restart is an ACPI reboot request, not a destroy: %v", err)
	}

	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "shutdown", "web"}, "", nil)
	if err := c.Stop("web"); err != nil {
		t.Errorf("Stop: %v", err)
	}
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "shutdown", "bad"}, "error", errNotFound)
	if err := c.Stop("bad"); err == nil {
		t.Error("Stop should propagate virsh failures")
	}

	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "dominfo", "web"}, "CPU(s): 2\n", nil)
	if out, err := c.Info("web"); err != nil || !strings.Contains(string(out), "CPU(s)") {
		t.Errorf("Info should return dominfo output: %v %q", err, out)
	}
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "dumpxml", "web"}, "<domain/>", nil)
	if out, err := c.DumpXML("web"); err != nil || string(out) != "<domain/>" {
		t.Errorf("DumpXML should return the domain definition: %v %q", err, out)
	}

	// Delete is destroy-then-undefine: both must be attempted, and an
	// undefine failure must surface.
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "destroy", "web"}, "", nil)
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "undefine", "web", "--remove-all-storage", "--nvram"}, "", nil)
	if err := c.Delete("web"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	names := map[string]bool{}
	for _, call := range fake.Calls() {
		names[strings.Join(call.Args, " ")] = true
	}
	if !names["-c "+backfillURI+" destroy web"] || !names["-c "+backfillURI+" undefine web --remove-all-storage --nvram"] {
		t.Errorf("Delete must destroy then undefine: %v", names)
	}
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "destroy", "stuck"}, "", nil)
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "undefine", "stuck", "--remove-all-storage", "--nvram"},
		"error: storage in use", errNotFound)
	if err := c.Delete("stuck"); err == nil {
		t.Error("Delete should propagate undefine failures")
	}
}

func TestBackfill_SSH_Refusal(t *testing.T) {
	err := NewClient(backfillURI).SSH("web", "uptime")
	if err == nil || !strings.Contains(err.Error(), `"web"`) || !strings.Contains(err.Error(), "ssh directly") {
		t.Fatalf("SSH must refuse with a pointer to plain ssh, got %v", err)
	}
}

func TestBackfill_NewClient(t *testing.T) {
	if got := NewClient("qemu+ssh://lab/system").URI; got != "qemu+ssh://lab/system" {
		t.Errorf("explicit URI must be kept, got %q", got)
	}
	t.Setenv("CORRAL_LIBVIRT_URI", "qemu+ssh://env/system")
	if got := NewClient("").URI; got != "qemu+ssh://env/system" {
		t.Errorf("env should supply the default URI, got %q", got)
	}
}

func TestBackfill_DialVNC_NoDisplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		err  error
		want string
	}{
		{"virsh failure", "", errNotFound, "libvirt display"},
		{"garbage", "not a url at all %%%", nil, "does not advertise"},
		{"wrong scheme", "spice://host:5900", nil, "does not advertise"},
		{"no host", "vnc://", nil, "does not advertise"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := withFake(t)
			fake.AddResponseKV("virsh", []string{"-c", backfillURI, "domdisplay", "web", "--type", "vnc"}, tc.out, tc.err)
			if _, err := NewClient(backfillURI).DialVNC("web"); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// stubDial captures DialVNC's address computation behind the dialDisplay seam.
type stubDial struct {
	ssh  *url.URL
	addr string
}

func withStubDial(t *testing.T) *stubDial {
	t.Helper()
	stub := &stubDial{}
	prev := dialDisplay
	dialDisplay = func(ssh *url.URL, addr string) (io.ReadWriteCloser, error) {
		stub.ssh, stub.addr = ssh, addr
		return nil, nil
	}
	t.Cleanup(func() { dialDisplay = prev })
	return stub
}

func TestBackfill_DialVNC_DirectAddress(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "domdisplay", "web", "--type", "vnc"},
		"vnc://10.0.0.5:5901\n", nil)
	stub := withStubDial(t)

	if _, err := NewClient(backfillURI).DialVNC("web"); err != nil {
		t.Fatal(err)
	}
	if stub.addr != "10.0.0.5:5901" {
		t.Errorf("addr = %q", stub.addr)
	}
	if stub.ssh != nil {
		t.Errorf("local URI must dial directly, ssh = %v", stub.ssh)
	}
}

func TestBackfill_DialVNC_DefaultPort(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "domdisplay", "web", "--type", "vnc"},
		"vnc://10.0.0.5\n", nil)
	stub := withStubDial(t)

	if _, err := NewClient(backfillURI).DialVNC("web"); err != nil {
		t.Fatal(err)
	}
	if stub.addr != "10.0.0.5:5900" {
		t.Errorf("a display without a port is 5900, got %q", stub.addr)
	}
}

// The address matrix: loopback displays on a qemu+ssh URI live on the
// hypervisor and must be carried through ssh -W; anything else dials direct.
func TestBackfill_DialVNC_SSHMatrix(t *testing.T) {
	sshURI := "qemu+ssh://deployer@hypervisor/system"
	for _, tc := range []struct {
		name     string
		uri      string
		display  string
		wantAddr string
		wantSSH  bool
	}{
		{"loopback via ssh", sshURI, "vnc://127.0.0.1:5901", "127.0.0.1:5901", true},
		{"localhost via ssh", sshURI, "vnc://localhost:5900", "localhost:5900", true},
		{"ipv6 loopback via ssh", sshURI, "vnc://[::1]:5902", "[::1]:5902", true},
		{"remote display dials direct", sshURI, "vnc://10.0.0.5:5901", "10.0.0.5:5901", false},
		{"loopback on local URI dials direct", backfillURI, "vnc://127.0.0.1:5901", "127.0.0.1:5901", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := withFake(t)
			fake.AddResponseKV("virsh", []string{"-c", tc.uri, "domdisplay", "web", "--type", "vnc"},
				tc.display+"\n", nil)
			stub := withStubDial(t)

			if _, err := NewClient(tc.uri).DialVNC("web"); err != nil {
				t.Fatal(err)
			}
			if stub.addr != tc.wantAddr {
				t.Errorf("addr = %q, want %q", stub.addr, tc.wantAddr)
			}
			if tc.wantSSH {
				if stub.ssh == nil {
					t.Fatal("want the ssh tunnel, got a direct dial")
				}
				if stub.ssh.Hostname() != "hypervisor" {
					t.Errorf("ssh host = %q", stub.ssh.Hostname())
				}
				if u := stub.ssh.User.Username(); u != "deployer" {
					t.Errorf("ssh user = %q", u)
				}
			} else if stub.ssh != nil {
				t.Errorf("want a direct dial, got ssh via %v", stub.ssh)
			}
		})
	}
}

func TestBackfill_DialSSHStream_NoHost(t *testing.T) {
	if _, err := dialSSHStream(nil, "", "127.0.0.1:5901"); err == nil ||
		!strings.Contains(err.Error(), "no host") {
		t.Fatalf("an SSH URI without a host must fail fast, got %v", err)
	}
}

func TestBackfill_MemoryMiB_Matrix(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", "4096"},
		{"4G", "4096"},
		{" 2G ", "2048"},
		{"512M", "512"},
		{"2048", "2048"},
	} {
		if got := memoryMiB(tc.in); got != tc.want {
			t.Errorf("memoryMiB(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBackfill_HumanBytes_Matrix(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{512 * 1024, "512Ki"},
		{2 << 20, "2Mi"},
		{3 << 30, "3.0Gi"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBackfill_Metrics_MemOnlyNoCPU(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("virsh", []string{"-c", backfillURI, "domstats", "web", "--cpu-total", "--balloon", "--raw"},
		"Domain: 'web'\n  balloon.rss=1048576\n", nil)
	cpuSampleIntervalOrig := cpuSampleInterval
	cpuSampleInterval = 0
	defer func() { cpuSampleInterval = cpuSampleIntervalOrig }()

	res, err := NewClient(backfillURI).Metrics("web")
	if err != nil {
		t.Fatal(err)
	}
	if res["mem"] != "1.0Gi" {
		t.Errorf("mem = %q, want resident size not balloon target", res["mem"])
	}
	if res["cpu"] != "" {
		t.Errorf("cpu should be blank without cpu.time, got %q", res["cpu"])
	}
}
