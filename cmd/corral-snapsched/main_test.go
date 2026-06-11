package main

import (
	"strings"
	"testing"

	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/shell"
)

func TestCronExpr(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"30m", "*/30 * * * *", false},
		{"1h", "0 * * * *", false},
		{"6h", "0 */6 * * *", false},
		{"12h", "0 */12 * * *", false},
		{"24h", "0 3 * * *", false},
		{"15 2 * * 0", "15 2 * * 0", false}, // raw cron passes through
		{"2h", "", true},
		{"whenever", "", true},
	}
	for _, tt := range tests {
		got, err := cronExpr(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("cronExpr(%q) = (%q, %v), want (%q, err=%v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestAddSchedule_AppliesRBACAndCronJob(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	kubevirt.SetApplyRunner(fake)
	t.Cleanup(func() { kubevirt.SetApplyRunner(shell.Real{}) })

	if err := addSchedule("web", "tailvm", "0 3 * * *", 7); err != nil {
		t.Fatalf("addSchedule: %v", err)
	}
	// SA + Role + RoleBinding + CronJob
	if n := len(fake.Calls()); n != 4 {
		t.Errorf("applied %d manifests, want 4", n)
	}
}

func TestAddSchedule_ApplyFails(t *testing.T) {
	fake := shell.NewFake() // no responses → apply errors
	kubevirt.SetApplyRunner(fake)
	t.Cleanup(func() { kubevirt.SetApplyRunner(shell.Real{}) })

	if err := addSchedule("web", "tailvm", "0 3 * * *", 7); err == nil {
		t.Fatal("addSchedule should propagate apply failure")
	}
}

func TestCronJobName(t *testing.T) {
	if got := cronJobName("web"); !strings.HasPrefix(got, "corral-snap-") {
		t.Errorf("cronJobName = %q", got)
	}
}
