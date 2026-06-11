package kubevirt

import (
	"strings"
	"testing"

	"github.com/hanthor/corral/pkg/shell"
	"github.com/hanthor/corral/pkg/types"
)

// newFeaturesFake wires a single Fake into every runner seam (client,
// package-level, apply) and restores the real runners afterwards.
func newFeaturesFake(t *testing.T) (*Client, *shell.Fake) {
	t.Helper()
	r := shell.NewFake()
	SetDefaultRunner(r)
	SetPackageRunner(r)
	SetApplyRunner(r)
	t.Cleanup(func() {
		SetDefaultRunner(nil)
		SetPackageRunner(shell.Real{})
		SetApplyRunner(shell.Real{})
	})
	return NewClientWithRunner("tailvm", r), r
}

// Cluster facts used by the live-migration tests: VM "web" runs on bihar and
// both nodes share the Intel CPU vendor (so a migration target exists).
const migratableVMIsJSON = `{
  "items": [{
    "metadata": {"name": "web", "namespace": "tailvm"},
    "status": {
      "nodeName": "bihar",
      "interfaces": [{"ipAddress": "10.0.0.5"}],
      "conditions": [{"type": "LiveMigratable", "status": "True"}]
    }
  }]
}`

const sameVendorNodesJSON = `{
  "items": [
    {"metadata": {"name": "bihar", "labels": {
      "kubevirt.io/schedulable": "true",
      "cpu-vendor.node.kubevirt.io/Intel": "true"}}},
    {"metadata": {"name": "karnataka", "labels": {
      "kubevirt.io/schedulable": "true",
      "cpu-vendor.node.kubevirt.io/Intel": "true"}}}
  ]
}`

const crossVendorNodesJSON = `{
  "items": [
    {"metadata": {"name": "bihar", "labels": {
      "kubevirt.io/schedulable": "true",
      "cpu-vendor.node.kubevirt.io/Intel": "true"}}},
    {"metadata": {"name": "karnataka", "labels": {
      "kubevirt.io/schedulable": "true",
      "cpu-vendor.node.kubevirt.io/AMD": "true"}}}
  ]
}`

func applyOK(r *shell.Fake) {
	r.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
}

// ── Migrate ───────────────────────────────────────────────────────

func TestClient_Migrate_WithTargetNode(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddPrefixResponse("kubectl patch vm web -n tailvm --type merge -p", "patched", nil)
	r.AddResponseKV("/fake/bin/virtctl", []string{"migrate", "web", "-n", "tailvm"}, "", nil)

	if err := c.Migrate("web", "karnataka"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var patched bool
	for _, call := range r.Calls() {
		if call.Name == "kubectl" && len(call.Args) > 0 && call.Args[0] == "patch" &&
			strings.Contains(strings.Join(call.Args, " "), "karnataka") {
			patched = true
		}
	}
	if !patched {
		t.Error("Migrate with a target node should pin the nodeSelector first")
	}
}

func TestClient_Migrate_SchedulerPick_Migratable(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, migratableVMIsJSON, nil)
	r.AddResponseKV("kubectl", []string{"get", "nodes", "-o", "json"}, sameVendorNodesJSON, nil)
	r.AddResponseKV("/fake/bin/virtctl", []string{"migrate", "web", "-n", "tailvm"}, "", nil)

	if err := c.Migrate("web", ""); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, call := range r.Calls() {
		if call.Name == "kubectl" && len(call.Args) > 0 && call.Args[0] == "patch" {
			t.Error("scheduler-pick migration must not patch the nodeSelector")
		}
	}
}

func TestClient_Migrate_CrossVendor_Errors(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, migratableVMIsJSON, nil)
	r.AddResponseKV("kubectl", []string{"get", "nodes", "-o", "json"}, crossVendorNodesJSON, nil)

	err := c.Migrate("web", "")
	if err == nil || !strings.Contains(err.Error(), "cannot be live-migrated") {
		t.Fatalf("cross-vendor migrate should fail with explanation, got: %v", err)
	}
}

func TestClient_Migrate_NoVMI_Errors(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, `{"items":[]}`, nil)

	if err := c.Migrate("web", ""); err == nil {
		t.Fatal("migrating a VM with no running VMI should fail")
	}
}

// ── Scale ─────────────────────────────────────────────────────────

const runningVMJSON = `{
  "spec": {
    "running": true,
    "template": {"spec": {"domain": {
      "cpu": {"sockets": 2, "cores": 1, "threads": 1, "maxSockets": 8},
      "memory": {"guest": "2048Mi", "maxGuest": "8192Mi"}
    }}}
  }
}`

func TestClient_ScaleCPU_Invalid(t *testing.T) {
	c, _ := newFeaturesFake(t)
	if err := c.ScaleCPU("web", 0); err == nil {
		t.Fatal("ScaleCPU(0) should fail")
	}
}

func TestClient_Scale_NothingToChange(t *testing.T) {
	c, _ := newFeaturesFake(t)
	if err := c.Scale("web", 0, ""); err == nil {
		t.Fatal("Scale with no changes should fail")
	}
}

func TestClient_Scale_InvalidMemory(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o", "json"}, runningVMJSON, nil)

	err := c.ScaleMemory("web", "junk")
	if err == nil || !strings.Contains(err.Error(), "invalid memory") {
		t.Fatalf("want invalid-memory error, got: %v", err)
	}
}

func TestClient_Scale_LiveHotplug(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o", "json"}, runningVMJSON, nil)
	r.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, migratableVMIsJSON, nil)
	r.AddResponseKV("kubectl", []string{"get", "nodes", "-o", "json"}, sameVendorNodesJSON, nil)
	r.AddPrefixResponse("kubectl patch vm web -n tailvm --type merge -p", "patched", nil)

	if err := c.Scale("web", 4, "4G"); err != nil {
		t.Fatalf("Scale: %v", err)
	}
	var sawCPU, sawMem bool
	for _, call := range r.Calls() {
		joined := strings.Join(call.Args, " ")
		if call.Name == "/fake/bin/virtctl" {
			t.Errorf("live hotplug must not stop/start the VM: %v", call.Args)
		}
		if strings.Contains(joined, `"sockets":4`) {
			sawCPU = true
		}
		if strings.Contains(joined, `"guest":"4096Mi"`) {
			sawMem = true
		}
	}
	if !sawCPU || !sawMem {
		t.Errorf("expected live CPU + memory patches (sawCPU=%v sawMem=%v)", sawCPU, sawMem)
	}
}

func TestClient_Scale_OfflineFallback_RestartsOnce(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o", "json"}, runningVMJSON, nil)
	// Cross-vendor nodes → no live migration → offline path.
	r.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, migratableVMIsJSON, nil)
	r.AddResponseKV("kubectl", []string{"get", "nodes", "-o", "json"}, crossVendorNodesJSON, nil)
	r.AddResponseKV("/fake/bin/virtctl", []string{"stop", "web", "-n", "tailvm"}, "", nil)
	r.AddResponseKV("/fake/bin/virtctl", []string{"start", "web", "-n", "tailvm"}, "", nil)
	// "kubectl get vmi web" is unregistered → errors → waitStopped sees the VMI gone.
	r.AddPrefixResponse("kubectl patch vm web -n tailvm --type merge -p", "patched", nil)

	if err := c.Scale("web", 4, ""); err != nil {
		t.Fatalf("Scale: %v", err)
	}
	var stops, starts int
	for _, call := range r.Calls() {
		if call.Name == "/fake/bin/virtctl" && len(call.Args) > 0 {
			switch call.Args[0] {
			case "stop":
				stops++
			case "start":
				starts++
			}
		}
	}
	if stops != 1 || starts != 1 {
		t.Errorf("offline scale should stop+start exactly once, got %d stops / %d starts", stops, starts)
	}
}

// ── Volumes ───────────────────────────────────────────────────────

func TestClient_AddVolume(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	applyOK(r)
	r.AddPrefixResponse("/fake/bin/virtctl addvolume web --volume-name=web-hp-", "", nil)

	pvc, err := c.AddVolume("web", "")
	if err != nil {
		t.Fatalf("AddVolume: %v", err)
	}
	if !strings.HasPrefix(pvc, "web-hp-") {
		t.Errorf("PVC name %q should be prefixed web-hp-", pvc)
	}
}

func TestClient_AddVolume_ApplyFails(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	// No apply response registered → PVC creation fails.

	if _, err := c.AddVolume("web", "20Gi"); err == nil {
		t.Fatal("AddVolume should fail when the PVC can't be created")
	}
}

// ── Snapshots / clone / templates ─────────────────────────────────

func TestClient_Snapshot_GeneratesName(t *testing.T) {
	c, r := newFeaturesFake(t)
	applyOK(r)

	snap, err := c.Snapshot("web", "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.HasPrefix(snap, "web-snap-") {
		t.Errorf("generated snapshot name %q should be prefixed web-snap-", snap)
	}
}

func TestClient_Snapshot_ExplicitName(t *testing.T) {
	c, r := newFeaturesFake(t)
	applyOK(r)

	snap, err := c.Snapshot("web", "before-upgrade")
	if err != nil || snap != "before-upgrade" {
		t.Fatalf("Snapshot: got (%q, %v)", snap, err)
	}
}

func TestClient_RestoreSnapshot(t *testing.T) {
	c, r := newFeaturesFake(t)
	applyOK(r)

	if err := c.RestoreSnapshot("web", "before-upgrade"); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
}

func TestClient_Clone(t *testing.T) {
	c, r := newFeaturesFake(t)
	applyOK(r)

	if err := c.Clone("golden", "web2"); err != nil {
		t.Fatalf("Clone: %v", err)
	}
}

func TestClient_CreateFromTemplate(t *testing.T) {
	c, r := newFeaturesFake(t)
	applyOK(r)

	if err := c.CreateFromTemplate("golden", "web2"); err != nil {
		t.Fatalf("CreateFromTemplate: %v", err)
	}
}

// ── Export helpers ────────────────────────────────────────────────

func TestClient_PrimaryPVC(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o", "json"}, `{
	  "spec": {"template": {"spec": {"volumes": [
	    {"containerDisk": {}},
	    {"persistentVolumeClaim": {"claimName": "web-disk"}}
	  ]}}}
	}`, nil)

	pvc, err := c.primaryPVC("web")
	if err != nil || pvc != "web-disk" {
		t.Fatalf("primaryPVC: got (%q, %v), want web-disk", pvc, err)
	}
}

func TestClient_PrimaryPVC_Ephemeral(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o", "json"},
		`{"spec":{"template":{"spec":{"volumes":[{"containerDisk":{}}]}}}}`, nil)

	pvc, err := c.primaryPVC("web")
	if err != nil || pvc != "" {
		t.Fatalf("ephemeral VM should have no primary PVC, got (%q, %v)", pvc, err)
	}
}

// ── Networks / instancetypes / catalogs ───────────────────────────

func TestListNADs(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddPrefixResponse("kubectl get net-attach-def -A", "default/br0\ntailvm/vlan10\n", nil)

	nads := ListNADs()
	if len(nads) != 2 || nads[0] != "default/br0" || nads[1] != "tailvm/vlan10" {
		t.Errorf("ListNADs = %v", nads)
	}
}

func TestListNADs_NoMultus(t *testing.T) {
	newFeaturesFake(t) // unregistered → kubectl errors, as without Multus CRDs

	if nads := ListNADs(); len(nads) != 0 {
		t.Errorf("ListNADs without Multus = %v, want empty", nads)
	}
}

func TestListInstanceTypes(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddPrefixResponse("kubectl get virtualmachineclusterinstancetypes", "u1.small\nu1.medium\n", nil)

	if got := ListInstanceTypes(); len(got) != 2 || got[0] != "u1.small" {
		t.Errorf("ListInstanceTypes = %v", got)
	}
}

func TestListPreferences(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddPrefixResponse("kubectl get virtualmachineclusterpreferences", "fedora\n", nil)

	if got := ListPreferences(); len(got) != 1 || got[0] != "fedora" {
		t.Errorf("ListPreferences = %v", got)
	}
}

// ── DataVolumes ───────────────────────────────────────────────────

func TestListDataVolumes(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "datavolumes", "-A", "-o", "json"}, `{
	  "items": [{
	    "metadata": {"name": "fedora-iso", "namespace": "tailvm"},
	    "spec": {
	      "source": {"http": {"url": "https://example.com/fedora.iso"}},
	      "pvc": {"resources": {"requests": {"storage": "10Gi"}}}
	    },
	    "status": {"phase": "Succeeded", "progress": "100.0%"}
	  }]
	}`, nil)

	dvs, err := ListDataVolumes()
	if err != nil {
		t.Fatalf("ListDataVolumes: %v", err)
	}
	if len(dvs) != 1 {
		t.Fatalf("got %d DataVolumes, want 1", len(dvs))
	}
	want := DataVolumeInfo{
		Name: "fedora-iso", Namespace: "tailvm", Size: "10Gi",
		Phase: "Succeeded", Progress: "100.0%", Source: "https://example.com/fedora.iso",
	}
	if dvs[0] != want {
		t.Errorf("DataVolume = %+v, want %+v", dvs[0], want)
	}
}

func TestListDataVolumes_NoCDI(t *testing.T) {
	newFeaturesFake(t)

	if _, err := ListDataVolumes(); err == nil {
		t.Fatal("ListDataVolumes without CDI should error")
	}
}

func TestImportDataVolume(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"},
		`{"items":[{"metadata":{"name":"longhorn"},"allowVolumeExpansion":true}]}`, nil)
	applyOK(r)
	// EnsureNamespace's create/label calls are unregistered → errors are tolerated.

	if err := ImportDataVolume("fedora", "", "https://example.com/fedora.qcow2", ""); err != nil {
		t.Fatalf("ImportDataVolume: %v", err)
	}
}

func TestDeleteDataVolume(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl",
		[]string{"delete", "datavolume", "fedora", "-n", "tailvm", "--ignore-not-found"}, "", nil)

	if err := DeleteDataVolume("tailvm", "fedora"); err != nil {
		t.Fatalf("DeleteDataVolume: %v", err)
	}
}

func TestDataVolumeStatus(t *testing.T) {
	_, r := newFeaturesFake(t)

	tests := []struct {
		json, want string
	}{
		{`{"status":{"phase":"Succeeded"}}`, "✓ ready"},
		{`{"status":{"phase":"ImportInProgress","progress":"42.0%"}}`, "↓ 42.0%"},
		{`{"status":{"phase":"ImportScheduled"}}`, "↓ importing"},
		{`{"status":{"phase":"Pending"}}`, "↓ queued"},
		{`{"status":{"phase":"Failed"}}`, "↓ Failed"},
	}
	for _, tt := range tests {
		r.AddResponseKV("kubectl",
			[]string{"get", "datavolume", "web-iso", "-n", "tailvm", "-o", "json"}, tt.json, nil)
		if got := DataVolumeStatus("web", "tailvm"); got != tt.want {
			t.Errorf("DataVolumeStatus(%s) = %q, want %q", tt.json, got, tt.want)
		}
	}
}

func TestDataVolumeStatus_NoDataVolume(t *testing.T) {
	newFeaturesFake(t)

	if got := DataVolumeStatus("web", "tailvm"); got != "" {
		t.Errorf("DataVolumeStatus without a DV = %q, want empty", got)
	}
}

// ── Observability ─────────────────────────────────────────────────

func TestClient_Metrics(t *testing.T) {
	c, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"top", "pod", "-n", "tailvm",
		"-l", "kubevirt.io/vm=web", "--no-headers"},
		"virt-launcher-web-abcde   12m   256Mi\n", nil)

	m := c.Metrics("web")
	if m["cpu"] != "12m" || m["mem"] != "256Mi" {
		t.Errorf("Metrics = %v", m)
	}
}

func TestClient_Metrics_Unavailable(t *testing.T) {
	c, _ := newFeaturesFake(t)

	m := c.Metrics("web")
	if m["cpu"] != "" || m["mem"] != "" {
		t.Errorf("Metrics without metrics-server = %v, want empty values", m)
	}
}

// ── Capabilities / namespace / misc ───────────────────────────────

func TestClusterCapabilities_Longhorn(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{
	  "items": [
	    {"metadata": {"name": "local-path",
	      "annotations": {"storageclass.kubernetes.io/is-default-class": "true"}},
	      "allowVolumeExpansion": false},
	    {"metadata": {"name": "longhorn"}, "allowVolumeExpansion": true}
	  ]
	}`, nil)
	r.AddResponseKV("kubectl", []string{"get", "volumesnapshotclass", "-o", "name"},
		"volumesnapshotclass.snapshot.storage.k8s.io/longhorn-snapshot\n", nil)

	caps := ClusterCapabilities()
	want := types.Capabilities{StorageClass: "longhorn", CanExpand: true, CanSnapshot: true}
	if caps != want {
		t.Errorf("ClusterCapabilities = %+v, want %+v", caps, want)
	}
}

func TestClusterCapabilities_NoCluster(t *testing.T) {
	newFeaturesFake(t)

	if caps := ClusterCapabilities(); caps != (types.Capabilities{}) {
		t.Errorf("ClusterCapabilities without a cluster = %+v, want zero", caps)
	}
}

func TestEnsureNamespace_Default(t *testing.T) {
	_, r := newFeaturesFake(t)

	EnsureNamespace("") // unregistered kubectl calls error, which is tolerated
	calls := r.Calls()
	if len(calls) != 2 {
		t.Fatalf("EnsureNamespace made %d calls, want 2 (create + label)", len(calls))
	}
	for _, call := range calls {
		if !contains(call.Args, DefaultNamespace) {
			t.Errorf("call %v should target the default namespace", call.Args)
		}
	}
}

func TestExposedPorts(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "svc", "web-proxy", "-n", "tailvm", "-o", "json"},
		`{"spec":{"ports":[{"port":80},{"port":443}]}}`, nil)

	ports := ExposedPorts("web", "tailvm")
	if len(ports) != 2 || ports[0] != 80 || ports[1] != 443 {
		t.Errorf("ExposedPorts = %v", ports)
	}
}

func TestExposedPorts_NoProxy(t *testing.T) {
	newFeaturesFake(t)

	if ports := ExposedPorts("web", "tailvm"); ports != nil {
		t.Errorf("ExposedPorts without a proxy = %v, want nil", ports)
	}
}

func TestQuantityToMib(t *testing.T) {
	tests := map[string]int{
		"":       0,
		"2Gi":    2048,
		"2G":     2048,
		"512Mi":  512,
		"512M":   512,
		"1024Ki": 1,
		"100":    100,
	}
	for in, want := range tests {
		if got := quantityToMib(in); got != want {
			t.Errorf("quantityToMib(%q) = %d, want %d", in, got, want)
		}
	}
}

// contains reports whether slice has the exact string (test helper).
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ── CreateVM source-type paths ────────────────────────────────────

func countApplies(r *shell.Fake) int {
	n := 0
	for _, call := range r.Calls() {
		if call.Name == "kubectl" && len(call.Args) > 0 && call.Args[0] == "apply" {
			n++
		}
	}
	return n
}

func createVMFake(t *testing.T) *shell.Fake {
	_, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	applyOK(r)
	return r
}

func TestCreateVM_ContainerDisk(t *testing.T) {
	r := createVMFake(t)

	err := CreateVM(types.CreateOpts{Name: "web", ContainerDisk: "quay.io/containerdisks/fedora:41"})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if n := countApplies(r); n != 1 { // VM only, no PVC
		t.Errorf("container-disk create applied %d manifests, want 1", n)
	}
}

func TestCreateVM_ContainerDiskWithDataDisk(t *testing.T) {
	r := createVMFake(t)

	err := CreateVM(types.CreateOpts{
		Name: "web", ContainerDisk: "quay.io/containerdisks/fedora:41", Disk: "20G"})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if n := countApplies(r); n != 2 { // data PVC + VM
		t.Errorf("container-disk+data create applied %d manifests, want 2", n)
	}
}

func TestCreateVM_ISO(t *testing.T) {
	r := createVMFake(t)

	err := CreateVM(types.CreateOpts{Name: "web", ISO: "https://example.com/fedora.iso"})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if n := countApplies(r); n != 3 { // ISO DataVolume + boot PVC + VM
		t.Errorf("ISO create applied %d manifests, want 3", n)
	}
}

func TestCreateVM_Import(t *testing.T) {
	r := createVMFake(t)

	err := CreateVM(types.CreateOpts{Name: "web", ImportURL: "https://example.com/fedora.qcow2"})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if n := countApplies(r); n != 2 { // boot DataVolume + VM
		t.Errorf("import create applied %d manifests, want 2", n)
	}
}

func TestCreateVM_ExistingPVC(t *testing.T) {
	r := createVMFake(t)

	err := CreateVM(types.CreateOpts{Name: "web", PVC: "my-disk"})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if n := countApplies(r); n != 1 { // VM only — PVC already exists
		t.Errorf("PVC create applied %d manifests, want 1", n)
	}
}

func TestCreateVM_ApplyFails(t *testing.T) {
	_, r := newFeaturesFake(t)
	r.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	// No apply response → manifest application fails.

	if err := CreateVM(types.CreateOpts{Name: "web", PVC: "my-disk"}); err == nil {
		t.Fatal("CreateVM should propagate apply failures")
	}
}
