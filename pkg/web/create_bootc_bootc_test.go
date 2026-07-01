//go:build bootc

package web

import (
	"errors"
	"testing"
)

// This file only builds with -tags bootc (what CI actually runs — see
// Justfile / .github/workflows/ci.yml). Without the tag, kubevirt.BootcAvailable()
// is always false and createBootc returns synchronously — see
// TestHandleBootcCreate_ReturnsTask in features_test.go for that path.
//
// Previously, the equivalent HTTP-level test self-skipped whenever bootc was
// compiled in ("handler spawns goroutine that races with cleanup"), which
// meant the actual build path never ran in CI. createBootc now returns the
// *buildTask handle directly, so tests can wait() on it instead of racing.

func TestCreateBootc_BuildFailure(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	// Fail the very first `kubectl apply` (the target PVC) so the build fails
	// fast, before touching any of the builder-VM orchestration that would
	// otherwise need faking too.
	fx.Runner.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "", errors.New("apply failed"))

	req := createRequest{
		Name:   "bootc-vm",
		Bootc:  "quay.io/centos-bootc/centos-bootc:stream9",
		SSHKey: "ssh-ed25519 AAAAtest",
	}
	id, task, err := createBootc(req, "tailvm")
	if err != nil {
		t.Fatalf("createBootc: %v (expected the build to start, then fail asynchronously)", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty task id")
	}
	if task == nil {
		t.Fatal("expected a non-nil task handle")
	}

	task.wait() // deterministic — no more racing the goroutine against cleanup

	snap := task.snapshot()
	if snap["status"] != "error" {
		t.Errorf("status = %q, want %q (snapshot: %v)", snap["status"], "error", snap)
	}
	if snap["error"] == "" {
		t.Error("expected a non-empty error message in the task snapshot")
	}
}
