package kubevirt

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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

	noLauncher := func(_, _ string) bool { return false }
	vms, err := parseVMList([]byte(sample), vmis, vendors, noProxy, noISO, noLauncher)
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

func TestParseVMList_KernelBootRescue(t *testing.T) {
	// A bootc VM whose VMI status froze: printableStatus stuck at "Starting"
	// but the launcher pod is Running. Corral must report it as Running.
	const sample = `{"items":[
      {"metadata":{"name":"bootcvm","namespace":"tailvm"},
       "spec":{"running":true,"template":{"spec":{"domain":{
         "cpu":{"sockets":2,"cores":1,"threads":1},"memory":{"guest":"4Gi"},
         "firmware":{"kernelBoot":{"container":{"image":"quay.io/x"}}}}}}},
       "status":{"printableStatus":"Starting"}},
      {"metadata":{"name":"reallystarting","namespace":"tailvm"},
       "spec":{"running":true,"template":{"spec":{"domain":{
         "cpu":{"cores":1},"memory":{"guest":"2Gi"},
         "firmware":{"kernelBoot":{"container":{"image":"quay.io/x"}}}}}}},
       "status":{"printableStatus":"Starting"}}
    ]}`

	noProxy := func(_, _ string) string { return "off" }
	noISO := func(_, _ string) string { return "" }
	// Only bootcvm's launcher is up; reallystarting's isn't yet.
	launcher := func(name, _ string) bool { return name == "bootcvm" }

	vms, err := parseVMList([]byte(sample), nil, nil, noProxy, noISO, launcher)
	if err != nil {
		t.Fatalf("parseVMList: %v", err)
	}
	if !vms[0].Running || vms[0].Status != "● Running" {
		t.Errorf("bootc VM with running launcher should be Running, got %q (running=%v)", vms[0].Status, vms[0].Running)
	}
	if vms[1].Running {
		t.Error("bootc VM whose launcher isn't up yet must NOT be rescued to Running")
	}
}

func TestParseVMList_NonKernelBootNotRescued(t *testing.T) {
	// A normal VM stuck "Starting" with a running launcher is NOT overridden —
	// the rescue is scoped to kernel-boot VMs (no firmware.kernelBoot here).
	const sample = `{"items":[
      {"metadata":{"name":"normal","namespace":"tailvm"},
       "spec":{"running":true,"template":{"spec":{"domain":{
         "cpu":{"cores":1},"memory":{"guest":"2Gi"}}}}},
       "status":{"printableStatus":"Starting"}}
    ]}`
	noStr := func(_, _ string) string { return "" }
	always := func(_, _ string) bool { return true }
	vms, err := parseVMList([]byte(sample), nil, nil, noStr, noStr, always)
	if err != nil {
		t.Fatalf("parseVMList: %v", err)
	}
	if vms[0].Running {
		t.Error("non-kernel-boot VM must not be rescued by the launcher fallback")
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

func TestParseMem_BigValues(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"16G", 16384},
		{"128M", 128},
		{"0G", 0},
		{"0M", 0},
		{"1G", 1024},
		{"4096M", 4096},
		{"2G", 2048},
	}
	for _, tt := range tests {
		got := parseMem(tt.input)
		if got != tt.want {
			t.Errorf("parseMem(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseMem_Invalid(t *testing.T) {
	tests := []string{"", "abc"}
	for _, in := range tests {
		got := parseMem(in)
		if got != 0 {
			t.Errorf("parseMem(%q) = %d, want 0 for invalid input", in, got)
		}
	}
	// "4Gigs" — Sscanf reads 4, HasSuffix fails, returns raw 4 (MiB)
	if got := parseMem("4Gigs"); got != 4 {
		t.Errorf("parseMem(4Gigs) = %d, want 4 (raw number)", got)
	}
}

func TestParseMem_RawNumber(t *testing.T) {
	got := parseMem("2048")
	if got != 2048 {
		t.Errorf("parseMem(%q) = %d, want 2048", "2048", got)
	}
}

func TestCpuSpec_Normal(t *testing.T) {
	cpu := cpuSpec(4)
	if cpu["sockets"] != 4 {
		t.Errorf("sockets = %v, want 4", cpu["sockets"])
	}
	if cpu["cores"] != 1 {
		t.Errorf("cores = %v, want 1", cpu["cores"])
	}
	if cpu["threads"] != 1 {
		t.Errorf("threads = %v, want 1", cpu["threads"])
	}
	if cpu["maxSockets"] != 16 {
		t.Errorf("maxSockets = %v, want 16 (4×)", cpu["maxSockets"])
	}
}

func TestCpuSpec_Minimum(t *testing.T) {
	cpu := cpuSpec(0)
	if cpu["sockets"] != 1 {
		t.Errorf("sockets = %v, want 1 (minimum)", cpu["sockets"])
	}
	if cpu["maxSockets"] != 4 {
		t.Errorf("maxSockets = %v, want 4 (minimum max)", cpu["maxSockets"])
	}
}

func TestCpuSpec_Small(t *testing.T) {
	cpu := cpuSpec(1)
	if cpu["sockets"] != 1 {
		t.Errorf("sockets = %v, want 1", cpu["sockets"])
	}
	if cpu["maxSockets"] != 4 {
		t.Errorf("maxSockets = %v, want 4 (minimum max)", cpu["maxSockets"])
	}
}

func TestCpuSpec_Large(t *testing.T) {
	cpu := cpuSpec(16)
	if cpu["sockets"] != 16 {
		t.Errorf("sockets = %v, want 16", cpu["sockets"])
	}
	if cpu["maxSockets"] != 64 {
		t.Errorf("maxSockets = %v, want 64", cpu["maxSockets"])
	}
}

func TestMemSpec(t *testing.T) {
	mem := memSpec(4096)
	if mem["guest"] != "4096Mi" {
		t.Errorf("guest = %v, want 4096Mi", mem["guest"])
	}
	if mem["maxGuest"] != "16384Mi" {
		t.Errorf("maxGuest = %v, want 16384Mi (4×)", mem["maxGuest"])
	}
}

func TestMemSpec_Minimum(t *testing.T) {
	mem := memSpec(0)
	if mem["guest"] != "1Mi" {
		t.Errorf("guest = %v, want 1Mi (minimum)", mem["guest"])
	}
}

func TestRandomPassword_Length(t *testing.T) {
	for i := 0; i < 10; i++ {
		pw := randomPassword()
		if len(pw) != 12 {
			t.Errorf("password length = %d, want 12", len(pw))
		}
	}
}

func TestRandomPassword_Charset(t *testing.T) {
	pw := randomPassword()
	for _, c := range pw {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			t.Errorf("password contains invalid char: %c", c)
		}
	}
}

func TestRandomPassword_Uniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		pw := randomPassword()
		if seen[pw] {
			t.Fatal("duplicate random password after 100 iterations")
		}
		seen[pw] = true
	}
}

func TestLoadSSHPublicKey_NoKeys(t *testing.T) {
	// Point HOME to an empty directory — no .ssh folder exists
	t.Setenv("HOME", t.TempDir())
	key := LoadSSHPublicKey()
	if key != "" {
		t.Errorf("expected empty key when no .ssh dir, got %q", key)
	}
}

func TestDefaultNamespace(t *testing.T) {
	// corral-vms unless CORRAL_NAMESPACE overrides (legacy deployments use tailvm).
	if DefaultNamespace != "corral-vms" {
		t.Errorf("DefaultNamespace = %q, want corral-vms", DefaultNamespace)
	}
	t.Setenv("CORRAL_NAMESPACE", "tailvm")
	if got := defaultNamespace(); got != "tailvm" {
		t.Errorf("defaultNamespace() with CORRAL_NAMESPACE=tailvm = %q", got)
	}
}

func TestGenerateBootDataVolume(t *testing.T) {
	dv := GenerateBootDataVolume("imported-disk", "tailvm", "https://example.com/image.qcow2", "30G", "longhorn")

	if dv["kind"] != "DataVolume" {
		t.Error("expected DataVolume")
	}
	spec := dv["spec"].(map[string]any)

	// Check source URL
	source := spec["source"].(map[string]any)
	httpSrc := source["http"].(map[string]any)
	if httpSrc["url"] != "https://example.com/image.qcow2" {
		t.Errorf("wrong URL: %s", httpSrc["url"])
	}

	// Check PVC sizing
	pvc := spec["pvc"].(map[string]any)
	requests := pvc["resources"].(map[string]any)["requests"].(map[string]any)
	if requests["storage"] != "30G" {
		t.Errorf("storage = %v, want 30G", requests["storage"])
	}

	// Check storage class
	if pvc["storageClassName"] != "longhorn" {
		t.Errorf("storageClassName = %v, want longhorn", pvc["storageClassName"])
	}
}

func TestGenerateBootDataVolume_DefaultSize(t *testing.T) {
	dv := GenerateBootDataVolume("imported-disk", "tailvm", "https://example.com/image.qcow2", "", "")

	spec := dv["spec"].(map[string]any)
	pvc := spec["pvc"].(map[string]any)
	requests := pvc["resources"].(map[string]any)["requests"].(map[string]any)
	if requests["storage"] != "20G" {
		t.Errorf("default storage = %v, want 20G", requests["storage"])
	}
}

func TestGenerateBootDataVolume_NoStorageClass(t *testing.T) {
	dv := GenerateBootDataVolume("imported-disk", "tailvm", "https://example.com/image.qcow2", "10G", "")

	spec := dv["spec"].(map[string]any)
	pvc := spec["pvc"].(map[string]any)
	if _, ok := pvc["storageClassName"]; ok {
		t.Error("storageClassName should not be set when empty")
	}
}

func TestGeneratePVCWithClass(t *testing.T) {
	pvc := GeneratePVCWithClass("test-disk", "tailvm", "20G", "longhorn")

	if pvc["kind"] != "PersistentVolumeClaim" {
		t.Error("expected PersistentVolumeClaim")
	}
	spec := pvc["spec"].(map[string]any)
	if spec["storageClassName"] != "longhorn" {
		t.Errorf("storageClassName = %v, want longhorn", spec["storageClassName"])
	}
}

func TestGeneratePVCWithClass_Empty(t *testing.T) {
	pvc := GeneratePVCWithClass("test-disk", "tailvm", "20G", "")

	spec := pvc["spec"].(map[string]any)
	if _, ok := spec["storageClassName"]; ok {
		t.Error("storageClassName should not be set when empty")
	}
}

func TestGenerateVM_PVC(t *testing.T) {
	opts := types.CreateOpts{
		Name: "pvcvm", Namespace: "default",
		PVC: "existing-pvc",
	}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	volumes := vmSpec["volumes"].([]map[string]any)

	found := false
	for _, v := range volumes {
		if v["name"] == "rootdisk" {
			found = true
			pvc := v["persistentVolumeClaim"].(map[string]any)
			if pvc["claimName"] != "existing-pvc" {
				t.Errorf("PVC claimName = %v, want existing-pvc", pvc["claimName"])
			}
		}
	}
	if !found {
		t.Error("rootdisk volume not found")
	}
}

func TestGenerateVM_Defaults(t *testing.T) {
	opts := types.CreateOpts{Name: "defaultvm"}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	domain := vmSpec["domain"].(map[string]any)

	// Default CPU spec should exist
	cpu := domain["cpu"].(map[string]any)
	if cpu["sockets"] != 2 {
		t.Errorf("default CPU sockets = %v, want 2", cpu["sockets"])
	}

	// Default memory should be 4G = 4096Mi
	mem := domain["memory"].(map[string]any)
	if mem["guest"] != "4096Mi" {
		t.Errorf("default memory = %v, want 4096Mi", mem["guest"])
	}

	// Should have cloud-init volume
	volumes := vmSpec["volumes"].([]map[string]any)
	var hasCloudInit bool
	for _, v := range volumes {
		if _, ok := v["cloudInitNoCloud"]; ok {
			hasCloudInit = true
		}
	}
	if !hasCloudInit {
		t.Error("cloud-init volume missing")
	}
}

func TestGenerateVM_InstanceType(t *testing.T) {
	opts := types.CreateOpts{
		Name: "instypevm", InstanceType: "u1.medium", Preference: "fedora",
	}
	vm := GenerateVM(opts)

	spec := vm["spec"].(map[string]any)

	// Instance type and preference should be set
	it := spec["instancetype"].(map[string]any)
	if it["name"] != "u1.medium" {
		t.Errorf("instancetype name = %v, want u1.medium", it["name"])
	}
	if it["kind"] != "VirtualMachineClusterInstancetype" {
		t.Errorf("instancetype kind = %v", it["kind"])
	}

	pref := spec["preference"].(map[string]any)
	if pref["name"] != "fedora" {
		t.Errorf("preference name = %v, want fedora", pref["name"])
	}

	// CPU and memory should NOT be in the domain when instancetype is used
	tmpl := spec["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	domain := vmSpec["domain"].(map[string]any)
	if _, ok := domain["cpu"]; ok {
		t.Error("cpu should not be set when instancetype is used")
	}
	if _, ok := domain["memory"]; ok {
		t.Error("memory should not be set when instancetype is used")
	}
}

func TestGenerateVM_CloudInitPassword(t *testing.T) {
	opts := types.CreateOpts{
		Name:              "pwvm",
		CloudInitPassword: "mypassword",
	}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	volumes := vmSpec["volumes"].([]map[string]any)

	for _, v := range volumes {
		if ci, ok := v["cloudInitNoCloud"]; ok {
			userData := ci.(map[string]any)["userData"].(string)
			if userData == "" {
				t.Error("empty userData")
			}
			if userData != "" && userData[:50] == "" {
				t.Error("unexpected empty userData start")
			}
		}
	}
}

func TestGenerateVM_SSHPublicKey(t *testing.T) {
	opts := types.CreateOpts{
		Name:         "sshvm",
		SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA test",
	}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	volumes := vmSpec["volumes"].([]map[string]any)

	for _, v := range volumes {
		if ci, ok := v["cloudInitNoCloud"]; ok {
			userData := ci.(map[string]any)["userData"].(string)
			if userData == "" {
				t.Error("empty cloud-init")
			}
		}
	}
}

func TestGeneratePVCSpec(t *testing.T) {
	pvc := GeneratePVC("test-disk", "tailvm", "10G")

	meta := pvc["metadata"].(map[string]any)
	if meta["namespace"] != "tailvm" {
		t.Errorf("namespace = %v, want tailvm", meta["namespace"])
	}

	spec := pvc["spec"].(map[string]any)
	accessModes := spec["accessModes"].([]string)
	if len(accessModes) == 0 || accessModes[0] != "ReadWriteOnce" {
		t.Error("missing ReadWriteOnce access mode")
	}

	resources := spec["resources"].(map[string]any)
	requests := resources["requests"].(map[string]any)
	if requests["storage"] != "10G" {
		t.Errorf("storage = %v, want 10G", requests["storage"])
	}
}

func TestGenerateDataVolume_Metadata(t *testing.T) {
	dv := GenerateDataVolume("test-iso", "tailvm", "https://example.com/ubuntu.iso")

	meta := dv["metadata"].(map[string]any)
	if meta["name"] != "test-iso" {
		t.Errorf("name = %v, want test-iso", meta["name"])
	}
	if meta["namespace"] != "tailvm" {
		t.Errorf("namespace = %v, want tailvm", meta["namespace"])
	}
}

func TestGenerateVM_ContainerDiskWithoutDatadisk(t *testing.T) {
	opts := types.CreateOpts{
		Name:          "containernodata",
		ContainerDisk: "quay.io/containerdisks/fedora:42",
		Disk:          "", // No explicit disk
	}
	vm := GenerateVM(opts)

	tmpl := vm["spec"].(map[string]any)["template"].(map[string]any)
	vmSpec := tmpl["spec"].(map[string]any)
	volumes := vmSpec["volumes"].([]map[string]any)

	hasDatadisk := false
	for _, v := range volumes {
		if v["name"] == "datadisk" {
			hasDatadisk = true
		}
	}
	if hasDatadisk {
		t.Error("datadisk should not exist when disk is empty")
	}
}

func TestMergeCloudInit_DuplicateListKeys(t *testing.T) {
	base := "#cloud-config\npassword: x\nssh_authorized_keys:\n  - server-key\n"
	extra := "#cloud-config\nssh_authorized_keys:\n  - test-key\n"
	out := mergeCloudInit(base, extra)

	var m map[string]any
	if err := yaml.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("merged user-data is not valid YAML: %v\n%s", err, out)
	}
	keys, _ := m["ssh_authorized_keys"].([]any)
	if len(keys) != 2 || keys[0] != "server-key" || keys[1] != "test-key" {
		t.Errorf("ssh_authorized_keys = %v, want both keys (server first)", keys)
	}
	if m["password"] != "x" {
		t.Errorf("password lost in merge: %v", m["password"])
	}
	if !strings.HasPrefix(out, "#cloud-config\n") {
		t.Errorf("merged user-data lost the #cloud-config header:\n%s", out)
	}
}

func TestMergeCloudInit_NewKeysAndScalarOverride(t *testing.T) {
	base := "#cloud-config\nssh_pwauth: true\n"
	extra := "packages:\n  - htop\nssh_pwauth: false\n"
	out := mergeCloudInit(base, extra)

	var m map[string]any
	if err := yaml.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	if m["ssh_pwauth"] != false {
		t.Errorf("scalar not overridden by extra: %v", m["ssh_pwauth"])
	}
	if pkgs, _ := m["packages"].([]any); len(pkgs) != 1 || pkgs[0] != "htop" {
		t.Errorf("packages = %v", m["packages"])
	}
}

func TestMergeCloudInit_NonYAMLExtraFallsBack(t *testing.T) {
	base := "#cloud-config\npassword: x\n"
	extra := ":\n:::not yaml{{"
	if out := mergeCloudInit(base, extra); out != base+extra {
		t.Errorf("non-YAML extra should fall back to append, got:\n%s", out)
	}
}
