package vdi

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// Upgrade path from v0.6.0, the previous release (docs/enterprise-readiness.md).
//
// v0.6.0 kept all VDI state on the VirtualMachine objects: the
// corral.dev/vdi-pool label, the corral.dev/vdi-assigned-to label with the
// user, and the corral.dev/vdi-claimed-at annotation. It had no Leases, no
// assignee annotation, and no ConfigMaps. testdata/v0.6.0/vms.json is the
// `kubectl get vm -o json` view of a pool that v0.6.0 `corral vdi assign`
// wrote: devpool-1 is assigned to alice and runs, devpool-2 is free, and
// devpool-3 is assigned to bob and stopped. scripts/e2e-vdi.sh does the same
// upgrade against a real cluster with the v0.6.0 binary.

const upgradeNS = "corral-vms"

func v060VMs(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/v0.6.0/vms.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// v060Cluster serves the v0.6.0 VMs and no Leases.
func v060Cluster(t *testing.T) func() []string {
	fake := withFake(t)
	vms := v060VMs(t)
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(vms), &list); err != nil {
		t.Fatal(err)
	}
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-A", "-o", "json", "-l", labelPool}, vms, nil)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-n", upgradeNS, "-o", "json", "-l", labelPool}, vms, nil)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-n", upgradeNS, "-o", "json", "-l", labelPool + "=devpool"}, vms, nil)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "-n", upgradeNS, "-l", labelVDIMember, "-o", "json"}, `{"items":[]}`, nil)
	for i, m := range []string{"devpool-1", "devpool-2", "devpool-3"} {
		fake.AddResponseKV("kubectl", []string{"get", "vm", m, "-n", upgradeNS, "--ignore-not-found", "-o", "json"}, string(list.Items[i]), nil)
		fake.AddResponseKV("kubectl", []string{"get", "lease", leaseName(m), "-n", upgradeNS, "-o", "json"}, "", errors.New(`leases.coordination.k8s.io "`+leaseName(m)+`" not found`))
		fake.AddResponseKV("kubectl", []string{"get", "vm", m, "-n", upgradeNS, "-o", `jsonpath={.metadata.labels.corral\.dev/vdi-pool}`}, "devpool", nil)
		fake.AddResponseKV("/fake/bin/virtctl", []string{"start", m, "-n", upgradeNS}, "", nil)
		fake.AddResponseKV("/fake/bin/virtctl", []string{"stop", m, "-n", upgradeNS}, "", nil)
		fake.AddResponseKV("kubectl", []string{"annotate", "vm", m, "-n", upgradeNS, annoClaimedAt + "-", annoAssignedTo + "-", "--overwrite"}, "", nil)
		fake.AddResponseKV("kubectl", []string{"label", "vm", m, "-n", upgradeNS, labelPool + "=devpool", labelAssignedTo + "-", "--overwrite"}, "", nil)
		fake.AddPrefixResponse("kubectl annotate vm "+m+" -n "+upgradeNS+" "+annoClaimedAt+"=", "", nil)
	}
	fake.AddPrefixResponse("kubectl label vm", "", nil)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", nil)
	return func() []string {
		var out []string
		for _, c := range fake.Calls() {
			s := c.Name + " " + strings.Join(c.Args, " ")
			if c.Stdin != "" {
				s += " <<" + c.Stdin
			}
			out = append(out, s)
		}
		return out
	}
}

func TestUpgradeV060_ListPoolsReadsLabelOnlyAssignments(t *testing.T) {
	v060Cluster(t)
	pools, err := ListPools()
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || pools[0].Name != "devpool" || pools[0].Namespace != upgradeNS || len(pools[0].Members) != 3 {
		t.Fatalf("pools = %+v", pools)
	}
	want := []Member{
		{Name: "devpool-1", AssignedTo: "alice", ClaimedAt: "2026-08-05T09:14:02Z", Running: true},
		{Name: "devpool-2"},
		{Name: "devpool-3", AssignedTo: "bob", ClaimedAt: "2026-08-05T10:40:51Z"},
	}
	for i, m := range pools[0].Members {
		if m != want[i] {
			t.Errorf("member %d = %+v, want %+v", i, m, want[i])
		}
	}
}

func TestUpgradeV060_BrokerKeepsLabelOnlyAssignmentsOutOfTheFreeCount(t *testing.T) {
	v060Cluster(t)
	newFakeCT(t)
	pools, err := KubeCluster().Pools(upgradeNS)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || pools[0].ID != "vm/corral-vms/devpool" || pools[0].Mode != PoolModePersistent ||
		pools[0].Members != 3 || pools[0].Claimed != 0 || pools[0].Free != 1 {
		t.Fatalf("pools = %+v, want persistent devpool with 3 members, 0 claimed (no Leases), 1 free", pools)
	}
	claims, err := KubeCluster().Claims(upgradeNS)
	if err != nil || len(claims) != 0 {
		t.Fatalf("claims = %+v, %v; a v0.6.0 assignment has no Lease", claims, err)
	}
}

func TestUpgradeV060_NewClaimDoesNotTakeALabelOnlyAssignment(t *testing.T) {
	calls := v060Cluster(t)
	got, err := Claim(upgradeNS, "devpool", "carol")
	if err != nil || got != "devpool-2" {
		t.Fatalf("Claim = %q, %v; want the free devpool-2", got, err)
	}
	for _, c := range calls() {
		if strings.HasPrefix(c, "kubectl create") && !strings.Contains(c, leaseName("devpool-2")) {
			t.Errorf("claim created a Lease for a member that v0.6.0 assigned: %s", c)
		}
		if strings.HasPrefix(c, "kubectl label vm devpool-1") || strings.HasPrefix(c, "kubectl label vm devpool-3") {
			t.Errorf("claim relabeled a member that v0.6.0 assigned: %s", c)
		}
	}
}

func TestUpgradeV060_HolderClaimAdoptsTheAssignmentAsALease(t *testing.T) {
	calls := v060Cluster(t)
	got, err := Claim(upgradeNS, "devpool", "bob")
	if err != nil || got != "devpool-3" {
		t.Fatalf("Claim = %q, %v; want bob's v0.6.0 desktop devpool-3", got, err)
	}
	var lease, anno, started bool
	for _, c := range calls() {
		switch {
		case strings.HasPrefix(c, "kubectl create"):
			lease = strings.Contains(c, `"name":"`+leaseName("devpool-3")+`"`) && strings.Contains(c, `"holderIdentity":"bob"`)
		case strings.HasPrefix(c, "kubectl annotate vm devpool-3"):
			anno = strings.Contains(c, annoAssignedTo+"=bob")
		case c == "/fake/bin/virtctl start devpool-3 -n "+upgradeNS:
			started = true
		}
	}
	if !lease || !anno || !started {
		t.Fatalf("adoption: lease=%v assignee annotation=%v started=%v\n%s", lease, anno, started, strings.Join(calls(), "\n"))
	}
}

func TestUpgradeV060_AdminUnassignClearsALabelOnlyAssignment(t *testing.T) {
	calls := v060Cluster(t)
	if err := Release(upgradeNS, "devpool-1", "alice"); err == nil || !strings.Contains(err.Error(), "has no claim") {
		t.Fatalf("a user release without a Lease = %v, want a no-claim error", err)
	}
	if err := Unassign(upgradeNS, "devpool-1"); err != nil {
		t.Fatalf("Unassign: %v", err)
	}
	var stopped, label, anno bool
	for _, c := range calls() {
		switch c {
		case "/fake/bin/virtctl stop devpool-1 -n " + upgradeNS:
			stopped = true
		case "kubectl label vm devpool-1 -n " + upgradeNS + " " + labelPool + "=devpool " + labelAssignedTo + "- --overwrite":
			label = true
		case "kubectl annotate vm devpool-1 -n " + upgradeNS + " " + annoClaimedAt + "- " + annoAssignedTo + "- --overwrite":
			anno = true
		}
		if strings.Contains(c, "delete") {
			// An ephemeral member is recreated; a v0.6.0 member has no mode label.
			t.Errorf("unassign of a label-only assignment deleted something: %s", c)
		}
	}
	if !stopped || !label || !anno {
		t.Fatalf("unassign: stopped=%v label=%v annotations=%v", stopped, label, anno)
	}
}
