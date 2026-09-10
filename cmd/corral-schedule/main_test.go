package main

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

func withFakeApply(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	kubevirt.SetApplyRunner(fake)
	t.Cleanup(func() { kubevirt.SetApplyRunner(shell.Real{}) })
	return fake
}

func TestValidCron(t *testing.T) {
	if err := validCron("0 9 * * 1-5"); err != nil {
		t.Errorf("valid cron rejected: %v", err)
	}
	for _, bad := range []string{"9am", "0 9 * *", "0 9 * * 1-5 extra"} {
		if err := validCron(bad); err == nil {
			t.Errorf("validCron(%q) should fail", bad)
		}
	}
}

func TestAddWindows_BothBoundaries(t *testing.T) {
	fake := withFakeApply(t)

	if err := addWindows("dev", "tailvm", "0 9 * * 1-5", "0 18 * * 1-5"); err != nil {
		t.Fatalf("addWindows: %v", err)
	}
	// SA + Role + RoleBinding + start CronJob + stop CronJob
	if n := len(fake.Calls()); n != 5 {
		t.Errorf("applied %d manifests, want 5", n)
	}
}

func TestAddWindows_StopOnly(t *testing.T) {
	fake := withFakeApply(t)

	if err := addWindows("dev", "tailvm", "", "0 22 * * *"); err != nil {
		t.Fatalf("addWindows: %v", err)
	}
	if n := len(fake.Calls()); n != 4 { // RBAC ×3 + one CronJob
		t.Errorf("applied %d manifests, want 4", n)
	}
}

func TestJobNames(t *testing.T) {
	if !strings.HasPrefix(startJobName("dev"), "corral-start-") ||
		!strings.HasPrefix(stopJobName("dev"), "corral-stop-") {
		t.Errorf("job names: %q / %q", startJobName("dev"), stopJobName("dev"))
	}
}

// ── reference resolution ──────────────────────────────────────────
//
// A schedule stops and starts somebody's instance on a timer, so the reference
// it stores has to be exactly right and has to be rejected when it is not:
// a malformed one would fire against nothing, silently, forever.

func TestResolveRef(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("default context supplies the backend", func(t *testing.T) {
		ref, err := resolveRef("web-1", "", "corral-vms")
		if err != nil {
			t.Fatalf("resolveRef: %v", err)
		}
		if ref.Name != "web-1" {
			t.Errorf("ref = %+v, want name web-1", ref)
		}
		if ref.Backend == "" {
			t.Error("a reference with no backend cannot be acted on later")
		}
	})

	t.Run("unknown context is refused", func(t *testing.T) {
		_, err := resolveRef("web-1", "no-such-context", "")
		if err == nil {
			t.Fatal("an unknown context must be an error")
		}
		if !strings.Contains(err.Error(), "corral context ls") {
			t.Errorf("error = %v, want it to point at `corral context ls`", err)
		}
	})

	t.Run("an unnamed instance is refused", func(t *testing.T) {
		if _, err := resolveRef("", "", ""); err == nil {
			t.Fatal("a reference with no instance name must not validate")
		}
	})
}

func TestOrDash(t *testing.T) {
	if got := orDash(""); got != "—" {
		t.Errorf("orDash(\"\") = %q, want an em dash so a column never renders empty", got)
	}
	if got := orDash("0 9 * * 1-5"); got != "0 9 * * 1-5" {
		t.Errorf("orDash passed through wrongly: %q", got)
	}
}
