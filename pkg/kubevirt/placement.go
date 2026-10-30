package kubevirt

// Create-time placement and grouping, shared by both VM generators.
//
// Performance comparisons need two VMs that differ only in the guest: same
// node, same size, and vCPUs that do not float across host CPUs another pod
// is using. KubeVirt expresses that as dedicatedCpuPlacement, which only
// schedules on a node whose kubelet runs the static CPU manager. Tags group
// the pair so scripts can find both halves again.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Placement is the create-time placement and grouping applied on top of a
// generated VM manifest. The zero value changes nothing.
type Placement struct {
	// DedicatedCPUs pins each vCPU to its own host CPU
	// (domain.cpu.dedicatedCpuPlacement). It needs guaranteed QoS, so the
	// memory request and limit are set equal to the guest memory.
	DedicatedCPUs bool
	// IsolateEmulatorThread gives the QEMU emulator thread one more dedicated
	// host CPU (domain.cpu.isolateEmulatorThread). KubeVirt honours it only
	// with DedicatedCPUs.
	IsolateEmulatorThread bool
	// Tags become corral.dev/tag.<key>=<value> labels on the VM.
	Tags map[string]string
}

// Validate refuses combinations KubeVirt would reject later.
func (p Placement) Validate() error {
	if p.IsolateEmulatorThread && !p.DedicatedCPUs {
		return fmt.Errorf("--isolate-emulator-thread needs --dedicated-cpus: KubeVirt isolates the emulator thread only for a VM with dedicated CPUs")
	}
	return nil
}

// ApplyPlacement edits a VM manifest from GenerateVM or GenerateBootcVM in
// place. Dedicated CPUs drop the hotplug headroom (maxSockets, maxGuest): a
// pinned VM keeps the CPUs and memory it was scheduled with.
func ApplyPlacement(vm map[string]any, p Placement) {
	if vm == nil {
		return
	}
	if len(p.Tags) > 0 {
		metadata, _ := vm["metadata"].(map[string]any)
		labels, _ := metadata["labels"].(map[string]any)
		if labels == nil && metadata != nil {
			labels = map[string]any{}
			metadata["labels"] = labels
		}
		for k, v := range p.Tags {
			labels[tagLabelPrefix+k] = v
		}
	}
	if !p.DedicatedCPUs {
		return
	}
	domain := templateDomain(vm)
	if domain == nil {
		return
	}
	cpu, _ := domain["cpu"].(map[string]any)
	if cpu == nil {
		cpu = map[string]any{}
		domain["cpu"] = cpu
	}
	cpu["dedicatedCpuPlacement"] = true
	delete(cpu, "maxSockets")
	if p.IsolateEmulatorThread {
		cpu["isolateEmulatorThread"] = true
	}
	memory, _ := domain["memory"].(map[string]any)
	if memory == nil {
		return
	}
	delete(memory, "maxGuest")
	if guest, ok := memory["guest"].(string); ok && guest != "" {
		domain["resources"] = map[string]any{
			"requests": map[string]any{"memory": guest},
			"limits":   map[string]any{"memory": guest},
		}
	}
}

func templateDomain(vm map[string]any) map[string]any {
	spec, _ := vm["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	tspec, _ := template["spec"].(map[string]any)
	domain, _ := tspec["domain"].(map[string]any)
	return domain
}

// labelValue is the Kubernetes label-value syntax: up to 63 characters,
// alphanumeric at both ends, with -_. inside.
var labelValue = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`)

// ParseTags parses repeated `key=value` flags. A bare `key` means key=true,
// the form the dashboard's tag chips use. A key must already be a valid tag
// name (lowercase letters, digits, -_.), so the label is the key the operator
// typed and a filter on it matches.
func ParseTags(flags []string) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	tags := make(map[string]string, len(flags))
	for _, f := range flags {
		key, value, hasValue := strings.Cut(f, "=")
		if !hasValue {
			value = "true"
		}
		if key == "" || sanitizeTag(key) != key {
			return nil, fmt.Errorf("invalid tag %q: the key must use lowercase letters, digits, and -_. (at most 63 characters)", f)
		}
		if len(value) > 63 || !labelValue.MatchString(value) {
			return nil, fmt.Errorf("invalid tag %q: the value must be at most 63 letters, digits, and -_., and start and end with a letter or digit", f)
		}
		tags[key] = value
	}
	return tags, nil
}

// TagFlags renders tags as sorted `--tag key=value` arguments, for passing
// them on to another command line.
func TagFlags(tags map[string]string) []string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		args = append(args, "--tag", k+"="+tags[k])
	}
	return args
}

// tagValuesFromLabels maps every corral.dev/tag.<key> label to its value.
func tagValuesFromLabels(labels map[string]string) map[string]string {
	var tags map[string]string
	for k, v := range labels {
		if t := strings.TrimPrefix(k, tagLabelPrefix); t != k && t != "" {
			if tags == nil {
				tags = map[string]string{}
			}
			tags[t] = v
		}
	}
	return tags
}

// cpuManagerLabel is set by virt-handler on every node: "true" when the
// node's kubelet runs the static CPU manager policy. KubeVirt schedules a VM
// with dedicated CPUs only onto a node that carries cpumanager=true.
const cpuManagerLabel = "cpumanager"

// CheckDedicatedCPUs refuses dedicated CPU placement that cannot schedule.
// With a node it checks that node; without one it needs at least one
// schedulable node with the static CPU manager. A refusal at create time is
// cheaper than a VM that sits in Scheduling, or a 20-minute bootc build whose
// final VM never starts.
func CheckDedicatedCPUs(node string) error {
	r := getPackageRunner()
	if node != "" {
		out, err := r.Run("kubectl", "get", "node", node, "-o", "jsonpath={.metadata.labels."+cpuManagerLabel+"}")
		if err != nil {
			return fmt.Errorf("--dedicated-cpus: cannot read node %s: %s", node, commandOutputOrErr(out, err))
		}
		if strings.TrimSpace(string(out)) != "true" {
			return cpuManagerError([]string{node})
		}
		return nil
	}
	out, err := r.Run("kubectl", "get", "nodes", "-o", "json")
	if err != nil {
		return fmt.Errorf("--dedicated-cpus: cannot list nodes: %s", commandOutputOrErr(out, err))
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return fmt.Errorf("--dedicated-cpus: cannot parse the node list: %w", err)
	}
	var without []string
	for _, n := range list.Items {
		if n.Spec.Unschedulable {
			continue
		}
		if n.Metadata.Labels[cpuManagerLabel] == "true" {
			return nil
		}
		without = append(without, n.Metadata.Name)
	}
	return cpuManagerError(without)
}

func cpuManagerError(nodes []string) error {
	subject := "no schedulable node has"
	if len(nodes) == 1 {
		subject = "node " + nodes[0] + " does not have"
	} else if len(nodes) > 1 {
		subject = "no schedulable node (" + strings.Join(nodes, ", ") + ") has"
	}
	return fmt.Errorf("--dedicated-cpus: %s label %s=true, so KubeVirt cannot pin vCPUs there. "+
		"KubeVirt sets that label only when both prerequisites hold: "+
		"(1) the node's kubelet runs the static CPU manager — set cpuManagerPolicy: static (--cpu-manager-policy=static) "+
		"and reserve system CPUs (reservedSystemCPUs / --reserved-cpus) in the kubelet configuration, "+
		"delete /var/lib/kubelet/cpu_manager_state, and restart the kubelet; "+
		"(2) the KubeVirt CR enables the CPUManager feature gate "+
		"(spec.configuration.developerConfiguration.featureGates)",
		subject, cpuManagerLabel)
}

func commandOutputOrErr(out []byte, err error) string {
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return err.Error()
}
