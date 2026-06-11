package cronops

import (
	"encoding/json"
	"strings"
	"testing"
)

// marshal round-trips a manifest so tests can navigate it as generic JSON.
func marshal(t *testing.T, obj map[string]any) string {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("manifest does not marshal: %v", err)
	}
	return string(b)
}

func TestServiceAccount(t *testing.T) {
	sa := ServiceAccount("tailvm")
	if sa["kind"] != "ServiceAccount" {
		t.Errorf("kind = %v", sa["kind"])
	}
	m := sa["metadata"].(map[string]any)
	if m["name"] != RBACName || m["namespace"] != "tailvm" {
		t.Errorf("metadata = %v", m)
	}
}

func TestRole_CoversSnapshotsAndVMs(t *testing.T) {
	s := marshal(t, Role("tailvm"))
	for _, want := range []string{
		"snapshot.kubevirt.io", "virtualmachinesnapshots",
		`"kubevirt.io"`, "virtualmachines", "patch", "create", "delete",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Role missing %q: %s", want, s)
		}
	}
}

func TestRoleBinding_BindsSA(t *testing.T) {
	rb := RoleBinding("tailvm")
	s := marshal(t, rb)
	if !strings.Contains(s, `"kind":"ServiceAccount"`) || !strings.Contains(s, RBACName) {
		t.Errorf("RoleBinding does not bind the SA: %s", s)
	}
}

func TestCronJob_Shape(t *testing.T) {
	cj := CronJob("corral-snap-web", "tailvm", "0 3 * * *", "echo hi",
		map[string]string{"corral.dev/snapsched": "web"})
	s := marshal(t, cj)
	for _, want := range []string{
		`"kind":"CronJob"`,
		`"schedule":"0 3 * * *"`,
		`"serviceAccountName":"` + RBACName + `"`,
		`"concurrencyPolicy":"Forbid"`,
		KubectlImage,
		`"corral.dev/snapsched":"web"`,
		ManagedLabel,
		"echo hi",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("CronJob missing %q", want)
		}
	}
}

func TestSnapshotScript(t *testing.T) {
	s := SnapshotScript("web", "tailvm", 7)
	for _, want := range []string{
		"VirtualMachineSnapshot",
		"name: web-auto-$ts",
		"namespace: tailvm",
		"corral.dev/auto-snap: web",
		"--sort-by=.metadata.creationTimestamp",
		"head -n -7",
		"xargs -r kubectl delete -n tailvm",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("SnapshotScript missing %q:\n%s", want, s)
		}
	}
}

func TestPowerScript(t *testing.T) {
	start := PowerScript("web", "tailvm", true)
	stop := PowerScript("web", "tailvm", false)
	if !strings.Contains(start, `"runStrategy":"Always"`) {
		t.Errorf("start script: %s", start)
	}
	if !strings.Contains(stop, `"runStrategy":"Halted"`) {
		t.Errorf("stop script: %s", stop)
	}
	// Both must clear the legacy running field — a VM can't have both styles.
	for _, s := range []string{start, stop} {
		if !strings.Contains(s, `"running":null`) {
			t.Errorf("script does not clear spec.running: %s", s)
		}
	}
}
