package kubevirt

import (
	"fmt"
	"io"
	"strings"
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
	bootcBuildFunc   func(name, namespace, imageURI, sshPublicKey, diskSize, storageClass, provisionScript, node string, progress io.Writer) (*BootcBuildResult, error)
	bootcVMFunc      func(name, namespace, pvcName, imageURI, sshKey, mem string, cpu int, node string) map[string]any
	bootcRebuildFunc func(name, namespace, imageURI, sshPublicKey, diskSize string, progress io.Writer) error
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
// PreferredStorageClass(). node "" lets the scheduler place the builder; a
// non-empty node pins it (so it co-locates with a node-bound local-path disk
// and the final VM can mount it). Errors if the plugin isn't compiled in.
func BootcBuildDisk(name, namespace, imageURI, sshPublicKey, diskSize, storageClass, provisionScript, node string, progress io.Writer) (*BootcBuildResult, error) {
	if bootcBuildFunc == nil {
		return nil, errBootcUnavailable()
	}
	return bootcBuildFunc(name, namespace, imageURI, sshPublicKey, diskSize, storageClass, provisionScript, node, progress)
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
// image to pull updates, or a different one to switch), then re-points the
// VM's kernel boot at the fresh disk and restarts it. It preserves the VM's
// sizing/networking — only the disk + kernelBoot change. Errors if the plugin
// isn't compiled in.
func BootcRebuild(name, namespace, imageURI, sshPublicKey, diskSize string, progress io.Writer) error {
	if bootcRebuildFunc == nil {
		return errBootcUnavailable()
	}
	return bootcRebuildFunc(name, namespace, imageURI, sshPublicKey, diskSize, progress)
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
