package move

import (
	"context"
	"os"
	"testing"

	"github.com/tuna-os/corral/pkg/export"
	"github.com/tuna-os/corral/pkg/types"
)

func TestExecuteProxmoxToQemu(t *testing.T) {
	s := newStub(t)
	src := Source{
		VM: types.VM{
			Name:      "pve-web",
			Backend:   "proxmox",
			Context:   "pve1",
			Namespace: "pve1",
			CPU:       2,
			Mem:       "4Gi",
			Disk:      "32Gi",
			Running:   true,
		},
		OSType: "linux",
	}
	target := to("qemu")
	target.Scratch = t.TempDir()

	plan := Preflight(src, target)
	if !plan.OK() {
		t.Fatalf("preflight refused proxmox to qemu: %s", refusalText(plan))
	}
	if plan.Format != export.Qcow2 {
		t.Fatalf("format = %v, want %v", plan.Format, export.Qcow2)
	}
	result, err := Execute(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(s.ingested) != 1 || s.ingested[0].Backend != "qemu" {
		t.Fatalf("ingested = %v, want one qemu ingest", s.ingested)
	}
	got := s.shapes[0]
	if got.CPU != 2 || got.Mem != "4Gi" || got.Disk != "32Gi" {
		t.Fatalf("shape = %+v, want CPU 2 / Mem 4Gi / Disk 32Gi", got)
	}
	if len(s.stopped) != 1 || s.stopped[0] != "pve-web" {
		t.Fatalf("stopped = %v, want [pve-web]", s.stopped)
	}
	if !result.SourceStopped {
		t.Fatalf("result = %+v, want SourceStopped", result)
	}
	if _, err := os.Stat(s.exportedTo); !os.IsNotExist(err) {
		t.Fatalf("the scratch artifact should be cleaned up, %s still exists", s.exportedTo)
	}
}

func TestExecuteQemuToKubevirt(t *testing.T) {
	s := newStub(t)
	src := Source{
		VM: types.VM{
			Name:    "local-web",
			Backend: "qemu",
			CPU:     2,
			Mem:     "4Gi",
			Disk:    "20Gi",
			Running: true,
		},
		OSType: "linux",
	}
	target := to("kubevirt")
	target.Scratch = t.TempDir()

	plan := Preflight(src, target)
	if !plan.OK() {
		t.Fatalf("preflight refused qemu to kubevirt: %s", refusalText(plan))
	}
	if plan.Format != export.Qcow2 {
		t.Fatalf("format = %v, want %v", plan.Format, export.Qcow2)
	}
	result, err := Execute(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(s.ingested) != 1 || s.ingested[0].Backend != "kubevirt" {
		t.Fatalf("ingested = %v, want one kubevirt ingest", s.ingested)
	}
	got := s.shapes[0]
	if got.CPU != 2 || got.Mem != "4Gi" || got.Disk != "20Gi" {
		t.Fatalf("shape = %+v, want CPU 2 / Mem 4Gi / Disk 20Gi", got)
	}
	if !result.SourceStopped || result.SourceDeleted {
		t.Fatalf("result = %+v, want stopped and not deleted", result)
	}
}

func TestExecuteQemuToProxmox(t *testing.T) {
	s := newStub(t)
	src := Source{
		VM: types.VM{
			Name:    "local-web",
			Backend: "qemu",
			CPU:     2,
			Mem:     "4Gi",
			Disk:    "20Gi",
			Running: true,
		},
		OSType: "linux",
	}
	target := to("proxmox")
	target.Scratch = t.TempDir()

	plan := Preflight(src, target)
	if !plan.OK() {
		t.Fatalf("preflight refused qemu to proxmox: %s", refusalText(plan))
	}
	if plan.Format != export.Qcow2 {
		t.Fatalf("format = %v, want %v", plan.Format, export.Qcow2)
	}
	if _, err := Execute(context.Background(), plan, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(s.ingested) != 1 || s.ingested[0].Backend != "proxmox" {
		t.Fatalf("ingested = %v, want one proxmox ingest", s.ingested)
	}
	got := s.shapes[0]
	if got.CPU != 2 || got.Mem != "4Gi" || got.Disk != "20Gi" {
		t.Fatalf("shape = %+v, want CPU 2 / Mem 4Gi / Disk 20Gi", got)
	}
}
