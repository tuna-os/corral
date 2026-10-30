package kubevirt

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/shell"
)

// gcVM is one item of `kubectl get vms -A -o json` as GC sees it.
type gcVM struct {
	ns, name  string
	ephemeral bool
	running   bool
	expiresAt time.Time // zero = no annotation
	stoppedAt time.Time // zero = no annotation (gc never stopped it)
}

func gcVMList(vms ...gcVM) string {
	var items []map[string]any
	for _, v := range vms {
		labels, ann := map[string]string{}, map[string]string{}
		if v.ephemeral {
			labels["corral.dev/ephemeral"] = "true"
		}
		if !v.expiresAt.IsZero() {
			ann["corral.dev/expires-at"] = v.expiresAt.Format(time.RFC3339)
		}
		if !v.stoppedAt.IsZero() {
			ann[stoppedAtAnnotation] = v.stoppedAt.Format(time.RFC3339)
		}
		status := "Stopped"
		if v.running {
			status = "Running"
		}
		items = append(items, map[string]any{
			"metadata": map[string]any{"name": v.name, "namespace": v.ns, "labels": labels, "annotations": ann},
			"spec":     map[string]any{"running": v.running},
			"status":   map[string]any{"printableStatus": status, "ready": v.running},
		})
	}
	b, _ := json.Marshal(map[string]any{"items": items})
	return string(b)
}

// gcCluster wires a fake cluster for GC: the VM list, the managed-PVC list,
// and success for every mutation, which the test then inspects.
func gcCluster(t *testing.T, pvcs string, vms ...gcVM) *shell.Fake {
	t.Helper()
	_, r := newFakeClient()
	t.Cleanup(func() { SetDefaultRunner(nil); SetPackageRunner(shell.DefaultKubectl) })
	r.AddPrefixResponse("kubectl get", `{"items":[]}`, nil)
	r.AddResponseKV("kubectl", []string{"get", "vms", "-A", "-o", "json"}, gcVMList(vms...), nil)
	r.AddPrefixResponse("kubectl get pvc -A -l "+ManagedLabel+"=true", pvcs, nil)
	r.AddPrefixResponse("/fake/bin/virtctl", "", nil)
	r.AddPrefixResponse("kubectl annotate", "", nil)
	r.AddPrefixResponse("kubectl delete", "", nil)
	return r
}

// mutations returns the calls that change the cluster, as "cmd args…".
func mutations(r *shell.Fake) []string {
	var out []string
	for _, c := range r.Calls() {
		line := c.Name + " " + strings.Join(c.Args, " ")
		if strings.HasPrefix(line, "/fake/bin/virtctl ") || strings.HasPrefix(line, "kubectl annotate") || strings.HasPrefix(line, "kubectl delete") {
			out = append(out, line)
		}
	}
	return out
}

func hasMutation(muts []string, prefix string) bool {
	return slices.ContainsFunc(muts, func(m string) bool { return strings.HasPrefix(m, prefix) })
}

// GC lists VMs in every namespace. An expired ephemeral VM must be stopped
// in its own namespace — not a permanent VM of the same name elsewhere.
func TestGC_StopsOnlyTheExpiredVMsNamespace(t *testing.T) {
	now := time.Now()
	r := gcCluster(t, "",
		gcVM{ns: "team-a", name: "web", ephemeral: true, running: true, expiresAt: now.Add(-time.Hour)},
		gcVM{ns: "team-b", name: "web", running: true},
	)
	res, err := GC(false, GCDefaultDeleteAfter)
	if err != nil {
		t.Fatal(err)
	}
	muts := mutations(r)
	if !hasMutation(muts, "/fake/bin/virtctl stop web -n team-a") {
		t.Errorf("did not stop team-a/web: %v", muts)
	}
	if !hasMutation(muts, "kubectl annotate vm web -n team-a "+stoppedAtAnnotation+"=") {
		t.Errorf("did not record gc-stopped-at on team-a/web: %v", muts)
	}
	for _, m := range muts {
		if strings.Contains(m, "-n team-b") {
			t.Errorf("touched the permanent team-b/web: %q", m)
		}
	}
	if len(res.Stopped) != 1 || len(res.Errors) != 0 {
		t.Errorf("result = %+v, want one stop and no errors", res)
	}
}

// Deleting a gc-expired VM must not make a same-named live VM in another
// namespace look dead, or the orphan pass deletes that VM's disk.
func TestGC_DeleteDoesNotOrphanSameNamedVMElsewhere(t *testing.T) {
	now := time.Now()
	r := gcCluster(t, "team-a/web-disk\nteam-b/web-disk\nteam-a/gone-bootc-disk\nteam-a/postgres-data-pv\n",
		gcVM{ns: "team-a", name: "web", ephemeral: true, stoppedAt: now.Add(-100 * time.Hour)},
		gcVM{ns: "team-b", name: "web", running: true},
	)
	res, err := GC(false, GCDefaultDeleteAfter)
	if err != nil {
		t.Fatal(err)
	}
	muts := mutations(r)
	if !hasMutation(muts, "kubectl delete vm web -n team-a") {
		t.Errorf("did not delete team-a/web: %v", muts)
	}
	if hasMutation(muts, "kubectl delete vm web -n team-b") {
		t.Errorf("deleted the live team-b/web: %v", muts)
	}
	if hasMutation(muts, "kubectl delete pvc web-disk -n team-b") {
		t.Fatalf("deleted the live team-b/web's disk as an orphan: %v", muts)
	}
	if !hasMutation(muts, "kubectl delete pvc gone-bootc-disk -n team-a --ignore-not-found") {
		t.Errorf("did not collect the real orphan team-a/gone-bootc-disk: %v", muts)
	}
	if hasMutation(muts, "kubectl delete pvc postgres-data-pv") {
		t.Errorf("deleted a PVC whose name is not a corral disk name: %v", muts)
	}
	if strings.Join(res.Deleted, ",") != "web" || !slices.Contains(res.OrphanedPVCs, "team-a/gone-bootc-disk") {
		t.Errorf("result = %+v", res)
	}
}

func TestGC_DeletedVMsOwnDisksAreCollectedWhenNoNamesake(t *testing.T) {
	now := time.Now()
	r := gcCluster(t, "team-a/scratch-disk\n",
		gcVM{ns: "team-a", name: "scratch", ephemeral: true, stoppedAt: now.Add(-100 * time.Hour)},
	)
	res, err := GC(false, GCDefaultDeleteAfter)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.OrphanedPVCs, "team-a/scratch-disk") {
		t.Errorf("orphans = %v, want the deleted VM's leftover disk", res.OrphanedPVCs)
	}
	if !hasMutation(mutations(r), "kubectl delete pvc scratch-disk -n team-a") {
		t.Error("leftover disk not deleted")
	}
}

func TestGC_DryRunReportsButTouchesNothing(t *testing.T) {
	now := time.Now()
	r := gcCluster(t, "team-a/gone-disk\n",
		gcVM{ns: "team-a", name: "old", ephemeral: true, running: true, expiresAt: now.Add(-time.Minute)},
		gcVM{ns: "team-a", name: "older", ephemeral: true, stoppedAt: now.Add(-100 * time.Hour)},
		gcVM{ns: "team-a", name: "fresh", ephemeral: true, running: true, expiresAt: now.Add(time.Hour)},
		gcVM{ns: "team-a", name: "user-stopped", ephemeral: true},
	)
	res, err := GC(true, GCDefaultDeleteAfter)
	if err != nil {
		t.Fatal(err)
	}
	if muts := mutations(r); len(muts) != 0 {
		t.Fatalf("dry run changed the cluster: %v", muts)
	}
	if strings.Join(res.Stopped, ",") != "old" || strings.Join(res.Deleted, ",") != "older" ||
		strings.Join(res.OrphanedPVCs, ",") != "team-a/gone-disk" {
		t.Errorf("dry-run report = %+v, want stop old, delete older, orphan gone-disk", res)
	}
}

func TestGC_StopFailureIsReportedAndOthersProceed(t *testing.T) {
	now := time.Now()
	r := gcCluster(t, "",
		gcVM{ns: "a", name: "bad", ephemeral: true, running: true, expiresAt: now.Add(-time.Hour)},
		gcVM{ns: "b", name: "good", ephemeral: true, running: true, expiresAt: now.Add(-time.Hour)},
	)
	r.AddPrefixResponse("/fake/bin/virtctl stop bad", "", errors.New("vm is locked"))
	res, err := GC(false, GCDefaultDeleteAfter)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "bad: stop: vm is locked") {
		t.Errorf("errors = %v, want the bad VM's stop failure", res.Errors)
	}
	if strings.Join(res.Stopped, ",") != "good" {
		t.Errorf("stopped = %v, want only good", res.Stopped)
	}
	if hasMutation(mutations(r), "kubectl annotate vm bad") {
		t.Error("marked a VM gc-stopped although the stop failed")
	}
}

func TestGC_ListFailures(t *testing.T) {
	_, r := newFakeClient()
	t.Cleanup(func() { SetDefaultRunner(nil); SetPackageRunner(shell.DefaultKubectl) })
	r.AddPrefixResponse("kubectl get vms", "", errors.New("connection refused"))
	if _, err := GC(false, GCDefaultDeleteAfter); err == nil || !strings.Contains(err.Error(), "listing VMs") {
		t.Errorf("err = %v, want a listing error", err)
	}

	r2 := gcCluster(t, "")
	r2.AddPrefixResponse("kubectl get pvc -A -l "+ManagedLabel+"=true", "", errors.New("forbidden"))
	res, err := GC(false, GCDefaultDeleteAfter)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "listing PVCs") {
		t.Errorf("errors = %v, want the PVC listing failure reported, not fatal", res.Errors)
	}
}

// DeleteVM removes the VM's proxy through the client's runner, which carries
// the client's kube context. It used to shell out on the global context, so a
// VM deleted in one cluster left its proxy behind and deleted same-named
// proxy objects in whatever cluster CORRAL_KUBE_CONTEXT pointed at.
func TestDeleteVM_RemovesProxyThroughClientRunner(t *testing.T) {
	c, r := newFakeClient()
	t.Cleanup(func() { SetDefaultRunner(nil); SetPackageRunner(shell.DefaultKubectl) })
	r.AddPrefixResponse("/fake/bin/virtctl", "", nil)
	r.AddPrefixResponse("kubectl delete", "", nil)
	r.AddPrefixResponse("kubectl get", "", nil)
	if err := c.DeleteVM("web"); err != nil {
		t.Fatal(err)
	}
	muts := mutations(r)
	const flags = " -n tailvm --ignore-not-found --wait=false -o name"
	for _, want := range []string{
		"kubectl delete deployment web-proxy" + flags,
		"kubectl delete service web-proxy web-lan" + flags,
		"kubectl delete serviceaccount corral-web-proxy" + flags,
		"kubectl delete role corral-web-proxy" + flags,
		"kubectl delete rolebinding corral-web-proxy" + flags,
	} {
		if !slices.Contains(muts, want) {
			t.Errorf("DeleteVM did not run %q through the client runner; got %v", want, muts)
		}
	}
}
