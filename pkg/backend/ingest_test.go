package backend

// KubeVirt ingest: the upload-then-PVC path, and the naming and passthrough
// facts the adapter derives. The disk is already in the cluster after the
// upload, so the VM must adopt the resulting PVC — pointing CDI at an
// ImportURL instead would make it fetch what was just uploaded.

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

type uploadArgs struct {
	claim, namespace, path, size, class string
}

type kubevirtStubs struct {
	uploadArgs *uploadArgs
	uploadErr  error
	created    *types.CreateOpts
	createErr  error
}

// stubKubevirt replaces the two package calls Ingest makes (virtctl upload
// goes through os/exec directly, so no shell fake can intercept it) and
// restores them when the test ends. The storage-class lookup still shells
// out, so it gets a canned empty StorageClass list.
func stubKubevirt(t *testing.T, s *kubevirtStubs) {
	t.Helper()
	oldUpload, oldCreate := kubevirtUpload, kubevirtCreateVM
	kubevirtUpload = func(claim, namespace, path, size, class string, _ io.Writer) error {
		s.uploadArgs = &uploadArgs{claim, namespace, path, size, class}
		return s.uploadErr
	}
	kubevirtCreateVM = func(opts types.CreateOpts) error {
		cp := opts
		s.created = &cp
		return s.createErr
	}
	t.Cleanup(func() { kubevirtUpload, kubevirtCreateVM = oldUpload, oldCreate })

	fake := shell.NewFake()
	fake.AddPrefixResponse("kubectl get sc", `{"items":[]}`, nil)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() { kubevirt.SetPackageRunner(shell.Real{}) })
}

func TestKubevirtIngest_UploadsThenCreatesFromThePVC(t *testing.T) {
	s := &kubevirtStubs{}
	stubKubevirt(t, s)

	ref := types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"}
	shape := Shape{CPU: 2, Mem: "4Gi", Disk: "30Gi", UEFI: true, SSHKey: "ssh-ed25519 AAA"}
	if err := (kubevirtAdapter{}).Ingest(ref, "/tmp/web-1.qcow2", shape); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if s.uploadArgs == nil {
		t.Fatal("no upload was attempted")
	}
	up := s.uploadArgs
	if up.claim != "web-1-disk" {
		t.Errorf("upload claim = %q, want %q", up.claim, "web-1-disk")
	}
	if up.namespace != "corral-vms" {
		t.Errorf("upload namespace = %q, want corral-vms", up.namespace)
	}
	if up.path != "/tmp/web-1.qcow2" || up.size != "30Gi" {
		t.Errorf("upload path/size = %q/%q, want /tmp/web-1.qcow2/30Gi", up.path, up.size)
	}

	if s.created == nil {
		t.Fatal("no VM was created after the upload")
	}
	got := s.created
	if got.PVC != "web-1-disk" {
		t.Errorf("VM PVC = %q, want the uploaded claim", got.PVC)
	}
	if got.ImportURL != "" {
		t.Errorf("VM ImportURL = %q: the disk is already in the cluster, CDI must not fetch it again", got.ImportURL)
	}
	if got.Namespace != "corral-vms" || got.Name != "web-1" {
		t.Errorf("VM identity = %s/%s, want corral-vms/web-1", got.Namespace, got.Name)
	}
	if !got.UEFI {
		t.Error("UEFI shape was not passed through to the VM")
	}
	if got.SSHPublicKey != "ssh-ed25519 AAA" {
		t.Errorf("SSH key = %q, want the shape's key", got.SSHPublicKey)
	}
	if got.CPU != 2 || got.Mem != "4Gi" || got.Disk != "30Gi" {
		t.Errorf("CPU/Mem/Disk = %d/%s/%s, want 2/4Gi/30Gi", got.CPU, got.Mem, got.Disk)
	}
}

// An empty namespace or disk size must fall back, never reach the cluster as
// "" — an upload with no size provisions nothing useful.
func TestKubevirtIngest_DefaultsNamespaceAndSize(t *testing.T) {
	s := &kubevirtStubs{}
	stubKubevirt(t, s)

	ref := types.InstanceRef{Backend: "kubevirt", Name: "web-1"}
	if err := (kubevirtAdapter{}).Ingest(ref, "/tmp/web-1.qcow2", Shape{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if s.uploadArgs.namespace != kubevirt.DefaultNamespace {
		t.Errorf("namespace = %q, want the default %q", s.uploadArgs.namespace, kubevirt.DefaultNamespace)
	}
	if s.uploadArgs.size != "20Gi" {
		t.Errorf("size = %q, want 20Gi", s.uploadArgs.size)
	}
	if s.created.Disk != "20Gi" || s.created.Namespace != kubevirt.DefaultNamespace {
		t.Errorf("created VM disk/namespace = %s/%s", s.created.Disk, s.created.Namespace)
	}
}

// A failed upload must abort before any VM exists: the create call would adopt
// a PVC that was never populated.
func TestKubevirtIngest_UploadFailureAbortsBeforeCreate(t *testing.T) {
	s := &kubevirtStubs{uploadErr: errors.New("virtctl: connection refused")}
	stubKubevirt(t, s)

	err := (kubevirtAdapter{}).Ingest(
		types.InstanceRef{Backend: "kubevirt", Namespace: "corral-vms", Name: "web-1"},
		"/tmp/web-1.qcow2", Shape{Disk: "30Gi"})
	if err == nil {
		t.Fatal("a failed upload must fail the ingest")
	}
	if !strings.Contains(err.Error(), "corral-vms/web-1-disk") {
		t.Errorf("error should name the claim it failed to fill, got: %v", err)
	}
	if s.created != nil {
		t.Error("a VM was created from an upload that never happened")
	}
}

// The manifest the create call produces: rootdisk bound to the uploaded claim
// (not an import source), EFI firmware when the shape asked for UEFI, and the
// SSH key in cloud-init userData.
func TestKubevirtIngest_ManifestBindsClaimAndFirmware(t *testing.T) {
	for _, uefi := range []bool{true, false} {
		opts := types.CreateOpts{
			Name: "web-1", Namespace: "corral-vms", Backend: "kubevirt",
			CPU: 2, Mem: "4Gi", Disk: "30Gi",
			PVC: "web-1-disk", SSHPublicKey: "ssh-ed25519 AAA", UEFI: uefi,
		}
		vm := kubevirt.GenerateVM(opts)
		rawBytes, _ := json.Marshal(vm)
		raw := string(rawBytes)

		if !strings.Contains(raw, `"claimName":"web-1-disk"`) {
			t.Errorf("UEFI=%v: manifest does not bind the uploaded claim:\n%s", uefi, raw)
		}
		if strings.Contains(raw, "import") && strings.Contains(raw, "http") {
			t.Errorf("UEFI=%v: manifest imports from a URL instead of adopting the PVC:\n%s", uefi, raw)
		}
		if hasEFI := strings.Contains(raw, `"efi"`); hasEFI != uefi {
			t.Errorf("UEFI=%v: manifest efi present = %v", uefi, hasEFI)
		}
		if !strings.Contains(raw, "ssh-ed25519 AAA") {
			t.Errorf("UEFI=%v: SSH key missing from cloud-init:\n%s", uefi, raw)
		}
	}
}

// The real CreateVM against fakes: the kubectl apply on the wire must carry
// the uploaded claim, in the destination namespace — the argv-level proof that
// the PVC path (not ImportURL) is what reaches the cluster.
func TestKubevirtIngest_CreateVMAppliesTheUploadedClaim(t *testing.T) {
	pkgFake := shell.NewFake()
	pkgFake.AddPrefixResponse("kubectl", "", nil)
	kubevirt.SetPackageRunner(pkgFake)
	t.Cleanup(func() { kubevirt.SetPackageRunner(shell.Real{}) })

	applyFake := shell.NewFake()
	applyFake.AddPrefixResponse("kubectl apply", "virtualmachine.created", nil)
	kubevirt.SetApplyRunner(applyFake)
	t.Cleanup(func() { kubevirt.SetApplyRunner(shell.Real{}) })

	err := kubevirt.CreateVM(types.CreateOpts{
		Name: "web-1", Namespace: "corral-vms", Backend: "kubevirt",
		CPU: 2, Mem: "4Gi", Disk: "30Gi",
		PVC: "web-1-disk", SSHPublicKey: "ssh-ed25519 AAA", UEFI: true,
	})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	var applied *shell.Call
	for _, c := range applyFake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			cp := c
			applied = &cp
		}
	}
	if applied == nil {
		t.Fatalf("no kubectl apply reached the cluster (calls: %v)", applyFake.Calls())
	}
	if !strings.Contains(applied.Stdin, "web-1-disk") {
		t.Errorf("applied manifest does not name the uploaded claim:\n%s", applied.Stdin)
	}
	if !strings.Contains(applied.Stdin, "corral-vms") {
		t.Errorf("applied manifest is not in the destination namespace:\n%s", applied.Stdin)
	}
	if !strings.Contains(applied.Stdin, "efi") {
		t.Errorf("applied manifest drops the UEFI firmware request:\n%s", applied.Stdin)
	}
}
