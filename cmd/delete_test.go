package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

const kvDelFlags = " -n corral-vms --ignore-not-found --wait=false -o name"

var kvEntry = types.RegistryEntry{Backend: "kubevirt", Namespace: "corral-vms"}

// deleteFlags sets delete's flags for one test.
func deleteFlags(t *testing.T, wait bool, timeout time.Duration, asJSON bool) {
	t.Helper()
	oldWait, oldTimeout, oldJSON := deleteWait, deleteTimeout, jsonOutput
	deleteWait, deleteTimeout, jsonOutput = wait, timeout, asJSON
	oldPoll := kubevirt.DeletePollInterval
	kubevirt.DeletePollInterval = time.Millisecond
	t.Cleanup(func() {
		deleteWait, deleteTimeout, jsonOutput = oldWait, oldTimeout, oldJSON
		kubevirt.DeletePollInterval = oldPoll
	})
}

func decodeDeletes(t *testing.T, out string) []deleteResult {
	t.Helper()
	var results []deleteResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("delete --json printed %q: %v", out, err)
	}
	return results
}

// Several names delete in one run, and --json prints one result per VM with
// the objects each delete removed.
func TestDelete_SeveralNamesPrintOneJSONResultEach(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", kvEntry)
	if err := registryStore.Set("db", kvEntry); err != nil {
		t.Fatal(err)
	}
	fake.AddResponse("kubectl delete vm web"+kvDelFlags, "virtualmachine.kubevirt.io/web\n", nil)
	fake.AddResponse("kubectl delete pvc web-bootc-disk web-disk web-data web-iso"+kvDelFlags, "persistentvolumeclaim/web-disk\n", nil)
	fake.AddResponse("kubectl delete vm db"+kvDelFlags, "virtualmachine.kubevirt.io/db\n", nil)
	forceDeletes(t)
	deleteFlags(t, false, time.Minute, true)

	var err error
	out := captureStdout(t, func() { err = deleteCmd.RunE(deleteCmd, []string{"web", "db"}) })
	if err != nil {
		t.Fatal(err)
	}
	results := decodeDeletes(t, out)
	if len(results) != 2 {
		t.Fatalf("results = %+v, want one per name", results)
	}
	web, db := results[0], results[1]
	if web.Name != "web" || !web.Deleted || strings.Join(web.Removed, " ") != "virtualmachine.kubevirt.io/web persistentvolumeclaim/web-disk" {
		t.Errorf("web result = %+v", web)
	}
	if db.Name != "db" || !db.Deleted || strings.Join(db.Removed, " ") != "virtualmachine.kubevirt.io/db" || db.Error != "" {
		t.Errorf("db result = %+v", db)
	}
	for _, name := range []string{"web", "db"} {
		if _, ok := registryStore.Get(name); ok {
			t.Errorf("delete left %s in the registry", name)
		}
	}
	if !strings.Contains(out, `"removed": [`) || !strings.Contains(out, `"deleted": true`) {
		t.Errorf("JSON shape changed: %s", out)
	}
}

// One failed delete does not stop the others; the run fails and the JSON
// names the error.
func TestDelete_OneFailureReportsAndContinues(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", kvEntry)
	if err := registryStore.Set("db", kvEntry); err != nil {
		t.Fatal(err)
	}
	fake.AddResponse("kubectl delete vm web"+kvDelFlags, "Error from server (Forbidden): nope", errors.New("exit status 1"))
	forceDeletes(t)
	deleteFlags(t, false, time.Minute, true)

	var err error
	out := captureStdout(t, func() { err = deleteCmd.RunE(deleteCmd, []string{"web", "db"}) })
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("err = %v, want 1 of 2 deletes failed", err)
	}
	results := decodeDeletes(t, out)
	if results[0].Deleted || !strings.Contains(results[0].Error, "Forbidden") {
		t.Errorf("web result = %+v, want the failure", results[0])
	}
	if !results[1].Deleted {
		t.Errorf("db result = %+v, want deleted", results[1])
	}
	if _, ok := registryStore.Get("web"); !ok {
		t.Error("a failed delete removed the registry entry")
	}
}

// --wait polls until what the delete removed is gone.
func TestDelete_WaitConfirmsTheObjectsAreGone(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", kvEntry)
	fake.AddResponse("kubectl delete vm web"+kvDelFlags, "virtualmachine.kubevirt.io/web\n", nil)
	forceDeletes(t)
	deleteFlags(t, true, time.Minute, false)

	out := captureStdout(t, func() {
		if err := deleteCmd.RunE(deleteCmd, []string{"web"}); err != nil {
			t.Fatal(err)
		}
	})
	if !ranCommandLine(fake, "kubectl get virtualmachine.kubevirt.io/web -n corral-vms --ignore-not-found -o name") {
		t.Errorf("--wait did not check the VM; calls %v", fake.Calls())
	}
	if !strings.Contains(out, "Deleted web (1 objects)") {
		t.Errorf("output = %q", out)
	}
}

// --wait fails when an object outlives --timeout.
func TestDelete_WaitFailsWhenObjectsRemain(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", kvEntry)
	fake.AddResponse("kubectl delete pvc web-bootc-disk web-disk web-data web-iso"+kvDelFlags, "persistentvolumeclaim/web-disk\n", nil)
	fake.AddResponse("kubectl get persistentvolumeclaim/web-disk -n corral-vms --ignore-not-found -o name", "persistentvolumeclaim/web-disk\n", nil)
	forceDeletes(t)
	deleteFlags(t, true, 10*time.Millisecond, true)

	var err error
	out := captureStdout(t, func() { err = deleteCmd.RunE(deleteCmd, []string{"web"}) })
	if err == nil || !strings.Contains(err.Error(), "persistentvolumeclaim/web-disk") {
		t.Errorf("err = %v, want the remaining PVC named", err)
	}
	if r := decodeDeletes(t, out)[0]; r.Deleted || !strings.Contains(r.Error, "still present") {
		t.Errorf("result = %+v, want not deleted", r)
	}
}

// A name no backend lists still deletes what an interrupted create left
// under it, through the default context.
func TestDelete_RemovesLeftoversOfAnInterruptedCreate(t *testing.T) {
	fake := kubevirtVMScratch(t, "other", kvEntry)
	t.Setenv("CORRAL_DEFAULT_BACKEND", "kubevirt")
	t.Setenv("CORRAL_KUBE_CONTEXT", "lab")
	fake.AddResponse("kubectl delete vm gone-bootc-builder"+kvDelFlags, "virtualmachine.kubevirt.io/gone-bootc-builder\n", nil)
	fake.AddResponse("kubectl delete pvc gone-bootc-disk gone-disk gone-data gone-iso"+kvDelFlags, "persistentvolumeclaim/gone-bootc-disk\n", nil)
	forceDeletes(t)
	deleteFlags(t, false, time.Minute, true)

	var err error
	out := captureStdout(t, func() { err = deleteCmd.RunE(deleteCmd, []string{"gone"}) })
	if err != nil {
		t.Fatal(err)
	}
	r := decodeDeletes(t, out)[0]
	if !r.Deleted || strings.Join(r.Removed, " ") != "virtualmachine.kubevirt.io/gone-bootc-builder persistentvolumeclaim/gone-bootc-disk" {
		t.Errorf("result = %+v", r)
	}

	// Nothing listed and nothing left behind is still "does not exist".
	deleteFlags(t, false, time.Minute, false)
	if err := deleteCmd.RunE(deleteCmd, []string{"never"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %v, want does not exist", err)
	}
}

// One prompt covers every name, and a "no" deletes nothing.
func TestDelete_PromptCoversEveryNameAndNoDeletesNothing(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", kvEntry)
	if err := registryStore.Set("db", kvEntry); err != nil {
		t.Fatal(err)
	}
	deleteFlags(t, false, time.Minute, false)
	old := forceDelete
	forceDelete = false
	t.Cleanup(func() { forceDelete = old })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("n\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	if err := deleteCmd.RunE(deleteCmd, []string{"web", "db"}); err == nil || err.Error() != "aborted" {
		t.Errorf("err = %v, want aborted", err)
	}
	for _, c := range fake.Calls() {
		if c.Name == "kubectl" && len(c.Args) > 0 && c.Args[0] == "delete" {
			t.Errorf("an aborted delete ran %v", c.Args)
		}
	}
}

func TestDelete_Flags(t *testing.T) {
	for _, flag := range []string{"force", "wait", "timeout"} {
		if deleteCmd.Flags().Lookup(flag) == nil {
			t.Errorf("delete has no --%s", flag)
		}
	}
	if deleteCmd.Args != nil {
		if err := deleteCmd.Args(deleteCmd, []string{"a", "b", "c"}); err != nil {
			t.Errorf("delete refuses several names: %v", err)
		}
	}
}
