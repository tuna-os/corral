// Package ct implements Containers (CT) — Proxmox-style pet pods. See
// docs/adr/0005-containers-as-pet-pods.md and CONTEXT.md's Container (CT)
// entry for the design. Unlike pkg/qemu and pkg/kubevirt, a CT is not a VM
// and doesn't implement types.Backend — it's a plain Kubernetes pod with a
// persistent-volume-backed data directory, not a hypervisor-backed guest.
package ct

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tuna-os/corral/pkg/shell"
)

// CreateOpts describes a new CT.
type CreateOpts struct {
	Name         string
	Namespace    string
	Image        string
	CPU          int    // vCPU cores → pod CPU limit/request
	Mem          string // e.g. "512Mi" → pod memory limit/request
	Disk         string // PVC size, e.g. "5Gi"
	StorageClass string // "" = cluster default
	Privileged   bool   // PVE's "Privileged" checkbox
}

// CT describes a running or stopped Container as reported by ListCTs.
type CT struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Phase      string `json:"phase"`   // pod phase: Running, Pending, Succeeded, Failed, or "Stopped" (no pod)
	Ready      bool   `json:"ready"`   // Running and containers ready
	Image      string `json:"image"`
	CPU        int    `json:"cpu"`
	Mem        string `json:"mem"`
	Privileged bool   `json:"privileged"`
}

// ctSpec is the subset of CreateOpts persisted as a PVC annotation. Stop
// deletes the pod (a bare Pod carries no spec once gone, unlike a KubeVirt
// VM object which persists independent of its VMI) — annotating the PVC,
// which does survive Stop, is what lets Start regenerate an identical pod
// without the caller re-specifying everything. Same pattern as bootc's
// corral.bootc/image annotation.
type ctSpec struct {
	Image      string `json:"image"`
	CPU        int    `json:"cpu"`
	Mem        string `json:"mem"`
	Privileged bool   `json:"privileged"`
}

const (
	specAnnotation = "corral.ct/spec"
	labelCT        = "corral.dev/ct"
	dataMountPath  = "/data"
)

var runner shell.Runner = shell.Real{}

// SetRunner overrides the command runner (for unit tests).
func SetRunner(r shell.Runner) { runner = r }

func run(name string, args ...string) ([]byte, error) { return runner.Run(name, args...) }

func runStdin(stdin, name string, args ...string) ([]byte, error) {
	return runner.RunStdin(stdin, name, args...)
}

func apply(obj map[string]any) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = runStdin(string(data), "kubectl", "apply", "-f", "-")
	return err
}

// pvcName is the stable name of a CT's persistent data volume — stable
// across Stop/Start so a new pod remounts the same volume.
func pvcName(name string) string { return name + "-data" }

// generatePVC builds the CT's persistent data volume, annotated with the
// spec Start needs to recreate the pod later.
func generatePVC(opts CreateOpts) (map[string]any, error) {
	spec := ctSpec{Image: opts.Image, CPU: opts.CPU, Mem: opts.Mem, Privileged: opts.Privileged}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	pvcSpec := map[string]any{
		"accessModes": []string{"ReadWriteOnce"},
		"resources":   map[string]any{"requests": map[string]any{"storage": opts.Disk}},
	}
	if opts.StorageClass != "" {
		pvcSpec["storageClassName"] = opts.StorageClass
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":      pvcName(opts.Name),
			"namespace": opts.Namespace,
			"labels":    map[string]any{labelCT: "true", "corral.dev/ct-name": opts.Name},
			"annotations": map[string]any{
				specAnnotation: string(specJSON),
			},
		},
		"spec": pvcSpec,
	}, nil
}

// generatePod builds the CT's pod from a spec (either a fresh CreateOpts or
// one recovered from the PVC annotation on Start). No init/sshd is baked in
// here — that's the curated ct-* image's job (see the ADR); this defaults
// the command to `sleep infinity` so a generic image (no baked-in
// long-running entrypoint) still stays up for exec/console access instead
// of crash-looping on exit.
func generatePod(name, namespace string, spec ctSpec) map[string]any {
	cpu := spec.CPU
	if cpu == 0 {
		cpu = 1
	}
	mem := spec.Mem
	if mem == "" {
		mem = "512Mi"
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    map[string]any{labelCT: "true", "corral.dev/ct-name": name},
		},
		"spec": map[string]any{
			"restartPolicy": "Always",
			"containers": []map[string]any{
				{
					"name":    "ct",
					"image":   spec.Image,
					"command": []string{"sleep", "infinity"},
					"stdin":   true,
					"tty":     true,
					"resources": map[string]any{
						"limits":   map[string]any{"cpu": strconv.Itoa(cpu), "memory": mem},
						"requests": map[string]any{"cpu": strconv.Itoa(cpu), "memory": mem},
					},
					"securityContext": map[string]any{"privileged": spec.Privileged},
					"volumeMounts": []map[string]any{
						{"name": "data", "mountPath": dataMountPath},
					},
				},
			},
			"volumes": []map[string]any{
				{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": pvcName(name)}},
			},
		},
	}
}

// ConsolePorts are the ports exposed on the CT's tailnet Service by
// default — just SSH, matching a plain container's usual single entry
// point. Curated ct-* images (with sshd baked in, per the ADR) would
// listen here; the generic image this mechanism is tested against won't
// answer, same as how a VM's exposed console ports don't imply the guest
// is listening (see kubevirt.ConsolePorts).
var ConsolePorts = []int{22}

// generateService exposes the CT's ports via a plain Service selecting the
// CT pod directly — simpler than the VM port-proxy (which exists to work
// around KubeVirt VMs not having a stable pod selector; a CT's own pod is
// the pod). tailscaleAnnotations, if non-nil, exposes it on the tailnet the
// same way VM proxies do.
func generateService(name, namespace string, ports []int, tailscaleAnnotations map[string]string) map[string]any {
	svcPorts := make([]map[string]any, 0, len(ports))
	for _, p := range ports {
		svcPorts = append(svcPorts, map[string]any{
			"port": p, "targetPort": p, "name": fmt.Sprintf("port-%d", p), "protocol": "TCP",
		})
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":        name + "-svc",
			"namespace":   namespace,
			"annotations": tailscaleAnnotations,
		},
		"spec": map[string]any{
			"type":     "ClusterIP",
			"selector": map[string]string{"corral.dev/ct-name": name},
			"ports":    svcPorts,
		},
	}
}

func tailscaleOperatorPresent() bool {
	_, err := run("kubectl", "get", "ingressclass", "tailscale")
	return err == nil
}

// ApplyProxy exposes a CT's ports via a tailnet Service. No-op if the
// Tailscale K8s operator isn't installed — same skip-when-absent behavior
// as kubevirt.ApplyProxy, for the same reason (the Service annotation is
// useless without the operator to act on it).
func ApplyProxy(name, namespace string, ports []int) error {
	if !tailscaleOperatorPresent() {
		return nil
	}
	annotations := map[string]string{
		"tailscale.com/expose":   "true",
		"tailscale.com/hostname": name + "-ct",
	}
	return apply(generateService(name, namespace, ports, annotations))
}

// Create provisions a CT: the PVC (annotated with its spec) and the pod.
func Create(opts CreateOpts) error {
	if opts.Disk == "" {
		opts.Disk = "5Gi"
	}
	pvc, err := generatePVC(opts)
	if err != nil {
		return err
	}
	if err := apply(pvc); err != nil {
		return fmt.Errorf("creating data PVC: %w", err)
	}
	spec := ctSpec{Image: opts.Image, CPU: opts.CPU, Mem: opts.Mem, Privileged: opts.Privileged}
	if err := apply(generatePod(opts.Name, opts.Namespace, spec)); err != nil {
		return fmt.Errorf("creating pod: %w", err)
	}
	return nil
}

// specFromPVC reads a CT's spec back from its data PVC's annotation — used
// by Start to recreate the pod after Stop deleted it.
func specFromPVC(name, namespace string) (ctSpec, error) {
	out, err := run("kubectl", "get", "pvc", pvcName(name), "-n", namespace,
		"-o", "jsonpath={.metadata.annotations.corral\\.ct/spec}")
	if err != nil {
		return ctSpec{}, fmt.Errorf("reading CT spec (has it been created?): %w", err)
	}
	var spec ctSpec
	if err := json.Unmarshal(out, &spec); err != nil {
		return ctSpec{}, fmt.Errorf("CT spec annotation is corrupt: %w", err)
	}
	return spec, nil
}

// Start creates the pod from the spec recorded on the data PVC — the PVC
// (and its annotation) survives Stop, so this recreates an identical pod
// without the caller re-specifying image/cpu/mem/privileged.
func Start(name, namespace string) error {
	spec, err := specFromPVC(name, namespace)
	if err != nil {
		return err
	}
	return apply(generatePod(name, namespace, spec))
}

// Stop deletes the pod, keeping the data PVC (and its spec annotation) so
// Start can bring it back.
func Stop(name, namespace string) error {
	_, err := run("kubectl", "delete", "pod", name, "-n", namespace, "--ignore-not-found")
	return err
}

// Delete removes the CT entirely: pod, data PVC, and (if present) its
// Service.
func Delete(name, namespace string) error {
	run("kubectl", "delete", "pod", name, "-n", namespace, "--ignore-not-found")
	run("kubectl", "delete", "svc", name+"-svc", "-n", namespace, "--ignore-not-found")
	_, err := run("kubectl", "delete", "pvc", pvcName(name), "-n", namespace, "--ignore-not-found")
	return err
}

// Exists reports whether a CT (its data PVC — the durable half of a CT's
// identity) exists, regardless of whether it's currently started.
func Exists(name, namespace string) bool {
	_, err := run("kubectl", "get", "pvc", pvcName(name), "-n", namespace, "-o", "name")
	return err == nil
}

// ListCTs returns every CT across all namespaces. A CT's durable identity
// is its data PVC (labeled corral.dev/ct=true) — it exists whether or not
// the pod is currently running, same as a stopped VM still being a VM.
// Live phase/readiness come from the pod when one exists; "Stopped"
// otherwise.
func ListCTs() ([]CT, error) {
	pvcOut, err := run("kubectl", "get", "pvc", "-A", "-l", labelCT+"=true", "-o", "json")
	if err != nil {
		return nil, err
	}
	var pvcRes struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Namespace   string            `json:"namespace"`
				Annotations map[string]string `json:"annotations"`
				Labels      map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(pvcOut, &pvcRes); err != nil {
		return nil, err
	}

	type podInfo struct {
		Phase string
		Ready bool
	}
	pods := map[string]podInfo{} // "ns/name" -> info
	if podOut, err := run("kubectl", "get", "pods", "-A", "-l", labelCT+"=true", "-o", "json"); err == nil {
		var podRes struct {
			Items []struct {
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
				Status struct {
					Phase             string `json:"phase"`
					ContainerStatuses []struct {
						Ready bool `json:"ready"`
					} `json:"containerStatuses"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal(podOut, &podRes) == nil {
			for _, p := range podRes.Items {
				ready := len(p.Status.ContainerStatuses) > 0
				for _, cs := range p.Status.ContainerStatuses {
					ready = ready && cs.Ready
				}
				pods[p.Metadata.Namespace+"/"+p.Metadata.Name] = podInfo{Phase: p.Status.Phase, Ready: ready}
			}
		}
	}

	var out []CT
	for _, item := range pvcRes.Items {
		name := item.Metadata.Labels["corral.dev/ct-name"]
		if name == "" {
			continue // not one of ours (shouldn't happen given the label selector, but be defensive)
		}
		ns := item.Metadata.Namespace
		var spec ctSpec
		json.Unmarshal([]byte(item.Metadata.Annotations[specAnnotation]), &spec)

		phase, ready := "Stopped", false
		if pi, ok := pods[ns+"/"+name]; ok {
			phase, ready = pi.Phase, pi.Ready
		}
		out = append(out, CT{
			Name: name, Namespace: ns, Phase: phase, Ready: ready,
			Image: spec.Image, CPU: spec.CPU, Mem: spec.Mem, Privileged: spec.Privileged,
		})
	}
	return out, nil
}
