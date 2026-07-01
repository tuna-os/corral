// Package cronops builds the Kubernetes manifests the scheduled-operation
// plugins (corral-snapsched, corral-schedule) apply: a namespaced
// ServiceAccount/Role/RoleBinding plus CronJobs whose pods run kubectl
// one-liners against the VM. Everything is plain manifests — the cluster does
// the scheduling, so the plugins work even when no workstation is online.
package cronops

import "fmt"

// KubectlImage runs the CronJob pods. bitnami/kubectl is multi-arch and ships
// a shell, which the snapshot-prune pipeline needs.
// KubectlImage runs the CronJob pods. Pinned to a specific digest to avoid
// supply-chain risk from mutable :latest tags.
const KubectlImage = "docker.io/bitnami/kubectl@sha256:5d4034684c0edacc9d51a6555dd2a62d3b250bc994e742a7ce463e1c1cb09c80"

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

// Rules covers both plugins: snapshot lifecycle and VM start/stop patches.
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
	all := map[string]string{ManagedLabel: "true"}
	for k, v := range labels {
		all[k] = v
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
						"spec": map[string]any{
							"serviceAccountName": RBACName,
							"restartPolicy":      "Never",
							"containers": []map[string]any{{
								"name":    "kubectl",
								"image":   KubectlImage,
								"command": []string{"/bin/sh", "-ec", script},
							}},
						},
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
