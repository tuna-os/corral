package vdi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tuna-os/corral/pkg/ct"
)

// ctDeleteTimeout bounds each `kubectl delete --wait` of a member object.
// Waiting matters: a released member is recreated under the same name, and
// a PVC that is still terminating would swallow the new one.
const ctDeleteTimeout = 2 * time.Minute

// kubeCTCluster implements ctCluster with kubectl (the package runner) and
// pkg/ct. It only touches Kubernetes CTs; Incus containers never join a
// CT pool.
type kubeCTCluster struct{}

func ctPoolConfigName(pool string) string { return ctPoolConfigPrefix + pool }

type ctPoolConfigMap struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

func (c ctPoolConfigMap) decode() (CTPoolSpec, CTPoolStatus, error) {
	var spec CTPoolSpec
	var status CTPoolStatus
	if err := json.Unmarshal([]byte(c.Data["spec"]), &spec); err != nil {
		return spec, status, fmt.Errorf("CT pool definition %s/%s is corrupt: %w", c.Metadata.Namespace, c.Metadata.Name, err)
	}
	// The object's identity wins over whatever the spec text claims.
	spec.Name, spec.Namespace = c.Metadata.Labels[labelCTPool], c.Metadata.Namespace
	if raw := c.Data["status"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &status); err != nil {
			status.Errors = []string{"recorded status is corrupt: " + err.Error()}
		}
	}
	return spec, status, nil
}

func (kubeCTCluster) GetPool(namespace, name string) (CTPoolSpec, CTPoolStatus, bool, error) {
	out, err := run("kubectl", "get", "configmap", ctPoolConfigName(name), "-n", namespace, "--ignore-not-found", "-o", "json")
	if err != nil {
		return CTPoolSpec{}, CTPoolStatus{}, false, fmt.Errorf("read CT pool %q: %w", name, err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return CTPoolSpec{}, CTPoolStatus{}, false, nil
	}
	var cm ctPoolConfigMap
	if err := json.Unmarshal(out, &cm); err != nil {
		return CTPoolSpec{}, CTPoolStatus{}, false, fmt.Errorf("decode CT pool %q: %w", name, err)
	}
	spec, status, err := cm.decode()
	return spec, status, err == nil, err
}

func (kubeCTCluster) ListPools(namespace string) ([]CTPoolSpec, []CTPoolStatus, error) {
	args := []string{"get", "configmap", "-l", labelCTPool, "-o", "json"}
	if namespace == "" {
		args = append(args, "-A")
	} else {
		args = append(args, "-n", namespace)
	}
	out, err := run("kubectl", args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list CT pools: %w", err)
	}
	var res struct {
		Items []ctPoolConfigMap `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, nil, fmt.Errorf("decode CT pools: %w", err)
	}
	var specs []CTPoolSpec
	var statuses []CTPoolStatus
	var errs []error
	for _, cm := range res.Items {
		spec, status, err := cm.decode()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		specs, statuses = append(specs, spec), append(statuses, status)
	}
	return specs, statuses, errors.Join(errs...)
}

func (kubeCTCluster) PutPoolSpec(spec CTPoolSpec) error {
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{
			"name": ctPoolConfigName(spec.Name), "namespace": spec.Namespace,
			"labels": map[string]string{labelCTPool: spec.Name, "app.kubernetes.io/managed-by": "corral-vdi"},
		},
		"data": map[string]string{"spec": string(data)},
	})
	if err != nil {
		return err
	}
	_, err = runner.RunStdin(string(manifest), "kubectl", "apply", "-f", "-")
	return err
}

func (kubeCTCluster) PutPoolStatus(namespace, name string, status CTPoolStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{"data": map[string]string{"status": string(data)}})
	if err != nil {
		return err
	}
	_, err = run("kubectl", "patch", "configmap", ctPoolConfigName(name), "-n", namespace, "--type", "merge", "-p", string(patch))
	return err
}

func (kubeCTCluster) DeletePool(namespace, name string) error {
	_, err := run("kubectl", "delete", "configmap", ctPoolConfigName(name), "-n", namespace, "--ignore-not-found")
	return err
}

type ctObjectMeta struct {
	Name              string            `json:"name"`
	Labels            map[string]string `json:"labels"`
	Annotations       map[string]string `json:"annotations"`
	DeletionTimestamp string            `json:"deletionTimestamp"`
}

func (kubeCTCluster) ListMembers(namespace, pool string) ([]ctMemberObs, error) {
	out, err := run("kubectl", "get", "pvc", "-n", namespace, "-l", labelCTPool+"="+pool, "-o", "json")
	if err != nil {
		return nil, err
	}
	var pvcs struct {
		Items []struct {
			Metadata ctObjectMeta `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &pvcs); err != nil {
		return nil, fmt.Errorf("decode member PVCs: %w", err)
	}
	out, err = run("kubectl", "get", "pods", "-n", namespace, "-l", labelCTPool+"="+pool, "-o", "json")
	if err != nil {
		return nil, err
	}
	var pods struct {
		Items []struct {
			Metadata ctObjectMeta `json:"metadata"`
			Status   struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type    string `json:"type"`
					Status  string `json:"status"`
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"conditions"`
				ContainerStatuses []struct {
					Ready bool `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &pods); err != nil {
		return nil, fmt.Errorf("decode member pods: %w", err)
	}
	podByName := map[string]int{}
	for i, p := range pods.Items {
		podByName[p.Metadata.Name] = i
	}
	var members []ctMemberObs
	for _, pvc := range pvcs.Items {
		name := pvc.Metadata.Labels[labelCTMember]
		if name == "" {
			continue
		}
		m := ctMemberObs{
			Name: name, SourceHash: pvc.Metadata.Labels[labelCTSource],
			AssignedTo: pvc.Metadata.Annotations[annoCTAssignedTo], Terminating: pvc.Metadata.DeletionTimestamp != "",
		}
		if i, ok := podByName[name]; ok {
			p := pods.Items[i]
			m.PodPhase = p.Status.Phase
			m.Terminating = m.Terminating || p.Metadata.DeletionTimestamp != ""
			m.PodReady = len(p.Status.ContainerStatuses) > 0
			for _, cs := range p.Status.ContainerStatuses {
				m.PodReady = m.PodReady && cs.Ready
			}
			for _, cond := range p.Status.Conditions {
				if cond.Type == "PodScheduled" && cond.Status == "False" && cond.Reason == "Unschedulable" {
					m.Unschedulable = cond.Message
				}
			}
		}
		members = append(members, m)
	}
	return members, nil
}

func ctMemberLabels(spec CTPoolSpec, member string) map[string]string {
	return map[string]string{labelCTPool: spec.Name, labelCTMember: member, labelCTSource: spec.Source.Hash()}
}

// CreateMember refuses to adopt a CT of the same name that is not a member
// of this pool: applying over it would rewrite somebody else's desktop.
func (kubeCTCluster) CreateMember(spec CTPoolSpec, member string) error {
	out, err := run("kubectl", "get", "pvc", member+"-data", "-n", spec.Namespace, "--ignore-not-found", "-o", "json")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) > 0 {
		var existing struct {
			Metadata ctObjectMeta `json:"metadata"`
		}
		if err := json.Unmarshal(out, &existing); err != nil {
			return fmt.Errorf("decode existing PVC %s-data: %w", member, err)
		}
		if existing.Metadata.Labels[labelCTPool] != spec.Name {
			return fmt.Errorf("a CT named %s that is not a member of pool %q already exists in ns/%s", member, spec.Name, spec.Namespace)
		}
	}
	return ct.Create(ct.CreateOpts{
		Name: member, Namespace: spec.Namespace, Image: spec.Source.Image,
		CPU: spec.Source.CPU, Mem: spec.Source.Mem, Disk: spec.Source.Disk, StorageClass: spec.Source.StorageClass,
		Privileged: spec.Source.Privileged, Init: spec.Source.Init,
		Labels: ctMemberLabels(spec, member),
	})
}

func (kubeCTCluster) StartMember(namespace, member string) error {
	return ct.StartPod(member, namespace)
}

// DeleteMember removes the pod, the console Service, and the data PVC,
// waiting until each is gone.
func (kubeCTCluster) DeleteMember(namespace, member string) error {
	timeout := "--timeout=" + ctDeleteTimeout.String()
	for _, obj := range [][2]string{{"pod", member}, {"svc", member + "-svc"}, {"pvc", member + "-data"}} {
		if _, err := run("kubectl", "delete", obj[0], obj[1], "-n", namespace, "--ignore-not-found", "--wait=true", timeout); err != nil {
			return fmt.Errorf("delete %s %s: %w", obj[0], obj[1], err)
		}
	}
	return nil
}

func (kubeCTCluster) SetAssignee(namespace, member, identity string) error {
	_, err := run("kubectl", "annotate", "pvc", member+"-data", "-n", namespace, annoCTAssignedTo+"="+identity, "--overwrite")
	return err
}

func (kubeCTCluster) ListClaims(namespace, pool string) (map[string]lease, error) {
	out, err := run("kubectl", "get", "lease", "-n", namespace, "-l", labelCTPool+"="+pool, "-o", "json")
	if err != nil {
		return nil, err
	}
	var res struct {
		Items []lease `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	claims := map[string]lease{}
	for _, l := range res.Items {
		if member := l.Metadata.Labels[labelVDIMember]; member != "" {
			claims[member] = l
		}
	}
	return claims, nil
}

func (kubeCTCluster) GetClaim(ref leaseRef) (lease, bool, error) {
	return findLease(ref.Namespace, ref.Name)
}

func (kubeCTCluster) CreateClaim(ref leaseRef, holder string, duration time.Duration) (bool, error) {
	created, _, err := createLease(ref, holder, duration, ctNow())
	return created, err
}

func (kubeCTCluster) ReplaceClaim(ref leaseRef, holder string, duration time.Duration, resourceVersion string) bool {
	return replaceLease(ref, holder, duration, ctNow(), resourceVersion)
}

func (kubeCTCluster) DeleteClaim(ref leaseRef, resourceVersion string) error {
	return deleteLease(ref.Namespace, ref.Name, resourceVersion)
}
