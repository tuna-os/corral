package vdi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Characterization tests for the #226 claim primitive (per-member Lease).
// They pin the observable contract that CT-backed pools (#230) reuse:
// first-writer-wins creation, idempotent re-claim by the holder, and
// resource-versioned takeover of an expired claim only.

const leaseGetArgs = "get lease corral-vdi-devpool-1 -n corral-vms -o json"

func leaseJSON(holder, renew string, rv string) string {
	return `{"metadata":{"name":"corral-vdi-devpool-1","resourceVersion":"` + rv + `"},` +
		`"spec":{"holderIdentity":"` + holder + `","renewTime":"` + renew + `","leaseDurationSeconds":3600}}`
}

func TestAcquireLease_CreateWins(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", nil)

	ok, err := acquireLease("corral-vms", "devpool", "devpool-1", "alice")
	if err != nil || !ok {
		t.Fatalf("acquireLease = %v, %v; want true, nil", ok, err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected a single create call, got %+v", calls)
	}
	var manifest lease
	if err := json.Unmarshal([]byte(calls[0].Stdin), &manifest); err != nil {
		t.Fatalf("lease manifest is not JSON: %v", err)
	}
	if manifest.Metadata.Name != leaseName("devpool-1") || manifest.Spec.HolderIdentity != "alice" {
		t.Errorf("lease manifest = %+v", manifest)
	}
	// The API server parses MicroTime with exactly six fractional digits.
	for _, ts := range []string{manifest.Spec.AcquireTime, manifest.Spec.RenewTime} {
		if _, err := time.Parse(leaseTimeLayout, ts); err != nil {
			t.Errorf("lease time %q is not a MicroTime: %v", ts, err)
		}
	}
	if manifest.Metadata.ResourceVersion != "" {
		t.Errorf("a create must not carry a resourceVersion, got %q", manifest.Metadata.ResourceVersion)
	}
	if manifest.Spec.LeaseDurationSeconds != int32(claimDuration.Seconds()) {
		t.Errorf("lease duration = %d, want %d", manifest.Spec.LeaseDurationSeconds, int32(claimDuration.Seconds()))
	}
	if !strings.Contains(calls[0].Stdin, `"corral.dev/vdi-member":"devpool-1"`) || !strings.Contains(calls[0].Stdin, `"corral.dev/vdi-pool":"devpool"`) {
		t.Errorf("lease manifest is missing pool/member labels: %s", calls[0].Stdin)
	}
}

func TestAcquireLease_HeldByOtherIsNotStolen(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", &fakeErr{"AlreadyExists"})
	fake.AddResponse("kubectl "+leaseGetArgs, leaseJSON("bob", time.Now().UTC().Format(time.RFC3339Nano), "7"), nil)

	ok, err := acquireLease("corral-vms", "devpool", "devpool-1", "alice")
	if err != nil || ok {
		t.Fatalf("acquireLease = %v, %v; want false, nil", ok, err)
	}
	for _, c := range fake.Calls() {
		if c.Args[0] == "replace" {
			t.Fatalf("a live claim held by another identity must not be replaced: %+v", c)
		}
	}
}

func TestAcquireLease_SameHolderIsIdempotent(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", &fakeErr{"AlreadyExists"})
	fake.AddResponse("kubectl "+leaseGetArgs, leaseJSON("alice", time.Now().UTC().Format(time.RFC3339Nano), "7"), nil)

	ok, err := acquireLease("corral-vms", "devpool", "devpool-1", "alice")
	if err != nil || !ok {
		t.Fatalf("acquireLease = %v, %v; want true, nil", ok, err)
	}
	for _, c := range fake.Calls() {
		if c.Args[0] == "replace" {
			t.Fatalf("re-claim by the holder must not rewrite the lease: %+v", c)
		}
	}
}

func TestAcquireLease_ExpiredIsReplacedWithResourceVersion(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", &fakeErr{"AlreadyExists"})
	fake.AddResponse("kubectl "+leaseGetArgs, leaseJSON("bob", "2000-01-01T00:00:00Z", "7"), nil)
	fake.AddResponseKV("kubectl", []string{"replace", "-f", "-"}, "", nil)

	ok, err := acquireLease("corral-vms", "devpool", "devpool-1", "alice")
	if err != nil || !ok {
		t.Fatalf("acquireLease = %v, %v; want true, nil", ok, err)
	}
	var replaced *lease
	for _, c := range fake.Calls() {
		if c.Args[0] == "replace" {
			replaced = &lease{}
			if err := json.Unmarshal([]byte(c.Stdin), replaced); err != nil {
				t.Fatalf("replace manifest is not JSON: %v", err)
			}
		}
	}
	if replaced == nil {
		t.Fatal("expected the expired lease to be replaced")
	}
	if replaced.Metadata.ResourceVersion != "7" || replaced.Spec.HolderIdentity != "alice" {
		t.Errorf("takeover must be a compare-and-swap on resourceVersion 7 for alice, got %+v", replaced)
	}
}

func TestAcquireLease_ReplaceConflictLoses(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", &fakeErr{"AlreadyExists"})
	fake.AddResponse("kubectl "+leaseGetArgs, leaseJSON("bob", "2000-01-01T00:00:00Z", "7"), nil)
	fake.AddResponseKV("kubectl", []string{"replace", "-f", "-"}, "", &fakeErr{"Conflict"})

	ok, err := acquireLease("corral-vms", "devpool", "devpool-1", "alice")
	if err != nil || ok {
		t.Fatalf("acquireLease = %v, %v; want false, nil (lost the race)", ok, err)
	}
}

func TestAcquireLease_UnreadableAfterCreateFailureErrors(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", &fakeErr{"forbidden"})
	fake.AddResponse("kubectl "+leaseGetArgs, "", &fakeErr{"forbidden"})

	if _, err := acquireLease("corral-vms", "devpool", "devpool-1", "alice"); err == nil {
		t.Fatal("expected an error when the lease can neither be created nor read")
	}
}

func TestLeaseExpired(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	mk := func(holder, renew string, secs int32) lease {
		var l lease
		l.Spec.HolderIdentity, l.Spec.RenewTime, l.Spec.LeaseDurationSeconds = holder, renew, secs
		return l
	}
	cases := []struct {
		name string
		l    lease
		want bool
	}{
		{"live", mk("alice", now.Add(-time.Minute).Format(time.RFC3339Nano), 3600), false},
		{"past duration", mk("alice", now.Add(-2*time.Hour).Format(time.RFC3339Nano), 3600), true},
		{"no holder", mk("", now.Format(time.RFC3339Nano), 3600), true},
		{"no renew time", mk("alice", "", 3600), true},
		{"zero duration", mk("alice", now.Format(time.RFC3339Nano), 0), true},
		{"bad renew time", mk("alice", "yesterday", 3600), true},
	}
	for _, tc := range cases {
		if got := leaseExpired(tc.l, now); got != tc.want {
			t.Errorf("%s: leaseExpired = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestClaim_LegacyLabelOnlyAssignmentIsNotStolen(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-n", "corral-vms", "-o", "json", "-l", labelPool + "=devpool"}, `{"items":[
		{"metadata":{"name":"devpool-1","namespace":"corral-vms","labels":{"corral.dev/vdi-pool":"devpool","corral.dev/vdi-assigned-to":"bob"}},"status":{"printableStatus":"Running"}}
	]}`, nil)
	fake.AddResponse("kubectl "+leaseGetArgs, "", &fakeErr{"NotFound"})

	if _, err := Claim("corral-vms", "devpool", "alice"); err == nil {
		t.Fatal("expected no free members: a label-only assignment has no expired claim to recover")
	}
	for _, c := range fake.Calls() {
		if c.Args[0] == "create" || c.Args[0] == "label" {
			t.Fatalf("claim mutated a legacy assignment: %+v", c)
		}
	}
}

func TestClaim_ExpiredLeaseIsRecovered(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-n", "corral-vms", "-o", "json", "-l", labelPool + "=devpool"}, `{"items":[
		{"metadata":{"name":"devpool-1","namespace":"corral-vms","labels":{"corral.dev/vdi-pool":"devpool","corral.dev/vdi-assigned-to":"bob"}},"status":{"printableStatus":"Running"}}
	]}`, nil)
	fake.AddResponse("kubectl "+leaseGetArgs, leaseJSON("bob", "2000-01-01T00:00:00Z", "7"), nil)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", &fakeErr{"AlreadyExists"})
	fake.AddResponseKV("kubectl", []string{"replace", "-f", "-"}, "", nil)
	fake.AddResponseKV("kubectl", []string{"label", "vm", "devpool-1", "-n", "corral-vms", labelPool + "=devpool", labelAssignedTo + "=alice", "--overwrite"}, "", nil)
	fake.AddPrefixResponse("kubectl annotate vm devpool-1 -n corral-vms "+annoClaimedAt+"=", "", nil)

	got, err := Claim("corral-vms", "devpool", "alice")
	if err != nil || got != "devpool-1" {
		t.Fatalf("Claim = %q, %v; want devpool-1", got, err)
	}
}

func TestClaim_RejectsEmptyIdentity(t *testing.T) {
	withFake(t)
	if _, err := Claim("corral-vms", "devpool", "  "); err == nil {
		t.Fatal("expected an error for an empty identity")
	}
}

func TestRelease_OwnerDeletesLeaseClearsLabelsAndStops(t *testing.T) {
	fake := withFake(t)
	fake.AddResponse("kubectl "+leaseGetArgs, leaseJSON("alice", time.Now().UTC().Format(time.RFC3339Nano), "7"), nil)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "devpool-1", "-n", "corral-vms", "--ignore-not-found", "-o", "json"}, vmJSON("devpool-1"), nil)
	fake.AddResponseKV("kubectl", []string{"label", "vm", "devpool-1", "-n", "corral-vms", labelPool + "=devpool", labelAssignedTo + "-", "--overwrite"}, "", nil)
	fake.AddResponseKV("kubectl", []string{"annotate", "vm", "devpool-1", "-n", "corral-vms", annoClaimedAt + "-", annoAssignedTo + "-", "--overwrite"}, "", nil)
	fake.AddPrefixResponse("kubectl delete", "", nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"stop", "devpool-1", "-n", "corral-vms"}, "", nil)

	if err := Release("corral-vms", "devpool-1", "alice"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	var deletedLease, clearedLabel, stopped bool
	for _, c := range fake.Calls() {
		all := strings.Join(c.Args, " ") + " " + c.Stdin
		switch {
		case c.Args[0] == "delete" && strings.Contains(all, leaseName("devpool-1")):
			deletedLease = true
			if !strings.Contains(all, "7") {
				t.Errorf("lease delete must be preconditioned on resourceVersion 7: %s", all)
			}
		case c.Args[0] == "label" && strings.Contains(all, labelAssignedTo+"-"):
			clearedLabel = true
		case strings.Contains(c.Name, "virtctl") && c.Args[0] == "stop":
			stopped = true
		}
	}
	if !deletedLease || !clearedLabel || !stopped {
		t.Errorf("release: deletedLease=%v clearedLabel=%v stopped=%v; want all true", deletedLease, clearedLabel, stopped)
	}
}

func TestRelease_MissingClaimWithIdentityErrors(t *testing.T) {
	fake := withFake(t)
	fake.AddResponse("kubectl "+leaseGetArgs, "", &fakeErr{"NotFound"})
	if err := Release("corral-vms", "devpool-1", "alice"); err == nil {
		t.Fatal("expected an error releasing a member with no claim for a named identity")
	}
}

func TestDeleteLease_PreconditionsOnResourceVersionViaRawAPI(t *testing.T) {
	fake := withFake(t)
	path := "/apis/coordination.k8s.io/v1/namespaces/corral-vms/leases/corral-vdi-devpool-1"
	fake.AddResponseKV("kubectl", []string{"delete", "--raw", path, "-f", "-"}, "", nil)

	if err := deleteLease("corral-vms", "corral-vdi-devpool-1", "7"); err != nil {
		t.Fatalf("deleteLease: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected one raw delete, got %+v", calls)
	}
	var opts struct {
		Kind          string `json:"kind"`
		Preconditions struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"preconditions"`
	}
	if err := json.Unmarshal([]byte(calls[0].Stdin), &opts); err != nil {
		t.Fatalf("DeleteOptions body is not JSON: %v", err)
	}
	if opts.Kind != "DeleteOptions" || opts.Preconditions.ResourceVersion != "7" {
		t.Errorf("DeleteOptions = %+v, want a resourceVersion 7 precondition", opts)
	}
}

func TestDeleteLease_GoneIsDeletedConflictIsError(t *testing.T) {
	fake := withFake(t)
	path := "/apis/coordination.k8s.io/v1/namespaces/corral-vms/leases/"
	fake.AddResponseKV("kubectl", []string{"delete", "--raw", path + "gone", "-f", "-"}, `Error from server (NotFound): leases "gone" not found`, &fakeErr{"exit status 1"})
	fake.AddResponseKV("kubectl", []string{"delete", "--raw", path + "moved", "-f", "-"}, `Error from server (Conflict): precondition failed`, &fakeErr{"exit status 1"})

	if err := deleteLease("corral-vms", "gone", "7"); err != nil {
		t.Errorf("deleting an absent lease must succeed, got %v", err)
	}
	if err := deleteLease("corral-vms", "moved", "7"); err == nil {
		t.Error("a failed precondition must be reported")
	}
}

func TestFindLease_AbsentIsNotAnError(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "corral-vdi-a", "-n", "ns", "--ignore-not-found", "-o", "json"}, "", nil)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "corral-vdi-b", "-n", "ns", "--ignore-not-found", "-o", "json"}, leaseJSON("bob", "2000-01-01T00:00:00Z", "3"), nil)

	if _, found, err := findLease("ns", "corral-vdi-a"); err != nil || found {
		t.Errorf("findLease(absent) = found %v, err %v; want false, nil", found, err)
	}
	l, found, err := findLease("ns", "corral-vdi-b")
	if err != nil || !found || l.Spec.HolderIdentity != "bob" || l.Metadata.ResourceVersion != "3" {
		t.Errorf("findLease(present) = %+v, %v, %v", l, found, err)
	}
}
