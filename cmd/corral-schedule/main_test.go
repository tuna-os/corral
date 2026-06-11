package main

import (
	"strings"
	"testing"

	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/shell"
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
