package vdi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/shell"
)

// The kubectl side of the CT pool adapter, against a recorded-response fake.

func withCTFake(t *testing.T) *shell.Fake {
	t.Helper()
	fake := withFake(t)
	ct.SetRunner(fake)
	t.Cleanup(func() { ct.SetRunner(shell.DefaultKubectl) })
	return fake
}

func TestKubeCTCluster_PoolDefinitionRoundTrip(t *testing.T) {
	fake := withCTFake(t)
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "", nil)
	spec := deskSpec(2)
	spec.Mode = PoolModeEphemeral
	if err := (kubeCTCluster{}).PutPoolSpec(spec); err != nil {
		t.Fatalf("PutPoolSpec: %v", err)
	}
	var cm ctPoolConfigMap
	if err := json.Unmarshal([]byte(fake.Calls()[0].Stdin), &cm); err != nil {
		t.Fatalf("applied ConfigMap is not JSON: %v", err)
	}
	if cm.Metadata.Name != "corral-vdi-ctpool-desk" || cm.Metadata.Labels[labelCTPool] != "desk" || cm.Data["status"] != "" {
		t.Fatalf("ConfigMap = %+v; the spec apply must not carry (and so reset) status", cm)
	}

	cm.Metadata.Namespace = "vdi"
	cm.Data["status"] = `{"pool":"desk","ready":2}`
	stored, _ := json.Marshal(cm)
	fake.AddResponseKV("kubectl", []string{"get", "configmap", "corral-vdi-ctpool-desk", "-n", "vdi", "--ignore-not-found", "-o", "json"}, string(stored), nil)
	got, status, found, err := (kubeCTCluster{}).GetPool("vdi", "desk")
	if err != nil || !found || got != spec || status.Ready != 2 {
		t.Fatalf("GetPool = %+v, %+v, %v, %v; want %+v", got, status, found, err, spec)
	}
}

func TestKubeCTCluster_GetPoolAbsent(t *testing.T) {
	fake := withCTFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "configmap", "corral-vdi-ctpool-ghost", "-n", "vdi", "--ignore-not-found", "-o", "json"}, "", nil)
	if _, _, found, err := (kubeCTCluster{}).GetPool("vdi", "ghost"); err != nil || found {
		t.Fatalf("GetPool(absent) = found %v, err %v", found, err)
	}
}

func TestKubeCTCluster_PutPoolStatusPatchesOnlyStatus(t *testing.T) {
	fake := withCTFake(t)
	fake.AddPrefixResponse("kubectl patch configmap corral-vdi-ctpool-desk -n vdi --type merge -p", "", nil)
	if err := (kubeCTCluster{}).PutPoolStatus("vdi", "desk", CTPoolStatus{Pool: "desk", Ready: 1}); err != nil {
		t.Fatalf("PutPoolStatus: %v", err)
	}
	args := fake.Calls()[0].Args
	var patch struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(args[len(args)-1]), &patch); err != nil || len(patch.Data) != 1 || !strings.Contains(patch.Data["status"], `"ready":1`) {
		t.Fatalf("patch = %s (%v)", args[len(args)-1], err)
	}
}

func TestKubeCTCluster_ListMembersMergesPVCAndPodState(t *testing.T) {
	fake := withCTFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "pvc", "-n", "vdi", "-l", labelCTPool + "=desk", "-o", "json"}, `{"items":[
		{"metadata":{"name":"desk-1-data","labels":{"corral.dev/vdi-ct-member":"desk-1","corral.dev/vdi-ct-source":"abc"},"annotations":{"corral.dev/vdi-assigned-to":"alice@example.com"}}},
		{"metadata":{"name":"desk-2-data","labels":{"corral.dev/vdi-ct-member":"desk-2","corral.dev/vdi-ct-source":"abc"}}},
		{"metadata":{"name":"desk-3-data","labels":{"corral.dev/vdi-ct-member":"desk-3"},"deletionTimestamp":"2026-10-01T00:00:00Z"}},
		{"metadata":{"name":"stray-data","labels":{}}}
	]}`, nil)
	fake.AddResponseKV("kubectl", []string{"get", "pods", "-n", "vdi", "-l", labelCTPool + "=desk", "-o", "json"}, `{"items":[
		{"metadata":{"name":"desk-1"},"status":{"phase":"Running","containerStatuses":[{"ready":true}]}},
		{"metadata":{"name":"desk-2"},"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":"0/1 nodes: Insufficient cpu"}]}}
	]}`, nil)

	members, err := (kubeCTCluster{}).ListMembers("vdi", "desk")
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("got %d members, want 3 (the unlabeled PVC is not a member): %+v", len(members), members)
	}
	want := map[string]CTMemberState{"desk-1": CTMemberReady, "desk-2": CTMemberUnschedulable, "desk-3": CTMemberTerminating}
	for _, m := range members {
		if m.state() != want[m.Name] {
			t.Errorf("%s state = %s, want %s", m.Name, m.state(), want[m.Name])
		}
	}
	if members[0].AssignedTo != "alice@example.com" || members[0].SourceHash != "abc" {
		t.Errorf("desk-1 = %+v", members[0])
	}
}

func TestKubeCTCluster_CreateMemberLabelsTheCT(t *testing.T) {
	fake := withCTFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "pvc", "desk-1-data", "-n", "vdi", "--ignore-not-found", "-o", "json"}, "", nil)
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "", nil)
	spec := deskSpec(1)
	if err := (kubeCTCluster{}).CreateMember(spec, "desk-1"); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	applied := 0
	for _, c := range fake.Calls() {
		if c.Args[0] != "apply" {
			continue
		}
		applied++
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(c.Stdin), &obj); err != nil {
			t.Fatal(err)
		}
		l := obj.Metadata.Labels
		if l[labelCTPool] != "desk" || l[labelCTMember] != "desk-1" || l[labelCTSource] != spec.Source.Hash() {
			t.Errorf("%s labels = %v", obj.Kind, l)
		}
	}
	if applied != 2 {
		t.Fatalf("applied %d objects, want PVC + pod", applied)
	}
}

func TestKubeCTCluster_CreateMemberRefusesAForeignCT(t *testing.T) {
	fake := withCTFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "pvc", "desk-1-data", "-n", "vdi", "--ignore-not-found", "-o", "json"},
		`{"metadata":{"name":"desk-1-data","labels":{"corral.dev/ct":"true"}}}`, nil)
	if err := (kubeCTCluster{}).CreateMember(deskSpec(1), "desk-1"); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("CreateMember over a foreign CT: %v", err)
	}
	for _, c := range fake.Calls() {
		if c.Args[0] == "apply" {
			t.Fatal("a foreign CT was overwritten")
		}
	}
}

func TestKubeCTCluster_DeleteMemberWaitsForEachObject(t *testing.T) {
	fake := withCTFake(t)
	fake.AddPrefixResponse("kubectl delete", "", nil)
	if err := (kubeCTCluster{}).DeleteMember("vdi", "desk-1"); err != nil {
		t.Fatalf("DeleteMember: %v", err)
	}
	var got []string
	for _, c := range fake.Calls() {
		joined := strings.Join(c.Args, " ")
		if !strings.Contains(joined, "--wait=true") || !strings.Contains(joined, "--ignore-not-found") {
			t.Errorf("delete without wait/ignore-not-found: %s", joined)
		}
		got = append(got, c.Args[1]+"/"+c.Args[2])
	}
	if strings.Join(got, ",") != "pod/desk-1,svc/desk-1-svc,pvc/desk-1-data" {
		t.Fatalf("deleted %v", got)
	}
}

func TestKubeCTCluster_ListClaimsByMember(t *testing.T) {
	fake := withCTFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "-n", "vdi", "-l", labelCTPool + "=desk", "-o", "json"}, `{"items":[
		{"metadata":{"name":"corral-vdi-ct-desk-1","resourceVersion":"4","labels":{"corral.dev/vdi-ct-pool":"desk","corral.dev/vdi-member":"desk-1"}},"spec":{"holderIdentity":"alice"}}
	]}`, nil)
	claims, err := (kubeCTCluster{}).ListClaims("vdi", "desk")
	if err != nil || len(claims) != 1 || claims["desk-1"].Spec.HolderIdentity != "alice" || claims["desk-1"].Metadata.ResourceVersion != "4" {
		t.Fatalf("ListClaims = %+v, %v", claims, err)
	}
}

func TestCTLeaseRef_DoesNotCollideWithVMPoolLeases(t *testing.T) {
	ref := ctLeaseRef("vdi", "desk", "desk-1")
	if ref.Name == leaseName("desk-1") {
		t.Fatalf("CT member lease %q collides with a VM member of the same name", ref.Name)
	}
	if ref.Labels[labelCTPool] != "desk" || ref.Labels[labelVDIMember] != "desk-1" {
		t.Fatalf("lease labels = %v", ref.Labels)
	}
}
