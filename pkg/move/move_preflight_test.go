package move

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/export"
	"github.com/tuna-os/corral/pkg/types"
)

// The tests below sweep the whole backend matrix through the stubbed seams.
// newStub mirrors reality (kubevirt/qemu/libvirt/incus/proxmox export a disk
// and ingest one; vsphere only exports one; virtualbox exports an OVA
// appliance and xen an XVA archive, neither a disk);
// export.Supported itself is a pure switch, so the backends without an export
// adapter are refused without touching anything.

// TestPreflightNineBackendRefusalTable pins which of the nine backends can be
// a move source or destination, so adding a backend (or an adapter) visibly
// moves its row.
func TestPreflightNineBackendRefusalTable(t *testing.T) {
	if len(backend.Backends) != 9 {
		t.Fatalf("backend.Backends has %d entries, want 9: %v", len(backend.Backends), backend.Backends)
	}
	newStub(t)

	exporters := map[string]bool{"kubevirt": true, "qemu": true, "libvirt": true, "incus": true, "proxmox": true, "vsphere": true}
	receivers := map[string]bool{"kubevirt": true, "qemu": true, "libvirt": true, "incus": true, "proxmox": true}
	// These export, but an artifact that is not a disk image cannot seed a move.
	appliances := map[string]bool{"virtualbox": true, "xen": true, "nutanix": true}

	for _, from := range backend.Backends {
		dst := "qemu"
		if from == "qemu" {
			dst = "libvirt"
		}
		src := sourceVM()
		src.VM.Backend = from
		plan := Preflight(src, to(dst))
		text := refusalText(plan)
		if exporters[from] {
			if strings.Contains(text, "cannot export a disk") || strings.Contains(text, "not a disk image") {
				t.Errorf("%s should be a move source, got:\n%s", from, text)
			}
		} else if appliances[from] {
			mustRefuse(t, plan, "not a disk image")
		} else {
			mustRefuse(t, plan, "cannot export a disk")
		}
	}

	for _, dst := range backend.Backends {
		src := sourceVM() // kubevirt in prod
		target := to(dst)
		if dst == src.VM.Backend {
			// Same backend AND same context is migrate's job, not move's.
			target.Context = src.VM.Context
			mustRefuse(t, Preflight(src, target), "corral migrate")
			continue
		}
		plan := Preflight(src, target)
		if receivers[dst] {
			if !plan.OK() {
				t.Errorf("kubevirt → %s should be accepted, got:\n%s", dst, refusalText(plan))
			}
		} else {
			mustRefuse(t, plan, "cannot receive a moved instance")
		}
	}
}

// TestPreflightUEFIRefusalAcrossDestinations sweeps every ingest-capable
// destination: a UEFI guest landing where there is no firmware path must be
// refused before anything is touched, and allowed where there is one.
func TestPreflightUEFIRefusalAcrossDestinations(t *testing.T) {
	s := newStub(t)

	n := 0
	for _, dst := range backend.Backends {
		if !s.ingestable[dst] {
			continue
		}
		n++
		from := "proxmox"
		if dst == "proxmox" {
			from = "qemu"
		}
		mkSrc := func() Source {
			src := sourceVM()
			src.VM.Backend = from
			src.UEFI = true
			return src
		}

		s.uefiOK[dst] = false
		mustRefuse(t, Preflight(mkSrc(), to(dst)), "boots via UEFI")

		s.uefiOK[dst] = true
		if plan := Preflight(mkSrc(), to(dst)); !plan.OK() {
			t.Errorf("UEFI guest → %s (firmware path present) should be accepted, got:\n%s",
				dst, refusalText(plan))
		}
	}
	if n != 5 {
		t.Fatalf("expected 5 ingest-capable destinations in the stub, saw %d", n)
	}

	// A destination that cannot ingest refuses for that reason, not firmware:
	// the branches are ordered so the operator fixes the capable pair first.
	for _, dst := range backend.Backends {
		if s.ingestable[dst] {
			continue
		}
		src := sourceVM()
		src.UEFI = true
		plan := Preflight(src, to(dst))
		mustRefuse(t, plan, "cannot receive a moved instance")
		if strings.Contains(refusalText(plan), "boots via UEFI") {
			t.Errorf("%s cannot ingest, so the refusal must not also blame firmware:\n%s",
				dst, refusalText(plan))
		}
	}
}

// TestPreflightUnknownFirmwareWarnsInsteadOfAssumingBIOS pins the zero-value
// trap: a backend that cannot answer (Inspect leaves UEFI unset) must warn,
// never refuse as if the guest were BIOS and never pass silently.
func TestPreflightUnknownFirmwareWarnsInsteadOfAssumingBIOS(t *testing.T) {
	s := newStub(t)
	s.uefiOK["qemu"] = false // even a BIOS-only destination must not refuse
	stubInspect(t, backend.GuestInfo{})

	for _, tc := range []struct {
		name string
		src  Source
	}{
		{"via Inspect of an unanswerable backend", Inspect(sourceVM().VM, false)},
		{"hand-built with the zero value", func() Source {
			src := sourceVM()
			src.UEFI = false
			src.OSType = ""
			return src
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := Preflight(tc.src, to("qemu"))
			if !plan.OK() {
				t.Fatalf("unknown firmware must not refuse the move:\n%s", refusalText(plan))
			}
			if strings.Contains(refusalText(plan)+warningText(plan), "UEFI") {
				t.Errorf("nothing known about firmware, so UEFI must not be mentioned at all:\n%s",
					refusalText(plan)+warningText(plan))
			}
			if !strings.Contains(warningText(plan), "did not record a guest OS type") {
				t.Errorf("the unknown OS must warn instead, got:\n%s", warningText(plan))
			}
		})
	}
}

// TestPreflightNonDiskSourcesRefusedTable pins the format gate: only a qcow2
// survives qemu-img convert on the way in, so anything else is refused with
// the reason rather than failing mid-pipeline.
func TestPreflightNonDiskSourcesRefusedTable(t *testing.T) {
	s := newStub(t)

	for _, tc := range []struct {
		name    string
		formats []export.Format
		ok      bool
	}{
		{"qcow2 first", []export.Format{export.Qcow2, export.RawGz}, true},
		{"qcow2 second", []export.Format{export.RawGz, export.Qcow2}, true},
		{"qcow2 only", []export.Format{export.Qcow2}, true},
		{"compressed raw only", []export.Format{export.RawGz}, false},
		{"instance archive only", []export.Format{export.IncusTar}, false},
		{"nothing offered", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.formats["kubevirt"] = tc.formats
			plan := Preflight(sourceVM(), to("qemu"))
			if tc.ok {
				if !plan.OK() {
					t.Fatalf("a qcow2 source should be accepted, got:\n%s", refusalText(plan))
				}
				if plan.Format != export.Qcow2 {
					t.Fatalf("plan.Format = %q, want qcow2", plan.Format)
				}
			} else {
				mustRefuse(t, plan, "not a disk image")
			}
		})
	}
}

// TestPreflightUnparseableSizeWarnsTable pins the space-check fallback: a
// size no parser reads warns and skips the check rather than refusing, and an
// empty size stays silent.
func TestPreflightUnparseableSizeWarnsTable(t *testing.T) {
	newStub(t)
	// The probe must never run for these cases: failing the test if called
	// proves the check was skipped, not merely passed.
	SetFreeSpaceFunc(func(string) (int64, error) {
		t.Helper()
		t.Fatal("free-space probe must not run when the size cannot be parsed")
		return 0, nil
	})

	for _, tc := range []struct {
		name   string
		disk   string
		warn   bool
		pinned bool
	}{
		{"gibberish warns", "lots", true, false},
		{"unit without a number warns", "G", true, false},
		{"blank stays silent", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := sourceVM()
			src.VM.Disk = tc.disk
			plan := Preflight(src, to("qemu"))
			if !plan.OK() {
				t.Fatalf("an unreadable size must not refuse the move:\n%s", refusalText(plan))
			}
			if plan.EstimatedBytes != 0 {
				t.Errorf("EstimatedBytes = %d, want 0 when the size cannot be parsed", plan.EstimatedBytes)
			}
			has := strings.Contains(warningText(plan), "could not read the source disk size")
			if has != tc.warn {
				t.Errorf("size warning present = %v, want %v:\n%s", has, tc.warn, warningText(plan))
			}
		})
	}

	// Control: a readable size still records the estimate and consults space.
	SetFreeSpaceFunc(func(string) (int64, error) { return 1 << 40, nil })
	plan := Preflight(sourceVM(), to("qemu"))
	if plan.EstimatedBytes != 20<<30 {
		t.Errorf("EstimatedBytes = %d, want %d", plan.EstimatedBytes, 20<<30)
	}
	if strings.Contains(warningText(plan), "could not read the source disk size") {
		t.Errorf("a readable size must not warn:\n%s", warningText(plan))
	}
}

// TestExecuteFailureOrdering pins the pipeline's failure semantics: each
// stage's failure leaves every later stage unattempted, the source survives
// anything but an explicit delete, and the scratch artifact never lingers.
func TestExecuteFailureOrdering(t *testing.T) {
	t.Run("stop failure attempts no export", func(t *testing.T) {
		s := newStub(t)
		s.stopErr = errors.New("virtctl: connection refused")
		exportCalled := false
		prev := exportDisk
		exportDisk = func(ctx context.Context, req export.Request, progress export.ProgressFunc) (export.Result, error) {
			exportCalled = true
			return prev(ctx, req, progress)
		}

		src := sourceVM()
		src.VM.Running = true
		target := to("qemu")
		target.Scratch = t.TempDir()
		plan := Preflight(src, target)

		if _, err := Execute(context.Background(), plan, nil); err == nil {
			t.Fatal("expected the stop failure to surface")
		} else if !strings.Contains(err.Error(), "stopping") {
			t.Fatalf("the error should blame the stop, got: %v", err)
		}
		if exportCalled || s.exportedTo != "" {
			t.Error("exporting a running guest's disk would produce a torn image")
		}
		if len(s.ingested) != 0 || len(s.deleted) != 0 {
			t.Errorf("nothing past the stop may run, ingested=%v deleted=%v", s.ingested, s.deleted)
		}
		if _, err := os.Stat(plan.Artifact); !os.IsNotExist(err) {
			t.Errorf("no artifact may exist, stat = %v", err)
		}
	})

	t.Run("export failure attempts no ingest", func(t *testing.T) {
		s := newStub(t)
		s.exportErr = errors.New("qemu-img: read error")
		ingestCalled := false
		prev := ingestDisk
		ingestDisk = func(ref types.InstanceRef, disk string, shape backend.Shape) error {
			ingestCalled = true
			return prev(ref, disk, shape)
		}

		target := to("qemu")
		target.Scratch = t.TempDir()
		plan := Preflight(sourceVM(), target)

		if _, err := Execute(context.Background(), plan, nil); err == nil {
			t.Fatal("expected the export failure to surface")
		} else if !strings.Contains(err.Error(), "exporting") {
			t.Fatalf("the error should blame the export, got: %v", err)
		}
		if ingestCalled || len(s.ingested) != 0 {
			t.Error("ingest must not run without an exported disk")
		}
		if len(s.deleted) != 0 {
			t.Errorf("a failed export must not delete the source, deleted=%v", s.deleted)
		}
		if _, err := os.Stat(plan.Artifact); !os.IsNotExist(err) {
			t.Errorf("no artifact may exist, stat = %v", err)
		}
	})

	t.Run("ingest failure leaves the source intact and cleans scratch", func(t *testing.T) {
		s := newStub(t)
		s.ingestErr = errors.New("no space on the pool")
		target := to("qemu")
		target.Scratch = t.TempDir()

		_, err := Execute(context.Background(), Preflight(sourceVM(), target), nil)
		if err == nil {
			t.Fatal("expected the ingest failure to surface")
		}
		if !strings.Contains(err.Error(), "the source is intact") {
			t.Fatalf("the operator needs to know the source survived, got: %v", err)
		}
		if len(s.deleted) != 0 {
			t.Errorf("a failed ingest must not delete the source, deleted=%v", s.deleted)
		}
		if _, err := os.Stat(s.exportedTo); !os.IsNotExist(err) {
			t.Error("a failed move must not leave gigabytes in scratch")
		}
	})

	t.Run("delete failure still succeeds with a warning", func(t *testing.T) {
		s := newStub(t)
		s.deleteErr = errors.New("kubectl: forbidden")
		target := to("qemu")
		target.Scratch = t.TempDir()
		target.DeleteSource = true

		result, err := Execute(context.Background(), Preflight(sourceVM(), target), nil)
		if err != nil {
			t.Fatalf("the move worked; a leftover source is not a failed move: %v", err)
		}
		if result.SourceDeleted {
			t.Error("SourceDeleted must be false when the delete failed")
		}
		if !strings.Contains(strings.Join(result.Warnings, "\n"), "could not be deleted") {
			t.Fatalf("the leftover source must be reported, got %v", result.Warnings)
		}
		if _, err := os.Stat(s.exportedTo); !os.IsNotExist(err) {
			t.Error("the scratch artifact must be cleaned even when the delete fails")
		}
	})

	t.Run("success cleans scratch", func(t *testing.T) {
		s := newStub(t)
		target := to("qemu")
		target.Scratch = t.TempDir()

		if _, err := Execute(context.Background(), Preflight(sourceVM(), target), nil); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := os.Stat(s.exportedTo); !os.IsNotExist(err) {
			t.Error("a successful move must not leave its artifact behind")
		}
	})
}

// TestExecuteRetainsSourceUnlessDeleteSource pins the default that makes the
// ordering above safe: the source stays unless the plan asked to remove it.
func TestExecuteRetainsSourceUnlessDeleteSource(t *testing.T) {
	s := newStub(t)
	dir := t.TempDir()

	keep := Preflight(sourceVM(), Target{Backend: "qemu", Scratch: dir})
	if keep.DeleteSource {
		t.Fatal("deleting the source must be opt-in: a failed ingest after a delete is unrecoverable")
	}
	result, err := Execute(context.Background(), keep, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.SourceDeleted || len(s.deleted) != 0 {
		t.Errorf("the default move must leave the source alone, got %+v / %v", result, s.deleted)
	}

	drop := Target{Backend: "qemu", Scratch: dir, DeleteSource: true}
	result, err = Execute(context.Background(), Preflight(sourceVM(), drop), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.SourceDeleted || len(s.deleted) != 1 || s.deleted[0] != "web-1" {
		t.Errorf("--delete-source should have removed web-1 once, got %+v / %v", result, s.deleted)
	}
}

// TestMoveSurfacesDroppedConfigAndAddressChange pins that deliberately
// incomplete travel is said out loud: backend-specific losses in Dropped, the
// MAC/IP change in every plan's warnings, and both carried onto the result.
func TestMoveSurfacesDroppedConfigAndAddressChange(t *testing.T) {
	newStub(t)

	for _, tc := range []struct {
		name string
		src  Source
		dst  Target
		want []string
	}{
		{"kubevirt loses its placement", sourceVM(), to("libvirt"),
			[]string{"MAC address and IP", "node placement", "instancetype"}},
		{"proxmox loses its HA wiring", func() Source {
			src := sourceVM()
			src.VM.Backend = "proxmox"
			return src
		}(), to("libvirt"),
			[]string{"PVE HA group"}},
		{"incus loses its profiles", func() Source {
			src := sourceVM()
			src.VM.Backend = "incus"
			return src
		}(), to("libvirt"),
			[]string{"Incus profiles"}},
		{"tags die on qemu", sourceVM(), to("qemu"),
			[]string{"tags"}},
		{"bootc management ends at the move", func() Source {
			src := sourceVM()
			src.VM.Bootc = true
			return src
		}(), to("libvirt"),
			[]string{"bootc image reference"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := Preflight(tc.src, tc.dst)
			if !plan.OK() {
				t.Fatalf("setup should be accepted, got:\n%s", refusalText(plan))
			}
			joined := strings.Join(plan.Dropped, "\n")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("dropped config should mention %q, got:\n%s", want, joined)
				}
			}
			if !strings.Contains(warningText(plan), "new MAC address") {
				t.Errorf("every plan must warn about the address change, got:\n%s", warningText(plan))
			}
		})
	}

	t.Run("result carries both", func(t *testing.T) {
		target := to("qemu")
		target.Scratch = t.TempDir()
		result, err := Execute(context.Background(), Preflight(sourceVM(), target), nil)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !strings.Contains(strings.Join(result.Dropped, "\n"), "MAC address and IP") {
			t.Errorf("the result must carry what was dropped, got %v", result.Dropped)
		}
		if !strings.Contains(strings.Join(result.Warnings, "\n"), "new MAC address") {
			t.Errorf("the result must carry the address warning, got %v", result.Warnings)
		}
	})
}
