package kubevirt

import (
	"encoding/json"
	"testing"

	"github.com/hanthor/corral/pkg/types"
)

func TestGenerateVM_Basic(t *testing.T) {
	opts := types.CreateOpts{
		Name: "testvm", Namespace: "default",
		Mem: "4G", CPU: 2, Disk: "20G",
	}
	vm := GenerateVM(opts)

	if vm["apiVersion"] != "kubevirt.io/v1" {
		t.Errorf("wrong apiVersion: %s", vm["apiVersion"])
	}
	if vm["kind"] != "VirtualMachine" {
		t.Errorf("wrong kind: %s", vm["kind"])
	}

	meta := vm["metadata"].(map[string]any)
	if meta["name"] != "testvm" {
		t.Errorf("wrong name: %s", meta["name"])
	}

	spec := vm["spec"].(map[string]any)
	running, ok := spec["running"]
	if !ok || running != false {
		t.Error("running should default to false")
	}
}

func TestGenerateVM_ISO(t *testing.T) {
	opts := types.CreateOpts{
		Name: "isovm", Namespace: "default",
		Mem: "8G", CPU: 4, Disk: "40G",
		ISO: "https://example.com/bluefin.iso",
	}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	volumes := vmSpec["volumes"].([]map[string]any)

	if len(volumes) != 3 { // iso, rootdisk, cloudinit
		t.Fatalf("expected 3 volumes, got %d", len(volumes))
	}

	// Check ISO volume
	isoVol := volumes[0]
	if isoVol["name"] != "iso" {
		t.Errorf("expected 'iso' volume, got %s", isoVol["name"])
	}
	pvc := isoVol["persistentVolumeClaim"].(map[string]any)
	if pvc["claimName"] != "isovm-iso" {
		t.Errorf("wrong iso claim: %s", pvc["claimName"])
	}

	// Check boot order
	disks := vmSpec["domain"].(map[string]any)["devices"].(map[string]any)["disks"].([]map[string]any)
	isoDisk := disks[0]
	if isoDisk["bootOrder"] != 1 {
		t.Errorf("iso bootOrder should be 1, got %v", isoDisk["bootOrder"])
	}
	rootDisk := disks[1]
	if rootDisk["bootOrder"] != 2 {
		t.Errorf("rootdisk bootOrder should be 2, got %v", rootDisk["bootOrder"])
	}
}

func TestGenerateVM_ContainerDisk(t *testing.T) {
	opts := types.CreateOpts{
		Name:          "containervm",
		Namespace:     "default",
		Mem:           "4G",
		CPU:           2,
		Disk:          "30G",
		ContainerDisk: "quay.io/containerdisks/ubuntu:24.04",
	}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	volumes := vmSpec["volumes"].([]map[string]any)

	hasContainer := false
	hasData := false
	for _, v := range volumes {
		if _, ok := v["containerDisk"]; ok {
			hasContainer = true
		}
		if v["name"] == "datadisk" {
			hasData = true
		}
	}
	if !hasContainer {
		t.Error("container disk volume missing")
	}
	if !hasData {
		t.Error("data disk volume missing (disk was specified)")
	}
}

func TestGenerateVM_CloudInit(t *testing.T) {
	opts := types.CreateOpts{
		Name:              "civm",
		CloudInitPassword: "testpass",
		CloudInitExtra:    "packages:\n  - tailscale\n",
	}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	volumes := vmSpec["volumes"].([]map[string]any)

	var found bool
	for _, v := range volumes {
		if ci, ok := v["cloudInitNoCloud"]; ok {
			found = true
			userData := ci.(map[string]any)["userData"].(string)
			if userData == "" {
				t.Error("userData should not be empty")
			}
			// Check password and extra
			data := userData
			if data == "" {
				t.Error("empty cloud-init")
			}
		}
	}
	if !found {
		t.Error("cloud-init volume missing")
	}
}

func TestGenerateVM_NodeSelector(t *testing.T) {
	opts := types.CreateOpts{
		Name: "nodevm", Node: "karnataka",
	}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	ns := vmSpec["nodeSelector"].(map[string]any)
	if ns["kubernetes.io/hostname"] != "karnataka" {
		t.Errorf("wrong node selector: %v", ns)
	}
}

func TestGeneratePVC(t *testing.T) {
	pvc := GeneratePVC("test-disk", "default", "20G")

	if pvc["kind"] != "PersistentVolumeClaim" {
		t.Error("expected PersistentVolumeClaim")
	}
	meta := pvc["metadata"].(map[string]any)
	if meta["name"] != "test-disk" {
		t.Error("wrong name")
	}
}

func TestGenerateDataVolume(t *testing.T) {
	dv := GenerateDataVolume("test-iso", "default", "https://example.com/test.iso")

	if dv["kind"] != "DataVolume" {
		t.Error("expected DataVolume")
	}
	spec := dv["spec"].(map[string]any)
	source := spec["source"].(map[string]any)
	httpSrc := source["http"].(map[string]any)
	if httpSrc["url"] != "https://example.com/test.iso" {
		t.Errorf("wrong URL: %s", httpSrc["url"])
	}
}

func TestGenerateProxyService(t *testing.T) {
	svc := GenerateProxyService("bluefin", "default", []int{22, 5900})

	meta := svc["metadata"].(map[string]any)
	ann := meta["annotations"].(map[string]string)
	if ann["tailscale.com/expose"] != "true" {
		t.Error("missing tailscale expose annotation")
	}
	if ann["tailscale.com/hostname"] != "bluefin-vm" {
		t.Errorf("wrong hostname: %s", ann["tailscale.com/hostname"])
	}

	ports := svc["spec"].(map[string]any)["ports"].([]map[string]any)
	if len(ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(ports))
	}
}

func TestGenerateProxyDeployment(t *testing.T) {
	deploy := GenerateProxyDeployment("bluefin", "default", []int{22, 5900})

	meta := deploy["metadata"].(map[string]any)
	if meta["name"] != "bluefin-proxy" {
		t.Errorf("wrong name: %s", meta["name"])
	}

	// Verify it's valid JSON (can be serialized)
	data, err := json.Marshal(deploy)
	if err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	_ = data
}

func TestParseVMList(t *testing.T) {
	// Includes the regression case: a VM using spec.runStrategy (not spec.running)
	// must still report running=true when its printableStatus is Running.
	const sample = `{"items":[
      {"metadata":{"name":"runstrat","namespace":"default","labels":{"corral.dev/template":"true"}},
       "spec":{"runStrategy":"Always","template":{"spec":{"domain":{"cpu":{"sockets":2,"cores":1,"threads":1},"memory":{"guest":"4Gi"}}}}},
       "status":{"ready":true,"printableStatus":"Running"}},
      {"metadata":{"name":"stopped","namespace":"default"},
       "spec":{"running":false,"template":{"spec":{"domain":{"cpu":{"cores":2},"memory":{"guest":"2Gi"}}}}},
       "status":{"printableStatus":"Stopped"}}
    ]}`

	noProxy := func(_, _ string) string { return "off" }
	noISO := func(_, _ string) string { return "" }
	vmis := map[string]vmiStatus{
		"default/runstrat": {Node: "karnataka", IP: "10.0.0.5", LiveMigratable: true, AgentConnected: true},
	}
	vendors := map[string]string{"bihar": "AMD", "karnataka": "AMD"} // same vendor → migratable

	vms, err := parseVMList([]byte(sample), vmis, vendors, noProxy, noISO)
	if err != nil {
		t.Fatalf("parseVMList: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(vms))
	}

	rs := vms[0]
	if !rs.Running {
		t.Error("runStrategy VM should report Running=true (regression: spec.running was empty)")
	}
	if rs.Status != "● Running" {
		t.Errorf("status = %q, want ● Running", rs.Status)
	}
	if rs.CPU != 2 {
		t.Errorf("CPU = %d, want 2 (sockets×cores×threads)", rs.CPU)
	}
	if !rs.IsTemplate {
		t.Error("expected IsTemplate=true from label")
	}
	if !rs.LiveMigratable {
		t.Error("expected LiveMigratable=true (same-vendor target exists)")
	}
	if rs.IP != "10.0.0.5" || rs.Node != "karnataka" || !rs.AgentConnected {
		t.Errorf("VMI overlay wrong: %+v", rs)
	}

	st := vms[1]
	if st.Running {
		t.Error("stopped VM should report Running=false")
	}
	if st.Status != "○ Stopped" {
		t.Errorf("status = %q, want ○ Stopped", st.Status)
	}
	if st.CPU != 2 {
		t.Errorf("legacy cores-based CPU = %d, want 2", st.CPU)
	}
}

func TestStatusLabel(t *testing.T) {
	cases := map[string]string{
		"Running": "● Running", "Stopped": "○ Stopped", "": "○ Stopped",
		"Paused": "⏸ Paused", "Migrating": "⇄ Migrating", "Starting": "◐ Starting",
	}
	for in, want := range cases {
		if got := statusLabel(in); got != want {
			t.Errorf("statusLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasMigrationTarget(t *testing.T) {
	mixed := map[string]string{"bihar": "Intel", "karnataka": "AMD"}
	if hasMigrationTarget("bihar", mixed) {
		t.Error("Intel node should have no AMD/same-vendor target in a mixed cluster")
	}
	same := map[string]string{"a": "AMD", "b": "AMD"}
	if !hasMigrationTarget("a", same) {
		t.Error("same-vendor cluster should have a migration target")
	}
}

func TestParseMem(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"4G", 4096},
		{"8G", 8192},
		{"512M", 512},
		{"1024M", 1024},
		{"1G", 1024},
	}
	for _, tt := range tests {
		got := parseMem(tt.input)
		if got != tt.want {
			t.Errorf("parseMem(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestRandomPassword(t *testing.T) {
	p1 := randomPassword()
	p2 := randomPassword()
	if p1 == p2 {
		t.Error("random passwords should differ")
	}
	if len(p1) != 12 {
		t.Errorf("expected 12-char password, got %d", len(p1))
	}
}
