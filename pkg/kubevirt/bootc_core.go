package kubevirt

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

func errBootcUnavailable() error {
	return fmt.Errorf("bootc support is not built into this binary (it's an optional plugin — build with `-tags bootc` or use the corral:bootc image)")
}

// bootc is an optional plugin compiled in only with the `bootc` build tag
// (`go build -tags bootc`). bootc — booting a container image as a VM — is a
// niche workflow, so the default binary/image omits it to stay lean. This file
// is always compiled and holds the registration seam; bootc.go (//go:build
// bootc) registers the real implementations in its init().

// BootcBuildResult describes a finished on-cluster bootc disk build. The disk is
// installed by a builder VM via `bootc install to-disk` and is fully
// self-bootable (GPT + ESP + bootloader), so the only thing callers need is the
// PVC holding it — the final VM boots it via UEFI firmware.
type BootcBuildResult struct {
	PVCName string
}

// Registered by bootc.go when the `bootc` build tag is set; nil otherwise.
var (
	bootcBuildFunc   func(name, namespace, imageURI, sshPublicKey, diskSize, storageClass, provisionScript string, kargs []string, node string, progress io.Writer) (*BootcBuildResult, error)
	bootcVMFunc      func(name, namespace, pvcName, imageURI, sshKey, mem string, cpu int, node string) map[string]any
	bootcRebuildFunc func(name, namespace, imageURI, sshPublicKey, diskSize, provisionScript string, progress io.Writer) error
	bootcResumeFunc  func(name, namespace string) (imageURI, pvcName string, ready, failed bool)
	bootcCleanupFunc func(builderName, namespace string)
)

// BootcAvailable reports whether the bootc plugin is compiled into this binary.
func BootcAvailable() bool { return bootcBuildFunc != nil }

// BootcImageOf returns the image a bootc VM was built from, read from the
// durable `corral.bootc/image` annotation (survives server-pod restarts, unlike
// the in-pod registry). Empty if the VM/annotation is absent.
func BootcImageOf(name, namespace string) string {
	out, err := runPkg("kubectl", "get", "vm", name, "-n", namespace,
		"-o", `jsonpath={.metadata.annotations.corral\.bootc/image}`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// BootcBuildDisk runs the on-cluster bootc disk build. storageClass "" uses
// PreferredStorageClass(). kargs go to `bootc install to-disk --karg`, so they
// are in the boot entry from the first boot; the builder VM records them for
// BootcBuiltKargs. node "" lets the scheduler place the builder; a non-empty
// node pins it (so it co-locates with a node-bound local-path disk and the
// final VM can mount it). provisionScript, when non-empty, runs inside the
// installed root before the disk is handed to the final VM. Errors if the
// plugin isn't compiled in.
func BootcBuildDisk(name, namespace, imageURI, sshPublicKey, diskSize, storageClass, provisionScript string, kargs []string, node string, progress io.Writer) (*BootcBuildResult, error) {
	if bootcBuildFunc == nil {
		return nil, errBootcUnavailable()
	}
	return bootcBuildFunc(name, namespace, imageURI, sshPublicKey, diskSize, storageClass, provisionScript, kargs, node, progress)
}

// bootcKargsAnnotation records, on the builder VM, the kernel arguments its
// `bootc install to-disk` installed. A `--resume` reads it back, because a
// resume skips the install and so cannot add a kernel argument itself.
const bootcKargsAnnotation = "corral.bootc/kargs"

// ValidateKargs rejects a kernel argument that is empty or holds whitespace:
// each one must be a single token for `bootc install to-disk --karg`.
func ValidateKargs(kargs []string) error {
	for _, karg := range kargs {
		if karg == "" || strings.IndexFunc(karg, unicode.IsSpace) >= 0 {
			return fmt.Errorf("--karg %q is not one kernel argument (it must be non-empty, with no whitespace)", karg)
		}
	}
	return nil
}

// BootcKargFlags renders kargs as shell words for a `bootc install to-disk`
// line in a generated script: ` --karg 'a=1' --karg 'b'`, each value
// single-quoted so the shell passes it through unchanged. Empty for no kargs.
func BootcKargFlags(kargs []string) string {
	var b strings.Builder
	for _, karg := range kargs {
		b.WriteString(" --karg '")
		b.WriteString(strings.ReplaceAll(karg, "'", `'\''`))
		b.WriteString("'")
	}
	return b.String()
}

// BootcBuiltKargs returns the kernel arguments that name's builder VM
// installed, read from its corral.bootc/kargs annotation. A builder from
// before the annotation existed, or one that installed none, returns nil.
func BootcBuiltKargs(name, namespace string) []string {
	out, err := runPkg("kubectl", "get", "vm", name+"-bootc-builder", "-n", namespace,
		"-o", "jsonpath={.metadata.annotations."+strings.ReplaceAll(bootcKargsAnnotation, ".", `\.`)+"}")
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// MissingKargs returns the requested kernel arguments that are not in
// installed, in request order.
func MissingKargs(requested, installed []string) []string {
	have := make(map[string]bool, len(installed))
	for _, karg := range installed {
		have[karg] = true
	}
	var missing []string
	for _, karg := range requested {
		if !have[karg] {
			missing = append(missing, karg)
		}
	}
	return missing
}

// GenerateBootcVM builds the final VM manifest that UEFI-boots a bootc-built
// block disk PVC. Returns nil if the plugin isn't compiled in.
func GenerateBootcVM(name, namespace, pvcName, imageURI, sshKey, mem string, cpu int, node string) map[string]any {
	if bootcVMFunc == nil {
		return nil
	}
	return bootcVMFunc(name, namespace, pvcName, imageURI, sshKey, mem, cpu, node)
}

// BootcRebuild rebuilds an existing bootc VM's disk from imageURI (the same
// image to pull updates, or a different one to switch), then re-points the VM
// at the fresh disk and restarts it. It preserves the
// sizing/networking — only the disk and optional provision script change.
// Errors if the plugin isn't compiled in.
func BootcRebuild(name, namespace, imageURI, sshPublicKey, diskSize, provisionScript string, progress io.Writer) error {
	if bootcRebuildFunc == nil {
		return errBootcUnavailable()
	}
	return bootcRebuildFunc(name, namespace, imageURI, sshPublicKey, diskSize, provisionScript, progress)
}

// BootcResumeState reports whether name has a completed-but-unfinished bootc
// build left behind by an interrupted `corral bootc create` — see
// bootcResumeState in bootc.go for the detection details. ready=true means
// GenerateBootcVM(name, namespace, pvcName, imageURI, ...) can be applied
// directly without rerunning the build. False (both ready and failed) if the
// plugin isn't compiled in — there's nothing to resume without it.
func BootcResumeState(name, namespace string) (imageURI, pvcName string, ready, failed bool) {
	if bootcResumeFunc == nil {
		return "", "", false, false
	}
	return bootcResumeFunc(name, namespace)
}

// BootcCleanupBuilder deletes a builder VM and its cloud-init secret after a
// resumed build's final VM has been created. No-op if the plugin isn't
// compiled in (there's no builder to clean up without it).
func BootcCleanupBuilder(builderName, namespace string) {
	if bootcCleanupFunc != nil {
		bootcCleanupFunc(builderName, namespace)
	}
}
