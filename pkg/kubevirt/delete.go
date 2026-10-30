package kubevirt

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// VMLabel ties an object to the VM corral created it for, so a delete finds
// it by selector even when its name does not follow corral's naming. Corral
// never sets it on a VirtualMachine: a VirtualMachineClone copies the source
// VM's labels to the clone, and the clone would then match its source.
const VMLabel = "corral.dev/vm"

// labelForVM sets VMLabel=vm on a manifest's metadata, keeping any labels it
// already has, and returns the manifest.
func labelForVM(obj map[string]any, vm string) map[string]any {
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		obj["metadata"] = meta
	}
	switch labels := meta["labels"].(type) {
	case map[string]any:
		labels[VMLabel] = vm
	case map[string]string:
		labels[VMLabel] = vm
	default:
		meta["labels"] = map[string]any{VMLabel: vm}
	}
	return obj
}

// bootcBuilderOf is the bootc builder VM corral runs to install name's disk.
func bootcBuilderOf(name string) string { return name + "-bootc-builder" }

// PurgeVM deletes a VM and every object corral created for it, and returns
// each removed object as kubectl names it ("persistentvolumeclaim/web-disk").
//
// It deletes by name, which covers VMs created before VMLabel existed, and by
// VMLabel, which covers hotplugged disks, snapshots and anything named
// differently:
//
//   - the VM, its VMI, and a leftover bootc builder VM (<name>-bootc-builder)
//     with its VMI, cloud-init Secret, and size-check pod;
//   - the disks: <name>-disk, -data, -iso, -bootc-disk DataVolumes and PVCs,
//     plus labelled ones that no other VM mounts (a clone's disks inherit the
//     source's labels, so a mounted disk is never collected);
//   - the cloud-init Secret, the Windows answer-file ConfigMap, the tailnet
//     and LAN proxy objects, the scheduled start/stop, snapshot and backup
//     CronJobs with the backup's rclone Secret;
//   - labelled VirtualMachineSnapshots, VirtualMachineRestores and
//     VirtualMachineClones.
//
// A missing object or an object kind the cluster does not serve is not an
// error. Any other kubectl failure is, and the remaining steps still run, so
// one failure does not strand the rest. Deletes do not wait for finalizers;
// WaitRemoved does that.
func (c *Client) PurgeVM(name string) ([]string, error) {
	p := &purge{c: c}
	builder := bootcBuilderOf(name)
	selector := VMLabel + "=" + name

	// The VMs first, so the guests stop and release their disks.
	p.delete("vm", name)
	p.delete("vm", builder)
	p.delete("vmi", name, builder)

	// DataVolume before PVC: while a CDI importer pod still mounts the PVC,
	// the PVC waits on its protection finalizer, and deleting the DataVolume
	// is what stops the importer.
	disks := make([]string, 0, len(pvcSuffixes))
	for _, suffix := range pvcSuffixes {
		disks = append(disks, name+suffix)
	}
	p.delete("datavolume", disks...)
	p.delete("pvc", disks...)
	p.deleteUnmountedDisks(name, selector, disks)

	p.delete("secret", name+"-cloudinit", builder+"-cloudinit", "corral-backup-rclone-"+name)
	p.delete("configmap", AutounattendConfigMapName(name))
	p.delete("pod", name+"-sizecheck")
	p.delete("deployment", name+"-proxy")
	p.delete("service", name+"-proxy", name+"-lan")
	p.delete("serviceaccount", "corral-"+name+"-proxy")
	p.delete("role", "corral-"+name+"-proxy")
	p.delete("rolebinding", "corral-"+name+"-proxy")
	// The corral-schedule, corral-snapsched and corral-backup plugins name
	// their per-VM CronJobs this way.
	p.delete("cronjob", "corral-start-"+name, "corral-stop-"+name, "corral-snap-"+name, "corral-backup-"+name)

	p.deleteSelected("service,deployment,serviceaccount", selector)
	for _, kind := range []string{"vmsnapshot", "vmrestore", "vmclone"} {
		p.deleteSelected(kind, selector)
	}
	return p.removed, errors.Join(p.errs...)
}

// purge collects what one PurgeVM removed and what failed.
type purge struct {
	c       *Client
	removed []string
	errs    []error
}

// delete removes the named objects of one kind.
func (p *purge) delete(kind string, names ...string) {
	args := append([]string{"delete", kind}, names...)
	p.run(kind, append(args, "-n", p.c.Namespace, "--ignore-not-found", "--wait=false", "-o", "name"))
}

// deleteSelected removes every object of the kinds that matches selector.
func (p *purge) deleteSelected(kinds, selector string) {
	p.run(kinds, []string{"delete", kinds, "-n", p.c.Namespace, "-l", selector,
		"--ignore-not-found", "--wait=false", "-o", "name"})
}

func (p *purge) run(kind string, args []string) {
	out, err := p.c.runner().Run("kubectl", args...)
	for _, obj := range objectNames(out) {
		// A selector also matches an object an earlier delete left
		// terminating; report it once.
		if !slices.Contains(p.removed, obj) {
			p.removed = append(p.removed, obj)
		}
	}
	if err != nil && !kindNotServed(out) {
		p.errs = append(p.errs, fmt.Errorf("delete %s: %w", kind, commandError(out, err)))
	}
}

// deleteUnmountedDisks removes the DataVolumes and PVCs labelled for name
// that no other VM mounts, apart from those already deleted by name.
func (p *purge) deleteUnmountedDisks(name, selector string, named []string) {
	labelled := map[string][]string{}
	for _, kind := range []string{"datavolume", "pvc"} {
		out, err := p.c.runner().Run("kubectl", "get", kind, "-n", p.c.Namespace, "-l", selector,
			"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
		if err != nil {
			if !kindNotServed(out) {
				p.errs = append(p.errs, fmt.Errorf("list %s: %w", kind, commandError(out, err)))
			}
			continue
		}
		for _, disk := range strings.Fields(string(out)) {
			if !slices.Contains(named, disk) {
				labelled[kind] = append(labelled[kind], disk)
			}
		}
	}
	if len(labelled["datavolume"]) == 0 && len(labelled["pvc"]) == 0 {
		return
	}
	mounted, err := p.c.disksMountedByOthers(name)
	if err != nil {
		p.errs = append(p.errs, err)
		return
	}
	for _, kind := range []string{"datavolume", "pvc"} {
		var free []string
		for _, disk := range labelled[kind] {
			if !mounted[disk] {
				free = append(free, disk)
			}
		}
		if len(free) > 0 {
			p.delete(kind, free...)
		}
	}
}

// disksMountedByOthers returns the PVC and DataVolume names that a VM other
// than name or its bootc builder mounts.
func (c *Client) disksMountedByOthers(name string) (map[string]bool, error) {
	out, err := c.runner().Run("kubectl", "get", "vm", "-n", c.Namespace, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("list vm: %w", commandError(out, err))
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Volumes []struct {
							PersistentVolumeClaim *struct {
								ClaimName string `json:"claimName"`
							} `json:"persistentVolumeClaim"`
							DataVolume *struct {
								Name string `json:"name"`
							} `json:"dataVolume"`
						} `json:"volumes"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("list vm: %w", err)
	}
	mounted := map[string]bool{}
	for _, vm := range list.Items {
		if vm.Metadata.Name == name || vm.Metadata.Name == bootcBuilderOf(name) {
			continue
		}
		for _, v := range vm.Spec.Template.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				mounted[v.PersistentVolumeClaim.ClaimName] = true
			}
			if v.DataVolume != nil {
				mounted[v.DataVolume.Name] = true
			}
		}
	}
	return mounted, nil
}

// objectNameRe matches one line of `kubectl -o name` output, for example
// "persistentvolumeclaim/web-disk" or "virtualmachine.kubevirt.io/web".
var objectNameRe = regexp.MustCompile(`^[a-z0-9.-]+/[a-z0-9.-]+$`)

// objectNames returns the object names in kubectl -o name output, skipping
// notices such as "No resources found" and warnings.
func objectNames(out []byte) []string {
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); objectNameRe.MatchString(line) {
			names = append(names, line)
		}
	}
	return names
}

// kindNotServed reports kubectl refusing a kind the cluster has no API for,
// such as vmrestore without the snapshot API. Nothing of that kind exists,
// so there is nothing to delete.
func kindNotServed(out []byte) bool {
	return strings.Contains(string(out), "the server doesn't have a resource type")
}

// commandError adds kubectl's own message to a bare exit status.
func commandError(out []byte, err error) error {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// DeletePollInterval is how often WaitRemoved checks for remaining objects.
// Tests shorten it.
var DeletePollInterval = 2 * time.Second

// WaitRemoved blocks until none of objects (as PurgeVM returns them) exists,
// or timeout passes. It returns an error naming what remains at the timeout.
func (c *Client) WaitRemoved(objects []string, timeout time.Duration) error {
	if len(objects) == 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	args := append([]string{"get"}, objects...)
	args = append(args, "-n", c.Namespace, "--ignore-not-found", "-o", "name")
	for {
		out, err := c.runner().Run("kubectl", args...)
		remaining := objectNames(out)
		if err == nil && len(remaining) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("could not confirm the delete after %s: %w", timeout, commandError(out, err))
			}
			return fmt.Errorf("still present after %s: %s", timeout, strings.Join(remaining, ", "))
		}
		time.Sleep(DeletePollInterval)
	}
}
