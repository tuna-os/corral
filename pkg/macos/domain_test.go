package macos

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

func testOptions() Options {
	return Options{Name: "sonoma", Namespace: "corral-vms", Node: "karnataka", CPU: 4, Memory: "6Gi", Disk: "64Gi", MediaDir: "/nonexistent", UploadProxyURL: "https://127.0.0.1:18443"}
}

func response(t *testing.T, r *shell.Fake, kind, name, namespace string, value any) {
	t.Helper()
	args := []string{"get", kind}
	if name != "" {
		args = append(args, name)
	}
	if namespace != "" {
		args = append(args, "-n", namespace)
	} else {
		args = append(args, "-A")
	}
	args = append(args, "--ignore-not-found", "-o", "json")
	text := ""
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		text = string(data)
	}
	r.AddResponseKV("kubectl", args, text, nil)
}

func existingFixture(t *testing.T) (*shell.Fake, Options) {
	t.Helper()
	r := shell.NewFake()
	o := testOptions()
	response(t, r, "vm", o.Name, o.Namespace, GenerateVM(o, "a3fb10e1-3d48-4660-a430-0bd19327d414", "PERSISTENT-SERIAL", "02:11:22:33:44:55"))
	response(t, r, "vmi", o.Name, o.Namespace, nil)
	response(t, r, "configmap", o.Name+"-domain-hook", o.Namespace, object{"metadata": metadata(o.Name+"-domain-hook", o.Namespace, o.Name), "data": object{"onDefineDomain": domainHook}})
	for _, suffix := range []string{"opencore", "recovery", "system"} {
		dv := dataVolume(o, suffix, "8Gi")
		dv["status"] = object{"phase": "Succeeded"}
		response(t, r, "dv", o.Name+"-"+suffix, o.Namespace, dv)
		response(t, r, "pvc", o.Name+"-"+suffix, o.Namespace, object{"metadata": metadata(o.Name+"-"+suffix, o.Namespace, o.Name), "status": object{"phase": "Bound"}})
	}
	response(t, r, "namespace", o.Namespace, "", object{"metadata": object{"name": o.Namespace}})
	response(t, r, "crd", "datavolumes.cdi.kubevirt.io", "", object{"metadata": object{"name": "datavolumes.cdi.kubevirt.io"}})
	response(t, r, "kubevirt", "", "", object{"items": []any{object{"metadata": object{"name": "kubevirt", "namespace": "kubevirt", "resourceVersion": "12"}, "spec": object{"configuration": object{"developerConfiguration": object{"featureGates": []any{"CPUManager", "Sidecar"}}}}}}})
	return r, o
}

func assertReadOnly(t *testing.T, r *shell.Fake) {
	t.Helper()
	for _, call := range r.Calls() {
		if call.Name != "kubectl" || len(call.Args) == 0 || call.Args[0] != "get" || call.Stdin != "" {
			t.Fatalf("unexpected mutation: %#v", call)
		}
	}
}

func TestCreateRerunPreservesVMDisksAndIdentityWithoutLocalMedia(t *testing.T) {
	r, o := existingFixture(t)
	// Different requested defaults must not overwrite an installed VM.
	o.CPU, o.Memory, o.Disk = 8, "12Gi", "128Gi"
	o.UploadProxyURL = ""
	for range 2 {
		if err := (Client{Runner: r}).Create(o); err != nil {
			t.Fatal(err)
		}
	}
	assertReadOnly(t, r)
}

func TestCreateRejectsAllForeignResourceKindsBeforeMutation(t *testing.T) {
	for _, resource := range []struct{ kind, suffix string }{{"vm", ""}, {"vmi", ""}, {"configmap", "-domain-hook"}, {"dv", "-system"}, {"pvc", "-system"}} {
		t.Run(resource.kind, func(t *testing.T) {
			r, o := existingFixture(t)
			o.EnableSidecar = true
			response(t, r, resource.kind, o.Name+resource.suffix, o.Namespace, object{"metadata": object{"name": o.Name + resource.suffix}})
			err := (Client{Runner: r}).Create(o)
			if err == nil || !strings.Contains(err.Error(), "unrelated") {
				t.Fatalf("wanted collision error, got %v", err)
			}
			assertReadOnly(t, r)
		})
	}
}

func TestMissingInstalledSystemDiskIsNotRecreated(t *testing.T) {
	r, o := existingFixture(t)
	response(t, r, "dv", o.Name+"-system", o.Namespace, nil)
	response(t, r, "pvc", o.Name+"-system", o.Namespace, nil)
	if err := (Client{Runner: r}).Create(o); err == nil || !strings.Contains(err.Error(), "refusing to recreate") {
		t.Fatalf("wanted missing persistent data error, got %v", err)
	}
	assertReadOnly(t, r)
}

func TestPVCBoundAloneDoesNotAuthorizeOverwriteOrProveUpload(t *testing.T) {
	o := testOptions()
	pvc := object{"metadata": metadata("sonoma-recovery", o.Namespace, o.Name), "status": object{"phase": "Bound"}}
	if populated(nil, pvc) {
		t.Fatal("Bound is not upload completion")
	}
	dv := dataVolume(o, "recovery", "8Gi")
	dv["metadata"].(map[string]any)["uid"] = "our-dv-uid"
	foreign := object{"metadata": object{"ownerReferences": []any{object{"kind": "DataVolume", "name": "sonoma-recovery", "controller": true, "uid": "other-dv-uid"}}}}
	if ownedPVC(foreign, dv, o.Name) {
		t.Fatal("foreign owner UID accepted")
	}
	foreign["metadata"].(map[string]any)["ownerReferences"] = []any{object{"kind": "DataVolume", "name": "sonoma-recovery", "controller": true, "uid": "our-dv-uid"}}
	if !ownedPVC(foreign, dv, o.Name) {
		t.Fatal("owned CDI child rejected")
	}
}

func TestEnableSidecarPreservesCPUManagerAndUsesOptimisticLock(t *testing.T) {
	r := shell.NewFake()
	response(t, r, "kubevirt", "", "", object{"items": []any{object{"metadata": object{"name": "kubevirt", "namespace": "kv", "resourceVersion": "42"}, "spec": object{"configuration": object{"developerConfiguration": object{"featureGates": []any{"CPUManager"}, "otherSetting": true}}}}}})
	r.AddPrefixResponse("kubectl patch", "", nil)
	if err := (Client{Runner: r}).enableSidecar(); err != nil {
		t.Fatal(err)
	}
	calls := r.Calls()
	if len(calls) != 2 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	call := calls[1]
	var patch object
	if err := json.Unmarshal([]byte(call.Args[len(call.Args)-1]), &patch); err != nil {
		t.Fatal(err)
	}
	if got := nested(patch, "spec", "configuration", "developerConfiguration", "featureGates"); !reflect.DeepEqual(got, []any{"CPUManager", "Sidecar"}) {
		t.Fatalf("gates replaced: %#v", got)
	}
	if nested(patch, "metadata", "resourceVersion") != "42" {
		t.Fatal("patch lacks optimistic lock")
	}
	if !strings.Contains(strings.Join(call.Args, " "), "--type=merge") {
		t.Fatal("patch must preserve unrelated configuration")
	}
}

func TestGenerateVMUsesTemplateHookAndSafeHardware(t *testing.T) {
	o := testOptions()
	vm := GenerateVM(o, "stable-uuid", "stable-serial", "02:11:22:33:44:55")
	if nested(vm, "spec", "runStrategy") != "Halted" {
		t.Fatal("new VMs must remain halted")
	}
	if nested(vm, "metadata", "annotations", "hooks.kubevirt.io/hookSidecars") != nil {
		t.Fatal("hook annotation is on VM rather than VMI template")
	}
	annotation, _ := nested(vm, "spec", "template", "metadata", "annotations", "hooks.kubevirt.io/hookSidecars").(string)
	var hooks []map[string]any
	if err := json.Unmarshal([]byte(annotation), &hooks); err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0]["image"] != nil {
		t.Fatal("must use installed release shim")
	}
	if nested(vm, "spec", "template", "spec", "domain", "cpu", "model") != "host-passthrough" {
		t.Fatal("named Intel CPU would constrain AMD scheduling")
	}
	if nested(vm, "spec", "template", "spec", "domain", "firmware", "uuid") != "stable-uuid" || nested(vm, "spec", "template", "spec", "domain", "firmware", "serial") != "stable-serial" {
		t.Fatal("identity lost")
	}
	efi := nested(vm, "spec", "template", "spec", "domain", "firmware", "bootloader", "efi")
	if !reflect.DeepEqual(efi, object{"secureBoot": false, "persistent": true}) {
		t.Fatalf("EFI: %#v", efi)
	}
	disks := nested(vm, "spec", "template", "spec", "domain", "devices", "disks").([]any)
	for _, disk := range disks {
		if nested(disk.(map[string]any), "disk", "bus") != "sata" {
			t.Fatal("macOS disks require SATA")
		}
	}
}

func TestDomainHookReplacesCPUOnceAndPreservesStorageNetworking(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to exercise shipped hook")
	}
	input := `<domain xmlns:qemu="http://libvirt.org/schemas/domain/qemu/1.0"><name>sonoma</name><uuid>stable-uuid</uuid><cpu mode="host-passthrough"><topology sockets="1" cores="4" threads="1"/></cpu><devices><input type="tablet" bus="usb"/><input type="mouse" bus="usb"/><disk type="file"><source file="/persistent/disk.img"/><target dev="sda" bus="sata"/></disk><interface type="ethernet"><mac address="52:54:00:12:34:56"/></interface><graphics type="vnc" port="5900"/><video><model type="bochs"/></video></devices><qemu:commandline><qemu:arg value="-global"/><qemu:arg value="unrelated.property=on"/><qemu:arg value="-cpu"/><qemu:arg value="host"/></qemu:commandline></domain>`
	type domain struct {
		UUID string `xml:"uuid"`
		CPU  struct {
			Topology struct {
				Sockets int `xml:"sockets,attr"`
				Cores   int `xml:"cores,attr"`
				Threads int `xml:"threads,attr"`
			} `xml:"topology"`
		} `xml:"cpu"`
		Devices struct {
			Inputs []struct {
				Type string `xml:"type,attr"`
				Bus  string `xml:"bus,attr"`
			} `xml:"input"`
			Disk struct {
				Source struct {
					File string `xml:"file,attr"`
				} `xml:"source"`
			} `xml:"disk"`
			Interface struct {
				MAC struct {
					Address string `xml:"address,attr"`
				} `xml:"mac"`
			} `xml:"interface"`
			Graphics struct {
				Type string `xml:"type,attr"`
			} `xml:"graphics"`
		} `xml:"devices"`
		Command struct {
			Args []struct {
				Value string `xml:"value,attr"`
			} `xml:"arg"`
		} `xml:"commandline"`
	}
	for iteration := range 2 {
		cmd := exec.Command(python, "-c", domainHook, "--vmi", "{}", "--domain", input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hook iteration %d: %v: %s", iteration, err, out)
		}
		var got domain
		if err := xml.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if got.UUID != "stable-uuid" || got.Devices.Disk.Source.File != "/persistent/disk.img" || got.Devices.Interface.MAC.Address != "52:54:00:12:34:56" || got.Devices.Graphics.Type != "vnc" {
			t.Fatalf("unrelated XML changed: %#v", got)
		}
		if got.CPU.Topology.Sockets != 1 || got.CPU.Topology.Cores != 4 || got.CPU.Topology.Threads != 1 {
			t.Fatal("macOS CPU topology changed: cores must not become separate sockets")
		}
		inputs := map[string]int{}
		for _, input := range got.Devices.Inputs {
			inputs[input.Type+"/"+input.Bus]++
		}
		if inputs["keyboard/usb"] != 1 || inputs["tablet/usb"] != 1 || inputs["mouse/usb"] != 0 {
			t.Fatal("USB keyboard/tablet lost or a relative mouse overrides absolute console input")
		}
		values := []string{}
		for _, arg := range got.Command.Args {
			values = append(values, arg.Value)
		}
		cpus := 0
		for i, value := range values {
			if value == "-cpu" {
				cpus++
				if i+1 >= len(values) || !strings.Contains(values[i+1], "vendor=GenuineIntel") || !strings.Contains(values[i+1], "+invtsc") {
					t.Fatal("Intel CPU identity or invariant TSC lost")
				}
			}
		}
		if cpus != 1 || len(values) < 2 || values[0] != "-global" || values[1] != "unrelated.property=on" {
			t.Fatalf("duplicate CPU or lost unrelated QEMU args: %q", values)
		}
		input = string(out)
	}
}

func TestUploadProxyIsLoopbackOnly(t *testing.T) {
	for _, proxy := range []string{"https://public.example:443", "http://192.168.1.2:18443", "file:///tmp/proxy", "https://127.0.0.1"} {
		o := testOptions()
		o.UploadProxyURL = proxy
		if err := validate(o); err == nil {
			t.Fatal(fmt.Sprintf("unsafe uploadproxy accepted: %s", proxy))
		}
	}
}

func TestUploadUsesOwnedExistingDVAndRequiresPersistentCompletion(t *testing.T) {
	r, o := existingFixture(t)
	dir, hash := domainIdentityFixture(t, "original personalized image")
	o.IdentityDir = dir
	before, err := os.ReadFile(filepath.Join(dir, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	response(t, r, "vm", o.Name, o.Namespace, nil)
	dv := dataVolume(o, "opencore", "1Gi")
	dv["metadata"].(map[string]any)["annotations"].(map[string]any)[openCoreSHAAnnotation] = hash
	dv["status"] = object{"phase": "UploadReady"}
	response(t, r, "dv", o.Name+"-opencore", o.Namespace, dv)
	r.AddPrefixResponse("kubectl wait dv", "", nil)
	r.AddPrefixResponse("virtctl image-upload", "", nil)
	err = (Client{Runner: r}).Create(o)
	if err == nil || !strings.Contains(err.Error(), "has not completed") {
		t.Fatalf("unconfirmed upload must not create a VM: %v", err)
	}
	for _, call := range r.Calls() {
		if call.Stdin != "" {
			t.Fatalf("created resource despite incomplete upload: %#v", call)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "identity.json"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("resume changed its persisted identity: %v", err)
	}
	gotHash, err := fileSHA256(context.Background(), filepath.Join(dir, "OpenCore.qcow2"))
	if err != nil || gotHash != hash {
		t.Fatalf("resume changed its personalized image: %v", err)
	}
}

func TestStartIsIdempotentForAnOwnedVMI(t *testing.T) {
	r, o := existingFixture(t)
	o.Start = true
	response(t, r, "vmi", o.Name, o.Namespace, object{"metadata": metadata(o.Name, o.Namespace, o.Name)})
	if err := (Client{Runner: r}).Create(o); err != nil {
		t.Fatal(err)
	}
	assertReadOnly(t, r)
}

func TestInheritedPVCLabelPreservesCDIManagement(t *testing.T) {
	o := testOptions()
	r := shell.NewFake()
	dv := object{"metadata": metadata(o.Name+"-opencore", o.Namespace, o.Name)}
	dv["metadata"].(map[string]any)["uid"] = "owned-dv-uid"
	pvc := object{"metadata": object{
		"uid":             "persistent-pvc-uid",
		"labels":          object{ownerLabel: o.Name, "app.kubernetes.io/managed-by": "cdi-controller"},
		"ownerReferences": []any{object{"kind": "DataVolume", "controller": true, "name": o.Name + "-opencore", "uid": "owned-dv-uid"}},
	}, "status": object{"phase": "Bound"}}
	if err := (Client{Runner: r}).ensurePVCLabels(o, o.Name+"-opencore", dv, pvc); err != nil {
		t.Fatal(err)
	}
	assertReadOnly(t, r)
}

func domainIdentityFixture(t *testing.T, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "identity.json")
	identity := openCoreIdentity{1, "iMac19,1", "C02YL000JV3Q", "C02900000GUJG36CB", "00112233-4455-4677-8899-AABBCCDDEEFF", "021122334455"}
	if err := writePrivateJSON(context.Background(), identityPath, identity); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(dir, "OpenCore.qcow2")
	if err := os.WriteFile(image, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	imageHash, err := fileSHA256(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	identityHash, err := fileSHA256(context.Background(), identityPath)
	if err != nil {
		t.Fatal(err)
	}
	receipt := openCoreReceipt{Version: 1, SourceSHA256: imageHash, IdentitySHA256: identityHash, ImageSHA256: imageHash}
	if err := writePrivateJSON(context.Background(), filepath.Join(dir, "OpenCore.qcow2.verified.json"), receipt); err != nil {
		t.Fatal(err)
	}
	return dir, imageHash
}

func TestIncompleteOpenCoreRejectsUnboundOrChangedIdentityBeforeMutation(t *testing.T) {
	for _, change := range []string{"missing-binding", "other-identity", "missing-receipt", "changed-image", "changed-identity"} {
		t.Run(change, func(t *testing.T) {
			r, o := existingFixture(t)
			dir, hash := domainIdentityFixture(t, "original")
			o.IdentityDir = dir
			response(t, r, "vm", o.Name, o.Namespace, nil)
			dv := dataVolume(o, "opencore", "1Gi")
			dv["metadata"].(map[string]any)["annotations"].(map[string]any)[openCoreSHAAnnotation] = hash
			dv["status"] = object{"phase": "UploadReady"}
			switch change {
			case "missing-binding":
				delete(dv["metadata"].(map[string]any)["annotations"].(map[string]any), openCoreSHAAnnotation)
			case "other-identity":
				o.IdentityDir, _ = domainIdentityFixture(t, "other personalized image")
			case "missing-receipt":
				if err := os.Remove(filepath.Join(dir, "OpenCore.qcow2.verified.json")); err != nil {
					t.Fatal(err)
				}
			case "changed-image":
				if err := os.WriteFile(filepath.Join(dir, "OpenCore.qcow2"), []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-identity":
				identityPath := filepath.Join(dir, "identity.json")
				identity, err := loadOpenCoreIdentity(identityPath)
				if err != nil {
					t.Fatal(err)
				}
				identity.Serial = "C02YL111JV3Q"
				if err := writePrivateJSON(context.Background(), identityPath, identity); err != nil {
					t.Fatal(err)
				}
			}
			response(t, r, "dv", o.Name+"-opencore", o.Namespace, dv)
			if err := (Client{Runner: r}).Create(o); err == nil {
				t.Fatal("unverified resume was accepted")
			}
			assertReadOnly(t, r)
		})
	}
}

func TestFreshOpenCoreBindsVerifiedImageBeforeWaitingForPVC(t *testing.T) {
	r, o := existingFixture(t)
	dir, hash := domainIdentityFixture(t, "cached source and personalized image")
	o.IdentityDir = dir
	identityHash, err := fileSHA256(context.Background(), filepath.Join(dir, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipt := openCoreReceipt{Version: 2, SourceSHA256: hash, IdentitySHA256: identityHash, ImageSHA256: hash, VMHideSHA256: vmHideSHA256, LiluSHA256: liluSHA256}
	if err := writePrivateJSON(context.Background(), filepath.Join(dir, "OpenCore.qcow2.verified.json"), receipt); err != nil {
		t.Fatal(err)
	}
	o.MediaDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(o.MediaDir, "OpenCore.qcow2"), []byte("cached source and personalized image"), 0600); err != nil {
		t.Fatal(err)
	}
	response(t, r, "vm", o.Name, o.Namespace, nil)
	response(t, r, "dv", o.Name+"-opencore", o.Namespace, nil)
	response(t, r, "pvc", o.Name+"-opencore", o.Namespace, nil)
	r.AddPrefixResponse("kubectl create", "", nil)
	r.AddPrefixResponse("kubectl wait pvc", "", context.Canceled)
	err = (Client{Runner: r}).Create(o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wanted interrupted PVC wait, got %v", err)
	}
	created := 0
	for _, call := range r.Calls() {
		if call.Name == "virtctl" {
			t.Fatalf("upload ran without a selected PVC: %#v", call)
		}
		if call.Stdin == "" {
			continue
		}
		var dv object
		if err := json.Unmarshal([]byte(call.Stdin), &dv); err != nil {
			t.Fatal(err)
		}
		if nested(dv, "metadata", "annotations", openCoreSHAAnnotation) != hash {
			t.Fatal("initial target has no verified image binding")
		}
		if nested(dv, "metadata", "annotations", "cdi.kubevirt.io/storage.bind.immediate.requested") != nil {
			t.Fatal("upload target can bind before its node is selected")
		}
		created++
	}
	if created != 1 {
		t.Fatalf("wanted only the bound-identity DataVolume, created %d resources", created)
	}
}

func TestProvisionRequiresExplicitNodeButInstalledVMDoesNot(t *testing.T) {
	r, o := existingFixture(t)
	o.Node = ""
	if err := (Client{Runner: r}).Create(o); err != nil {
		t.Fatal(err)
	}
	assertReadOnly(t, r)
	r, o = existingFixture(t)
	o.Node = ""
	response(t, r, "vm", o.Name, o.Namespace, nil)
	if err := (Client{Runner: r}).Create(o); err == nil || !strings.Contains(err.Error(), "--node") {
		t.Fatalf("new VM accepted without node: %v", err)
	}
	assertReadOnly(t, r)
}

func TestOwnedPendingPVCSelectsNodeWithImmutableGuard(t *testing.T) {
	r := shell.NewFake()
	o := testOptions()
	dv := dataVolume(o, "recovery", "8Gi")
	dv["metadata"].(map[string]any)["uid"] = "dv-uid"
	pvc := object{"metadata": object{
		"uid": "pvc-uid", "resourceVersion": "frequently-changing",
		"labels":          object{"app.kubernetes.io/managed-by": "cdi-controller"},
		"annotations":     object{"keep": "untouched"},
		"ownerReferences": []any{object{"kind": "DataVolume", "controller": true, "name": o.Name + "-recovery", "uid": "dv-uid"}},
	}, "status": object{"phase": "Pending"}}
	r.AddPrefixResponse("kubectl patch pvc", "", nil)
	if err := (Client{Runner: r}).ensurePVCLabels(o, o.Name+"-recovery", dv, pvc); err != nil {
		t.Fatal(err)
	}
	calls := r.Calls()
	if len(calls) != 1 {
		t.Fatalf("unexpected commands: %#v", calls)
	}
	var patch object
	if err := json.Unmarshal([]byte(calls[0].Args[len(calls[0].Args)-1]), &patch); err != nil {
		t.Fatal(err)
	}
	want := object{"metadata": object{"uid": "pvc-uid", "labels": object{ownerLabel: o.Name}, "annotations": object{selectedNodeAnnotation: o.Node}}}
	if !reflect.DeepEqual(patch, want) {
		t.Fatalf("must only merge owned PVC metadata with immutable UID: %#v", patch)
	}
}

func TestExistingPVPlacementMismatchFailsBeforeMutation(t *testing.T) {
	for _, placement := range []string{"selected-node", "pv-affinity"} {
		t.Run(placement, func(t *testing.T) {
			r, o := existingFixture(t)
			name := o.Name + "-system"
			pvc := object{"metadata": metadata(name, o.Namespace, o.Name), "status": object{"phase": "Bound"}}
			if placement == "selected-node" {
				pvc["metadata"].(map[string]any)["annotations"] = object{selectedNodeAnnotation: "another-node"}
			} else {
				pvc["spec"] = object{"volumeName": "installed-system-pv"}
				response(t, r, "pv", "installed-system-pv", "", object{"spec": object{"nodeAffinity": object{"required": object{"nodeSelectorTerms": []any{object{"matchExpressions": []any{object{"key": "kubernetes.io/hostname", "operator": "In", "values": []any{"another-node"}}}}}}}}})
			}
			response(t, r, "pvc", name, o.Namespace, pvc)
			if err := (Client{Runner: r}).Create(o); err == nil || !strings.Contains(err.Error(), "refusing to move") {
				t.Fatalf("installed storage node mismatch accepted: %v", err)
			}
			assertReadOnly(t, r)
		})
	}
}

func TestStoppedExistingVMHonorsExplicitStartWithoutReprovision(t *testing.T) {
	r, o := existingFixture(t)
	o.Start = true
	o.EnableSidecar = true
	r.AddPrefixResponse("virtctl start", "", nil)
	if err := (Client{Runner: r}).Create(o); err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, call := range r.Calls() {
		if call.Name == "kubectl" && call.Args[0] == "get" {
			continue
		}
		if call.Name == "virtctl" && reflect.DeepEqual(call.Args, []string{"start", o.Name, "-n", o.Namespace}) {
			starts++
			continue
		}
		t.Fatalf("existing VM was reprovisioned: %#v", call)
	}
	if starts != 1 {
		t.Fatalf("explicit start made %d start calls", starts)
	}
}

func TestCanceledCreateCannotMutateResources(t *testing.T) {
	r := shell.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Client{Runner: r, Context: ctx}).Create(testOptions()); !errors.Is(err, context.Canceled) {
		t.Fatalf("wanted cancellation, got %v", err)
	}
	if len(r.Calls()) != 0 {
		t.Fatal("canceled request ran commands")
	}
}

// Model WFFC population: node selection is necessary, but CDI remains gated
// until the native uploader requests force binding and populates the volume.
type resumeUploadRunner struct {
	*shell.Fake
	t        *testing.T
	o        Options
	dv, pvc  object
	selected bool
	hash     string
}

func (r *resumeUploadRunner) Run(name string, args ...string) ([]byte, error) {
	out, err := r.Fake.Run(name, args...)
	if err != nil {
		return out, err
	}
	if name == "kubectl" && len(args) > 2 && args[0] == "patch" && args[1] == "pvc" {
		var patch object
		if err := json.Unmarshal([]byte(args[len(args)-1]), &patch); err != nil {
			return nil, err
		}
		if nested(patch, "metadata", "uid") != nested(r.pvc, "metadata", "uid") || nested(patch, "metadata", "annotations", selectedNodeAnnotation) != r.o.Node {
			return nil, fmt.Errorf("PVC was not selected with its immutable identity")
		}
		r.selected = true
		r.pvc["metadata"].(map[string]any)["annotations"] = object{selectedNodeAnnotation: r.o.Node}
		r.pvc["status"] = object{"phase": "Bound"}
		response(r.t, r.Fake, "pvc", r.o.Name+"-opencore", r.o.Namespace, r.pvc)
	}
	if name == "kubectl" && len(args) > 1 && args[0] == "wait" && args[1] == "dv" && !r.selected {
		return nil, fmt.Errorf("upload wait ran before PVC node selection")
	}
	if name == "kubectl" && len(args) > 1 && args[0] == "wait" && args[1] == "dv" && nested(r.dv, "status", "phase") == "WaitForFirstConsumer" {
		return nil, fmt.Errorf("CDI cannot become upload-ready before population is requested")
	}
	if name == "virtctl" && len(args) > 0 && args[0] == "image-upload" {
		if !r.selected {
			return nil, fmt.Errorf("upload ran before PVC node selection")
		}
		image := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "--image-path=") {
				image = strings.TrimPrefix(arg, "--image-path=")
			}
		}
		hash, err := fileSHA256(context.Background(), image)
		if err != nil || hash != r.hash {
			return nil, fmt.Errorf("upload did not use the original personalized image: %v", err)
		}
		r.dv["status"] = object{"phase": "Succeeded"}
		response(r.t, r.Fake, "dv", r.o.Name+"-opencore", r.o.Namespace, r.dv)
	}
	return out, nil
}

func TestResumeSelectsOwnedPVCBeforeUploadAndKeepsOriginalIdentity(t *testing.T) {
	fake, o := existingFixture(t)
	dir, hash := domainIdentityFixture(t, "original personalized image")
	o.IdentityDir = dir
	response(t, fake, "vm", o.Name, o.Namespace, nil)
	dv := dataVolume(o, "opencore", "1Gi")
	dv["metadata"].(map[string]any)["uid"] = "resume-dv"
	dv["metadata"].(map[string]any)["annotations"].(map[string]any)[openCoreSHAAnnotation] = hash
	dv["status"] = object{"phase": "WaitForFirstConsumer"}
	pvc := object{"metadata": metadata(o.Name+"-opencore", o.Namespace, o.Name), "status": object{"phase": "Pending"}}
	pvc["metadata"].(map[string]any)["uid"] = "resume-pvc"
	response(t, fake, "dv", o.Name+"-opencore", o.Namespace, dv)
	response(t, fake, "pvc", o.Name+"-opencore", o.Namespace, pvc)
	fake.AddPrefixResponse("kubectl patch pvc", "", nil)
	fake.AddPrefixResponse("kubectl wait dv", "", nil)
	fake.AddPrefixResponse("virtctl image-upload", "", nil)
	fake.AddPrefixResponse("kubectl create", "", nil)
	r := &resumeUploadRunner{Fake: fake, t: t, o: o, dv: dv, pvc: pvc, hash: hash}
	if err := (Client{Runner: r}).Create(o); err != nil {
		t.Fatal(err)
	}
	uploads, createdVMs := 0, 0
	for _, call := range fake.Calls() {
		if call.Name == "virtctl" {
			uploads++
		}
		if call.Stdin != "" {
			var resource object
			if err := json.Unmarshal([]byte(call.Stdin), &resource); err != nil {
				t.Fatal(err)
			}
			if resource["kind"] != "VirtualMachine" {
				t.Fatalf("resume recreated persistent resource: %#v", resource)
			}
			// Apple attestation rejects iMessage lookups unless en0 matches ROM.
			nic := nested(resource, "spec", "template", "spec", "domain", "devices", "interfaces").([]any)[0]
			if nested(nic.(map[string]any), "macAddress") != "02:11:22:33:44:55" {
				t.Fatalf("VM NIC is not pinned to the OpenCore ROM: %#v", nic)
			}
			createdVMs++
		}
	}
	if uploads != 1 || createdVMs != 1 {
		t.Fatalf("wanted one remaining upload and one initial VM, got %d uploads and %d VMs", uploads, createdVMs)
	}
}
