package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

// Finding 3 across the launcher boundary: EnsureVMAdmitted pins the
// admitted definition's identity, and StartAndWaitAdmitted refuses anything
// that changed since — a force-create landing between ensure and start no
// longer starts the replacement while claiming the original manifest.
//
// Old-fails/new-passes direction: this test references new symbols
// (EnsureVMAdmitted, StartAndWaitAdmitted), so it fails on old code by
// construction. Runtime direction note: the same staging under old code's
// plain EnsureVM + StartAndWaitCtx starts (or silently reuses) the
// replacement with no refusal; the committed assertions run through the
// admitted entries.

func TestEnsureStartAdmitted_RefusesReplacementAfterAdmission(t *testing.T) {
	home, units := reuseEnv(t)
	im, vm := validReuseVM(t, home, units, "tuna-v1")
	created, admitted, err := EnsureVMAdmitted(LaunchOpts{Name: "tuna-v1"}, "/img", im)
	if err != nil {
		t.Fatalf("admitted ensure of a matching VM: %v", err)
	}
	if created {
		t.Error("matching VM must reuse, not create")
	}
	if admitted.MetaSHA == "" || admitted.CmdSHA == "" || admitted.UnitSHA == "" || !admitted.UnitPresent {
		t.Fatalf("admission left an incomplete token: %+v", admitted)
	}
	// Another legitimate force-create replaces the VM between ensure and
	// start: different image bytes, still internally coherent.
	vm.meta["source_sha256"] = strings.Repeat("9", 64)
	vm.rewrite(t)
	started, _, err := StartAndWaitAdmitted(context.Background(), "tuna-v1", admitted, time.Second)
	if started {
		t.Error("start over a replaced definition must report started=false")
	}
	if err == nil || !strings.Contains(err.Error(), "changed since admission") {
		t.Errorf("replaced definition must refuse naming the change, got %v", err)
	}
}

func TestEnsureVMAdmitted_BindsManifestAtAdmission(t *testing.T) {
	// Admission runs the manifest binding against the exact bytes the
	// token would digest: a VM that stopped matching the manifest mints
	// no token and fails naming the manifest mismatch. This drives the
	// same bind closure EnsureVMAdmitted passes to AdmitDefinition.
	home, units := reuseEnv(t)
	im, vm := validReuseVM(t, home, units, "tuna-v1")
	vm.meta["source_sha256"] = strings.Repeat("7", 64)
	vm.rewrite(t)
	opts := LaunchOpts{Name: "tuna-v1"}
	tok, err := qemu.AdmitDefinition("tuna-v1", func(meta []byte) error {
		return checkManifestBytes(meta, "tuna-v1", im, opts)
	})
	if err == nil || !strings.Contains(err.Error(), "not admitted") || !strings.Contains(err.Error(), "different image") {
		t.Errorf("manifest mismatch must refuse admission naming it, got %v", err)
	}
	if tok != (qemu.DefinitionToken{}) {
		t.Errorf("failed admission must return no token, got %+v", tok)
	}
}

func TestStartAndWaitAdmitted_CanceledContextDoesNotStartVM(t *testing.T) {
	home, units := reuseEnv(t)
	im, _ := validReuseVM(t, home, units, "tuna-v1")
	_, admitted, err := EnsureVMAdmitted(LaunchOpts{Name: "tuna-v1"}, "/img", im)
	if err != nil {
		t.Fatalf("admit fixture VM: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started, _, err := StartAndWaitAdmitted(ctx, "tuna-v1", admitted, time.Second)
	if started {
		t.Error("canceled admitted launch must not report started")
	}
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled admitted launch must return context cancellation, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "tuna-v1", "qemu.pid")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("canceled admitted launch must not start QEMU or write a pid record; stat error = %v", statErr)
	}
}
