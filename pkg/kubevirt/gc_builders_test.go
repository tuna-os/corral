package kubevirt

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/shell"
)

// A disk under a bootc build in progress is not an orphan: its builder
// owns it until the final VM exists.
func TestGC_KeepsTheDiskOfABuildInProgress(t *testing.T) {
	r := gcCluster(t, "corral-vms/desk-bootc-disk\ncorral-vms/gone-bootc-disk\n",
		gcVM{ns: "corral-vms", name: "desk-bootc-builder", running: true},
	)
	res, err := GC(false, GCDefaultDeleteAfter)
	if err != nil {
		t.Fatal(err)
	}
	muts := mutations(r)
	if hasMutation(muts, "kubectl delete pvc desk-bootc-disk") {
		t.Errorf("collected the disk a running build installs onto: %v", muts)
	}
	if !hasMutation(muts, "kubectl delete pvc gone-bootc-disk") {
		t.Errorf("did not collect a true orphan: %v", muts)
	}
	if !slices.Equal(res.OrphanedPVCs, []string{"corral-vms/gone-bootc-disk"}) {
		t.Errorf("orphans = %v", res.OrphanedPVCs)
	}
}

func TestPlanOrphanBuilders(t *testing.T) {
	now := time.Now()
	builders := []orphanBuilder{
		{Namespace: "corral-vms", Name: "old", Created: now.Add(-3 * time.Hour)},
		{Namespace: "corral-vms", Name: "young", Created: now.Add(-10 * time.Minute)},
		{Namespace: "corral-vms", Name: "done", Created: now.Add(-3 * time.Hour)},
		{Namespace: "team", Name: "done", Created: now.Add(-3 * time.Hour)},
	}
	vms := map[string]bool{"corral-vms/done": true}
	got := planOrphanBuilders(builders, vms, now, GCDefaultBuilderAge)
	var names []string
	for _, b := range got {
		names = append(names, b.Namespace+"/"+b.Name)
	}
	// old: no VM and old enough. young: may still be building. done: its VM
	// exists in corral-vms, but not in team.
	if want := []string{"corral-vms/old", "team/done"}; !slices.Equal(names, want) {
		t.Errorf("orphans = %v, want %v", names, want)
	}
}

func builderList(t *testing.T, items ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func vmItem(ns, name string, created time.Time, builder bool) map[string]any {
	labels := map[string]string{}
	if builder {
		labels["corral-bootc-builder"] = name
	}
	return map[string]any{"metadata": map[string]any{
		"name": name, "namespace": ns, "labels": labels,
		"creationTimestamp": created.UTC().Format(time.RFC3339),
	}}
}

// GCBuilders purges the final VM's name of an old builder with no VM, and
// in dry-run only reports it.
func TestGCBuilders_PurgesInterruptedBuilds(t *testing.T) {
	old := time.Now().Add(-3 * time.Hour)
	for _, dryRun := range []bool{false, true} {
		_, r := newFakeClient()
		t.Cleanup(func() { SetDefaultRunner(nil); SetPackageRunner(shell.DefaultKubectl) })
		r.AddResponse("kubectl get vm -A -o json", builderList(t,
			vmItem("corral-vms", "lost-bootc-builder", old, true),
			vmItem("corral-vms", "kept-bootc-builder", old, true),
			vmItem("corral-vms", "kept", old, false),
			vmItem("corral-vms", "fake-bootc-builder", old, false), // not corral's builder
		), nil)
		r.AddPrefixResponse("kubectl delete", "", nil)
		r.AddPrefixResponse("kubectl get", "", nil)
		res, err := GCBuilders(dryRun, GCDefaultBuilderAge)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(res.OrphanedBuilders, []string{"corral-vms/lost"}) {
			t.Errorf("dryRun=%v: orphans = %v", dryRun, res.OrphanedBuilders)
		}
		var deletes []string
		for _, m := range mutations(r) {
			if strings.HasPrefix(m, "kubectl delete") {
				deletes = append(deletes, m)
			}
		}
		if dryRun && len(deletes) > 0 {
			t.Errorf("dry run deleted %v", deletes)
		}
		if !dryRun && !slices.Contains(deletes, "kubectl delete vm lost-bootc-builder -n corral-vms --ignore-not-found --wait=false -o name") {
			t.Errorf("did not purge the lost build: %v", deletes)
		}
		for _, d := range deletes {
			if strings.Contains(d, "kept") || strings.Contains(d, "fake-") {
				t.Errorf("touched a builder that is not interrupted: %q", d)
			}
		}
	}
}
