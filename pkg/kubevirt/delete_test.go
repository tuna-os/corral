package kubevirt

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// purgeFake answers every kubectl delete and get with nothing, so a test
// registers only the answers it cares about.
func purgeFake(t *testing.T) (*Client, *shell.Fake) {
	t.Helper()
	r := shell.NewFake()
	r.AddPrefixResponse("kubectl delete", "", nil)
	r.AddPrefixResponse("kubectl get", "", nil)
	return NewClientWithRunner("corral-vms", r), r
}

func callLines(r *shell.Fake) []string {
	var out []string
	for _, c := range r.Calls() {
		out = append(out, c.Name+" "+strings.Join(c.Args, " "))
	}
	return out
}

const delFlags = " -n corral-vms --ignore-not-found --wait=false -o name"

// Every object corral creates for a VM is deleted by name, and the
// labelled kinds by selector, so a VM from before the label still goes.
func TestPurgeVM_DeletesEverythingCorralCreated(t *testing.T) {
	c, r := purgeFake(t)
	if _, err := c.PurgeVM("web"); err != nil {
		t.Fatal(err)
	}
	calls := callLines(r)
	for _, want := range []string{
		"kubectl delete vm web" + delFlags,
		"kubectl delete vm web-bootc-builder" + delFlags,
		"kubectl delete vmi web web-bootc-builder" + delFlags,
		"kubectl delete datavolume web-bootc-disk web-disk web-data web-iso" + delFlags,
		"kubectl delete pvc web-bootc-disk web-disk web-data web-iso" + delFlags,
		"kubectl delete secret web-cloudinit web-bootc-builder-cloudinit corral-backup-rclone-web" + delFlags,
		"kubectl delete configmap web-autounattend" + delFlags,
		"kubectl delete pod web-sizecheck" + delFlags,
		"kubectl delete deployment web-proxy" + delFlags,
		"kubectl delete service web-proxy web-lan" + delFlags,
		"kubectl delete serviceaccount corral-web-proxy" + delFlags,
		"kubectl delete role corral-web-proxy" + delFlags,
		"kubectl delete rolebinding corral-web-proxy" + delFlags,
		"kubectl delete cronjob corral-start-web corral-stop-web corral-snap-web corral-backup-web" + delFlags,
		"kubectl delete service,deployment,serviceaccount -n corral-vms -l corral.dev/vm=web --ignore-not-found --wait=false -o name",
		"kubectl delete vmsnapshot -n corral-vms -l corral.dev/vm=web --ignore-not-found --wait=false -o name",
		"kubectl delete vmrestore -n corral-vms -l corral.dev/vm=web --ignore-not-found --wait=false -o name",
		"kubectl delete vmclone -n corral-vms -l corral.dev/vm=web --ignore-not-found --wait=false -o name",
	} {
		if !slices.Contains(calls, want) {
			t.Errorf("PurgeVM did not run %q", want)
		}
	}
	// The VM goes first so its guest releases the disks, and the
	// DataVolume before its PVC so a running importer stops.
	vm := slices.Index(calls, "kubectl delete vm web"+delFlags)
	dv := slices.Index(calls, "kubectl delete datavolume web-bootc-disk web-disk web-data web-iso"+delFlags)
	pvc := slices.Index(calls, "kubectl delete pvc web-bootc-disk web-disk web-data web-iso"+delFlags)
	if !(vm < dv && dv < pvc) {
		t.Errorf("delete order vm=%d dv=%d pvc=%d, want vm < dv < pvc", vm, dv, pvc)
	}
}

// Removed is what kubectl reports it deleted, once each, without its notices.
func TestPurgeVM_ReportsRemovedObjects(t *testing.T) {
	c, r := purgeFake(t)
	r.AddResponse("kubectl delete vm web"+delFlags, "virtualmachine.kubevirt.io/web\n", nil)
	r.AddResponse("kubectl delete vm web-bootc-builder"+delFlags, "virtualmachine.kubevirt.io/web-bootc-builder\n", nil)
	r.AddResponse("kubectl delete pvc web-bootc-disk web-disk web-data web-iso"+delFlags,
		"persistentvolumeclaim/web-bootc-disk\n", nil)
	r.AddResponse("kubectl delete service web-proxy web-lan"+delFlags, "service/web-proxy\n", nil)
	// The selector matches the Service the delete by name left terminating.
	r.AddResponse("kubectl delete service,deployment,serviceaccount -n corral-vms -l corral.dev/vm=web --ignore-not-found --wait=false -o name",
		"service/web-proxy\n", nil)
	r.AddResponse("kubectl delete vmsnapshot -n corral-vms -l corral.dev/vm=web --ignore-not-found --wait=false -o name",
		"No resources found\n", nil)
	removed, err := c.PurgeVM("web")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"virtualmachine.kubevirt.io/web",
		"virtualmachine.kubevirt.io/web-bootc-builder",
		"persistentvolumeclaim/web-bootc-disk",
		"service/web-proxy",
	}
	if !slices.Equal(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
}

// A real failure is returned and the remaining steps still run; a kind the
// cluster does not serve is not a failure.
func TestPurgeVM_SurfacesErrorsAndContinues(t *testing.T) {
	c, r := purgeFake(t)
	r.AddResponse("kubectl delete pvc web-bootc-disk web-disk web-data web-iso"+delFlags,
		`Error from server (Forbidden): persistentvolumeclaims "web-disk" is forbidden`, errors.New("exit status 1"))
	r.AddResponse("kubectl delete vmrestore -n corral-vms -l corral.dev/vm=web --ignore-not-found --wait=false -o name",
		`error: the server doesn't have a resource type "vmrestore"`, errors.New("exit status 1"))
	_, err := c.PurgeVM("web")
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("err = %v, want the PVC failure", err)
	}
	if strings.Contains(err.Error(), "vmrestore") {
		t.Errorf("err = %v, a kind the cluster does not serve is not a failure", err)
	}
	if !slices.Contains(callLines(r), "kubectl delete vmclone -n corral-vms -l corral.dev/vm=web --ignore-not-found --wait=false -o name") {
		t.Error("PurgeVM stopped at the first failure")
	}
}

// A labelled disk that another VM mounts stays: a clone's disks carry the
// source VM's labels.
func TestPurgeVM_KeepsLabelledDisksOtherVMsMount(t *testing.T) {
	c, r := purgeFake(t)
	r.AddResponse(`kubectl get pvc -n corral-vms -l corral.dev/vm=web -o jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`,
		"web-data\nweb-hp-1\nrestore-abc-rootdisk\n", nil)
	r.AddResponse("kubectl get vm -n corral-vms -o json", `{"items":[
		{"metadata":{"name":"web"},"spec":{"template":{"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"web-hp-1"}}]}}}},
		{"metadata":{"name":"copy"},"spec":{"template":{"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"restore-abc-rootdisk"}}]}}}}
	]}`, nil)
	if _, err := c.PurgeVM("web"); err != nil {
		t.Fatal(err)
	}
	calls := callLines(r)
	if !slices.Contains(calls, "kubectl delete pvc web-hp-1"+delFlags) {
		t.Errorf("the hotplugged disk was not deleted; calls %v", calls)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "kubectl delete") && strings.Contains(call, "restore-abc-rootdisk") {
			t.Errorf("deleted a disk another VM mounts: %q", call)
		}
	}
}

// scriptedRunner answers each call with the next of its answers.
type scriptedRunner struct {
	answers []string
	calls   int
}

func (s *scriptedRunner) Run(string, ...string) ([]byte, error) {
	i := min(s.calls, len(s.answers)-1)
	s.calls++
	return []byte(s.answers[i]), nil
}
func (s *scriptedRunner) RunStdin(_ string, name string, args ...string) ([]byte, error) {
	return s.Run(name, args...)
}
func (s *scriptedRunner) LookPath(name string) (string, error) { return "/fake/bin/" + name, nil }

func fastDeletePolls(t *testing.T) {
	t.Helper()
	old := DeletePollInterval
	DeletePollInterval = time.Millisecond
	t.Cleanup(func() { DeletePollInterval = old })
}

func TestWaitRemoved_PollsUntilGone(t *testing.T) {
	fastDeletePolls(t)
	r := &scriptedRunner{answers: []string{
		"virtualmachine.kubevirt.io/web\npersistentvolumeclaim/web-disk\n",
		"persistentvolumeclaim/web-disk\n",
		"",
	}}
	c := NewClientWithRunner("corral-vms", r)
	err := c.WaitRemoved([]string{"virtualmachine.kubevirt.io/web", "persistentvolumeclaim/web-disk"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 3 {
		t.Errorf("polled %d times, want 3", r.calls)
	}
}

func TestWaitRemoved_TimesOutNamingWhatRemains(t *testing.T) {
	fastDeletePolls(t)
	r := &scriptedRunner{answers: []string{"persistentvolumeclaim/web-disk\n"}}
	c := NewClientWithRunner("corral-vms", r)
	err := c.WaitRemoved([]string{"persistentvolumeclaim/web-disk"}, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "persistentvolumeclaim/web-disk") {
		t.Fatalf("err = %v, want the PVC named", err)
	}
}

func TestWaitRemoved_AsksForExactlyTheRemovedObjects(t *testing.T) {
	r := shell.NewFake()
	r.AddResponse("kubectl get virtualmachine.kubevirt.io/web secret/web-cloudinit -n corral-vms --ignore-not-found -o name", "", nil)
	c := NewClientWithRunner("corral-vms", r)
	if err := c.WaitRemoved([]string{"virtualmachine.kubevirt.io/web", "secret/web-cloudinit"}, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitRemoved(nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls()) != 1 {
		t.Errorf("calls = %v, want one get and none for nothing removed", r.Calls())
	}
}

// Objects corral creates for a VM carry VMLabel; the VM itself does not.
func TestVMLabelOnCreatedObjects(t *testing.T) {
	labels := func(obj map[string]any) map[string]any {
		l, _ := obj["metadata"].(map[string]any)["labels"].(map[string]any)
		return l
	}
	pvc := labelForVM(GeneratePVC("web-disk", "ns", "1Gi"), "web")
	if labels(pvc)[VMLabel] != "web" || labels(pvc)[ManagedLabel] != "true" {
		t.Errorf("PVC labels = %v, want %s=web next to %s", labels(pvc), VMLabel, ManagedLabel)
	}
	if l := labels(labelForVM(GenerateDataVolume("web-iso", "ns", "http://x/iso", ""), "web")); l[VMLabel] != "web" {
		t.Errorf("DataVolume labels = %v", l)
	}
	if l := labels(GenerateAutounattendConfigMap("web", "ns", "<xml/>")); l[VMLabel] != "web" {
		t.Errorf("ConfigMap labels = %v", l)
	}
	for _, obj := range []map[string]any{GenerateProxyService("web", "ns", []int{22}), GenerateProxyDeployment("web", "ns", []int{22}), GenerateLANService("web", "ns", []int{22})} {
		if l, _ := obj["metadata"].(map[string]any)["labels"].(map[string]string); l[VMLabel] != "web" {
			t.Errorf("%s labels = %v", obj["kind"], l)
		}
	}
	if rbac := GenerateProxyRBAC("web", "ns"); strings.Count(rbac, VMLabel+`: "web"`) != 3 {
		t.Errorf("proxy RBAC does not label all three objects:\n%s", rbac)
	}
	if l := labels(GenerateVM(types.CreateOpts{Name: "web", Namespace: "ns"})); l[VMLabel] != nil {
		t.Errorf("VM carries %s; a clone would copy it and match its source", VMLabel)
	}
}
