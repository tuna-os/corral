package demo

import (
	"encoding/json"
	"strings"
	"testing"
)

// The demo cluster is the fixture the smoke and TUI tiers assume: canned
// kubectl/virtctl output with the shapes real parsers consume. These tests pin
// that fidelity — item counts, labels, statuses, and state transitions —
// through the same Run/RunStdin entry points the backends use.

func mustRun(t *testing.T, d *demoCluster, name string, args ...string) []byte {
	t.Helper()
	out, err := d.Run(name, args...)
	if err != nil {
		t.Fatalf("Run(%s %v): %v", name, args, err)
	}
	return out
}

func vmItems(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("fixture is not a list: %v", err)
	}
	return list.Items
}

func strPath(m map[string]any, keys ...string) (string, bool) {
	var cur any = m
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = obj[k]
		if !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

func TestBackfill_VMListFidelity(t *testing.T) {
	d := newDemoCluster()
	items := vmItems(t, mustRun(t, d, "kubectl", "get", "vms", "-A", "-o", "json"))
	if len(items) != 8 {
		t.Fatalf("fleet should hold 8 VMs, got %d", len(items))
	}
	byName := map[string]map[string]any{}
	for _, it := range items {
		name, _ := strPath(it, "metadata", "name")
		byName[name] = it
	}

	// Labels the tag chips and filters read.
	labels, _ := byName["web-prod"]["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["corral.dev/tag.prod"] != "true" || labels["corral.dev/tag.web"] != "true" {
		t.Errorf("web-prod labels: %v", labels)
	}
	// Ephemeral VMs carry their expiry for the reaper.
	scratch := byName["scratch"]
	labels, _ = scratch["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["corral.dev/ephemeral"] != "true" {
		t.Errorf("scratch labels: %v", labels)
	}
	if exp, ok := strPath(scratch, "metadata", "annotations", "corral.dev/expires-at"); !ok || exp == "" {
		t.Errorf("scratch should carry expires-at")
	}
	// Templates are marked, bootc images advertise their firmware.
	tmpl, _ := byName["golden-ubuntu"]["metadata"].(map[string]any)["labels"].(map[string]any)
	if tmpl["corral.dev/template"] != "true" {
		t.Errorf("golden-ubuntu labels: %v", tmpl)
	}
	if _, ok := byName["builder"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["domain"].(map[string]any)["firmware"]; !ok {
		t.Error("bootc builder should advertise kernelBoot firmware")
	}
	// Status shapes the fleet rows consume.
	if st, _ := strPath(byName["web-prod"], "status", "printableStatus"); st != "Running" {
		t.Errorf("printableStatus = %q", st)
	}
	if ready, _ := byName["web-prod"]["status"].(map[string]any)["ready"].(bool); !ready {
		t.Error("a Running VM should be ready")
	}
	// A scheduled-but-uns started VM keeps its nodeSelector; a stopped one
	// with no node has none.
	if _, ok := byName["iso-install"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["nodeSelector"]; !ok {
		t.Error("iso-install should keep its nodeSelector")
	}
	if _, ok := byName["dev-fedora"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["nodeSelector"]; ok {
		t.Error("stopped dev-fedora has no node to select")
	}
	// Every item carries runStrategy for the lifecycle buttons.
	for _, it := range items {
		if rs, _ := strPath(it, "spec", "runStrategy"); rs != "RerunOnFailure" {
			t.Errorf("%v: runStrategy = %q", it["metadata"], rs)
		}
	}
}

func TestBackfill_SingleVMAndVMI(t *testing.T) {
	d := newDemoCluster()

	out := mustRun(t, d, "kubectl", "get", "vm", "web-prod", "-n", "corral-vms")
	var vm map[string]any
	if err := json.Unmarshal(out, &vm); err != nil {
		t.Fatalf("vm fixture: %v", err)
	}
	if name, _ := strPath(vm, "metadata", "name"); name != "web-prod" {
		t.Errorf("name = %q", name)
	}

	out = mustRun(t, d, "kubectl", "get", "vm", "web-prod", "-n", "corral-vms", "-o", "name")
	if string(out) != "virtualmachine.kubevirt.io/web-prod" {
		t.Errorf("-o name = %q", out)
	}

	if _, err := d.Run("kubectl", "get", "vm", "nope", "-n", "corral-vms"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Errorf("missing VM should 404, got %v", err)
	}

	out = mustRun(t, d, "kubectl", "get", "vmi", "web-prod", "-n", "corral-vms")
	var vmi map[string]any
	if err := json.Unmarshal(out, &vmi); err != nil {
		t.Fatalf("vmi fixture: %v", err)
	}
	if !strings.Contains(string(out), `"ipAddress":"10.42.1.20"`) {
		t.Errorf("vmi should expose the guest IP: %s", out)
	}
	if _, err := d.Run("kubectl", "get", "vmi", "nope", "-n", "corral-vms"); err == nil {
		t.Error("missing VMI should 404")
	}
}

func TestBackfill_VMIListAndLaunchers(t *testing.T) {
	d := newDemoCluster()

	items := vmItems(t, mustRun(t, d, "kubectl", "get", "vmis", "-A", "-o", "json"))
	// Running + Paused + Migrating have instances; Stopped/Starting do not.
	if len(items) != 5 {
		t.Fatalf("want 5 VMIs, got %d", len(items))
	}

	items = vmItems(t, mustRun(t, d, "kubectl", "get", "pods", "-A", "-l", "kubevirt.io=virt-launcher", "-o", "json"))
	if len(items) != 5 {
		t.Fatalf("want 5 launcher pods, got %d", len(items))
	}
	for _, it := range items {
		meta, _ := it["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if !strings.HasPrefix(name, "virt-launcher-") {
			t.Errorf("pod name = %q", name)
		}
		labels, _ := meta["labels"].(map[string]string)
		if len(labels) == 0 {
			// Decoded as map[string]any by the generic unmarshal.
			raw, _ := meta["labels"].(map[string]any)
			if raw["vm.kubevirt.io/name"] == nil {
				t.Errorf("%s misses its vm label", name)
			}
		}
		if phase, _ := strPath(it, "status", "phase"); phase != "Running" {
			t.Errorf("%s phase = %q", name, phase)
		}
	}
}

func TestBackfill_TopLines(t *testing.T) {
	d := newDemoCluster()
	out := mustRun(t, d, "kubectl", "top", "pod", "-A", "-l", "kubevirt.io=virt-launcher")
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// Running fleet minus the paused desktop: the sparkline feed.
	if len(lines) != 4 {
		t.Fatalf("want 4 top lines, got %d: %q", len(lines), out)
	}
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) != 4 || fields[0] != "corral-vms" || !strings.HasSuffix(fields[2], "m") || !strings.HasSuffix(fields[3], "Mi") {
			t.Errorf("top line shape: %q", l)
		}
	}

	// Per-VM usage for a running VM; silence for a stopped one.
	out = mustRun(t, d, "kubectl", "top", "pod", "-l", "kubevirt.io/vm=web-prod", "-n", "corral-vms")
	if !strings.HasPrefix(string(out), "virt-launcher-web-prod ") {
		t.Errorf("per-VM top: %q", out)
	}
	out = mustRun(t, d, "kubectl", "top", "pod", "-l", "kubevirt.io/vm=dev-fedora", "-n", "corral-vms")
	if len(out) != 0 {
		t.Errorf("stopped VM should have no usage, got %q", out)
	}
}

func TestBackfill_DemoMiB(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"4Gi", 4096},
		{"512Mi", 512},
		{"bogus", 0},
		{"", 0},
	} {
		if got := demoMiB(tc.in); got != tc.want {
			t.Errorf("demoMiB(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestBackfill_CTFixtures(t *testing.T) {
	d := newDemoCluster()

	items := vmItems(t, mustRun(t, d, "kubectl", "get", "pvc", "-A", "-l", "corral.dev/ct=true", "-o", "json"))
	if len(items) != 2 {
		t.Fatalf("want 2 CT PVCs, got %d", len(items))
	}
	for _, it := range items {
		labels, _ := it["metadata"].(map[string]any)["labels"].(map[string]any)
		if labels["corral.dev/ct"] != "true" || labels["corral.dev/ct-name"] == nil {
			t.Errorf("PVC labels: %v", labels)
		}
		ann, _ := it["metadata"].(map[string]any)["annotations"].(map[string]any)
		var spec struct {
			Image string `json:"image"`
		}
		if err := json.Unmarshal([]byte(ann["corral.ct/spec"].(string)), &spec); err != nil || spec.Image == "" {
			t.Errorf("PVC spec annotation: %v %v", ann, err)
		}
	}

	// Only the running CT has a pod.
	items = vmItems(t, mustRun(t, d, "kubectl", "get", "pods", "-A", "-l", "corral.dev/ct=true", "-o", "json"))
	if len(items) != 1 {
		t.Fatalf("want 1 CT pod, got %d", len(items))
	}
	if name, _ := strPath(items[0], "metadata", "name"); name != "dev-shell" {
		t.Errorf("CT pod = %q", name)
	}
}

func TestBackfill_DeleteVMAndCT(t *testing.T) {
	d := newDemoCluster()

	mustRun(t, d, "kubectl", "delete", "vm", "scratch", "-n", "corral-vms")
	if v := d.find("scratch", "corral-vms"); v != nil {
		t.Error("scratch should be gone from the fleet")
	}
	items := vmItems(t, mustRun(t, d, "kubectl", "get", "vms", "-A", "-o", "json"))
	if len(items) != 7 {
		t.Errorf("fleet should shrink to 7, got %d", len(items))
	}

	// Deleting the pod stops the CT but keeps its record...
	mustRun(t, d, "kubectl", "delete", "pod", "dev-shell", "-n", "corral-vms")
	items = vmItems(t, mustRun(t, d, "kubectl", "get", "pods", "-A", "-l", "corral.dev/ct=true", "-o", "json"))
	if len(items) != 0 {
		t.Errorf("stopped CT should have no pod: %d", len(items))
	}
	// ...while deleting the PVC removes it.
	mustRun(t, d, "kubectl", "delete", "pvc", "ct-files-data", "-n", "corral-vms")
	items = vmItems(t, mustRun(t, d, "kubectl", "get", "pvc", "-A", "-l", "corral.dev/ct=true", "-o", "json"))
	if len(items) != 1 {
		t.Errorf("want 1 CT PVC left, got %d", len(items))
	}
}

func TestBackfill_ApplyManifest(t *testing.T) {
	d := newDemoCluster()

	if _, err := d.RunStdin("kind: VirtualMachine\nmetadata:\n  name: fresh-vm\n", "kubectl", "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	if v := d.find("fresh-vm", "corral-vms"); v == nil || v.Status != "Running" {
		t.Errorf("applied VM should join the fleet running: %+v", v)
	}

	// A CT pod flips its CT to Running.
	d.cts[0].Running = false
	if _, err := d.RunStdin("kind: Pod\nmetadata:\n  name: dev-shell\n  namespace: corral-vms\n", "kubectl", "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	if !d.cts[0].Running {
		t.Error("applied pod should flip its CT to Running")
	}

	// A CT PVC mints a CT record; a nameless manifest is a silent success.
	if _, err := d.RunStdin("kind: PersistentVolumeClaim\nmetadata:\n  name: ct-new-data\n  labels:\n    corral.dev/ct: \"true\"\n", "kubectl", "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RunStdin("kind: Whatever\n", "kubectl", "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}
}

func TestBackfill_TagLabels(t *testing.T) {
	d := newDemoCluster()

	mustRun(t, d, "kubectl", "label", "vm", "web-prod", "-n", "corral-vms", "corral.dev/tag.edge=true")
	if v := d.find("web-prod", "corral-vms"); !hasTag(v.Tags, "edge") {
		t.Errorf("edge tag missing: %v", v.Tags)
	}
	mustRun(t, d, "kubectl", "label", "vm", "web-prod", "-n", "corral-vms", "corral.dev/tag.prod-")
	if v := d.find("web-prod", "corral-vms"); hasTag(v.Tags, "prod") || !hasTag(v.Tags, "web") {
		t.Errorf("only prod should be removed: %v", v.Tags)
	}
	// Unknown VMs and non-tag labels are ignored, not errors.
	mustRun(t, d, "kubectl", "label", "vm", "nope", "-n", "corral-vms", "corral.dev/tag.x=true")
	mustRun(t, d, "kubectl", "label", "vm", "web-prod", "-n", "corral-vms", "other.io/label=1")
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func TestBackfill_VirtctlTransitions(t *testing.T) {
	d := newDemoCluster()

	// virtctl spells the kind some of the time ("pause vm x") and not others
	// ("start x"); both must resolve to the VM.
	mustRun(t, d, "virtctl", "pause", "vm", "web-prod", "-n", "corral-vms")
	mustRun(t, d, "virtctl", "unpause", "web-prod", "-n", "corral-vms")
	if v := d.find("web-prod", "corral-vms"); v.Status != "Running" {
		t.Errorf("status = %s", v.Status)
	}
	mustRun(t, d, "virtctl", "restart", "web-prod", "-n", "corral-vms")

	// Starting a stopped VM without an address assigns both.
	mustRun(t, d, "virtctl", "start", "dev-fedora", "-n", "corral-vms")
	if v := d.find("dev-fedora", "corral-vms"); v.Status != "Running" || v.IP == "" || v.Node == "" {
		t.Errorf("started VM should gain IP and node: %+v", v)
	}

	// Migrate walks the node ring.
	mustRun(t, d, "virtctl", "migrate", "web-prod", "-n", "corral-vms")
	if v := d.find("web-prod", "corral-vms"); v.Node != "corral-2" {
		t.Errorf("corral-1 should migrate to corral-2, got %s", v.Node)
	}

	if _, err := d.Run("virtctl", "stop", "nope", "-n", "corral-vms"); err == nil {
		t.Error("unknown VM should error")
	}
}

func TestBackfill_DoctorAndInfraQueries(t *testing.T) {
	d := newDemoCluster()

	out := mustRun(t, d, "kubectl", "get", "kubevirt", "-A", "-o", "json")
	var cr struct {
		Items []struct {
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &cr); err != nil || len(cr.Items) != 1 || cr.Items[0].Status.Phase != "Deployed" {
		t.Fatalf("kubevirt CR should read deployed: %s %v", out, err)
	}
	out = mustRun(t, d, "kubectl", "get", "kubevirt", "-o", "name")
	if !strings.Contains(string(out), "kubevirt.kubevirt.io/kubevirt") {
		t.Errorf("-o name form: %q", out)
	}
	// jsonpath is a plain string, not a list — hasJSONOutput must not claim it.
	out = mustRun(t, d, "kubectl", "get", "kubevirt", "-o", "jsonpath={.items[0].status.phase}")
	if strings.HasPrefix(strings.TrimSpace(string(out)), "{") {
		t.Errorf("jsonpath should not return a list: %q", out)
	}

	out = mustRun(t, d, "kubectl", "get", "cdi", "-o", "name")
	if !strings.Contains(string(out), "cdi.cdi.kubevirt.io/cdi") {
		t.Errorf("cdi: %q", out)
	}
	out = mustRun(t, d, "kubectl", "get", "sc", "-o", "json")
	if !strings.Contains(string(out), "demo-nvme") {
		t.Errorf("storageclass: %q", out)
	}
	out = mustRun(t, d, "kubectl", "get", "storageprofile", "-o", "json")
	if !strings.Contains(string(out), "csi-clone") {
		t.Errorf("storageprofile: %q", out)
	}
	out = mustRun(t, d, "kubectl", "get", "volumesnapshotclass", "-o", "json")
	if !strings.Contains(string(out), "demo-snapclass") {
		t.Errorf("snapclass json: %q", out)
	}
	out = mustRun(t, d, "kubectl", "get", "volumesnapshotclass", "-o", "name")
	if !strings.Contains(string(out), "demo-snapclass") {
		t.Errorf("snapclass name: %q", out)
	}

	// Named gets 404 like kubectl; bare gets return empty lists.
	if _, err := d.Run("kubectl", "get", "datavolume", "nope", "-n", "corral-vms", "-o", "json"); err == nil {
		t.Error("missing datavolume should 404")
	}
	out = mustRun(t, d, "kubectl", "get", "datavolume", "-n", "corral-vms", "-o", "json")
	if !strings.Contains(string(out), `"items":[]`) {
		t.Errorf("empty list shape: %q", out)
	}
}

func TestBackfill_IncusAndPatchAndGeneric(t *testing.T) {
	d := newDemoCluster()

	// One container (a CT) and one virtual machine (a VM): the split the
	// fleet merger depends on.
	out := mustRun(t, d, "incus", "list")
	var insts []struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &insts); err != nil || len(insts) != 2 {
		t.Fatalf("incus list: %s %v", out, err)
	}
	types := map[string]string{}
	for _, in := range insts {
		types[in.Type] = in.Name
	}
	if types["container"] != "incus-demo-container" || types["virtual-machine"] != "incus-demo-vm" {
		t.Errorf("incus split: %v", types)
	}

	// Node pinning through patch.
	mustRun(t, d, "kubectl", "patch", "vm", "web-prod", "-n", "corral-vms", "--type", "merge",
		"-p", `{"spec":{"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":"corral-3"}}}}}`)
	if v := d.find("web-prod", "corral-vms"); v.Node != "corral-3" {
		t.Errorf("pinned node = %q", v.Node)
	}

	// Non-kubectl binaries and unhandled verbs succeed quietly.
	mustRun(t, d, "systemctl", "is-active", "whatever")
	mustRun(t, d, "kubectl", "get", "events", "-n", "corral-vms")
	mustRun(t, d, "kubectl", "scale", "vm", "web-prod")
}

func TestBackfill_LocalSystemctl(t *testing.T) {
	d := newDemoCluster()

	out, err := d.localSystemctl()
	if err != nil || len(out) != 0 {
		t.Errorf("empty args should succeed quietly: %q %v", out, err)
	}
	if _, err := d.localSystemctl("is-active", "corral-laptop-dev"); err == nil {
		t.Error("unknown unit should read inactive")
	}
	if _, err := d.localSystemctl("start", "corral-laptop-dev"); err != nil {
		t.Fatal(err)
	}
	if out, err := d.localSystemctl("is-active", "corral-laptop-dev"); err != nil || string(out) != "active\n" {
		t.Errorf("started unit should read active: %q %v", out, err)
	}
	if _, err := d.localSystemctl("stop", "corral-laptop-dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.localSystemctl("is-active", "corral-laptop-dev"); err == nil {
		t.Error("stopped unit should read inactive again")
	}
	if _, err := d.localSystemctl("daemon-reload"); err != nil {
		t.Errorf("other verbs succeed quietly: %v", err)
	}
}

func TestBackfill_RunningAndHelpers(t *testing.T) {
	for status, want := range map[string]bool{
		"Running": true, "Paused": true, "Migrating": true,
		"Stopped": false, "Starting": false, "": false,
	} {
		if got := (&demoVM{Status: status}).running(); got != want {
			t.Errorf("running(%q) = %v, want %v", status, got, want)
		}
	}
	if d := newDemoCluster(); d.find("nope", "") != nil {
		t.Error("find should miss unknown VMs")
	}
	if d := newDemoCluster(); d.find("web-prod", "other-ns") != nil {
		t.Error("find should respect the namespace")
	}

	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"get", "-o", "json"}, true},
		{[]string{"get", "-ojson"}, true},
		{[]string{"get", "-o", "jsonpath={.x}"}, false},
		{[]string{"get"}, false},
	} {
		if got := hasJSONOutput(tc.args); got != tc.want {
			t.Errorf("hasJSONOutput(%v) = %v", tc.args, got)
		}
	}
	if got := flagValue([]string{"get", "-n", "corral-vms"}, "-n"); got != "corral-vms" {
		t.Errorf("flagValue = %q", got)
	}
	if got := flagValue([]string{"get", "-n"}, "-n"); got != "" {
		t.Errorf("trailing flag has no value: %q", got)
	}
}
