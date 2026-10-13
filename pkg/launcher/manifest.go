// Package launcher implements the one-line Corral QEMU + loopback web noVNC
// launcher: an immutable-manifest-bound, fail-closed path from a verified
// Tuna disk image to a browser console on 127.0.0.1.
//
// There is currently no release-qualified Tuna VM artifact authorized for
// this launcher. Package/image qualification remains an explicit prerequisite
// the launcher enforces — it never claims a default asset, never invents a
// digest, and refuses every unverified byte. The caller supplies either a
// manifest file (--manifest) or explicit image coordinates (--image,
// --sha256, --size, --arch, --firmware); the launcher verifies before it
// admits.
//
// VM survival semantics (documented, owned):
//
//   - The launcher owns the web server it starts (loopback only) and shuts
//     it down on exit (SIGINT/SIGTERM), verified.
//   - The VM survives web shutdown by default, like `corral web` today —
//     closing the browser never stops the guest.
//   - --stop-vm stops the VM on launcher exit (verified exit, systemd or
//     direct child, whichever started it).
//   - A VM the launcher created but never got healthy is left stopped on
//     disk for inspection, never deleted silently.
package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Manifest is the immutable binding between a launcher invocation and the
// exact Tuna image bytes it may boot. Every field is required — a manifest
// that omits the digest, length, guest arch or firmware is refused, not
// defaulted.
type Manifest struct {
	// Version is the manifest schema version. Only "1" is accepted.
	Version string `json:"version"`
	// Name is a human label (e.g. "tuna-v1"). It selects nothing.
	Name string `json:"name"`
	// Image is exactly one verified disk image. Per-guest-arch selection is
	// one manifest per arch — a manifest never silently substitutes an
	// image for another arch.
	Image ManifestImage `json:"image"`
}

// ManifestImage is one verified disk artifact.
type ManifestImage struct {
	// URL is the https (or file) location of the image bytes.
	URL string `json:"url"`
	// SHA256 is the exact hex digest of the image bytes (64 lowercase hex).
	SHA256 string `json:"sha256"`
	// Size is the exact length in bytes. Downloads longer or shorter are
	// refused.
	Size int64 `json:"size"`
	// Arch is the guest architecture ("amd64" or "arm64"). It must match
	// the VM the launcher creates — the disk, emulator, machine,
	// accelerator, CPU and firmware are bound together.
	Arch string `json:"arch"`
	// Firmware is "uefi" or "bios" and must match the image's boot mode.
	Firmware string `json:"firmware"`
	// Format is "qcow2" or "raw". Empty means "qcow2".
	Format string `json:"format"`
	// VirtualSize is the image's guest-visible size in bytes (its
	// virtual-disk bound), when the manifest author states one. It lets
	// preflight bound conversion demand before admission; zero means
	// unstated, in which case preflight estimates from Size and the
	// authoritative post-admission inspect governs. Optional so existing
	// manifests keep validating unchanged.
	VirtualSize int64 `json:"virtual_size,omitempty"`
}

// maxManifestBytes bounds a manifest read: real manifests are hundreds of
// bytes, so anything past a megabyte is not a manifest.
const maxManifestBytes = 1 << 20

// ParseManifest reads and validates a manifest file. Fail-closed: any
// missing field, bad digest, non-positive size, unknown arch/firmware, or
// non-https/non-file URL is an error. The read itself is bounded and
// regular-file-only, so a FIFO or an unbounded file cannot hang or gorge
// the parse. Unknown JSON fields are ignored (forward compatibility for
// future manifest versions); every field the launcher ACTS on is validated
// fail-closed below.
func ParseManifest(path string) (Manifest, error) {
	var m Manifest
	// Non-blocking open: the open itself must not hang on a FIFO, before
	// any descriptor check below can run.
	f, err := openFileRead(path)
	if err != nil {
		return m, fmt.Errorf("reading manifest %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	// The OPEN file is checked, not a pre-open stat: a path swapped for a
	// FIFO between stat and open would otherwise hang the read below.
	if fi, err := f.Stat(); err != nil {
		return m, fmt.Errorf("reading manifest %s: %w", path, err)
	} else if !fi.Mode().IsRegular() {
		return m, fmt.Errorf("refusing non-regular manifest %s (%s) — manifests must be regular files", path, fi.Mode())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return m, fmt.Errorf("reading manifest %s: %w", path, err)
	}
	if len(data) > maxManifestBytes {
		return m, fmt.Errorf("manifest %s exceeds %d bytes — refusing an unbounded manifest", path, maxManifestBytes)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parsing manifest %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return m, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	return m, nil
}

// Validate enforces the fail-closed contract.
func (m Manifest) Validate() error {
	if m.Version != "1" {
		return fmt.Errorf("version must be \"1\", got %q", m.Version)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name is required")
	}
	return m.Image.Validate()
}

// Validate enforces the fail-closed image contract.
func (im ManifestImage) Validate() error {
	if im.URL == "" {
		return fmt.Errorf("image.url is required")
	}
	if !strings.HasPrefix(im.URL, "https://") && !strings.HasPrefix(im.URL, "file://") {
		return fmt.Errorf("image.url must be https:// or file:// (got %q) — plain http is refused", im.URL)
	}
	if len(im.SHA256) != 64 {
		return fmt.Errorf("image.sha256 must be 64 hex characters, got %d", len(im.SHA256))
	}
	if _, err := hex.DecodeString(im.SHA256); err != nil {
		return fmt.Errorf("image.sha256 is not hex: %w", err)
	}
	if strings.ToLower(im.SHA256) != im.SHA256 {
		return fmt.Errorf("image.sha256 must be lowercase hex")
	}
	if im.Size <= 0 {
		return fmt.Errorf("image.size must be positive, got %d", im.Size)
	}
	if im.Arch != "amd64" && im.Arch != "arm64" {
		return fmt.Errorf("image.arch must be amd64 or arm64, got %q", im.Arch)
	}
	if im.Firmware != "uefi" && im.Firmware != "bios" {
		return fmt.Errorf("image.firmware must be uefi or bios, got %q", im.Firmware)
	}
	format := im.Format
	if format == "" {
		format = "qcow2"
	}
	if format != "qcow2" && format != "raw" {
		return fmt.Errorf("image.format must be qcow2 or raw, got %q", im.Format)
	}
	if im.VirtualSize < 0 {
		return fmt.Errorf("image.virtual_size must be positive or unstated, got %d", im.VirtualSize)
	}
	return nil
}

// EffectiveFormat returns "qcow2" when Format is empty, else Format.
func (im ManifestImage) EffectiveFormat() string {
	if im.Format == "" {
		return "qcow2"
	}
	return im.Format
}

// VerifyBytes checks exact length and digest of already-read bytes. It is
// the unit-testable core of the download contract: admit only bytes that
// match both.
func (im ManifestImage) VerifyBytes(data []byte) error {
	if int64(len(data)) != im.Size {
		return fmt.Errorf("length mismatch: got %d bytes, manifest wants %d", len(data), im.Size)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != im.SHA256 {
		return fmt.Errorf("digest mismatch: bytes do not match manifest sha256 %s", im.SHA256)
	}
	return nil
}

// CacheKey derives the cache filename from the digest, never from the URL
// basename — two URLs with the same basename must never share a cache entry.
func (im ManifestImage) CacheKey() string {
	return im.SHA256 + "." + im.EffectiveFormat()
}
