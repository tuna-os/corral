package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// headroomBytes is the free-space reserve every digest-bound creation keeps
// beyond the converted guest image: qcow2 metadata overhead on the
// conversion, NVRAM vars, serial/QMP sockets, logs, and room for the
// guest's first writes.
const headroomBytes = 2 * 1024 * 1024 * 1024

// TemplateInfo is the inspected identity of a template image: what qemu-img
// info reports about the bytes on disk. VirtualSize (guest-visible bytes)
// bounds conversion demand; FileSize (compressed container bytes) does not.
type TemplateInfo struct {
	Format      string
	VirtualSize uint64
	FileSize    int64
	BackingFile string
	DataFile    string
}

// InspectTemplate inspects a template image via `qemu-img info
// --output=json`. The file must be a non-empty regular file; the returned
// Format, VirtualSize, backing-file and external-data-file locations come
// from QEMU's structured metadata — the data-file path is
// `format-specific.data.data-file` for QCOW2 (ImageInfoSpecificQCow2 in
// qapi/block-core.json), not a top-level field. A top-level `data-file`
// claim is still refused downstream: no real qemu-img emits one, so any
// such claim is either a future QEMU speaking a new dialect (fail closed)
// or a forged report.
func InspectTemplate(ctx context.Context, qemuImg, template string) (TemplateInfo, error) {
	var info TemplateInfo
	st, err := os.Stat(template)
	if err != nil {
		return info, fmt.Errorf("statting template %s: %w", template, err)
	}
	if !st.Mode().IsRegular() {
		return info, fmt.Errorf("template %s is not a regular file — refusing to convert it", template)
	}
	if st.Size() == 0 {
		return info, fmt.Errorf("template %s is empty — refusing to convert it", template)
	}
	info.FileSize = st.Size()
	out, err := exec.CommandContext(ctx, qemuImg, "info", "--output=json", template).CombinedOutput()
	if err != nil {
		return info, fmt.Errorf("inspecting template %s: %s: %w", template, strings.TrimSpace(string(out)), err)
	}
	var raw struct {
		Format         string  `json:"format"`
		VirtualSize    uint64  `json:"virtual-size"`
		Backing        *string `json:"backing-filename"`
		DataFile       *string `json:"data-file"`
		FormatSpecific struct {
			Type string `json:"type"`
			Data struct {
				DataFile *string `json:"data-file"`
			} `json:"data"`
		} `json:"format-specific"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return info, fmt.Errorf("parsing qemu-img info for %s: %w", template, err)
	}
	info.Format = raw.Format
	info.VirtualSize = raw.VirtualSize
	if raw.Backing != nil {
		info.BackingFile = *raw.Backing
	}
	// The nested path is QEMU's real external-data location; the top-level
	// claim is a fail-closed tripwire (see above).
	if nested := raw.FormatSpecific.Data.DataFile; nested != nil {
		info.DataFile = *nested
	} else if raw.DataFile != nil {
		info.DataFile = *raw.DataFile
	}
	if info.VirtualSize == 0 {
		return info, fmt.Errorf("template %s reports no virtual size — refusing to convert an image whose demand cannot be bounded", template)
	}
	return info, nil
}

// verifyTemplateSelfContained proves a digest-bound template carries no
// bytes outside the manifest's digest binding: no external QCOW backing
// file, no external data file, and a container format equal to the
// manifest's. A backing chain whose parent is not the admitted bytes would
// boot code the digest does not cover.
func verifyTemplateSelfContained(ctx context.Context, qemuImg, template, expectedFormat string) error {
	info, err := InspectTemplate(ctx, qemuImg, template)
	if err != nil {
		return err
	}
	if info.BackingFile != "" {
		return fmt.Errorf("template %s has an external backing file %q outside the digest binding — refusing to boot bytes the manifest does not cover (use a self-contained image)", template, info.BackingFile)
	}
	if info.DataFile != "" {
		return fmt.Errorf("template %s has an external data file %q outside the digest binding — refusing to boot bytes the manifest does not cover (use a self-contained image)", template, info.DataFile)
	}
	if expectedFormat != "" && info.Format != expectedFormat {
		return fmt.Errorf("template %s is format %q but the manifest binds %q — refusing a mismatched container", template, info.Format, expectedFormat)
	}
	return nil
}

// dirFreeBytes measures free space; a variable so tests can starve the disk
// without filling one.
var dirFreeBytes = freeBytesOnDir

// verifyConversionCapacity proves the converted guest image the template
// implies fits in the VM home alongside the 2 GiB headroom. The bound is
// the template's VIRTUAL size (guest-visible bytes): `qemu-img convert`
// materializes dense guest extents, so a small highly-compressed source can
// demand orders of magnitude more than its file length. Converted qcow2
// metadata runs about virtual/64 for 64K clusters plus refcount tables
// (tens of MB on a 20G disk) — the headroom absorbs it with room left for
// vars, sockets, logs and first writes. The retained cache copy already
// occupies its bytes, so only the conversion plus headroom is new demand.
// Callers run this immediately before the mutating convert (and the
// launcher re-checks post-admission); the manifest-size preflight estimate
// never substitutes for it.
func verifyConversionCapacity(ctx context.Context, qemuImg, template string) error {
	info, err := InspectTemplate(ctx, qemuImg, template)
	if err != nil {
		return err
	}
	free, err := dirFreeBytes(VMHome())
	if err != nil {
		return fmt.Errorf("cannot measure free space in %s: %w", VMHome(), err)
	}
	need := info.VirtualSize + headroomBytes
	if free < need {
		return fmt.Errorf("need %.1f GB free for the %.1f GB virtual disk's converted guest copy and 2 GB headroom, have %.1f GB (template file is %.3f GB — compressed size does not bound conversion demand)",
			float64(need)/1e9, float64(info.VirtualSize)/1e9, float64(free)/1e9, float64(info.FileSize)/1e9)
	}
	return nil
}
