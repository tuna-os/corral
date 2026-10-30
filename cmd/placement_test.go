package cmd

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// placementScratch is createScratch plus the KubeVirt runners on one fake,
// with the placement flags restored afterwards.
func placementScratch(t *testing.T) *shell.Fake {
	t.Helper()
	createScratch(t)
	saved := []any{createKubevirt, createContainerDisk, createNode, createDedicatedCPUs, createIsolateEmulator, createTags, createInstanceType, createBootc}
	t.Cleanup(func() {
		createKubevirt = saved[0].(bool)
		createContainerDisk = saved[1].(string)
		createNode = saved[2].(string)
		createDedicatedCPUs = saved[3].(bool)
		createIsolateEmulator = saved[4].(bool)
		createTags = saved[5].([]string)
		createInstanceType = saved[6].(string)
		createBootc = saved[7].(string)
	})
	fake := shell.NewFake()
	kubevirt.SetDefaultRunner(fake)
	kubevirt.SetPackageRunner(fake)
	kubevirt.SetApplyRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetDefaultRunner(nil)
		kubevirt.SetPackageRunner(shell.Real{})
		kubevirt.SetApplyRunner(shell.Real{})
	})
	return fake
}

func appliedVM(fake *shell.Fake) map[string]any {
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" && strings.Contains(c.Stdin, `"kind":"VirtualMachine"`) {
			var vm map[string]any
			if json.Unmarshal([]byte(c.Stdin), &vm) == nil {
				return vm
			}
		}
	}
	return nil
}

func TestCreateFlags_Placement(t *testing.T) {
	for _, name := range []string{"dedicated-cpus", "isolate-emulator-thread", "tag"} {
		if createCmd.Flags().Lookup(name) == nil {
			t.Errorf("corral create has no --%s flag", name)
		}
	}
	for _, name := range []string{"prefix", "tag"} {
		if listCmd.Flags().Lookup(name) == nil {
			t.Errorf("corral list has no --%s flag", name)
		}
	}
	saved := createTags
	t.Cleanup(func() { createTags = saved })
	if err := createCmd.Flags().Parse([]string{"--tag", "pair=run42", "--tag", "desktop=gnome,x"}); err != nil {
		t.Fatal(err)
	}
	// StringArray: a comma stays inside its value rather than splitting it.
	if want := []string{"pair=run42", "desktop=gnome,x"}; !reflect.DeepEqual(createTags, want) {
		t.Errorf("--tag parsed to %q, want %q", createTags, want)
	}
}

// --dedicated-cpus on a node without the static CPU manager is refused
// before anything is applied, and the refusal names the node.
func TestCreateDedicatedCPUsRefusedWithoutCPUManager(t *testing.T) {
	fake := placementScratch(t)
	fake.AddResponseKV("kubectl", []string{"get", "node", "karnataka", "-o", "jsonpath={.metadata.labels.cpumanager}"}, "false", nil)
	fake.AddPrefixResponse("kubectl", "", nil)
	createKubevirt, createContainerDisk, createNode, createDedicatedCPUs = true, "quay.io/containerdisks/fedora:latest", "karnataka", true

	err := executeCreate("perf-a", true)
	if err == nil || !strings.Contains(err.Error(), "node karnataka") || !strings.Contains(err.Error(), "cpumanager=true") {
		t.Fatalf("err = %v, want a refusal naming node karnataka and cpumanager=true", err)
	}
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && (c.Args[0] == "apply" || c.Args[0] == "create") {
			t.Errorf("refused create still ran kubectl %v", c.Args)
		}
	}
}

// With a capable node the VM is applied pinned and tagged.
func TestCreateDedicatedCPUsAndTagsReachTheManifest(t *testing.T) {
	fake := placementScratch(t)
	fake.AddResponseKV("kubectl", []string{"get", "node", "n1", "-o", "jsonpath={.metadata.labels.cpumanager}"}, "true", nil)
	fake.AddPrefixResponse("kubectl", "", nil)
	createKubevirt, createContainerDisk, createNode = true, "quay.io/containerdisks/fedora:latest", "n1"
	createDedicatedCPUs, createTags = true, []string{"pair=run42", "desktop=gnome"}

	if err := executeCreate("perf-a", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	vm := appliedVM(fake)
	if vm == nil {
		t.Fatalf("no VirtualMachine applied; calls were %v", fake.Calls())
	}
	labels := vm["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["corral.dev/tag.pair"] != "run42" || labels["corral.dev/tag.desktop"] != "gnome" {
		t.Errorf("labels = %v", labels)
	}
	cpu := vm["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["domain"].(map[string]any)["cpu"].(map[string]any)
	if cpu["dedicatedCpuPlacement"] != true {
		t.Errorf("cpu = %v", cpu)
	}
}

func TestCreatePlacementValidation(t *testing.T) {
	placementScratch(t)
	createKubevirt, createContainerDisk = true, "quay.io/containerdisks/fedora:latest"

	createTags = []string{"Pair=x"}
	if err := executeCreate("v", true); err == nil || !strings.Contains(err.Error(), "invalid tag") {
		t.Errorf("bad tag: err = %v", err)
	}
	createTags = nil

	createIsolateEmulator = true
	if err := executeCreate("v", true); err == nil || !strings.Contains(err.Error(), "needs --dedicated-cpus") {
		t.Errorf("isolate without dedicated: err = %v", err)
	}
	createIsolateEmulator = false

	createDedicatedCPUs, createInstanceType = true, "u1.large"
	if err := executeCreate("v", true); err == nil || !strings.Contains(err.Error(), "--instancetype") {
		t.Errorf("dedicated with instancetype: err = %v", err)
	}
}

// Backends without KubeVirt placement refuse the flags instead of creating
// an unpinned, untagged VM.
func TestCreatePlacementRefusedOffKubeVirt(t *testing.T) {
	createScratch(t, config.ContextConfig{Name: "lab", Backend: "incus", Context: "lab"})
	saved, savedTags := createDedicatedCPUs, createTags
	t.Cleanup(func() { createDedicatedCPUs, createTags = saved, savedTags })
	rootContext = "lab"

	createDedicatedCPUs = true
	if err := executeCreate("v", false); err == nil || !strings.Contains(err.Error(), "--dedicated-cpus: supported on the KubeVirt backend only, not on backend incus") {
		t.Errorf("--dedicated-cpus on incus: err = %v", err)
	}
	createDedicatedCPUs, createTags = false, []string{"pair=x"}
	if err := executeCreate("v", false); err == nil || !strings.Contains(err.Error(), "--tag: supported on the KubeVirt backend only") {
		t.Errorf("--tag on incus: err = %v", err)
	}
	assertNoLocalVM(t, "v")
}

func TestFilterVMs(t *testing.T) {
	vms := []types.VM{
		{Name: "tuna-q-a", TagValues: map[string]string{"pair": "run42", "desktop": "gnome"}},
		{Name: "tuna-q-b", TagValues: map[string]string{"pair": "run42", "desktop": "tuna"}},
		{Name: "other", TagValues: map[string]string{"pair": "run42"}},
		{Name: "tuna-q-c", Backend: "qemu"},
	}
	names := func(vs []types.VM) []string {
		var out []string
		for _, v := range vs {
			out = append(out, v.Name)
		}
		return out
	}
	for _, tc := range []struct {
		prefix string
		tags   map[string]string
		want   []string
	}{
		{"", nil, []string{"tuna-q-a", "tuna-q-b", "other", "tuna-q-c"}},
		{"tuna-q-", nil, []string{"tuna-q-a", "tuna-q-b", "tuna-q-c"}},
		{"", map[string]string{"pair": "run42"}, []string{"tuna-q-a", "tuna-q-b", "other"}},
		{"tuna-q-", map[string]string{"pair": "run42", "desktop": "tuna"}, []string{"tuna-q-b"}},
		{"", map[string]string{"pair": "run43"}, nil},
	} {
		if got := names(filterVMs(vms, tc.prefix, tc.tags)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("filterVMs(%q, %v) = %v, want %v", tc.prefix, tc.tags, got, tc.want)
		}
	}
}

// `corral list --json` honours the filters and prints [] when nothing matches.
func TestListJSONFilters(t *testing.T) {
	fake := kubevirtVMScratch(t, "tuna-q-a", types.RegistryEntry{Backend: "kubevirt", Namespace: "corral-vms"})
	_ = fake
	t.Setenv("CORRAL_DEFAULT_BACKEND", "kubevirt")
	if err := config.AddContext(config.ContextConfig{Name: "k8s", Backend: "kubevirt"}); err != nil {
		t.Fatal(err)
	}
	fake.AddResponse("kubectl get vms -A -o json", `{"items":[
		{"metadata":{"name":"tuna-q-a","namespace":"corral-vms","labels":{"corral.dev/tag.pair":"x"}}},
		{"metadata":{"name":"tuna-q-b","namespace":"corral-vms","labels":{"corral.dev/tag.pair":"y"}}},
		{"metadata":{"name":"punjab","namespace":"corral-vms","labels":{"corral.dev/tag.pair":"x"}}}]}`, nil)
	savedJSON, savedPrefix, savedTags := jsonOutput, listPrefix, listTags
	t.Cleanup(func() { jsonOutput, listPrefix, listTags = savedJSON, savedPrefix, savedTags })
	jsonOutput = true

	listPrefix, listTags = "tuna-q-", []string{"pair=x"}
	out := captureStdout(t, func() {
		if err := runList(); err != nil {
			t.Errorf("list: %v", err)
		}
	})
	var vms []types.VM
	if err := json.Unmarshal([]byte(out), &vms); err != nil {
		t.Fatalf("list --json is not a JSON array: %v\n%s", err, out)
	}
	if len(vms) != 1 || vms[0].Name != "tuna-q-a" || vms[0].TagValues["pair"] != "x" {
		t.Errorf("list --prefix tuna-q- --tag pair=x = %+v", vms)
	}

	listPrefix, listTags = "", []string{"pair=none"}
	out = captureStdout(t, func() {
		if err := runList(); err != nil {
			t.Errorf("list: %v", err)
		}
	})
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("an empty filtered list printed %q, want []", out)
	}

	listTags = []string{"Bad=x"}
	if err := runList(); err == nil {
		t.Error("an invalid --tag filter was accepted")
	}
}

// `corral info --json` prints the structured details, not the raw object.
func TestInfoJSONOnKubeVirt(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "corral-vms"})
	fake.AddResponse("kubectl get vm web -n corral-vms -o json", `{
		"metadata":{"name":"web","namespace":"corral-vms","labels":{"corral.dev/tag.pair":"x"}},
		"spec":{"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":"karnataka"},
		  "domain":{"cpu":{"sockets":4,"dedicatedCpuPlacement":true},"memory":{"guest":"6Gi"},
		  "devices":{"inputs":[{"type":"tablet","bus":"usb","name":"tablet"}]}}}}},
		"status":{"printableStatus":"Stopped"}}`, nil)
	fake.AddResponse("kubectl get vmi web -n corral-vms -o json", "NotFound", errors.New("exit status 1"))
	saved := jsonOutput
	t.Cleanup(func() { jsonOutput = saved })
	jsonOutput = true

	out := captureStdout(t, func() {
		if err := infoCmd.RunE(infoCmd, []string{"web"}); err != nil {
			t.Errorf("info --json: %v", err)
		}
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("info --json is not JSON: %v\n%s", err, out)
	}
	for key, want := range map[string]any{"name": "web", "namespace": "corral-vms", "backend": "kubevirt", "node": "karnataka", "status": "Stopped", "memory": "6Gi"} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	cpu, _ := got["cpu"].(map[string]any)
	if cpu["vcpus"] != float64(4) || cpu["dedicatedCpuPlacement"] != true {
		t.Errorf("cpu = %v", cpu)
	}
	inputs, _ := got["inputs"].([]any)
	if len(inputs) != 1 || inputs[0].(map[string]any)["bus"] != "usb" {
		t.Errorf("inputs = %v", inputs)
	}
	if tags, _ := got["tagValues"].(map[string]any); tags["pair"] != "x" {
		t.Errorf("tagValues = %v", got["tagValues"])
	}
}

// A backend with no structured details refuses --json by name; plain info
// still prints its own description.
func TestInfoJSONRefusedWithoutDetailer(t *testing.T) {
	remoteVMScratch(t, "web", types.RegistryEntry{Backend: "incus", Context: "lab"})
	saved := jsonOutput
	t.Cleanup(func() { jsonOutput = saved })
	jsonOutput = true
	err := infoCmd.RunE(infoCmd, []string{"web"})
	if err == nil || !strings.Contains(err.Error(), "incus backend does not support info --json") {
		t.Errorf("info --json on incus: err = %v", err)
	}
}
