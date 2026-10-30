package kubevirt

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// renderedDomain marshals a manifest and reads back what kubectl would see.
type renderedVM struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Domain struct {
					CPU       map[string]any `json:"cpu"`
					Memory    map[string]any `json:"memory"`
					Resources struct {
						Requests map[string]string `json:"requests"`
						Limits   map[string]string `json:"limits"`
					} `json:"resources"`
				} `json:"domain"`
				NodeSelector map[string]string `json:"nodeSelector"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func render(t *testing.T, vm map[string]any) renderedVM {
	t.Helper()
	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatal(err)
	}
	var r renderedVM
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// A perf VM: pinned vCPUs, guaranteed-QoS memory, no hotplug headroom, and
// the pair tags as corral.dev/tag.* labels next to the existing ones.
func TestGenerateVM_DedicatedCPUsAndTags(t *testing.T) {
	vm := render(t, GenerateVM(types.CreateOpts{
		Name: "perf-a", Namespace: "corral-vms", CPU: 4, Mem: "6G", Node: "n1",
		ContainerDisk:         "quay.io/containerdisks/fedora:latest",
		DedicatedCPUs:         true,
		IsolateEmulatorThread: true,
		Tags:                  map[string]string{"pair": "run42", "desktop": "gnome"},
	}))
	dom := vm.Spec.Template.Spec.Domain
	if dom.CPU["dedicatedCpuPlacement"] != true || dom.CPU["isolateEmulatorThread"] != true {
		t.Errorf("cpu = %v, want dedicatedCpuPlacement and isolateEmulatorThread", dom.CPU)
	}
	if _, ok := dom.CPU["maxSockets"]; ok {
		t.Errorf("cpu keeps hotplug headroom maxSockets: %v", dom.CPU)
	}
	if dom.CPU["sockets"] != float64(4) {
		t.Errorf("sockets = %v, want 4", dom.CPU["sockets"])
	}
	if _, ok := dom.Memory["maxGuest"]; ok {
		t.Errorf("memory keeps hotplug headroom maxGuest: %v", dom.Memory)
	}
	if dom.Memory["guest"] != "6144Mi" || dom.Resources.Requests["memory"] != "6144Mi" || dom.Resources.Limits["memory"] != "6144Mi" {
		t.Errorf("memory = %v, resources = %+v; want guest == request == limit == 6144Mi", dom.Memory, dom.Resources)
	}
	if vm.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != "n1" {
		t.Errorf("nodeSelector = %v", vm.Spec.Template.Spec.NodeSelector)
	}
	want := map[string]string{"corral": "perf-a", "corral.dev/tag.pair": "run42", "corral.dev/tag.desktop": "gnome"}
	if !reflect.DeepEqual(vm.Metadata.Labels, want) {
		t.Errorf("labels = %v, want %v", vm.Metadata.Labels, want)
	}
}

// Without the flags the manifest is what it always was.
func TestGenerateVM_NoPlacementKeepsHotplug(t *testing.T) {
	vm := render(t, GenerateVM(types.CreateOpts{Name: "plain", CPU: 2, Mem: "4G"}))
	dom := vm.Spec.Template.Spec.Domain
	if _, ok := dom.CPU["dedicatedCpuPlacement"]; ok {
		t.Errorf("unexpected dedicatedCpuPlacement: %v", dom.CPU)
	}
	if dom.CPU["maxSockets"] == nil || dom.Memory["maxGuest"] == nil {
		t.Errorf("hotplug headroom missing: cpu=%v memory=%v", dom.CPU, dom.Memory)
	}
	if dom.Resources.Requests != nil || dom.Resources.Limits != nil {
		t.Errorf("unexpected resources: %+v", dom.Resources)
	}
	if len(vm.Metadata.Labels) != 1 {
		t.Errorf("labels = %v, want only corral", vm.Metadata.Labels)
	}
}

func TestPlacementValidate(t *testing.T) {
	if err := (Placement{IsolateEmulatorThread: true}).Validate(); err == nil || !strings.Contains(err.Error(), "--dedicated-cpus") {
		t.Errorf("isolate without dedicated: err = %v", err)
	}
	if err := (Placement{DedicatedCPUs: true, IsolateEmulatorThread: true}).Validate(); err != nil {
		t.Errorf("valid placement refused: %v", err)
	}
}

func TestParseTags(t *testing.T) {
	got, err := ParseTags([]string{"pair=run42", "desktop=gnome", "perf", "empty="})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"pair": "run42", "desktop": "gnome", "perf": "true", "empty": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseTags = %v, want %v", got, want)
	}
	if got, err := ParseTags(nil); got != nil || err != nil {
		t.Errorf("ParseTags(nil) = %v, %v", got, err)
	}
	for _, bad := range []string{"Pair=x", "=x", "pa ir=x", "pair=-x", "pair=a b", "pair=" + strings.Repeat("x", 64)} {
		if _, err := ParseTags([]string{bad}); err == nil {
			t.Errorf("ParseTags(%q) accepted an invalid tag", bad)
		}
	}
}

func TestTagFlags(t *testing.T) {
	got := TagFlags(map[string]string{"pair": "run42", "desktop": "gnome"})
	want := []string{"--tag", "desktop=gnome", "--tag", "pair=run42"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TagFlags = %v, want %v", got, want)
	}
}

// key=value tags reach types.VM, so `corral list --tag` can filter on them;
// Tags keeps only the =true chips the dashboard toggles.
func TestParseVMList_TagValues(t *testing.T) {
	out := []byte(`{"items":[{"metadata":{"name":"a","namespace":"ns","labels":{
		"corral.dev/tag.pair":"run42","corral.dev/tag.web":"true","kubevirt.io/vm":"a"}}}]}`)
	vms, err := parseVMList(out, nil, nil,
		func(string, string) string { return "" }, func(string, string) string { return "" }, nil)
	if err != nil || len(vms) != 1 {
		t.Fatalf("parseVMList = %v, %v", vms, err)
	}
	if want := map[string]string{"pair": "run42", "web": "true"}; !reflect.DeepEqual(vms[0].TagValues, want) {
		t.Errorf("TagValues = %v, want %v", vms[0].TagValues, want)
	}
	if !reflect.DeepEqual(vms[0].Tags, []string{"web"}) {
		t.Errorf("Tags = %v, want [web]", vms[0].Tags)
	}
}

func cpuManagerFake(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	SetPackageRunner(fake)
	t.Cleanup(func() { SetPackageRunner(shell.Real{}) })
	return fake
}

func TestCheckDedicatedCPUs_NamedNode(t *testing.T) {
	fake := cpuManagerFake(t)
	args := []string{"get", "node", "karnataka", "-o", "jsonpath={.metadata.labels.cpumanager}"}

	fake.AddResponseKV("kubectl", args, "false", nil)
	err := CheckDedicatedCPUs("karnataka")
	if err == nil {
		t.Fatal("a node with cpumanager=false was accepted")
	}
	for _, want := range []string{"node karnataka", "cpumanager=true", "cpuManagerPolicy: static", "restart the kubelet", "CPUManager feature gate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}

	fake.AddResponseKV("kubectl", args, "true", nil)
	if err := CheckDedicatedCPUs("karnataka"); err != nil {
		t.Errorf("a node with cpumanager=true was refused: %v", err)
	}

	fake.AddResponseKV("kubectl", args, `Error from server (NotFound): nodes "karnataka" not found`, errors.New("exit status 1"))
	if err := CheckDedicatedCPUs("karnataka"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a missing node: err = %v", err)
	}
}

func TestCheckDedicatedCPUs_AnyNode(t *testing.T) {
	fake := cpuManagerFake(t)
	args := []string{"get", "nodes", "-o", "json"}
	nodes := func(items ...string) string { return `{"items":[` + strings.Join(items, ",") + `]}` }
	node := func(name, cpumanager string, unschedulable bool) string {
		b, _ := json.Marshal(map[string]any{
			"metadata": map[string]any{"name": name, "labels": map[string]string{"cpumanager": cpumanager}},
			"spec":     map[string]any{"unschedulable": unschedulable},
		})
		return string(b)
	}

	fake.AddResponseKV("kubectl", args, nodes(node("a", "false", false), node("b", "true", false)), nil)
	if err := CheckDedicatedCPUs(""); err != nil {
		t.Errorf("one capable node was not enough: %v", err)
	}

	// The only capable node is cordoned: nothing can take the VM.
	fake.AddResponseKV("kubectl", args, nodes(node("a", "false", false), node("b", "true", true)), nil)
	err := CheckDedicatedCPUs("")
	if err == nil || !strings.Contains(err.Error(), "node a does not have") {
		t.Errorf("no schedulable capable node: err = %v", err)
	}

	fake.AddResponseKV("kubectl", args, nodes(node("a", "false", false), node("c", "", false)), nil)
	err = CheckDedicatedCPUs("")
	if err == nil || !strings.Contains(err.Error(), "no schedulable node (a, c)") {
		t.Errorf("several incapable nodes: err = %v", err)
	}
}
