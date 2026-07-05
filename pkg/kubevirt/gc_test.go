package kubevirt

import (
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/types"
)

func TestPlanGC_StopsExpiredRunning(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	vms := []types.VM{
		{Name: "expired", Ephemeral: true, Running: true, ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}
	plan := planGC(vms, now, GCDefaultDeleteAfter)
	if plan["expired"] != GCStop {
		t.Errorf("expired running ephemeral VM should be GCStop, got %v", plan["expired"])
	}
}

func TestPlanGC_LeavesNotYetExpiredRunning(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	vms := []types.VM{
		{Name: "fresh", Ephemeral: true, Running: true, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)},
	}
	plan := planGC(vms, now, GCDefaultDeleteAfter)
	if action, planned := plan["fresh"]; planned {
		t.Errorf("not-yet-expired VM should not be planned, got %v", action)
	}
}

func TestPlanGC_IgnoresNonEphemeral(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	vms := []types.VM{
		{Name: "permanent", Ephemeral: false, Running: true, ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}
	plan := planGC(vms, now, GCDefaultDeleteAfter)
	if _, planned := plan["permanent"]; planned {
		t.Error("non-ephemeral VM must never be planned regardless of ExpiresAt")
	}
}

func TestPlanGC_IgnoresInvalidOrMissingExpiresAt(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	vms := []types.VM{
		{Name: "no-expiry", Ephemeral: true, Running: true, ExpiresAt: ""},
		{Name: "garbage-expiry", Ephemeral: true, Running: true, ExpiresAt: "not-a-time"},
	}
	plan := planGC(vms, now, GCDefaultDeleteAfter)
	if len(plan) != 0 {
		t.Errorf("VMs with no/invalid ExpiresAt must be left alone, got plan %v", plan)
	}
}

func TestPlanGC_DeletesPastGracePeriod(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	vms := []types.VM{
		{Name: "long-stopped", Ephemeral: true, Running: false, StoppedAt: now.Add(-73 * time.Hour).Format(time.RFC3339)},
	}
	plan := planGC(vms, now, GCDefaultDeleteAfter)
	if plan["long-stopped"] != GCDelete {
		t.Errorf("stopped VM past the grace period should be GCDelete, got %v", plan["long-stopped"])
	}
}

func TestPlanGC_KeepsRecentlyStopped(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	vms := []types.VM{
		{Name: "recently-stopped", Ephemeral: true, Running: false, StoppedAt: now.Add(-1 * time.Hour).Format(time.RFC3339)},
	}
	plan := planGC(vms, now, GCDefaultDeleteAfter)
	if _, planned := plan["recently-stopped"]; planned {
		t.Error("a VM stopped less than deleteAfter ago must not be deleted yet")
	}
}

func TestPlanGC_NeverDeletesUserStopped(t *testing.T) {
	// StoppedAt empty means gc didn't stop it — the user did (or it was
	// never running to begin with). Must never be auto-deleted: the user
	// might restart it, and gc has no record of when *it* stopped the VM.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	vms := []types.VM{
		{Name: "user-stopped", Ephemeral: true, Running: false, StoppedAt: ""},
	}
	plan := planGC(vms, now, GCDefaultDeleteAfter)
	if _, planned := plan["user-stopped"]; planned {
		t.Error("a VM gc never stopped itself must never be auto-deleted")
	}
}

func TestEphemeralTTL_Default(t *testing.T) {
	if got := ephemeralTTL(""); got != 4*time.Hour {
		t.Errorf("empty TTL should default to 4h, got %v", got)
	}
}

func TestEphemeralTTL_Invalid(t *testing.T) {
	if got := ephemeralTTL("not-a-duration"); got != 4*time.Hour {
		t.Errorf("invalid TTL should fall back to the 4h default, got %v", got)
	}
	if got := ephemeralTTL("-1h"); got != 4*time.Hour {
		t.Errorf("non-positive TTL should fall back to the 4h default, got %v", got)
	}
}

func TestEphemeralTTL_Valid(t *testing.T) {
	if got := ephemeralTTL("2h30m"); got != 2*time.Hour+30*time.Minute {
		t.Errorf("valid TTL should parse as-is, got %v", got)
	}
}
