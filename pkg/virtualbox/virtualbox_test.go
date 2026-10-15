package virtualbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

const runningInfo = `name="web"
VMState="running"
cpus="4"
memory="4096"
`

const poweredOffInfo = `name="db"
VMState="poweroff"
cpus="2"
memory="2048"
`

func argv(c shell.Call) string { return c.Name + " " + strings.Join(c.Args, " ") }

func TestListRunningAndPoweredOff(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"list", "vms"}, "\"web\" {11111111-2222-3333-4444-555555555555}\n\"db\" {aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee}\n", nil)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "web", "--machinereadable"}, runningInfo, nil)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "db", "--machinereadable"}, poweredOffInfo, nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	vms, err := NewClient("local").List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("len(vms) = %d, want 2", len(vms))
	}

	web := vms[0]
	if web.Name != "web" || web.Backend != "virtualbox" || web.Context != "local" || web.Namespace != "virtualbox" || web.Node != "localhost" {
		t.Fatalf("bad identity: %+v", web)
	}
	if !web.Running || !web.Ready || web.Status != "running" {
		t.Fatalf("bad running state: %+v", web)
	}
	if web.CPU != 4 || web.Mem != "4096 MB" {
		t.Fatalf("bad resources: %+v", web)
	}
	if web.ID == "" {
		t.Fatal("SetIdentity was not applied: empty ID")
	}

	db := vms[1]
	if db.Running || db.Ready {
		t.Fatalf("powered-off VM must not be Running/Ready: %+v", db)
	}
	if db.Status != "poweroff" || db.CPU != 2 || db.Mem != "2048 MB" {
		t.Fatalf("bad powered-off VM: %+v", db)
	}

	want := []string{
		"VBoxManage list vms",
		"VBoxManage showvminfo web --machinereadable",
		"VBoxManage showvminfo db --machinereadable",
	}
	calls := f.Calls()
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i, w := range want {
		if argv(calls[i]) != w {
			t.Fatalf("call %d = %q, want %q", i, argv(calls[i]), w)
		}
	}
}

func TestListEmpty(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"list", "vms"}, "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	vms, err := NewClient("").List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 0 {
		t.Fatalf("len(vms) = %d, want 0", len(vms))
	}
}

func TestListToleratesMalformedLine(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"list", "vms"}, "garbage line\n\"web\" {11111111-2222-3333-4444-555555555555}\n\"unterminated {zzz}\n\n", nil)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "web", "--machinereadable"}, runningInfo, nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	vms, err := NewClient("").List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "web" {
		t.Fatalf("malformed lines must be skipped: %+v", vms)
	}
}

func TestListPropagatesListError(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"list", "vms"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if _, err := NewClient("").List(); err == nil {
		t.Fatal("List must propagate the list vms failure")
	}
}

func TestListToleratesShowvminfoFailure(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"list", "vms"}, "\"ghost\" {11111111-2222-3333-4444-555555555555}\n", nil)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "ghost", "--machinereadable"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	vms, err := NewClient("").List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "ghost" {
		t.Fatalf("VM with failed showvminfo must still be listed: %+v", vms)
	}
}

func TestExists(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "web", "--machinereadable"}, runningInfo, nil)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "ghost", "--machinereadable"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	c := NewClient("")
	if !c.Exists("web") {
		t.Fatal("Exists(web) = false, want true")
	}
	if c.Exists("ghost") {
		t.Fatal("Exists(ghost) = true, want false")
	}
	if got := argv(f.Calls()[0]); got != "VBoxManage showvminfo web --machinereadable" {
		t.Fatalf("Exists argv = %q", got)
	}
}

func TestStartUsesHeadless(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("VBoxManage", "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Start("web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := argv(*f.LastCall()); got != "VBoxManage startvm web --type headless" {
		t.Fatalf("Start argv = %q, want headless startvm", got)
	}
}

func TestStartPropagatesError(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"startvm", "web", "--type", "headless"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Start("web"); !errors.Is(err, errBoom) {
		t.Fatalf("Start err = %v, want errBoom", err)
	}
}

func TestStopGracefulNoFallback(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("VBoxManage", "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Stop("web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 || argv(calls[0]) != "VBoxManage controlvm web acpipowerbutton" {
		t.Fatalf("graceful stop must issue exactly one acpipowerbutton call: %v", calls)
	}
}

// The guest is already off (or has no ACPI driver), so the ACPI request
// fails and the client must fall back to a hard poweroff.
func TestStopFallsBackToPoweroff(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"controlvm", "db", "acpipowerbutton"}, "", errBoom)
	f.AddResponseKV("VBoxManage", []string{"controlvm", "db", "poweroff"}, "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Stop("db"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want acpipowerbutton then poweroff", calls)
	}
	if argv(calls[0]) != "VBoxManage controlvm db acpipowerbutton" {
		t.Fatalf("first call = %q, want acpipowerbutton", argv(calls[0]))
	}
	if argv(calls[1]) != "VBoxManage controlvm db poweroff" {
		t.Fatalf("second call = %q, want poweroff", argv(calls[1]))
	}
}

func TestStopPropagatesErrorWhenBothFail(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"controlvm", "db", "acpipowerbutton"}, "", errBoom)
	f.AddResponseKV("VBoxManage", []string{"controlvm", "db", "poweroff"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Stop("db"); !errors.Is(err, errBoom) {
		t.Fatalf("Stop err = %v, want errBoom", err)
	}
}

func TestDeleteUnregistersWithDelete(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("VBoxManage", "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Delete("web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := argv(*f.LastCall()); got != "VBoxManage unregistervm web --delete" {
		t.Fatalf("Delete argv = %q, want unregister with --delete", got)
	}
}

func TestDeletePropagatesError(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"unregistervm", "web", "--delete"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Delete("web"); !errors.Is(err, errBoom) {
		t.Fatalf("Delete err = %v, want errBoom", err)
	}
}

func TestResetUsesControlvmReset(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("VBoxManage", "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Reset("web"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := argv(*f.LastCall()); got != "VBoxManage controlvm web reset" {
		t.Fatalf("Reset argv = %q, want controlvm reset", got)
	}
}

func TestResetPropagatesError(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"controlvm", "web", "reset"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Reset("web"); !errors.Is(err, errBoom) {
		t.Fatalf("Reset err = %v, want errBoom", err)
	}
}

// Pause must freeze the guest, not power it off. Both stop it executing;
// only one keeps its memory, and confusing them turns a pause into a reboot.
func TestPauseAndResumeUseControlvmPauseAndResume(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("VBoxManage", "", nil)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	c := NewClient("")
	if err := c.Pause("web"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := c.Resume("web"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want pause then resume", calls)
	}
	if argv(calls[0]) != "VBoxManage controlvm web pause" {
		t.Fatalf("first call = %q, want controlvm pause", argv(calls[0]))
	}
	if argv(calls[1]) != "VBoxManage controlvm web resume" {
		t.Fatalf("second call = %q, want controlvm resume", argv(calls[1]))
	}
	for _, call := range calls {
		for _, arg := range call.Args {
			if arg == "poweroff" {
				t.Fatal("pause used controlvm poweroff, which destroys the guest instead of freezing it")
			}
		}
	}
}

func TestPausePropagatesError(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"controlvm", "web", "pause"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Pause("web"); !errors.Is(err, errBoom) {
		t.Fatalf("Pause err = %v, want errBoom", err)
	}
}

func TestResumePropagatesError(t *testing.T) {
	f := shell.NewFake()
	f.AddResponseKV("VBoxManage", []string{"controlvm", "web", "resume"}, "", errBoom)
	SetRunner(f)
	defer SetRunner(shell.Real{})

	if err := NewClient("").Resume("web"); !errors.Is(err, errBoom) {
		t.Fatalf("Resume err = %v, want errBoom", err)
	}
}

var errBoom = errors.New("boom")
