package kubevirt

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tuna-os/corral/pkg/types"
)

// This file holds "Proxmox-parity" VM operations that don't fit the other
// per-capability files: guest-agent introspection, secondary networks,
// templates, instancetypes/preferences, the ISO/image library, observability
// (events/metrics), and storage capability detection. Everything shells out
// to virtctl/kubectl — no client-side Kubernetes SDK — to keep Corral a
// single static binary.

// ── Guest agent ───────────────────────────────────────────────────

// GuestInfo returns guest-agent data (OS, filesystems, users). Errors when the
// agent is not connected.
func (c *Client) GuestInfo(name string) (map[string]any, error) {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	out, err := c.runner().Run(virtctl, "guestosinfo", name, "-n", c.Namespace)
	if err != nil {
		return nil, fmt.Errorf("guest agent not available (is qemu-guest-agent installed and running?)")
	}
	var os any
	json.Unmarshal(out, &os)
	res["os"] = os
	if out, err := c.runner().Run(virtctl, "fslist", name, "-n", c.Namespace); err == nil {
		var fs any
		json.Unmarshal(out, &fs)
		res["filesystems"] = fs
	}
	if out, err := c.runner().Run(virtctl, "userlist", name, "-n", c.Namespace); err == nil {
		var users any
		json.Unmarshal(out, &users)
		res["users"] = users
	}
	return res, nil
}

// ── Secondary networks (Multus) ───────────────────────────────────

// ListNADs returns Multus NetworkAttachmentDefinitions ("ns/name"). Empty when
// Multus isn't installed.
func ListNADs() []string {
	out, err := runPkg("kubectl", "get", "net-attach-def", "-A",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}{\"\\n\"}{end}")
	if err != nil {
		return []string{}
	}
	nads := []string{}
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n = strings.TrimSpace(n); n != "" {
			nads = append(nads, n)
		}
	}
	return nads
}

// ResolveNAD picks the NAD `--lan`/`--network-nad` should use: explicit wins
// outright; otherwise exactly one NAD on the cluster is used automatically,
// and zero or several is an error rather than a guess (guessing wrong wires
// the VM onto the wrong LAN silently). available is ListNADs()'s output,
// passed in so this stays a pure, testable decision — no cluster call here.
func ResolveNAD(explicit string, available []string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	switch len(available) {
	case 0:
		return "", fmt.Errorf("--lan needs a Multus NetworkAttachmentDefinition on the cluster, but none exist (see docs/lan-networking.md)")
	case 1:
		return available[0], nil
	default:
		return "", fmt.Errorf("multiple NetworkAttachmentDefinitions exist (%s) — specify one with --network-nad", strings.Join(available, ", "))
	}
}

// AddNIC attaches a secondary interface backed by a Multus NAD to the VM
// (bridge binding). Applied to the VM spec; KubeVirt hotplugs it on a running
// VM where supported, else it takes effect on next boot.
func (c *Client) AddNIC(name, nad, iface string) error {
	if iface == "" {
		iface = "net1"
	}
	// JSON-patch: append an interface + a matching multus network.
	patch := fmt.Sprintf(`[
      {"op":"add","path":"/spec/template/spec/domain/devices/interfaces/-","value":{"name":%q,"bridge":{}}},
      {"op":"add","path":"/spec/template/spec/networks/-","value":{"name":%q,"multus":{"networkName":%q}}}
    ]`, iface, iface, nad)
	_, err := c.runner().Run("kubectl", "patch", "vm", name, "-n", c.Namespace,
		"--type", "json", "-p", patch)
	return err
}

// ── Templates ─────────────────────────────────────────────────────

// MarkTemplate labels (or unlabels) a VM as a golden template. Templates are
// ordinary stopped VMs that "create from template" clones.
// tagLabelPrefix namespaces user tags as VM labels so they survive round-trips
// and are selectable with `kubectl get vm -l corral.dev/tag.<name>`.
const tagLabelPrefix = "corral.dev/tag."

// tagsFromLabels extracts the sorted set of user tags from a VM's labels.
func tagsFromLabels(labels map[string]string) []string {
	var tags []string
	for k, v := range labels {
		if v == "true" && strings.HasPrefix(k, tagLabelPrefix) {
			if t := strings.TrimPrefix(k, tagLabelPrefix); t != "" {
				tags = append(tags, t)
			}
		}
	}
	sort.Strings(tags)
	return tags
}

// sanitizeTag normalises a user tag to a valid Kubernetes label-name segment
// (lowercase alnum plus -_.). Returns "" if nothing valid remains.
func sanitizeTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	var b strings.Builder
	for _, r := range tag {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-_.")
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

// SetTag adds (on) or removes (off) a single tag label on a VM.
func (c *Client) SetTag(name, tag string, on bool) error {
	t := sanitizeTag(tag)
	if t == "" {
		return fmt.Errorf("invalid tag %q (use letters, digits, -_.)", tag)
	}
	val := tagLabelPrefix + t + "=true"
	if !on {
		val = tagLabelPrefix + t + "-" // trailing - removes the label
	}
	_, err := c.runner().Run("kubectl", "label", "vm", name, "-n", c.Namespace, val, "--overwrite")
	return err
}

func (c *Client) MarkTemplate(name string, on bool) error {
	val := "corral.dev/template=true"
	if !on {
		val = "corral.dev/template-" // trailing - removes the label
	}
	_, err := c.runner().Run("kubectl", "label", "vm", name, "-n", c.Namespace, val, "--overwrite")
	return err
}

// CreateFromTemplate clones a template VM into a new VM (via VirtualMachineClone,
// which copies disks too). The clone is not itself a template.
func (c *Client) CreateFromTemplate(template, newName string) error {
	if err := c.Clone(template, newName); err != nil {
		return err
	}
	return nil
}

// ── Instancetypes / preferences ───────────────────────────────────

// ListInstanceTypes returns cluster-wide KubeVirt instancetype names (sizing).
func ListInstanceTypes() []string {
	return kubectlNames("virtualmachineclusterinstancetypes")
}

// ListPreferences returns cluster-wide KubeVirt preference names (guest defaults).
func ListPreferences() []string {
	return kubectlNames("virtualmachineclusterpreferences")
}

func kubectlNames(resource string) []string {
	out, err := runPkg("kubectl", "get", resource,
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	if err != nil {
		return []string{}
	}
	names := []string{}
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// ── ISO / image library (CDI DataVolumes) ─────────────────────────

// DataVolumeInfo is a row in the image library.
type DataVolumeInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Size      string `json:"size"`
	Phase     string `json:"phase"`
	Progress  string `json:"progress"`
	Source    string `json:"source"`
}

// ListDataVolumes returns all CDI DataVolumes (imported ISOs/images).
func ListDataVolumes() ([]DataVolumeInfo, error) {
	out, err := runPkg("kubectl", "get", "datavolumes", "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Source struct {
					HTTP *struct {
						URL string `json:"url"`
					} `json:"http"`
					Registry *struct {
						URL string `json:"url"`
					} `json:"registry"`
				} `json:"source"`
				PVC     *pvcResources `json:"pvc"`
				Storage *pvcResources `json:"storage"`
			} `json:"spec"`
			Status struct {
				Phase    string `json:"phase"`
				Progress string `json:"progress"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	dvs := []DataVolumeInfo{}
	for _, it := range res.Items {
		size := ""
		if it.Spec.PVC != nil {
			size = it.Spec.PVC.Resources.Requests.Storage
		} else if it.Spec.Storage != nil {
			size = it.Spec.Storage.Resources.Requests.Storage
		}
		src := ""
		if it.Spec.Source.HTTP != nil {
			src = it.Spec.Source.HTTP.URL
		} else if it.Spec.Source.Registry != nil {
			src = it.Spec.Source.Registry.URL
		}
		dvs = append(dvs, DataVolumeInfo{
			Name:      it.Metadata.Name,
			Namespace: it.Metadata.Namespace,
			Size:      size,
			Phase:     it.Status.Phase,
			Progress:  it.Status.Progress,
			Source:    src,
		})
	}
	return dvs, nil
}

type pvcResources struct {
	Resources struct {
		Requests struct {
			Storage string `json:"storage"`
		} `json:"requests"`
	} `json:"resources"`
}

// ImportDataVolume creates a CDI DataVolume importing an image from a URL.
func ImportDataVolume(name, namespace, url, size string) error {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	if size == "" {
		size = DetectISOSize(url)
	}
	EnsureNamespace(namespace)
	dv := GenerateDataVolume(name, namespace, url, size)
	if sc := PreferredStorageClass(); sc != "" {
		dv["spec"].(map[string]any)["pvc"].(map[string]any)["storageClassName"] = sc
	}
	return Apply(dv)
}

// DeleteDataVolume removes a DataVolume (and its PVC).
func DeleteDataVolume(namespace, name string) error {
	_, err := runPkg("kubectl", "delete", "datavolume", name, "-n", namespace, "--ignore-not-found")
	return err
}

// ── Observability: events + metrics ───────────────────────────────

// EventInfo is one row in the Proxmox-style task/event log.
type EventInfo struct {
	Time    string `json:"time"`
	Type    string `json:"type"` // Normal | Warning
	Reason  string `json:"reason"`
	Object  string `json:"object"` // kind/name
	Message string `json:"message"`
}

// Events returns recent Kubernetes events for a VM (its VM/VMI objects and its
// virt-launcher pod), newest first.
func (c *Client) Events(name string) ([]EventInfo, error) {
	out, err := c.runner().Run("kubectl", "get", "events", "-n", c.Namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var res struct {
		Items []struct {
			Type           string `json:"type"`
			Reason         string `json:"reason"`
			Message        string `json:"message"`
			LastTimestamp  string `json:"lastTimestamp"`
			EventTime      string `json:"eventTime"`
			InvolvedObject struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"involvedObject"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	launcher := "virt-launcher-" + name
	evs := []EventInfo{}
	for _, it := range res.Items {
		n := it.InvolvedObject.Name
		if n != name && !strings.HasPrefix(n, launcher) {
			continue
		}
		t := it.LastTimestamp
		if t == "" {
			t = it.EventTime
		}
		evs = append(evs, EventInfo{
			Time:    t,
			Type:    it.Type,
			Reason:  it.Reason,
			Object:  it.InvolvedObject.Kind + "/" + n,
			Message: it.Message,
		})
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].Time > evs[j].Time })
	if len(evs) > 50 {
		evs = evs[:50]
	}
	return evs, nil
}

// Metrics returns the VM's live CPU and memory usage (its virt-launcher pod),
// via metrics-server. Empty strings if metrics aren't available yet.
func (c *Client) Metrics(name string) map[string]string {
	out, err := c.runner().Run("kubectl", "top", "pod", "-n", c.Namespace,
		"-l", "kubevirt.io/vm="+name, "--no-headers")
	res := map[string]string{"cpu": "", "mem": ""}
	if err != nil {
		return res
	}
	if f := strings.Fields(string(out)); len(f) >= 3 {
		res["cpu"] = f[1]
		res["mem"] = f[2]
	}
	return res
}

// Usage is one `kubectl top` reading for a running VM's launcher pod.
type Usage struct {
	MilliCPU int    // millicores (1000 = one full vCPU)
	MemBytes int64  // working-set memory of the launcher pod
	Node     string // node the launcher pod runs on ("" when unscheduled)
}

// SampleAllUsage returns current CPU and memory usage for every running VM,
// keyed by "namespace/vm". It joins `kubectl top` (usage by pod) with the
// virt-launcher pods' vm.kubevirt.io/name label and their node. Returns nil
// when metrics-server is unavailable, so callers degrade gracefully.
func SampleAllUsage() map[string]Usage {
	out, err := runPkg("kubectl", "get", "pods", "-A", "-l", "kubevirt.io=virt-launcher", "-o", "json")
	if err != nil {
		return nil
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &pods) != nil {
		return nil
	}
	type podVM struct{ key, node string }
	podToVM := map[string]podVM{} // "ns/podname" -> "ns/vm" + node
	for _, p := range pods.Items {
		if vm := p.Metadata.Labels["vm.kubevirt.io/name"]; vm != "" {
			podToVM[p.Metadata.Namespace+"/"+p.Metadata.Name] = podVM{p.Metadata.Namespace + "/" + vm, p.Spec.NodeName}
		}
	}
	if len(podToVM) == 0 {
		return map[string]Usage{}
	}
	top, err := runPkg("kubectl", "top", "pod", "-A", "-l", "kubevirt.io=virt-launcher", "--no-headers")
	if err != nil {
		return nil // metrics-server absent
	}
	res := map[string]Usage{}
	for _, line := range strings.Split(strings.TrimSpace(string(top)), "\n") {
		f := strings.Fields(line) // NAMESPACE NAME CPU(cores) MEMORY(bytes)
		if len(f) < 3 {
			continue
		}
		if vm, ok := podToVM[f[0]+"/"+f[1]]; ok {
			u := Usage{MilliCPU: parseMilliCPU(f[2]), Node: vm.node}
			if len(f) > 3 {
				u.MemBytes = parseTopBytes(f[3])
			}
			res[vm.key] = u
		}
	}
	return res
}

// SampleAllCPU is SampleAllUsage reduced to millicores per "namespace/vm".
func SampleAllCPU() map[string]int {
	all := SampleAllUsage()
	if all == nil {
		return nil
	}
	res := make(map[string]int, len(all))
	for k, u := range all {
		res[k] = u.MilliCPU
	}
	return res
}

// parseTopBytes parses a `kubectl top` memory value ("512Mi", "2Gi", "900Ki",
// or a bare byte count). Unparseable values read as 0.
func parseTopBytes(s string) int64 {
	s = strings.TrimSpace(s)
	mult := int64(1)
	for suffix, m := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40} {
		if strings.HasSuffix(s, suffix) {
			s, mult = strings.TrimSuffix(s, suffix), m
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n * mult
}

// parseMilliCPU parses a `kubectl top` CPU value ("123m", or "1" = 1 core).
func parseMilliCPU(s string) int {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "m") {
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "m"))
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f * 1000)
	}
	return 0
}

// ── Storage / capability detection ────────────────────────────────

// GeneratePVCWithClass is GeneratePVC with an explicit StorageClass ("" = cluster default).
func GeneratePVCWithClass(name, namespace, size, storageClass string) map[string]any {
	pvc := GeneratePVC(name, namespace, size)
	if storageClass != "" {
		pvc["spec"].(map[string]any)["storageClassName"] = storageClass
	}
	return pvc
}

// PreferredStorageClass returns the StorageClass Corral prefers for new disks:
// "local-path" when present (local NVMe speed — no network IO), else "" (cluster default).
// Snapshot and expansion features are detected automatically from the cluster.
func PreferredStorageClass() string {
	return ClusterCapabilities().StorageClass
}

// ClusterCapabilities reports which optional storage operations are available,
// so callers can enable/disable UI controls rather than fail on click.
func ClusterCapabilities() types.Capabilities {
	out, err := runPkg("kubectl", "get", "sc", "-o", "json")
	if err != nil {
		return types.Capabilities{}
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			AllowVolumeExpansion *bool `json:"allowVolumeExpansion"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil {
		return types.Capabilities{}
	}
	expand := map[string]bool{}
	var preferred, def string
	for _, it := range res.Items {
		expand[it.Metadata.Name] = it.AllowVolumeExpansion != nil && *it.AllowVolumeExpansion
		if it.Metadata.Name == "local-path" {
			preferred = "local-path"
		} else if (strings.Contains(it.Metadata.Name, "topolvm") || it.Metadata.Name == "topolvm") && preferred != "local-path" {
			preferred = it.Metadata.Name
		}
		if it.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			def = it.Metadata.Name
		}
	}
	effective := preferred
	if effective == "" {
		effective = def
	}
	return types.Capabilities{
		StorageClass: effective,
		CanExpand:    expand[effective],
		CanSnapshot:  hasSnapshotClass(),
	}
}

func hasSnapshotClass() bool {
	out, err := runPkg("kubectl", "get", "volumesnapshotclass", "-o", "name")
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// quantityToMib parses a Kubernetes memory quantity (Gi/G/Mi/M/Ki/K or raw) to MiB.
func quantityToMib(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var num float64
	var unit string
	fmt.Sscanf(s, "%f%s", &num, &unit)
	switch strings.ToLower(unit) {
	case "gi", "g":
		return int(num * 1024)
	case "mi", "m", "":
		return int(num)
	case "ki", "k":
		return int(num / 1024)
	default:
		return int(num)
	}
}
