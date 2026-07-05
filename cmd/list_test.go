package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/types"
)

func TestEphemeralSummary_RunningWithTimeLeft(t *testing.T) {
	vm := types.VM{
		Running:   true,
		ExpiresAt: time.Now().Add(90 * time.Minute).Format(time.RFC3339),
	}
	got := ephemeralSummary(vm)
	if !strings.Contains(got, "left") {
		t.Errorf("expected a %q hint, got %q", "left", got)
	}
}

func TestEphemeralSummary_RunningPastExpiry(t *testing.T) {
	vm := types.VM{
		Running:   true,
		ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}
	if got := ephemeralSummary(vm); !strings.Contains(got, "stop due") {
		t.Errorf("expected a stop-due hint for an expired running VM, got %q", got)
	}
}

func TestEphemeralSummary_InvalidExpiresAt(t *testing.T) {
	vm := types.VM{Running: true, ExpiresAt: "garbage"}
	if got := ephemeralSummary(vm); !strings.Contains(got, "no valid TTL") {
		t.Errorf("expected an invalid-TTL hint, got %q", got)
	}
}

func TestEphemeralSummary_GCStopped(t *testing.T) {
	vm := types.VM{Running: false, StoppedAt: time.Now().Format(time.RFC3339)}
	if got := ephemeralSummary(vm); !strings.Contains(got, "gc-stopped") {
		t.Errorf("expected a gc-stopped hint, got %q", got)
	}
}

func TestEphemeralSummary_UserStopped(t *testing.T) {
	vm := types.VM{Running: false, StoppedAt: ""}
	if got := ephemeralSummary(vm); got != "" {
		t.Errorf("a VM stopped by the user (not gc) should render no hint, got %q", got)
	}
}
