package export

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/vsphere"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"
)

// convertRunner stands in for qemu-img: it records the call and writes the
// converted image to the last argument, as the real command would.
type convertRunner struct{ *shell.Fake }

func (c convertRunner) Run(name string, args ...string) ([]byte, error) {
	out, err := c.Fake.Run(name, args...)
	if err == nil && len(args) > 0 && args[0] == "convert" {
		err = os.WriteFile(args[len(args)-1], []byte("converted"), 0o644)
	}
	return out, err
}

func TestVSphere_ExportsADiskFromTheSimulator(t *testing.T) {
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Remove)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)
	gov, err := govmomi.NewClient(context.Background(), server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	client, err := vsphere.New(vsphere.Config{Host: "simulator", Username: "user", Password: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	vsphere.SetClientForContext("vc", client.WithVimClient(gov.Client))
	t.Cleanup(vsphere.ResetClients)
	vms, err := client.List()
	if err != nil || len(vms) == 0 {
		t.Fatalf("List: %v %v", vms, err)
	}

	fake := shell.NewFake()
	fake.AddPrefixResponse("/fake/bin/qemu-img convert", "", nil)
	SetRunner(convertRunner{fake})
	t.Cleanup(func() { SetRunner(shell.Real{}) })

	dest := filepath.Join(t.TempDir(), "web.qcow2")
	ref := types.InstanceRef{Backend: "vsphere", Context: "vc", Name: vms[0].Name}
	result, err := (VSphere{}).Export(context.Background(), Request{Ref: ref, Dest: dest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != Qcow2 || result.Consistency != snapshot.Crash {
		t.Errorf("result = %+v, want qcow2, crash for a running VM without tools", result)
	}
	calls := fake.Calls()
	if len(calls) == 0 {
		t.Fatal("qemu-img never ran")
	}
	if got := calls[len(calls)-1].Args; got[0] != "convert" || got[len(got)-2] != dest+".vmdk" || got[len(got)-1] != dest {
		t.Errorf("qemu-img args = %v", got)
	}
	if _, err := os.Stat(dest + ".vmdk"); !os.IsNotExist(err) {
		t.Errorf("the downloaded VMDK was left behind: %v", err)
	}
}
