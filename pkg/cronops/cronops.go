// Package cronops builds the Kubernetes manifests the scheduled-operation
// plugins (corral-snapsched, corral-schedule) apply: a namespaced
// ServiceAccount/Role/RoleBinding plus CronJobs whose pods run kubectl
// one-liners against the VM. Everything is plain manifests — the cluster does
// the scheduling, so the plugins work even when no workstation is online.
package cronops

import "fmt"

// KubectlImage runs the CronJob pods. bitnami/kubectl is multi-arch and ships
// a shell, which the snapshot-prune pipeline needs. Digest-pinned (see #66)
// — this image's pods run as the corral-sched ServiceAccount with real
// cluster credentials (VM patch, export, snapshot create/delete), so a
// supply-chain push to the mutable :latest tag would run attacker code with
// that access.
const KubectlImage = "docker.io/bitnami/kubectl:latest@sha256:cd9daa0cc6968665402654b887bdc59aba0f774d0d0a36808eb9259fb642aa5c"

// ManagedLabel marks every object the scheduled-ops plugins create.
const ManagedLabel = "corral.dev/scheduled-op"

// RBACName is the shared ServiceAccount/Role/RoleBinding name per namespace.
const RBACName = "corral-sched"

func meta(name, ns string, labels map[string]string) map[string]any {
	m := map[string]any{"name": name, "namespace": ns}
	if len(labels) > 0 {
		l := map[string]any{}
		for k, v := range labels {
			l[k] = v
		}
		m["labels"] = l
	}
	return m
}

// PolicyRule is one RBAC rule.
type PolicyRule struct {
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

// Rules covers all scheduled-op plugins sharing the corral-sched Role:
// snapshot lifecycle, VM start/stop patches, and (for corral-backup)
// VMExport lifecycle + the pod portforward subresource virtctl's
// --port-forward export path needs.
func Rules() []PolicyRule {
	return []PolicyRule{
		{
			APIGroups: []string{"snapshot.kubevirt.io"},
			Resources: []string{"virtualmachinesnapshots"},
			Verbs:     []string{"create", "get", "list", "delete"},
		},
		{
			APIGroups: []string{"kubevirt.io"},
			Resources: []string{"virtualmachines"},
			Verbs:     []string{"get", "patch"},
		},
		{
			APIGroups: []string{"export.kubevirt.io"},
			Resources: []string{"virtualmachineexports"},
			Verbs:     []string{"create", "get", "list", "delete"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods/portforward"},
			Verbs:     []string{"create"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods"},
			Verbs:     []string{"get", "list"},
		},
	}
}

// ServiceAccount manifest.
func ServiceAccount(ns string) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "ServiceAccount",
		"metadata":   meta(RBACName, ns, nil),
	}
}

// Role manifest with Rules().
func Role(ns string) map[string]any {
	rules := []map[string]any{}
	for _, r := range Rules() {
		rules = append(rules, map[string]any{
			"apiGroups": r.APIGroups,
			"resources": r.Resources,
			"verbs":     r.Verbs,
		})
	}
	return map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "Role",
		"metadata":   meta(RBACName, ns, nil),
		"rules":      rules,
	}
}

// RoleBinding manifest binding the Role to the ServiceAccount.
func RoleBinding(ns string) map[string]any {
	return map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "RoleBinding",
		"metadata":   meta(RBACName, ns, nil),
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "Role",
			"name":     RBACName,
		},
		"subjects": []map[string]any{
			{"kind": "ServiceAccount", "name": RBACName, "namespace": ns},
		},
	}
}

// CronJob wraps a shell script in a CronJob running as the corral-sched SA.
// labels are added on top of the ManagedLabel marker.
func CronJob(name, ns, schedule, script string, labels map[string]string) map[string]any {
	return cronJob(name, ns, schedule, script, labels, nil, nil)
}

// CronJobWithSecret is CronJob, plus a Secret mounted read-only at
// mountPath — corral-backup's schedule needs the caller's rclone config
// (remote credentials) available inside the CronJob pod, which plain
// CronJob has no way to express.
func CronJobWithSecret(name, ns, schedule, script string, labels map[string]string, secretName, mountPath string) map[string]any {
	volumes := []map[string]any{
		{"name": "rclone-config", "secret": map[string]any{"secretName": secretName}},
	}
	volumeMounts := []map[string]any{
		{"name": "rclone-config", "mountPath": mountPath, "readOnly": true},
	}
	return cronJob(name, ns, schedule, script, labels, volumes, volumeMounts)
}

func cronJob(name, ns, schedule, script string, labels map[string]string, volumes, volumeMounts []map[string]any) map[string]any {
	all := map[string]string{ManagedLabel: "true"}
	for k, v := range labels {
		all[k] = v
	}
	container := map[string]any{
		"name":    "kubectl",
		"image":   KubectlImage,
		"command": []string{"/bin/sh", "-ec", script},
	}
	if len(volumeMounts) > 0 {
		container["volumeMounts"] = volumeMounts
	}
	podSpec := map[string]any{
		"serviceAccountName": RBACName,
		"restartPolicy":      "Never",
		"containers":         []map[string]any{container},
	}
	if len(volumes) > 0 {
		podSpec["volumes"] = volumes
	}
	return map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   meta(name, ns, all),
		"spec": map[string]any{
			"schedule":                   schedule,
			"concurrencyPolicy":          "Forbid",
			"successfulJobsHistoryLimit": 1,
			"failedJobsHistoryLimit":     2,
			"jobTemplate": map[string]any{
				"spec": map[string]any{
					"backoffLimit":            1,
					"ttlSecondsAfterFinished": 3600,
					"template": map[string]any{
						"metadata": map[string]any{"labels": map[string]any{ManagedLabel: "true"}},
						"spec":     podSpec,
					},
				},
			},
		},
	}
}

// SnapshotScript creates one VirtualMachineSnapshot labeled for the VM, then
// prunes the oldest auto-snapshots beyond keep.
func SnapshotScript(vm, ns string, keep int) string {
	return fmt.Sprintf(`ts=$(date +%%Y%%m%%d%%H%%M%%S)
kubectl create -f - <<EOF
apiVersion: snapshot.kubevirt.io/v1beta1
kind: VirtualMachineSnapshot
metadata:
  name: %[1]s-auto-$ts
  namespace: %[2]s
  labels:
    corral.dev/auto-snap: %[1]s
spec:
  source:
    apiGroup: kubevirt.io
    kind: VirtualMachine
    name: %[1]s
EOF
kubectl get vmsnapshot -n %[2]s -l corral.dev/auto-snap=%[1]s \
  --sort-by=.metadata.creationTimestamp -o name | head -n -%[3]d \
  | xargs -r kubectl delete -n %[2]s`, vm, ns, keep)
}

// BackupScript exports the VM's primary disk (gzip), rclone-copies it to
// dest, then prunes older backups for this VM beyond keep. Runs in the
// KubectlImage CronJob pod, which has kubectl + a shell but not virtctl or
// rclone — both are fetched at runtime (matching the existing pattern in
// pkg/kubevirt's proxy Deployment script, which likewise `apk add`s its own
// tools rather than requiring a bespoke pre-built image) rather than
// requiring a new corral-owned container image. rclone needs its remote's
// credentials — see cmd/corral-backup's `schedule` command, which mounts
// the caller's local rclone config as a Secret at
// /root/.config/rclone/rclone.conf before this script ever runs.
func BackupScript(vm, ns, dest string, keep int) string {
	return fmt.Sprintf(`KV_VERSION=$(kubectl get kubevirt kubevirt -n kubevirt -o jsonpath='{.status.observedKubeVirtVersion}')
curl -sL -o /usr/local/bin/virtctl "https://github.com/kubevirt/kubevirt/releases/download/${KV_VERSION}/virtctl-${KV_VERSION}-linux-amd64"
chmod +x /usr/local/bin/virtctl
curl -s https://rclone.org/install.sh | bash >/dev/null

VOL=$(kubectl get vm %[1]s -n %[2]s -o jsonpath='{.spec.template.spec.volumes[?(@.persistentVolumeClaim)].persistentVolumeClaim.claimName}' | awk '{print $1}')
if [ -z "$VOL" ]; then echo "no persistent disk on %[1]s — nothing to back up" >&2; exit 1; fi

TS=$(date +%%Y%%m%%d%%H%%M%%S)
FNAME="%[1]s-$TS.img.gz"
kubectl delete vmexport %[1]s-export -n %[2]s --ignore-not-found
virtctl vmexport download %[1]s-export --namespace=%[2]s --vm=%[1]s --volume="$VOL" \
  --output=/tmp/"$FNAME" --format=gzip --insecure --port-forward
rclone copyto /tmp/"$FNAME" "%[3]s/$FNAME"
rm -f /tmp/"$FNAME"
kubectl delete vmexport %[1]s-export -n %[2]s --ignore-not-found

rclone lsf "%[3]s" --files-only | grep "^%[1]s-" | sort | head -n -%[4]d | while IFS= read -r f; do
  [ -n "$f" ] && rclone deletefile "%[3]s/$f"
done`, vm, ns, dest, keep)
}

// PowerScript flips the VM's runStrategy. It clears the legacy spec.running
// field in the same merge patch — a VM may use either style, never both.
func PowerScript(vm, ns string, start bool) string {
	strategy := "Halted"
	if start {
		strategy = "Always"
	}
	return fmt.Sprintf(
		`kubectl patch vm %s -n %s --type merge -p '{"spec":{"running":null,"runStrategy":"%s"}}'`,
		vm, ns, strategy)
}
