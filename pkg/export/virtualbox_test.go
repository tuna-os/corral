package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/types"
)

// writingRunner stands in for VBoxManage export: it records the call through
// the fake and writes the appliance the real command would.
type writingRunner struct {
	*shell.Fake
	dest string
}

func (w writingRunner) Run(name string, args ...string) ([]byte, error) {
	out, err := w.Fake.Run(name, args...)
	if err == nil && len(args) > 0 && args[0] == "export" {
		err = os.WriteFile(w.dest, []byte("ova"), 0o644)
	}
	return out, err
}

func TestVirtualBox_ExportsAnOVA(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "sub", "web.ova")
	fake := shell.NewFake()
	fake.AddResponse("VBoxManage showvminfo web --machinereadable", "name=\"web\"\nVMState=\"poweroff\"\n", nil)
	fake.AddResponse("VBoxManage export web --output "+dest, "", nil)
	SetRunner(writingRunner{fake, dest})
	t.Cleanup(func() { SetRunner(shell.Real{}) })

	ref := types.InstanceRef{Backend: "virtualbox", Name: "web"}
	result, err := (VirtualBox{}).Export(context.Background(), Request{Ref: ref, Dest: dest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != OVA || result.Consistency != snapshot.Offline || result.Bytes != 3 {
		t.Errorf("result = %+v", result)
	}
	calls := fake.Calls()
	if len(calls) != 2 || strings.Join(calls[1].Args, " ") != "export web --output "+dest {
		t.Errorf("calls = %v", calls)
	}
}

func TestVirtualBox_RefusesARunningVM(t *testing.T) {
	for _, state := range []string{"running", "paused"} {
		fake := withFake(t)
		fake.AddResponse("VBoxManage showvminfo web --machinereadable", "VMState=\""+state+"\"\n", nil)
		ref := types.InstanceRef{Backend: "virtualbox", Name: "web"}
		_, err := (VirtualBox{}).Export(context.Background(), Request{Ref: ref, Dest: filepath.Join(t.TempDir(), "web.ova")}, nil)
		var unsup *Unsupported
		if !errors.As(err, &unsup) || !strings.Contains(err.Error(), "corral stop web") {
			t.Errorf("%s: err = %v, want a typed refusal with the remedy", state, err)
		}
		for _, c := range fake.Calls() {
			if c.Args[0] == "export" {
				t.Errorf("%s: export ran: %v", state, fake.Calls())
			}
		}
	}
}

func TestVirtualBox_OffersOnlyOVA(t *testing.T) {
	if got := Formats("virtualbox"); len(got) != 1 || got[0] != OVA {
		t.Errorf("formats = %v, want [ova]", got)
	}
	ref := types.InstanceRef{Backend: "virtualbox", Name: "web"}
	if _, err := (VirtualBox{}).Export(context.Background(), Request{Ref: ref, Dest: "x", Format: Qcow2}, nil); err == nil {
		t.Error("qcow2 was accepted from VirtualBox")
	}
}
