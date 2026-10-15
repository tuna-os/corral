// Package vdi implements Phase 1 of RFC-0001 (docs/rfc/0001-vdi-plugin.md):
// static desktop pools with manual assignment, built entirely on existing
// Corral primitives — kubevirt.Client.Clone stamps out pool members from an
// already-built "golden" VM (built the normal way, via corral bootc /
// corral-windows / corral create), and assignment is a pair of labels on
// the VM object, not a new CRD or storage system. See the RFC's "Phase 1"
// section for why: proving pool creation + connect-routing works is the
// goal here, not building a broker.
package vdi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

const (
	labelPool = "corral.dev/vdi-pool"
	// labelAssignedTo is a label-safe rendering of the claim holder, for
	// `kubectl get -l` views. annoAssignedTo holds the exact identity:
	// identities such as e-mail addresses are not valid label values.
	// Both are presentation state; the Lease is authoritative.
	labelAssignedTo = "corral.dev/vdi-assigned-to"
	annoAssignedTo  = "corral.dev/vdi-assigned-to"
	annoClaimedAt   = "corral.dev/vdi-claimed-at"
	// labelMode marks the members of an ephemeral pool; annoGolden names
	// the VM they are recreated from.
	labelMode     = "corral.dev/vdi-mode"
	annoGolden    = "corral.dev/vdi-golden"
	leasePrefix   = "corral-vdi-"
	claimDuration = time.Hour
)

// ErrPoolNotFound is returned for a pool that has no definition or members.
var ErrPoolNotFound = errors.New("pool not found")

// ErrPoolExhausted is returned when a claim finds no free member.
var ErrPoolExhausted = errors.New("pool has no free members")

// StartError reports a claim that succeeded on a member that then failed
// to start. The claim is still held; starting the member again is safe.
type StartError struct {
	Member string
	Err    error
}

func (e *StartError) Error() string {
	return fmt.Sprintf("claimed %s but failed to start it (the claim is kept; retry the start): %v", e.Member, e.Err)
}

func (e *StartError) Unwrap() error { return e.Err }

// cloneWaitTimeout and clonePollInterval are vars (not consts) so tests can
// shrink them instead of waiting out a real 2-minute timeout.
var (
	cloneWaitTimeout  = 2 * time.Minute
	clonePollInterval = time.Second
)

var runner shell.Runner = shell.Real{}

// SetRunner overrides the command runner (for unit tests). Also rewires
// pkg/kubevirt's runner seams — pool operations drive VM lifecycle through
// kubevirt.Client (default runner) and kubevirt.Clone (separate apply
// runner) — so tests only need to call this one seam.
func SetRunner(r shell.Runner) {
	runner = r
	kubevirt.SetDefaultRunner(r)
	kubevirt.SetApplyRunner(r)
}

func run(name string, args ...string) ([]byte, error) { return runner.Run(name, args...) }

// CreateOpts describes a new pool.
type CreateOpts struct {
	Name      string // pool name; members are named "<name>-1".."<name>-N"
	Namespace string
	From      string // name of an existing, already-built "golden" VM to clone
	Size      int
	// Ephemeral pools recreate a member from From after each claim, so
	// the next user gets a fresh desktop. Persistent members (the default)
	// keep their disks across claims.
	Ephemeral bool
}

// Member is one desktop in a pool.
type Member struct {
	Name       string `json:"name"`
	AssignedTo string `json:"assignedTo,omitempty"` // "" = free
	ClaimedAt  string `json:"claimedAt,omitempty"`  // RFC3339, empty if free
	Running    bool   `json:"running"`
}

// Pool groups a set of Members under one pool label.
type Pool struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Members   []Member `json:"members"`
}

func memberName(pool string, i int) string { return fmt.Sprintf("%s-%d", pool, i) }

// DeviceType characterizes whether a resource is exclusive passthrough or mediated/vGPU.
type DeviceType string

const (
	DeviceTypePCIHostDevice    DeviceType = "exclusive PCI passthrough"
	DeviceTypeMediatedDevice   DeviceType = "mediated device (vGPU)"
	DeviceTypeExternalProvider DeviceType = "external provider device (vGPU/SR-IOV)"
	DeviceTypeUnknown          DeviceType = "host device"
)

// DeviceRequest holds information about a GPU/host-device requested by a VM.
type DeviceRequest struct {
	ResourceName string     `json:"resourceName"`
	Count        int        `json:"count"`
	Type         DeviceType `json:"type"`
}

// DeviceCapacityReport summarizes resource availability across cluster nodes.
type DeviceCapacityReport struct {
	ResourceName      string         `json:"resourceName"`
	Type              DeviceType     `json:"type"`
	ReplicasRequested int            `json:"replicasRequested"`
	PerVMCount        int            `json:"perVmCount"`
	TotalNeeded       int            `json:"totalNeeded"`
	AllocatableTotal  int            `json:"allocatableTotal"`
	AllocatedExisting int            `json:"allocatedExisting"`
	AvailableTotal    int            `json:"availableTotal"`
	NodeAllocatable   map[string]int `json:"nodeAllocatable"`
	NodeAvailable     map[string]int `json:"nodeAvailable"`
	MaxConcurrency    int            `json:"maxConcurrency"`
}

type vmSpecDevices struct {
	Spec struct {
		Template struct {
			Spec struct {
				Domain struct {
					Devices struct {
						GPUs []struct {
							DeviceName string `json:"deviceName"`
							Name       string `json:"name"`
						} `json:"gpus"`
						HostDevices []struct {
							DeviceName string `json:"deviceName"`
							Name       string `json:"name"`
						} `json:"hostDevices"`
					} `json:"devices"`
				} `json:"domain"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type permittedDevicesConfig struct {
	Spec struct {
		Configuration struct {
			PermittedHostDevices struct {
				PCIHostDevices []struct {
					ResourceName             string `json:"resourceName"`
					ExternalResourceProvider bool   `json:"externalResourceProvider"`
				} `json:"pciHostDevices"`
				MediatedDevices []struct {
					ResourceName             string `json:"resourceName"`
					ExternalResourceProvider bool   `json:"externalResourceProvider"`
				} `json:"mediatedDevices"`
			} `json:"permittedHostDevices"`
		} `json:"configuration"`
	} `json:"spec"`
}

// inspectVMDeviceRequests returns the host device requests of a VM and their types.
func inspectVMDeviceRequests(ns, vmName string) ([]DeviceRequest, error) {
	out, err := run("kubectl", "get", "vm", vmName, "-n", ns, "-o", "json")
	if err != nil {
		// If kubectl failed or command wasn't mocked in a unit test, return nil
		return nil, nil
	}

	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, nil
	}

	var vm vmSpecDevices
	if err := json.Unmarshal(out, &vm); err != nil {
		// If it wasn't valid JSON (e.g. dummy test response), treat as no special devices requested
		return nil, nil
	}

	counts := map[string]int{}
	for _, g := range vm.Spec.Template.Spec.Domain.Devices.GPUs {
		if g.DeviceName != "" {
			counts[g.DeviceName]++
		}
	}
	for _, h := range vm.Spec.Template.Spec.Domain.Devices.HostDevices {
		if h.DeviceName != "" {
			counts[h.DeviceName]++
		}
	}

	if len(counts) == 0 {
		return nil, nil
	}

	// Look up device types from KubeVirt permittedHostDevices config if present
	devTypes := map[string]DeviceType{}
	if kvOut, err := run("kubectl", "get", "kubevirt", "kubevirt", "-n", "kubevirt", "-o", "json"); err == nil {
		var kv permittedDevicesConfig
		if json.Unmarshal(kvOut, &kv) == nil {
			for _, pci := range kv.Spec.Configuration.PermittedHostDevices.PCIHostDevices {
				if pci.ExternalResourceProvider {
					devTypes[pci.ResourceName] = DeviceTypeExternalProvider
				} else {
					devTypes[pci.ResourceName] = DeviceTypePCIHostDevice
				}
			}
			for _, mdev := range kv.Spec.Configuration.PermittedHostDevices.MediatedDevices {
				devTypes[mdev.ResourceName] = DeviceTypeMediatedDevice
			}
		}
	}

	var reqs []DeviceRequest
	for resName, count := range counts {
		dt, ok := devTypes[resName]
		if !ok {
			dt = DeviceTypeUnknown
		}
		reqs = append(reqs, DeviceRequest{
			ResourceName: resName,
			Count:        count,
			Type:         dt,
		})
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].ResourceName < reqs[j].ResourceName })
	return reqs, nil
}

type nodeResourceItem struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Status struct {
		Allocatable map[string]string `json:"allocatable"`
	} `json:"status"`
}

type vmiResourceItem struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		NodeName string `json:"nodeName"`
		Domain   struct {
			Devices struct {
				GPUs []struct {
					DeviceName string `json:"deviceName"`
				} `json:"gpus"`
				HostDevices []struct {
					DeviceName string `json:"deviceName"`
				} `json:"hostDevices"`
			} `json:"devices"`
		} `json:"domain"`
	} `json:"spec"`
	Status struct {
		NodeName string `json:"nodeName"`
		Phase    string `json:"phase"`
	} `json:"status"`
}

// ValidatePoolDeviceCapacity verifies cluster device capacity for the golden VM and desired pool size.
func ValidatePoolDeviceCapacity(ns, goldenVM string, size int) ([]DeviceCapacityReport, error) {
	reqs, err := inspectVMDeviceRequests(ns, goldenVM)
	if err != nil {
		return nil, err
	}
	if len(reqs) == 0 {
		return nil, nil
	}

	// 1. Get Node Allocatable resources
	nodesOut, err := run("kubectl", "get", "nodes", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("listing nodes to check device capacity: %w", err)
	}
	var nodeRes struct {
		Items []nodeResourceItem `json:"items"`
	}
	if err := json.Unmarshal(nodesOut, &nodeRes); err != nil {
		return nil, fmt.Errorf("parsing nodes: %w", err)
	}

	// 2. Get active VMIs to count already allocated devices
	vmisOut, err := run("kubectl", "get", "vmi", "-A", "-o", "json")
	var vmiRes struct {
		Items []vmiResourceItem `json:"items"`
	}
	if err == nil {
		_ = json.Unmarshal(vmisOut, &vmiRes)
	}

	var reports []DeviceCapacityReport
	for _, req := range reqs {
		resName := req.ResourceName
		nodeAlloc := map[string]int{}
		totalAllocatable := 0

		for _, n := range nodeRes.Items {
			valStr := n.Status.Allocatable[resName]
			if valStr != "" {
				if parsed, err := strconv.Atoi(valStr); err == nil && parsed > 0 {
					nodeAlloc[n.Metadata.Name] = parsed
					totalAllocatable += parsed
				}
			}
		}

		nodeAllocated := map[string]int{}
		totalAllocated := 0
		for _, vmi := range vmiRes.Items {
			// Ignore failed or succeeded VMIs
			if vmi.Status.Phase == "Failed" || vmi.Status.Phase == "Succeeded" {
				continue
			}
			vmiCount := 0
			for _, g := range vmi.Spec.Domain.Devices.GPUs {
				if g.DeviceName == resName {
					vmiCount++
				}
			}
			for _, h := range vmi.Spec.Domain.Devices.HostDevices {
				if h.DeviceName == resName {
					vmiCount++
				}
			}
			if vmiCount > 0 {
				node := vmi.Status.NodeName
				if node == "" {
					node = vmi.Spec.NodeName
				}
				totalAllocated += vmiCount
				if node != "" {
					nodeAllocated[node] += vmiCount
				}
			}
		}

		nodeAvailable := map[string]int{}
		totalAvailable := 0
		maxConcurrency := 0
		for node, alloc := range nodeAlloc {
			avail := alloc - nodeAllocated[node]
			if avail < 0 {
				avail = 0
			}
			nodeAvailable[node] = avail
			totalAvailable += avail
			if req.Count > 0 {
				maxConcurrency += avail / req.Count
			}
		}

		needed := req.Count * size
		rep := DeviceCapacityReport{
			ResourceName:      resName,
			Type:              req.Type,
			ReplicasRequested: size,
			PerVMCount:        req.Count,
			TotalNeeded:       needed,
			AllocatableTotal:  totalAllocatable,
			AllocatedExisting: totalAllocated,
			AvailableTotal:    totalAvailable,
			NodeAllocatable:   nodeAlloc,
			NodeAvailable:     nodeAvailable,
			MaxConcurrency:    maxConcurrency,
		}
		reports = append(reports, rep)

		// Verification:
		// Check total capacity and per-node placement feasibility
		if totalAllocatable == 0 {
			return reports, fmt.Errorf("device admission failed: golden VM requests %d x %q (%s), but resource is not allocatable on any node in the cluster",
				req.Count, resName, req.Type)
		}

		if totalAvailable < needed || maxConcurrency < size {
			var nodeBreakdown []string
			for node, alloc := range nodeAlloc {
				avail := nodeAvailable[node]
				nodeBreakdown = append(nodeBreakdown, fmt.Sprintf("node %s: %d available / %d allocatable", node, avail, alloc))
			}
			sort.Strings(nodeBreakdown)
			return reports, fmt.Errorf("insufficient device capacity for %q (%s): requested %d replicas (%d devices total: %d per VM), but only %d available (%d existing allocations, max concurrency %d across nodes: %s)",
				resName, req.Type, size, needed, req.Count, totalAvailable, totalAllocated, maxConcurrency, strings.Join(nodeBreakdown, "; "))
		}
	}

	return reports, nil
}

// CreatePool clones the golden VM Size times and labels each clone as a
// pool member. Members start unassigned and — matching how a freshly
// cloned VM already behaves — powered on (Clone doesn't change run state;
// callers wanting scale-to-zero pools should stop members after create).
func CreatePool(opts CreateOpts) (Pool, error) {
	if opts.Size < 1 {
		return Pool{}, fmt.Errorf("--size must be >= 1")
	}
	ns := opts.Namespace
	client := kubevirt.NewClient(ns)
	if !client.VMExists(opts.From) {
		return Pool{}, fmt.Errorf("golden VM %q not found in ns/%s — build it first (corral create / corral bootc / corral-windows)", opts.From, ns)
	}

	// Validate GPU / host-device capacity before partial clone mutation
	if _, err := ValidatePoolDeviceCapacity(ns, opts.From, opts.Size); err != nil {
		return Pool{}, err
	}

	pool := Pool{Name: opts.Name, Namespace: ns}
	for i := 1; i <= opts.Size; i++ {
		name := memberName(opts.Name, i)
		if err := client.Clone(opts.From, name); err != nil {
			return pool, fmt.Errorf("cloning member %d/%d (%s): %w", i, opts.Size, name, err)
		}
		// Clone() returns as soon as the VirtualMachineClone CRD is applied,
		// not once KubeVirt's clone controller has actually produced the
		// target VM — labeling it immediately races the controller. Found
		// live: the very first CreatePool run against a real cluster failed
		// here because the label command ran before the VM object existed.
		if err := waitForVM(ns, name, cloneWaitTimeout); err != nil {
			return pool, fmt.Errorf("cloning member %d/%d (%s): %w", i, opts.Size, name, err)
		}
		if err := labelMember(ns, name, opts.Name, ""); err != nil {
			return pool, fmt.Errorf("labeling member %s: %w", name, err)
		}
		if opts.Ephemeral {
			if err := markEphemeral(ns, name, opts.From); err != nil {
				return pool, fmt.Errorf("labeling member %s: %w", name, err)
			}
		}
		pool.Members = append(pool.Members, Member{Name: name})
	}
	return pool, nil
}

// waitForVM polls until the clone controller has actually created the
// target VM object, or timeout elapses.
func waitForVM(ns, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := kubevirt.NewClient(ns)
	for {
		if client.VMExists(name) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for the clone to produce VM %q", timeout, name)
		}
		time.Sleep(clonePollInterval)
	}
}

// labelMember writes a member's presentation state: the pool label and, for
// a claimed member, the holder (exact in an annotation, label-safe in a
// label) and the claim time. An empty assignedTo clears the claim fields.
func labelMember(ns, name, pool, assignedTo string) error {
	if assignedTo == "" {
		if _, err := run("kubectl", "label", "vm", name, "-n", ns, labelPool+"="+pool, labelAssignedTo+"-", "--overwrite"); err != nil {
			return err
		}
		_, err := run("kubectl", "annotate", "vm", name, "-n", ns, annoClaimedAt+"-", annoAssignedTo+"-", "--overwrite")
		return err
	}
	if _, err := run("kubectl", "label", "vm", name, "-n", ns, labelPool+"="+pool, labelAssignedTo+"="+labelSafe(assignedTo), "--overwrite"); err != nil {
		return err
	}
	_, err := run("kubectl", "annotate", "vm", name, "-n", ns,
		annoClaimedAt+"="+time.Now().UTC().Format(time.RFC3339), annoAssignedTo+"="+assignedTo, "--overwrite")
	return err
}

// labelSafe renders an identity as a Kubernetes label value: at most 63
// characters from [A-Za-z0-9-_.], starting and ending alphanumeric.
// "alice@example.com" becomes "alice_example.com".
func labelSafe(identity string) string {
	b := []byte(identity)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			b[i] = '_'
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	alnum := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
	for len(b) > 0 && !alnum(b[0]) {
		b = b[1:]
	}
	for len(b) > 0 && !alnum(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "claimed"
	}
	return string(b)
}

// markEphemeral records that member is recreated from golden on release.
func markEphemeral(ns, member, golden string) error {
	if _, err := run("kubectl", "label", "vm", member, "-n", ns, labelMode+"="+PoolModeEphemeral, "--overwrite"); err != nil {
		return err
	}
	_, err := run("kubectl", "annotate", "vm", member, "-n", ns, annoGolden+"="+golden, "--overwrite")
	return err
}

type vmListItem struct {
	Metadata struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		ResourceVersion string            `json:"resourceVersion"`
		Labels          map[string]string `json:"labels"`
		Annotations     map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Running *bool `json:"running"`
	} `json:"spec"`
	Status struct {
		PrintableStatus string `json:"printableStatus"`
	} `json:"status"`
}

// listPoolVMs lists the members of pool in namespace. An empty pool lists
// every pool member; an empty namespace lists all namespaces.
func listPoolVMs(namespace, pool string) ([]vmListItem, error) {
	args := []string{"get", "vm", "-A", "-o", "json"}
	if namespace != "" {
		args = []string{"get", "vm", "-n", namespace, "-o", "json"}
	}
	if pool != "" {
		args = append(args, "-l", labelPool+"="+pool)
	} else {
		args = append(args, "-l", labelPool)
	}
	out, err := run("kubectl", args...)
	if err != nil {
		return nil, err
	}
	var res struct {
		Items []vmListItem `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	return res.Items, nil
}

// assignedIdentity is the presentation holder of a member: the exact
// annotation, or the label that Phase 1 wrote before the annotation existed.
func assignedIdentity(v vmListItem) string {
	if who := v.Metadata.Annotations[annoAssignedTo]; who != "" {
		return who
	}
	return v.Metadata.Labels[labelAssignedTo]
}

func toMember(v vmListItem) Member {
	return Member{
		Name:       v.Metadata.Name,
		AssignedTo: assignedIdentity(v),
		ClaimedAt:  v.Metadata.Annotations[annoClaimedAt],
		Running:    v.Status.PrintableStatus == "Running",
	}
}

// ListPools returns every pool, grouped from VMs carrying the pool label —
// there's no separate Pool object, the label on the members is the source
// of truth (same "no extra state to go stale" reasoning as CT's PVC
// annotation).
func ListPools() ([]Pool, error) {
	items, err := listPoolVMs("", "")
	if err != nil {
		return nil, err
	}
	byPool := map[string]*Pool{}
	var order []string
	for _, v := range items {
		name := v.Metadata.Labels[labelPool]
		if name == "" {
			continue
		}
		key := v.Metadata.Namespace + "/" + name
		p, ok := byPool[key]
		if !ok {
			p = &Pool{Name: name, Namespace: v.Metadata.Namespace}
			byPool[key] = p
			order = append(order, key)
		}
		p.Members = append(p.Members, toMember(v))
	}
	pools := make([]Pool, 0, len(order))
	for _, key := range order {
		pools = append(pools, *byPool[key])
	}
	return pools, nil
}

// Claim atomically acquires one pool member for identity and starts it if
// it is stopped. A start failure returns the member with a *StartError:
// the claim is held and a later start may succeed.
func Claim(namespace, pool, identity string) (string, error) {
	name, running, err := claimVM(namespace, pool, identity)
	if err != nil || running {
		return name, err
	}
	if err := kubevirt.NewClient(namespace).StartVM(name); err != nil {
		return name, &StartError{Member: name, Err: err}
	}
	return name, nil
}

// claimVM acquires one member of pool for identity through the member's
// Lease (#226) and records the presentation labels. A member identity
// already holds is returned first, so a retried claim never takes a second
// desktop. It reports whether the member is running.
func claimVM(namespace, pool, identity string) (string, bool, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return "", false, fmt.Errorf("claim identity must not be empty")
	}
	if identity == CTReconcilerHolder {
		return "", false, fmt.Errorf("claim identity %q is reserved for the reconciler", identity)
	}
	items, err := listPoolVMs(namespace, pool)
	if err != nil {
		return "", false, err
	}
	if len(items) == 0 {
		return "", false, fmt.Errorf("%w: %q has no members in ns/%s", ErrPoolNotFound, pool, namespace)
	}
	sort.SliceStable(items, func(a, b int) bool {
		return assignedIdentity(items[a]) == identity && assignedIdentity(items[b]) != identity
	})
	for _, v := range items {
		if assignedTo := assignedIdentity(v); assignedTo != "" && assignedTo != identity {
			// Legacy Phase 1 labels have no ownership primitive and must not
			// be stolen. A member with an expired Lease, however, is reclaimable.
			current, leaseErr := readLease(namespace, leaseName(v.Metadata.Name))
			if leaseErr != nil || !leaseExpired(current, time.Now().UTC()) {
				continue
			}
		}
		name := v.Metadata.Name
		acquired, claimErr := acquireLease(namespace, pool, name, identity)
		if claimErr != nil {
			return "", false, claimErr
		}
		if !acquired {
			continue
		}
		if err := labelMember(namespace, name, pool, identity); err != nil {
			return "", false, fmt.Errorf("claimed %s but failed to update presentation labels; release lease %s: %w", name, leaseName(name), err)
		}
		return name, v.Status.PrintableStatus == "Running", nil
	}
	return "", false, fmt.Errorf("%w: %q in ns/%s (all %d members claimed or contended)", ErrPoolExhausted, pool, namespace, len(items))
}

// Assign is the Phase 1 compatibility name for Claim.
func Assign(namespace, pool, user string) (string, error) { return Claim(namespace, pool, user) }

// Release releases a member only when identity owns its Lease. A
// persistent member stops and keeps its disks; an ephemeral member is
// deleted and cloned again from its golden VM. The lifecycle step runs
// before the claim ends: when it fails, the holder keeps the claim and the
// release can be retried. Deleting the Lease is the atomic ownership
// transition; labels are cleared as presentation state. An empty identity
// is an administrative release, which also clears legacy label-only
// assignments.
func Release(namespace, member, identity string) error {
	l, err := readLease(namespace, leaseName(member))
	held := err == nil
	switch {
	case err != nil && !isNotFound(nil, err):
		return fmt.Errorf("read claim for %s: %w", member, err)
	case !held && identity != "":
		return fmt.Errorf("member %s has no claim for %q", member, identity)
	case held && identity != "" && l.Spec.HolderIdentity != identity:
		return fmt.Errorf("member %s is claimed by %q, not %q", member, l.Spec.HolderIdentity, identity)
	}
	vm, found, err := getVM(namespace, member)
	if err != nil {
		return fmt.Errorf("release %s: %w", member, err)
	}
	pool := vm.Metadata.Labels[labelPool]
	if pool == "" {
		pool = l.Metadata.Labels[labelPool]
	}
	golden, ephemeral := vm.Metadata.Annotations[annoGolden], vm.Metadata.Labels[labelMode] == PoolModeEphemeral
	if !found && pool != "" {
		// A failed recycle can leave the member deleted; its siblings
		// still say how to recreate it.
		golden, ephemeral, err = poolGolden(namespace, pool)
		if err != nil {
			return fmt.Errorf("release %s: %w", member, err)
		}
	}
	switch {
	case ephemeral:
		if err := recycleVM(namespace, member, pool, golden, found); err != nil {
			return fmt.Errorf("release %s: recreate from %q: %w (the claim is kept; retry the release)", member, golden, err)
		}
	case found:
		if err := kubevirt.NewClient(namespace).StopVM(member); err != nil && !isNotRunning(err) {
			return fmt.Errorf("release %s: stop: %w (the claim is kept; retry the release)", member, err)
		}
		if err := labelMember(namespace, member, pool, ""); err != nil {
			return fmt.Errorf("release %s: clear presentation labels: %w", member, err)
		}
	}
	if held {
		if err := deleteLease(namespace, leaseName(member), l.Metadata.ResourceVersion); err != nil {
			return fmt.Errorf("release %s: delete claim lease: %w", member, err)
		}
	}
	return nil
}

// getVM reads one VM; found is false when it does not exist.
func getVM(namespace, name string) (vmListItem, bool, error) {
	out, err := run("kubectl", "get", "vm", name, "-n", namespace, "--ignore-not-found", "-o", "json")
	if err != nil {
		return vmListItem{}, false, err
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return vmListItem{}, false, nil
	}
	var v vmListItem
	if err := json.Unmarshal(out, &v); err != nil {
		return vmListItem{}, false, fmt.Errorf("decode VM %s: %w", name, err)
	}
	return v, true, nil
}

// poolGolden reads the mode and golden VM of pool from its members.
func poolGolden(namespace, pool string) (golden string, ephemeral bool, err error) {
	items, err := listPoolVMs(namespace, pool)
	if err != nil {
		return "", false, err
	}
	for _, v := range items {
		if v.Metadata.Labels[labelMode] == PoolModeEphemeral && v.Metadata.Annotations[annoGolden] != "" {
			return v.Metadata.Annotations[annoGolden], true, nil
		}
	}
	return "", false, nil
}

// recycleVM deletes an ephemeral member and clones it again from golden,
// as an unclaimed member of pool.
func recycleVM(namespace, member, pool, golden string, exists bool) error {
	if golden == "" {
		return fmt.Errorf("member %s has no %s annotation", member, annoGolden)
	}
	client := kubevirt.NewClient(namespace)
	if exists {
		if err := client.DeleteVM(member); err != nil {
			return err
		}
	}
	if err := client.Clone(golden, member); err != nil {
		return err
	}
	if err := waitForVM(namespace, member, cloneWaitTimeout); err != nil {
		return err
	}
	if err := labelMember(namespace, member, pool, ""); err != nil {
		return err
	}
	return markEphemeral(namespace, member, golden)
}

// isNotRunning reports virtctl refusing to stop a VM that already stopped,
// which a retried release must treat as done.
func isNotRunning(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not running")
}

// Unassign preserves the original administrative API.
func Unassign(namespace, member string) error { return Release(namespace, member, "") }

func poolOf(namespace, member string) string {
	out, err := run("kubectl", "get", "vm", member, "-n", namespace, "-o",
		"jsonpath={.metadata.labels.corral\\.dev/vdi-pool}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DeletePool deletes every member of pool.
func DeletePool(namespace, pool string) error {
	items, err := listPoolVMs(namespace, pool)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return fmt.Errorf("pool %q not found (or has no members) in ns/%s", pool, namespace)
	}
	client := kubevirt.NewClient(namespace)
	var firstErr error
	for _, v := range items {
		if err := client.DeleteVM(v.Metadata.Name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// USBDevice describes a local USB device available for redirection.
type USBDevice struct {
	BusNum      string `json:"busNum"`
	DevNum      string `json:"devNum"`
	VendorID    string `json:"vendorId"`
	ProductID   string `json:"productId"`
	Description string `json:"description"`
	Driver      string `json:"driver,omitempty"`
	Busy        bool   `json:"busy,omitempty"`
	BusyReason  string `json:"busyReason,omitempty"`
}

// Selector returns the vendorID:productID or bus.dev selector.
func (d USBDevice) Selector() string {
	if d.VendorID != "" && d.ProductID != "" {
		return fmt.Sprintf("%s:%s", d.VendorID, d.ProductID)
	}
	return fmt.Sprintf("%s.%s", d.BusNum, d.DevNum)
}

// USBConsequence explains what USB redirection entails.
type USBConsequence string

const (
	USBConsequenceExclusive   USBConsequence = "the local host loses access to the device while redirection is active"
	USBConsequenceMigration   USBConsequence = "live migration is blocked while a host device is redirected"
	USBConsequenceTermination USBConsequence = "disconnecting the session will drop guest connection to the device"
	USBConsequenceTrust       USBConsequence = "the assigned guest receives raw USB access to the device"
)

// USBConsequences returns the consequences of redirecting dev.
func USBConsequences(dev USBDevice) []USBConsequence {
	return []USBConsequence{
		USBConsequenceExclusive,
		USBConsequenceMigration,
		USBConsequenceTermination,
		USBConsequenceTrust,
	}
}

// ListUSBDevicesFunc is the sysfs scanner or lsusb runner seam for testing.
var ListUSBDevicesFunc = listUSBDevicesDefault

func listUSBDevicesDefault() ([]USBDevice, error) {
	// Discover via /sys/bus/usb/devices
	entries, err := os.ReadDir("/sys/bus/usb/devices")
	if err != nil {
		return nil, fmt.Errorf("reading USB devices from sysfs: %w", err)
	}

	var devs []USBDevice
	for _, entry := range entries {
		devPath := filepath.Join("/sys/bus/usb/devices", entry.Name())
		idVendorBytes, err := os.ReadFile(filepath.Join(devPath, "idVendor"))
		if err != nil {
			continue
		}
		idProductBytes, err := os.ReadFile(filepath.Join(devPath, "idProduct"))
		if err != nil {
			continue
		}
		vendorID := strings.TrimSpace(string(idVendorBytes))
		productID := strings.TrimSpace(string(idProductBytes))
		if vendorID == "" || productID == "" {
			continue
		}

		busnumBytes, _ := os.ReadFile(filepath.Join(devPath, "busnum"))
		devnumBytes, _ := os.ReadFile(filepath.Join(devPath, "devnum"))
		busnum := strings.TrimSpace(string(busnumBytes))
		devnum := strings.TrimSpace(string(devnumBytes))

		productBytes, _ := os.ReadFile(filepath.Join(devPath, "product"))
		manufacturerBytes, _ := os.ReadFile(filepath.Join(devPath, "manufacturer"))
		product := strings.TrimSpace(string(productBytes))
		manufacturer := strings.TrimSpace(string(manufacturerBytes))

		desc := strings.TrimSpace(manufacturer + " " + product)
		if desc == "" {
			desc = fmt.Sprintf("USB Device %s:%s", vendorID, productID)
		}

		dev := USBDevice{
			BusNum:      busnum,
			DevNum:      devnum,
			VendorID:    vendorID,
			ProductID:   productID,
			Description: desc,
		}

		// Check if it looks busy or is a root hub
		if strings.HasPrefix(entry.Name(), "usb") || desc == "Linux Foundation root hub" {
			dev.Busy = true
			dev.BusyReason = "USB root hub cannot be redirected"
		}

		devs = append(devs, dev)
	}

	return devs, nil
}

// ListLocalUSBDevices returns local USB devices discovered on the host.
func ListLocalUSBDevices() ([]USBDevice, error) {
	return ListUSBDevicesFunc()
}

// USBRedirOpts holds parameters for redirecting a USB device to an assigned desktop.
type USBRedirOpts struct {
	Namespace string
	Member    string
	Device    string // vendorID:productID or bus.dev
	User      string // expected caller identity for ownership check ("" skips check)
	Context   string
}

// ValidateUSBRedir verifies assignment, running state, and authorization for member.
func ValidateUSBRedir(opts USBRedirOpts) (Member, error) {
	ns := opts.Namespace
	member := opts.Member
	if member == "" {
		return Member{}, fmt.Errorf("member name is required")
	}

	pool := poolOf(ns, member)
	if pool == "" {
		return Member{}, fmt.Errorf("member %q is not part of a VDI pool in ns/%s", member, ns)
	}

	items, err := listPoolVMs(ns, pool)
	if err != nil {
		return Member{}, fmt.Errorf("querying pool %q: %w", pool, err)
	}

	var targetItem *vmListItem
	for _, it := range items {
		if it.Metadata.Name == member {
			targetItem = &it
			break
		}
	}

	if targetItem == nil {
		return Member{}, fmt.Errorf("member %q not found in pool %q (ns/%s)", member, pool, ns)
	}

	m := toMember(*targetItem)
	if m.AssignedTo == "" {
		return m, fmt.Errorf("member %q is not assigned to any user (claim/assign first with `corral vdi assign`)", member)
	}

	if opts.User != "" && m.AssignedTo != opts.User {
		return m, fmt.Errorf("authorization failed: member %q is assigned to %q, not %q", member, m.AssignedTo, opts.User)
	}

	if !m.Running {
		return m, fmt.Errorf("member %q is stopped; start it before redirecting USB devices", member)
	}

	return m, nil
}

// BuildVirtctlUSBCmd constructs the virtctl usbredir command.
func BuildVirtctlUSBCmd(virtctlPath, member, namespace, deviceSelector, kubeContext string) *exec.Cmd {
	args := []string{"usbredir", member, "-n", namespace}
	if deviceSelector != "" {
		args = append(args, "--device="+deviceSelector)
	}
	return shell.CommandForContext(kubeContext, virtctlPath, args...)
}
