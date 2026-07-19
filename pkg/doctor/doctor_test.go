package doctor

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

// withFake installs a fake runner for the duration of the test. The /dev/kvm
// probe is stubbed to pass — it reads the real device node, which CI lacks.
func withFake(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	SetRunner(fake)
	prevKVM := statDevKVM
	statDevKVM = func() error { return nil }
	t.Cleanup(func() { SetRunner(shell.Real{}); statDevKVM = prevKVM })
	return fake
}

// scriptReachable makes the cluster-connectivity gate pass so the KubeVirt
// checks run at all.
func scriptReachable(fake *shell.Fake) {
	fake.AddResponse("kubectl get --raw /livez --request-timeout=3s", "ok", nil)
}

const healthyKubeVirtJSON = `{
  "spec": {
    "configuration": {
      "vmRolloutStrategy": "LiveUpdate",
      "developerConfiguration": {
        "featureGates": ["Snapshot", "HotplugVolumes", "VMExport"]
      }
    },
    "workloadUpdateStrategy": {
      "workloadUpdateMethods": ["LiveMigrate"]
    }
  }
}`

const healthySCJSON = `{
  "items": [
    {
      "metadata": {
        "annotations": {"storageclass.kubernetes.io/is-default-class": "true"}
      },
      "allowVolumeExpansion": false
    },
    {
      "metadata": {"annotations": {}},
      "allowVolumeExpansion": true
    }
  ]
}`

// scriptHealthyCluster registers responses describing a fully configured cluster.
func scriptHealthyCluster(fake *shell.Fake) {
	scriptReachable(fake)
	fake.AddResponse("kubectl get kubevirt -n kubevirt", "kubevirt", nil)
	fake.AddResponse("kubectl get deploy -A -l cdi.kubevirt.io=cdi-operator", "cdi-operator", nil)
	fake.AddResponse("kubectl get kubevirt kubevirt -n kubevirt -o json", healthyKubeVirtJSON, nil)
	fake.AddResponse("kubectl get sc -o json", healthySCJSON, nil)
	fake.AddResponse("kubectl get volumesnapshotclass -o name",
		"volumesnapshotclass.snapshot.storage.k8s.io/longhorn-snapshot\n", nil)
	fake.AddResponse("kubectl get deploy -A -l kubevirt.io=virt-exportproxy", "virt-exportproxy", nil)
	fake.AddResponse("kubectl get apiservices v1beta1.metrics.k8s.io", "ok", nil)
	fake.AddResponse("kubectl get svc registry-cache -n corral", "registry-cache", nil)
}

func checkByName(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in %d results", name, len(checks))
	return Check{}
}

func TestRun_HealthyCluster_AllOK(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)

	checks := Run()
	if len(checks) == 0 {
		t.Fatal("Run() returned no checks")
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("check %q not OK on a healthy cluster: %s", c.Name, c.Detail)
		}
		if c.Name == "" || c.Detail == "" {
			t.Errorf("check has empty Name or Detail: %+v", c)
		}
		if c.Fixable {
			t.Errorf("check %q marked fixable although OK", c.Name)
		}
	}
}

func TestRun_ExpectedCheckNames(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)

	checks := Run()
	for _, want := range []string{
		"KubeVirt installed",
		"CDI installed",
		"LiveUpdate rollout strategy",
		"LiveMigrate workload updates",
		"Feature gate: Snapshot",
		"Feature gate: HotplugVolumes",
		"Feature gate: VMExport",
		"Default StorageClass",
		"Expandable StorageClass",
		"VolumeSnapshotClass",
		"Export proxy",
		"metrics-server",
	} {
		checkByName(t, checks, want)
	}
}

func TestRun_NoCluster_CollapsesToOneRow(t *testing.T) {
	// No responses registered: the connectivity gate fails, so the dozen
	// KubeVirt checks collapse into one "Cluster reachable" row — a laptop
	// with no cluster is a valid setup, not a wall of failures. The local
	// checks still run (and pass via the fake's LookPath).
	withFake(t)

	checks := Run()
	c := checks[0]
	if c.Name != "Cluster reachable" || c.OK {
		t.Fatalf("first check = %+v, want a failed 'Cluster reachable'", c)
	}
	for _, c := range checks {
		if c.Name == "KubeVirt installed" {
			t.Error("cluster checks should be skipped when the cluster is unreachable")
		}
		if c.Fixable {
			t.Errorf("check %q fixable without a cluster", c.Name)
		}
	}
	checkByName(t, checks, "QEMU (local backend)")
	checkByName(t, checks, "KVM acceleration")
}

func TestRun_EmptyCluster_OnlyInstallsFixable(t *testing.T) {
	// Reachable cluster with nothing installed: every cluster check fails;
	// only the KubeVirt/CDI/metrics installs may claim to be fixable (the
	// gate/strategy checks need KubeVirt present first). Local checks pass
	// via the fake's LookPath and are exempt.
	fake := withFake(t)
	scriptReachable(fake)

	checks := Run()
	if len(checks) == 0 {
		t.Fatal("Run() returned no checks")
	}
	local := map[string]bool{
		"QEMU (local backend)": true, "KVM acceleration": true,
		"Tailscale CLI": true, "virtctl CLI": true,
	}
	installable := map[string]bool{"KubeVirt installed": true, "CDI installed": true, "metrics-server": true}
	for _, c := range checks {
		if local[c.Name] {
			continue
		}
		if c.OK {
			t.Errorf("check %q OK without a cluster", c.Name)
		}
		if c.Fixable != installable[c.Name] {
			t.Errorf("check %q fixable=%v, want %v", c.Name, c.Fixable, installable[c.Name])
		}
	}
}

func TestFix_InstallsKubeVirtAndCDI(t *testing.T) {
	fake := withFake(t)
	scriptReachable(fake)
	// Bare cluster, but kubectl apply works.
	fake.AddPrefixResponse("kubectl apply -f", "applied", nil)
	// reconcileKubeVirt's follow-up read fails (webhook not up) — tolerated.
	fake.AddPrefixResponse("kubectl patch kubevirt", "patched", nil)
	// metrics-server install patches the deployment for --kubelet-insecure-tls.
	fake.AddPrefixResponse("kubectl patch deployment metrics-server", "patched", nil)

	fixed, err := Fix()
	if err != nil {
		t.Fatalf("Fix() error: %v", err)
	}
	if len(fixed) != 3 {
		t.Errorf("Fix() fixed %v, want the KubeVirt + CDI + metrics-server installs", fixed)
	}
	var urls []string
	for _, call := range fake.Calls() {
		if len(call.Args) > 2 && call.Args[0] == "apply" {
			urls = append(urls, call.Args[2])
		}
	}
	for _, want := range []string{
		"kubevirt/releases/download/" + KubeVirtVersion + "/kubevirt-operator.yaml",
		"kubevirt/releases/download/" + KubeVirtVersion + "/kubevirt-cr.yaml",
		"containerized-data-importer/releases/download/" + CDIVersion + "/cdi-operator.yaml",
		"containerized-data-importer/releases/download/" + CDIVersion + "/cdi-cr.yaml",
	} {
		found := false
		for _, u := range urls {
			if strings.Contains(u, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no kubectl apply for %s (applied: %v)", want, urls)
		}
	}
}

func TestFix_InstallFails_ReturnsError(t *testing.T) {
	fake := withFake(t)
	scriptReachable(fake) // reachable, but apply not registered → install fails

	if _, err := Fix(); err == nil {
		t.Fatal("Fix() should propagate the install failure")
	}
}

func TestRun_MisconfiguredKubeVirt_FlagsFixable(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)
	// KubeVirt installed but with default (unconfigured) spec.
	fake.AddResponse("kubectl get kubevirt kubevirt -n kubevirt -o json", `{"spec":{}}`, nil)

	checks := Run()
	for _, name := range []string{
		"LiveUpdate rollout strategy",
		"LiveMigrate workload updates",
		"Feature gate: Snapshot",
		"Feature gate: HotplugVolumes",
		"Feature gate: VMExport",
	} {
		c := checkByName(t, checks, name)
		if c.OK {
			t.Errorf("%q OK on an unconfigured KubeVirt", name)
		}
		if !c.Fixable {
			t.Errorf("%q should be fixable when KubeVirt is installed", name)
		}
	}
}

func TestRun_StorageGaps(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)
	// Single non-default, non-expandable SC; no snapshot class.
	fake.AddResponse("kubectl get sc -o json",
		`{"items":[{"metadata":{"annotations":{}},"allowVolumeExpansion":false}]}`, nil)
	fake.AddResponse("kubectl get volumesnapshotclass -o name", "", nil)

	checks := Run()
	for _, name := range []string{"Default StorageClass", "Expandable StorageClass", "VolumeSnapshotClass"} {
		if c := checkByName(t, checks, name); c.OK {
			t.Errorf("%q OK despite storage gaps", name)
		}
	}
}

func TestRun_LonghornDefault_Flagged(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)
	fake.AddResponse("kubectl get sc -o json", `{"items":[
		{"metadata":{"name":"longhorn","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}},
		 "provisioner":"driver.longhorn.io","allowVolumeExpansion":true}]}`, nil)

	checks := Run()
	c := checkByName(t, checks, "Default StorageClass performance")
	if c.OK {
		t.Error("expected the Longhorn-default check to fail")
	}
	if !strings.Contains(c.Detail, "longhorn") {
		t.Errorf("detail should name the SC: %s", c.Detail)
	}
}

func TestRun_LocalPathDefault_NoLonghornCheck(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake) // default fixture's SC has no provisioner set (not Longhorn)

	checks := Run()
	for _, c := range checks {
		if c.Name == "Default StorageClass performance" {
			t.Errorf("Longhorn-perf check should be absent when the default SC isn't Longhorn, got: %+v", c)
		}
	}
}

func TestRun_GPUPassthrough_NotPermitted_CheckAbsent(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake) // healthyKubeVirtJSON has no permittedHostDevices

	checks := Run()
	for _, c := range checks {
		if c.Name == "GPU/PCI passthrough" {
			t.Errorf("GPU check should be absent when no device is permitted, got: %+v", c)
		}
	}
}

func TestRun_GPUPassthrough_PermittedButNotAllocatable_Fails(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)
	fake.AddResponse("kubectl get kubevirt kubevirt -n kubevirt -o json", `{
		"spec":{"configuration":{
			"vmRolloutStrategy":"LiveUpdate",
			"developerConfiguration":{"featureGates":["Snapshot","HotplugVolumes","VMExport"]},
			"permittedHostDevices":{"pciHostDevices":[{"resourceName":"amd.com/gpu","pciVendorSelector":"1002:744c"}]}
		},"workloadUpdateStrategy":{"workloadUpdateMethods":["LiveMigrate"]}}}`, nil)
	fake.AddResponse("kubectl get nodes -o json", `{"items":[{"status":{"allocatable":{"cpu":"8"}}}]}`, nil)

	checks := Run()
	c := checkByName(t, checks, "GPU/PCI passthrough")
	if c.OK {
		t.Error("expected the check to fail — resource permitted but not allocatable on any node")
	}
	if !strings.Contains(c.Detail, "amd.com/gpu") {
		t.Errorf("detail should name the missing resource: %s", c.Detail)
	}
}

func TestRun_GPUPassthrough_Allocatable_OK(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)
	fake.AddResponse("kubectl get kubevirt kubevirt -n kubevirt -o json", `{
		"spec":{"configuration":{
			"vmRolloutStrategy":"LiveUpdate",
			"developerConfiguration":{"featureGates":["Snapshot","HotplugVolumes","VMExport"]},
			"permittedHostDevices":{"pciHostDevices":[{"resourceName":"amd.com/gpu","pciVendorSelector":"1002:744c"}]}
		},"workloadUpdateStrategy":{"workloadUpdateMethods":["LiveMigrate"]}}}`, nil)
	fake.AddResponse("kubectl get nodes -o json", `{"items":[{"status":{"allocatable":{"amd.com/gpu":"1"}}}]}`, nil)

	checks := Run()
	c := checkByName(t, checks, "GPU/PCI passthrough")
	if !c.OK {
		t.Errorf("expected the check to pass: %s", c.Detail)
	}
}

func TestFix_PatchesKubeVirtOnce(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)
	// Misconfigured KubeVirt with one pre-existing custom gate to preserve.
	fake.AddResponse("kubectl get kubevirt kubevirt -n kubevirt -o json",
		`{"spec":{"configuration":{"developerConfiguration":{"featureGates":["ExpandDisks"]}}}}`, nil)
	fake.AddPrefixResponse("kubectl patch kubevirt kubevirt -n kubevirt --type merge -p", "patched", nil)

	fixed, err := Fix()
	if err != nil {
		t.Fatalf("Fix() error: %v", err)
	}
	if len(fixed) != 5 { // LiveUpdate, LiveMigrate, 3 gates
		t.Errorf("Fix() fixed %d checks, want 5: %v", len(fixed), fixed)
	}

	var patches []shell.Call
	for _, call := range fake.Calls() {
		if len(call.Args) > 0 && call.Args[0] == "patch" {
			patches = append(patches, call)
		}
	}
	if len(patches) != 1 {
		t.Fatalf("expected exactly 1 kubectl patch (shared fix deduped), got %d", len(patches))
	}
	patch := strings.Join(patches[0].Args, " ")
	for _, want := range []string{"LiveUpdate", "LiveMigrate", "Snapshot", "HotplugVolumes", "VMExport", "ExpandDisks"} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch missing %q: %s", want, patch)
		}
	}
}

func TestFix_HealthyCluster_NoPatches(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)

	fixed, err := Fix()
	if err != nil {
		t.Fatalf("Fix() error: %v", err)
	}
	if len(fixed) != 0 {
		t.Errorf("Fix() on healthy cluster fixed %v, want nothing", fixed)
	}
	for _, call := range fake.Calls() {
		if len(call.Args) > 0 && call.Args[0] == "patch" {
			t.Errorf("unexpected kubectl patch on healthy cluster: %v", call.Args)
		}
	}
}

func TestFix_PatchFails_ReturnsError(t *testing.T) {
	fake := withFake(t)
	scriptHealthyCluster(fake)
	fake.AddResponse("kubectl get kubevirt kubevirt -n kubevirt -o json", `{"spec":{}}`, nil)
	// No patch response registered → the patch command errors.

	if _, err := Fix(); err == nil {
		t.Fatal("Fix() should propagate the kubectl patch failure")
	}
}

func TestKubevirtConfig_InvalidJSON(t *testing.T) {
	fake := withFake(t)
	fake.AddResponse("kubectl get kubevirt kubevirt -n kubevirt -o json", "not json", nil)

	cfg := kubevirtConfig()
	if cfg.RolloutStrategy != "" || len(cfg.FeatureGates) != 0 || len(cfg.WorkloadUpdateMethods) != 0 {
		t.Errorf("invalid JSON should yield zero config, got %+v", cfg)
	}
}

func TestStorageClasses_InvalidJSON(t *testing.T) {
	fake := withFake(t)
	fake.AddResponse("kubectl get sc -o json", "not json", nil)

	if scs := storageClasses(); scs != nil {
		t.Errorf("invalid JSON should yield nil, got %+v", scs)
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		slice []string
		item  string
		want  bool
	}{
		{nil, "a", false},
		{[]string{}, "a", false},
		{[]string{"a"}, "a", true},
		{[]string{"a", "b"}, "b", true},
		{[]string{"a", "b"}, "c", false},
		{[]string{"Snapshot", "HotplugVolumes"}, "Snapshot", true},
	}
	for _, tt := range tests {
		if got := contains(tt.slice, tt.item); got != tt.want {
			t.Errorf("contains(%v, %q) = %v, want %v", tt.slice, tt.item, got, tt.want)
		}
	}
}

func TestUnion(t *testing.T) {
	tests := []struct {
		a, b []string
		want []string
	}{
		{nil, nil, []string{}},
		{[]string{"a"}, nil, []string{"a"}},
		{nil, []string{"a"}, []string{"a"}},
		{[]string{"a"}, []string{"b"}, []string{"a", "b"}},
		{[]string{"a"}, []string{"a"}, []string{"a"}},
		{[]string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		got := union(tt.a, tt.b)
		if len(got) != len(tt.want) {
			t.Errorf("union(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			continue
		}
		for _, s := range tt.want {
			if !contains(got, s) {
				t.Errorf("union(%v, %v) missing %q", tt.a, tt.b, s)
			}
		}
	}
}

func TestDetailIf(t *testing.T) {
	if got := detailIf(true, "yes", "no"); got != "yes" {
		t.Errorf("detailIf(true): got %q, want 'yes'", got)
	}
	if got := detailIf(false, "yes", "no"); got != "no" {
		t.Errorf("detailIf(false): got %q, want 'no'", got)
	}
}

func TestGateDetail(t *testing.T) {
	tests := map[string]string{
		"Snapshot":       "VM snapshots / restore",
		"HotplugVolumes": "add/remove disks on running VMs",
		"VMExport":       "backup/export of VM disks",
		"UnknownGate":    "",
	}
	for gate, want := range tests {
		if got := gateDetail(gate); got != want {
			t.Errorf("gateDetail(%q) = %q, want %q", gate, got, want)
		}
	}
}
