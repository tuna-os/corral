//go:build bootc

package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// This file only builds with -tags bootc (what CI actually runs — see
// Justfile / .github/workflows/ci.yml). Without the tag, kubevirt.BootcAvailable()
// is always false and createBootc returns synchronously — see
// TestHandleBootcCreate_ReturnsTask in features_test.go for that path.
//
// Previously, the equivalent HTTP-level test self-skipped whenever bootc was
// compiled in ("handler spawns goroutine that races with cleanup"), which
// meant the actual build path never ran in CI. createBootc now returns the
// *asyncTask handle directly, so tests can wait() on it instead of racing.

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

func TestBootcRebuild_HTTPRejectsNonDefaultBeforeExternalEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CORRAL_ADMINS", "")
	t.Setenv("CORRAL_AUTH_REQUIRED", "")
	t.Setenv("CORRAL_KUBE_CONTEXT", "actual-default")
	fx := NewTestFixture()
	defer fx.Close()
	if !kubevirt.BootcAvailable() {
		t.Fatal("bootc-tagged test must exercise the actual compiled backend")
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "external-command-executed")
	t.Setenv("TUNA_REBUILD_SIDE_EFFECT", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, command := range []string{"kubectl", "virtctl"} {
		if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\nprintf executed > \"$TUNA_REBUILD_SIDE_EFFECT\"\nexit 17\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// These are real OS child processes, not fake-runner call/argument echoes.
	// The positive control below must execute one; rejected requests must not.
	kubevirt.SetApplyRunner(shell.Real{})
	kubevirt.SetDefaultRunner(shell.Real{})
	kubevirt.SetPackageRunner(shell.Real{})
	defer func() {
		kubevirt.SetApplyRunner(fx.Runner)
		kubevirt.SetDefaultRunner(fx.Runner)
		kubevirt.SetPackageRunner(fx.Runner)
	}()
	ref := types.InstanceRef{Backend: "kubevirt", Context: "actual-default", Namespace: "team", Name: "owned"}
	if err := store.SetRef(ref, types.RegistryEntry{Password: "retained"}); err != nil {
		t.Fatal(err)
	}
	before := store.All()
	for _, query := range []string{"context=other", "context=actual-default&context=other", "context=bad%zz"} {
		for _, body := range []string{`{}`, `{"image":"quay.io/centos-bootc/centos-bootc:stream9"}`} {
			response, err := http.Post(fx.Server.URL+"/api/vms/team/owned/bootc/rebuild?"+query, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != http.StatusBadRequest {
				t.Fatalf("unsupported selector %q: HTTP %d, %q, %v", query, response.StatusCode, data, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("unsupported selector %q executed an external operation: %v", query, err)
			}
			if after := store.All(); !reflect.DeepEqual(before, after) {
				t.Fatalf("unsupported selector %q changed actual registry data: %+v", query, after)
			}
		}
	}
	// The supported default still reaches the real backend's durable image
	// lookup. Its deliberately failing executable prevents any cluster access.
	response, err := http.Post(fx.Server.URL+"/api/vms/team/owned/bootc/rebuild?context="+url.QueryEscape("actual-default"), "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("default image-lookup failure: HTTP %d", response.StatusCode)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "executed" {
		t.Fatalf("default positive control did not execute the real backend lookup: %q, %v", data, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	response, err = http.Post(fx.Server.URL+"/api/vms/team/owned/bootc/rebuild", "application/json",
		strings.NewReader(`{"image":"quay.io/centos-bootc/centos-bootc:stream9"}`))
	if err != nil {
		t.Fatal(err)
	}
	var accepted map[string]string
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("default rebuild no longer starts its backend task: HTTP %d, %+v", response.StatusCode, accepted)
	}
	task, ok := tasks.get(accepted["task"])
	if !ok {
		t.Fatalf("default rebuild returned no actual task: %+v", accepted)
	}
	task.wait()
	if snapshot := task.snapshot(); snapshot["status"] != "error" {
		t.Fatalf("failing real external command was not observed by default rebuild: %+v", snapshot)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "executed" {
		t.Fatalf("default rebuild did not reach the actual external boundary: %q, %v", data, err)
	}
	if after := store.All(); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed default rebuild changed existing credentials: %+v", after)
	}
}
