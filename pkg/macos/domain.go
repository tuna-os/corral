package macos

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tuna-os/corral/pkg/shell"
)

//go:embed domain_hook.py
var domainHook string

const manager = "corral-macos"
const ownerLabel = "corral.dev/macos"
const openCoreSHAAnnotation = "corral.dev/opencore-sha256"
const selectedNodeAnnotation = "volume.kubernetes.io/selected-node"

// Options describes a new macOS VM. Existing VMs and disks are never reapplied.
type Options struct {
	Name, Namespace, Node, MediaDir, Memory, Disk, UploadProxyURL string
	IdentityDir                                                   string
	CPU                                                           int
	EnableSidecar, Start                                          bool
}

// Client is bound to an explicit kubeconfig context by its caller. It never
// changes kubectl's current context or any host configuration.
type Client struct {
	Runner  shell.Runner
	Context context.Context
}

type object = map[string]any

func nested(m map[string]any, keys ...string) any {
	var value any = m
	for _, key := range keys {
		mapping, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = mapping[key]
	}
	return value
}

func (c Client) get(kind, name, namespace string) (object, error) {
	args := []string{"get", kind}
	if name != "" {
		args = append(args, name)
	}
	if namespace != "" {
		args = append(args, "-n", namespace)
	} else {
		args = append(args, "-A")
	}
	args = append(args, "--ignore-not-found", "-o", "json")
	out, err := c.Runner.Run("kubectl", args...)
	if err != nil {
		return nil, fmt.Errorf("reading %s %s: %w: %s", kind, name, err, strings.TrimSpace(string(out)))
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, nil
	}
	var result object
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("decoding %s %s: %w", kind, name, err)
	}
	return result, nil
}

func labels(name string) map[string]any {
	return map[string]any{"app.kubernetes.io/managed-by": manager, ownerLabel: name}
}

func metadata(name, namespace, owner string) map[string]any {
	return map[string]any{"name": name, "namespace": namespace, "labels": labels(owner)}
}

func owned(m object, name string) bool {
	return m != nil && nested(m, "metadata", "labels", "app.kubernetes.io/managed-by") == manager && nested(m, "metadata", "labels", ownerLabel) == name
}

func checkOwned(m object, kind, resource, name string) error {
	if m != nil && !owned(m, name) {
		return fmt.Errorf("refusing unrelated %s %q: not owned by corral macos VM %q", kind, resource, name)
	}
	return nil
}

// PVCs created by CDI do not necessarily inherit labels on every release.
// Accept its controller reference only when the referenced DV itself is owned.
func ownedPVC(pvc, dv object, name string) bool {
	if owned(pvc, name) {
		return true
	}
	if !owned(dv, name) || pvc == nil {
		return false
	}
	refs, _ := nested(pvc, "metadata", "ownerReferences").([]any)
	uid, _ := nested(dv, "metadata", "uid").(string)
	for _, raw := range refs {
		ref, ok := raw.(map[string]any)
		if ok && uid != "" && ref["kind"] == "DataVolume" && ref["controller"] == true && ref["uid"] == uid && ref["name"] == nested(dv, "metadata", "name") {
			return true
		}
	}
	return false
}

func (c Client) ensurePVCLabels(o Options, name string, dv, pvc object) error {
	if pvc == nil {
		out, err := c.Runner.Run("kubectl", "wait", "pvc", name, "-n", o.Namespace, "--for=create", "--timeout=300s")
		if err != nil {
			return fmt.Errorf("waiting for CDI PVC %s: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		pvc, err = c.get("pvc", name, o.Namespace)
		if err != nil {
			return err
		}
		dv, err = c.get("dv", name, o.Namespace)
		if err != nil {
			return err
		}
	}
	if !ownedPVC(pvc, dv, o.Name) {
		return fmt.Errorf("refusing unrelated CDI PVC %q", name)
	}
	if err := c.checkPVCNode(o, name, pvc); err != nil {
		return err
	}
	annotations := object{}
	volume, _ := nested(pvc, "spec", "volumeName").(string)
	if o.Node != "" && nested(pvc, "status", "phase") != "Bound" && volume == "" {
		annotations[selectedNodeAnnotation] = o.Node
	}
	if nested(pvc, "metadata", "labels", ownerLabel) == o.Name && (len(annotations) == 0 || nested(pvc, "metadata", "annotations", selectedNodeAnnotation) == o.Node) {
		return nil
	}
	// CDI owns its managed-by label and updates status/annotations concurrently.
	// Guard the PVC's immutable identity, not its frequently changing version.
	uid, _ := nested(pvc, "metadata", "uid").(string)
	if uid == "" {
		return fmt.Errorf("owned CDI PVC %q has no UID; refusing an unguarded metadata patch", name)
	}
	meta := object{"uid": uid, "labels": object{ownerLabel: o.Name}}
	if len(annotations) != 0 {
		meta["annotations"] = annotations
	}
	patch, err := json.Marshal(object{"metadata": meta})
	if err != nil {
		return err
	}
	out, err := c.Runner.Run("kubectl", "patch", "pvc", name, "-n", o.Namespace, "--type=merge", "-p", string(patch))
	if err != nil {
		return fmt.Errorf("labelling owned CDI PVC %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Check placement before any mutation. A selected node is never reset, and an
// already bound local PV must remain compatible with the requested VM host.
func (c Client) checkPVCNode(o Options, name string, pvc object) error {
	if pvc == nil || o.Node == "" {
		return nil
	}
	selected, _ := nested(pvc, "metadata", "annotations", selectedNodeAnnotation).(string)
	if selected != "" && selected != o.Node {
		return fmt.Errorf("PVC %q is selected for node %q, not requested node %q; refusing to move it", name, selected, o.Node)
	}
	volume, _ := nested(pvc, "spec", "volumeName").(string)
	if volume == "" {
		return nil
	}
	pv, err := c.get("pv", volume, "")
	if err != nil {
		return err
	}
	if pv == nil {
		return fmt.Errorf("PVC %q references missing PV %q", name, volume)
	}
	terms, _ := nested(pv, "spec", "nodeAffinity", "required", "nodeSelectorTerms").([]any)
	if terms == nil {
		return nil
	}
	for _, raw := range terms {
		term, _ := raw.(map[string]any)
		possible := false
		for _, field := range []string{"matchExpressions", "matchFields"} {
			requirements, _ := term[field].([]any)
			for _, raw := range requirements {
				requirement, _ := raw.(map[string]any)
				possible = true
				// Other labels cannot be inferred from --node. Reject only a
				// provable mismatch, rather than guessing their values.
				if (field != "matchExpressions" || requirement["key"] != "kubernetes.io/hostname") && (field != "matchFields" || requirement["key"] != "metadata.name") {
					continue
				}
				values, _ := requirement["values"].([]any)
				contains := false
				for _, value := range values {
					contains = contains || value == o.Node
				}
				if (requirement["operator"] == "In" && !contains) || (requirement["operator"] == "NotIn" && contains) || requirement["operator"] == "DoesNotExist" {
					possible = false
					break
				}
			}
			if !possible && len(requirements) != 0 {
				break
			}
		}
		if possible {
			return nil
		}
	}
	return fmt.Errorf("PV %q for PVC %q has node affinity incompatible with requested node %q; refusing to move it", volume, name, o.Node)
}

func populated(dv, pvc object) bool {
	if nested(dv, "status", "phase") == "Succeeded" {
		return pvc != nil
	}
	if pvc == nil {
		return false
	}
	if nested(pvc, "metadata", "annotations", "cdi.kubevirt.io/storage.pod.phase") == "Succeeded" {
		return true
	}
	value, _ := nested(pvc, "metadata", "annotations", "cdi.kubevirt.io/storage.populatedFor").(string)
	return value != "" && value == nested(pvc, "metadata", "name")
}

func (c Client) create(m object) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	out, err := c.Runner.RunStdin(string(data), "kubectl", "create", "-f", "-")
	if err != nil {
		return fmt.Errorf("creating %s %v: %w: %s", m["kind"], nested(m, "metadata", "name"), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c Client) installation() (object, error) {
	list, err := c.get("kubevirt", "", "")
	if err != nil {
		return nil, err
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		return nil, fmt.Errorf("expected exactly one KubeVirt installation, found %d", len(items))
	}
	kv, ok := items[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid KubeVirt installation response")
	}
	return kv, nil
}

func sidecarEnabled(kv object) bool {
	gates, _ := nested(kv, "spec", "configuration", "developerConfiguration", "featureGates").([]any)
	for _, gate := range gates {
		if gate == "Sidecar" {
			return true
		}
	}
	return false
}

// Requirements is read-only. Sidecar is deprecated since KubeVirt 1.9, but
// remains required by this VM-local shim; the release supplies its image.
func (c Client) Requirements(namespace string) error {
	return c.requirements(namespace, false)
}

func (c Client) requirements(namespace string, allowDisabledSidecar bool) error {
	for _, binary := range []string{"kubectl", "virtctl"} {
		if _, err := c.Runner.LookPath(binary); err != nil {
			return fmt.Errorf("%s is required: %w", binary, err)
		}
	}
	if ns, err := c.get("namespace", namespace, ""); err != nil {
		return err
	} else if ns == nil {
		return fmt.Errorf("namespace %q must exist before creating a VM", namespace)
	}
	if crd, err := c.get("crd", "datavolumes.cdi.kubevirt.io", ""); err != nil {
		return err
	} else if crd == nil {
		return fmt.Errorf("CDI DataVolumes are required")
	}
	kv, err := c.installation()
	if err != nil {
		return err
	}
	if !sidecarEnabled(kv) && !allowDisabledSidecar {
		return fmt.Errorf("KubeVirt Sidecar feature gate is disabled; create with --enable-sidecar to append it without replacing existing gates")
	}
	return nil
}

func (c Client) enableSidecar() error {
	kv, err := c.installation()
	if err != nil || sidecarEnabled(kv) {
		return err
	}
	gates, _ := nested(kv, "spec", "configuration", "developerConfiguration", "featureGates").([]any)
	gates = append(gates, "Sidecar")
	patch := object{"metadata": object{"resourceVersion": nested(kv, "metadata", "resourceVersion")}, "spec": object{"configuration": object{"developerConfiguration": object{"featureGates": gates}}}}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	out, err := c.Runner.Run("kubectl", "patch", "kubevirt", fmt.Sprint(nested(kv, "metadata", "name")), "-n", fmt.Sprint(nested(kv, "metadata", "namespace")), "--type=merge", "-p", string(data))
	if err != nil {
		return fmt.Errorf("enabling Sidecar: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var dnsName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var quantity = regexp.MustCompile(`^[1-9][0-9]*(Mi|Gi|Ti)$`)

func validate(o Options) error {
	if len(o.Name) > 48 || !dnsName.MatchString(o.Name) {
		return fmt.Errorf("VM name must be a DNS label of at most 48 characters")
	}
	if len(o.Namespace) > 63 || !dnsName.MatchString(o.Namespace) {
		return fmt.Errorf("namespace must be a DNS label")
	}
	if o.CPU < 1 || !quantity.MatchString(o.Memory) || !quantity.MatchString(o.Disk) {
		return fmt.Errorf("CPU must be positive; memory and disk must be positive Mi/Gi/Ti quantities")
	}
	if o.UploadProxyURL == "" {
		return nil
	}
	// UploadProxy is intentionally local-only: never publish CDI to the LAN.
	u, err := url.Parse(o.UploadProxyURL)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() == "" {
		return fmt.Errorf("--uploadproxy-url must be a loopback HTTP(S) URL with a port")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("--uploadproxy-url must use loopback; port-forward CDI uploadproxy locally")
	}
	return nil
}

func newIdentity() (string, string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	return uuid, "CORRAL" + strings.ToUpper(hex.EncodeToString(id[:6])), nil
}

// GenerateVM builds a halted VM. Identity is generated only for an absent VM;
// callers must never regenerate or apply this manifest over an existing VM.
// mac pins the NIC to the OpenCore ROM: Apple's device attestation rejects
// iMessage lookups when ROM and the built-in en0 address differ.
func GenerateVM(o Options, uuid, serial, mac string) map[string]any {
	hook, _ := json.Marshal([]any{object{"args": []string{"--version", "v1alpha2"}, "imagePullPolicy": "IfNotPresent", "configMap": object{"name": o.Name + "-domain-hook", "key": "onDefineDomain", "hookPath": "/usr/bin/onDefineDomain"}}})
	disks := []any{}
	volumes := []any{}
	for i, suffix := range []string{"opencore", "recovery", "system"} {
		disk := object{"name": suffix, "disk": object{"bus": "sata"}}
		if i == 0 {
			disk["bootOrder"] = 1
		}
		disks = append(disks, disk)
		volumes = append(volumes, object{"name": suffix, "persistentVolumeClaim": object{"claimName": o.Name + "-" + suffix}})
	}
	spec := object{
		"domain": object{
			"machine":   object{"type": "q35"},
			"cpu":       object{"model": "host-passthrough", "sockets": 1, "maxSockets": 1, "cores": o.CPU, "threads": 1},
			"resources": object{"requests": object{"memory": o.Memory}},
			"firmware":  object{"uuid": uuid, "serial": serial, "bootloader": object{"efi": object{"secureBoot": false, "persistent": true}}},
			"devices":   object{"disks": disks, "autoattachMemBalloon": false, "inputs": []any{object{"name": "tablet", "type": "tablet", "bus": "usb"}}, "interfaces": []any{object{"name": "default", "model": "virtio", "masquerade": object{}, "macAddress": mac}}},
		},
		"networks": []any{object{"name": "default", "pod": object{}}},
		"volumes":  volumes,
	}
	if o.Node != "" {
		spec["nodeSelector"] = object{"kubernetes.io/hostname": o.Node}
	}
	return object{"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine", "metadata": metadata(o.Name, o.Namespace, o.Name), "spec": object{"runStrategy": "Halted", "template": object{"metadata": object{"labels": labels(o.Name), "annotations": object{"hooks.kubevirt.io/hookSidecars": string(hook)}}, "spec": spec}}}
}

func dataVolume(o Options, suffix, size string) object {
	source := object{"upload": object{}}
	if suffix == "system" {
		source = object{"blank": object{}}
	}
	meta := metadata(o.Name+"-"+suffix, o.Namespace, o.Name)
	meta["annotations"] = object{"cdi.kubevirt.io/storage.deleteAfterCompletion": "false"}
	return object{"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataVolume", "metadata": meta, "spec": object{"source": source, "storage": object{"accessModes": []string{"ReadWriteOnce"}, "resources": object{"requests": object{"storage": size}}}}}
}

// Create performs a full collision preflight before mutating anything. Reruns
// resume incomplete uploads, never reupload populated media, never replace a
// system disk, and leave an existing VM (including UUID/serial/EFI) untouched.
func (c Client) Create(o Options) error {
	ctx := c.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validate(o); err != nil {
		return err
	}
	vm, err := c.get("vm", o.Name, o.Namespace)
	if err != nil {
		return err
	}
	if err := checkOwned(vm, "VM", o.Name, o.Name); err != nil {
		return err
	}
	if vm == nil && o.Node == "" {
		return fmt.Errorf("--node is required when provisioning a new VM so local disks bind to the intended host")
	}
	vmi, err := c.get("vmi", o.Name, o.Namespace)
	if err != nil {
		return err
	}
	if err := checkOwned(vmi, "VMI", o.Name, o.Name); err != nil {
		return err
	}
	hookName := o.Name + "-domain-hook"
	hook, err := c.get("configmap", hookName, o.Namespace)
	if err != nil {
		return err
	}
	if err := checkOwned(hook, "ConfigMap", hookName, o.Name); err != nil {
		return err
	}
	type diskState struct {
		suffix, size, path string
		imageSHA           string
		dv, pvc            object
	}
	disks := []diskState{{suffix: "opencore", size: "1Gi", path: filepath.Join(o.MediaDir, "OpenCore.qcow2")}, {suffix: "recovery", size: "8Gi", path: filepath.Join(o.MediaDir, "Recovery.qcow2")}, {suffix: "system", size: o.Disk}}
	for i := range disks {
		d := &disks[i]
		name := o.Name + "-" + d.suffix
		d.dv, err = c.get("dv", name, o.Namespace)
		if err != nil {
			return err
		}
		if err := checkOwned(d.dv, "DataVolume", name, o.Name); err != nil {
			return err
		}
		d.pvc, err = c.get("pvc", name, o.Namespace)
		if err != nil {
			return err
		}
		if d.pvc != nil && !ownedPVC(d.pvc, d.dv, o.Name) {
			return fmt.Errorf("refusing unrelated PVC %q", name)
		}
		if err := c.checkPVCNode(o, name, d.pvc); err != nil {
			return err
		}
		if vm != nil && d.dv == nil && d.pvc == nil {
			return fmt.Errorf("existing VM %q is missing disk %q; refusing to recreate installed data", o.Name, name)
		}
		if nested(d.dv, "status", "phase") == "Succeeded" && d.pvc == nil {
			return fmt.Errorf("completed DataVolume %q has lost its PVC; refusing replacement", name)
		}
		if d.suffix != "system" && !populated(d.dv, d.pvc) {
			if vm != nil {
				return fmt.Errorf("existing VM %q has incomplete media %q; refusing to overwrite an attached disk", o.Name, name)
			}
			if d.pvc != nil && d.dv == nil {
				return fmt.Errorf("PVC %q has no completed upload or DataVolume; refusing overwrite", name)
			}
			if o.UploadProxyURL == "" {
				return fmt.Errorf("--uploadproxy-url is required for incomplete installer media; use corral macos provision to manage its local tunnel")
			}
			// A resumed personalized OpenCore upload does not need the source
			// cache; its immutable local receipt and DV binding are authoritative.
			if d.suffix == "opencore" && d.dv != nil {
				continue
			}
			info, err := os.Stat(d.path)
			if err != nil {
				return fmt.Errorf("installer media: %w (run corral macos prepare)", err)
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				return fmt.Errorf("installer media %q is not a nonempty regular file", d.path)
			}
		}
	}
	if err := c.requirements(o.Namespace, o.EnableSidecar); err != nil {
		return err
	}
	if vm != nil {
		return c.startRequested(o)
	}
	if !populated(disks[0].dv, disks[0].pvc) {
		if o.IdentityDir == "" {
			return fmt.Errorf("a persistent private OpenCore identity directory is required")
		}
		if disks[0].dv == nil && disks[0].pvc == nil {
			path, err := PersonalizeOpenCore(ctx, disks[0].path, o.IdentityDir)
			if err != nil {
				return fmt.Errorf("personalizing OpenCore identity: %w", err)
			}
			disks[0].path = path
		}
		path, hash, err := verifyPersonalizedOpenCore(ctx, o.IdentityDir)
		if err != nil {
			return fmt.Errorf("existing OpenCore upload requires its original verified identity and image: %w", err)
		}
		disks[0].path = path
		if disks[0].dv != nil && nested(disks[0].dv, "metadata", "annotations", openCoreSHAAnnotation) != hash {
			return fmt.Errorf("OpenCore DataVolume %q does not match the original personalized image SHA256; restore its original identity receipt and image, refusing reupload", o.Name+"-opencore")
		}
		// Retain the hash for the initial DV creation, before upload can begin.
		disks[0].imageSHA = hash
	}
	// Explicit opt-in is the only cluster-wide mutation in this workflow.
	if o.EnableSidecar {
		if err := c.enableSidecar(); err != nil {
			return err
		}
	}
	if hook == nil {
		if err := c.create(object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(hookName, o.Namespace, o.Name), "data": object{"onDefineDomain": domainHook}}); err != nil {
			return err
		}
	} else if vm == nil && nested(hook, "data", "onDefineDomain") != domainHook {
		// Resume a failed pre-VM provision with this plugin's corrected hook.
		// Existing VMs retain their current definition and hook.
		patch, err := json.Marshal(object{"metadata": object{"resourceVersion": nested(hook, "metadata", "resourceVersion")}, "data": object{"onDefineDomain": domainHook}})
		if err != nil {
			return err
		}
		out, err := c.Runner.Run("kubectl", "patch", "configmap", hookName, "-n", o.Namespace, "--type=merge", "-p", string(patch))
		if err != nil {
			return fmt.Errorf("updating owned pre-boot hook: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	for _, d := range disks {
		name := o.Name + "-" + d.suffix
		if d.pvc == nil && d.dv == nil {
			dv := dataVolume(o, d.suffix, d.size)
			if d.imageSHA != "" {
				dv["metadata"].(map[string]any)["annotations"].(map[string]any)[openCoreSHAAnnotation] = d.imageSHA
			}
			if err := c.create(dv); err != nil {
				return err
			}
		}
		if populated(d.dv, d.pvc) {
			continue
		}
		if err := c.ensurePVCLabels(o, name, d.dv, d.pvc); err != nil {
			return err
		}
		if d.suffix == "system" || populated(d.dv, d.pvc) {
			continue
		}
		if d.suffix == "opencore" {
			path, hash, err := verifyPersonalizedOpenCore(ctx, o.IdentityDir)
			if err != nil {
				return fmt.Errorf("verifying original OpenCore image before upload: %w", err)
			}
			if path != d.path || hash != d.imageSHA {
				return fmt.Errorf("OpenCore image changed while waiting for upload; restore the original personalized image, refusing reupload")
			}
		}
		// Node selection must precede force binding. The native uploader both
		// requests CDI population and waits for its server; waiting UploadReady
		// here first would deadlock a WaitForFirstConsumer DataVolume.
		out, err := c.Runner.Run("virtctl", "image-upload", "dv", name, "-n", o.Namespace, "--no-create", "--size="+d.size, "--image-path="+d.path, "--force-bind", "--uploadproxy-url="+o.UploadProxyURL, "--insecure", "--wait-secs=300")
		if err != nil {
			return fmt.Errorf("uploading %s (rerun to resume): %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		out, err = c.Runner.Run("kubectl", "wait", "dv", name, "-n", o.Namespace, "--for=jsonpath={.status.phase}=Succeeded", "--timeout=300s")
		if err != nil {
			return fmt.Errorf("waiting for completed upload %s: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		// A successful CLI exit is not sufficient evidence of persistent media.
		dv, err := c.get("dv", name, o.Namespace)
		if err != nil {
			return err
		}
		pvc, err := c.get("pvc", name, o.Namespace)
		if err != nil {
			return err
		}
		if !populated(dv, pvc) {
			return fmt.Errorf("upload %q has not completed; rerun after CDI reports Succeeded", name)
		}
		if !ownedPVC(pvc, dv, o.Name) {
			return fmt.Errorf("uploaded PVC %q is not controlled by the owned DataVolume", name)
		}
		if err := c.checkPVCNode(o, name, pvc); err != nil {
			return err
		}
	}
	errNoIdentity := fmt.Errorf("creating the VM requires its OpenCore identity.json to pin the NIC to ROM; restore the original identity backup")
	if o.IdentityDir == "" {
		return errNoIdentity
	}
	identity, err := loadOpenCoreIdentity(filepath.Join(o.IdentityDir, "identity.json"))
	if err != nil {
		return err
	}
	if identity == nil {
		return errNoIdentity
	}
	uuid, serial, err := newIdentity()
	if err != nil {
		return err
	}
	if err := c.create(GenerateVM(o, uuid, serial, identity.macAddress())); err != nil {
		return err
	}
	return c.startRequested(o)
}

func (c Client) startRequested(o Options) error {
	if o.Start {
		// VMIs are read-only checked so --start is itself idempotent.
		vmi, err := c.get("vmi", o.Name, o.Namespace)
		if err != nil {
			return err
		}
		if vmi == nil {
			out, err := c.Runner.Run("virtctl", "start", o.Name, "-n", o.Namespace)
			if err != nil {
				return fmt.Errorf("starting VM: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}
