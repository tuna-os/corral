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

// backupPodSpec digs the pod spec out of a BackupCronJob manifest.
func backupPodSpec(t *testing.T) map[string]any {
	t.Helper()
	cj := BackupCronJob("corral-backup-web", "tailvm", "0 3 * * *", "echo hi",
		nil, "corral-backup-rclone-config", "/etc/rclone")
	var m struct {
		Spec struct {
			JobTemplate struct {
				Spec struct {
					Template struct {
						Spec map[string]any `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			} `json:"jobTemplate"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(marshal(t, cj)), &m); err != nil {
		t.Fatal(err)
	}
	return m.Spec.JobTemplate.Spec.Template.Spec
}

func TestBackupCronJob_MountsSecretReadOnly(t *testing.T) {
	s := marshal(t, backupPodSpec(t))
	for _, want := range []string{
		`"secretName":"corral-backup-rclone-config"`,
		`"mountPath":"/etc/rclone"`,
		`"readOnly":true`,
		`"name":"RCLONE_CONFIG","value":"/etc/rclone/rclone.conf"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("BackupCronJob missing %q: %s", want, s)
		}
	}
}

// #313: the backup pod must not depend on its images' default user, and
// must get rclone from its image instead of installing it at runtime.
func TestBackupCronJob_NonRootWithToolsVolume(t *testing.T) {
	spec := backupPodSpec(t)
	s := marshal(t, spec)
	for _, want := range []string{
		`"runAsNonRoot":true`,
		`"runAsUser":1001`,
		`"allowPrivilegeEscalation":false`,
		`"emptyDir":{}`,
		`"mountPath":"` + ToolsDir + `"`,
		`"name":"PATH","value":"` + ToolsDir + `:`,
		`"serviceAccountName":"` + RBACName + `"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("BackupCronJob pod spec missing %q: %s", want, s)
		}
	}
	inits := spec["initContainers"].([]any)
	if len(inits) != 1 || inits[0].(map[string]any)["image"] != KubectlImage {
		t.Errorf("init container should run %s: %v", KubectlImage, inits)
	}
	ctrs := spec["containers"].([]any)
	if len(ctrs) != 1 || ctrs[0].(map[string]any)["image"] != BackupImage {
		t.Errorf("main container should run %s: %v", BackupImage, ctrs)
	}
	for _, img := range []string{KubectlImage, BackupImage} {
		if !strings.Contains(img, "@sha256:") {
			t.Errorf("image %s is not digest-pinned", img)
		}
	}
}

func TestToolsScript(t *testing.T) {
	s := ToolsScript()
	for _, want := range []string{
		"observedKubeVirtVersion",
		"aarch64|arm64) ARCH=arm64",
		"curl -fsSL -o /tools/virtctl",
		"virtctl-${KV_VERSION}-linux-${ARCH}",
		"chmod +x /tools/virtctl",
		`cp "$(command -v kubectl)" /tools/kubectl`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ToolsScript missing %q:\n%s", want, s)
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

func TestRole_CoversExportAndPortForward(t *testing.T) {
	s := marshal(t, Role("tailvm"))
	for _, want := range []string{
		"export.kubevirt.io", "virtualmachineexports",
		"pods/portforward",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Role missing %q: %s", want, s)
		}
	}
}

func TestBackupScript(t *testing.T) {
	s := BackupScript("web", "tailvm", "r2:backups/corral", 5)
	for _, want := range []string{
		"kubectl get vm web -n tailvm",
		"persistentVolumeClaim.claimName",
		"virtctl vmexport download web-export",
		"--namespace=tailvm", "--vm=web",
		`rclone copyto /tmp/"$FNAME" "r2:backups/corral/$FNAME"`,
		`grep "^web-"`,
		"head -n -5",
		"rclone deletefile",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("BackupScript missing %q:\n%s", want, s)
		}
	}
	// #313: the pod runs as non-root, so neither script may write outside
	// ToolsDir and /tmp, or pipe an installer into a shell.
	for _, script := range []string{s, ToolsScript()} {
		for _, bad := range []string{"/usr/local/bin", "/usr/bin", "install.sh", "| bash", "| sh"} {
			if strings.Contains(script, bad) {
				t.Errorf("script contains %q:\n%s", bad, script)
			}
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
