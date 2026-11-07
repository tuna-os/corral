package macos

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	osxKVMRevision   = "4c378a4b5e0b219783683012bec680325eb40719"
	osxKVMBaseURL    = "https://raw.githubusercontent.com/kholia/OSX-KVM/" + osxKVMRevision + "/"
	downloaderSHA256 = "39ac6d26bd265f5d32198062f515ad15ef93afb7a74e702be2b008090d5bd5f3"
	openCoreSHA256   = "6ed36c0c2a4206ccc695f6b1a734a1cc6f94d288b0517c705d351c63cb92a6f3"
	vmHideURL        = "https://github.com/Carnations-Botanica/VMHide/releases/download/2.0.0/VMHide-2.0.0-RELEASE.zip"
	vmHideSHA256     = "31ffb861192eea52ad166fc0629b411ae48d8507c75935ab875bc61aebaa8dbb"
	liluURL          = "https://github.com/acidanthera/Lilu/releases/download/1.7.2/Lilu-1.7.2-RELEASE.zip"
	liluSHA256       = "53967d7dcfaab01023a33df2e969a89522f13d6654a6a56ac4711b62dabf3ab8"
	recoveryRelease  = "sequoia"
	recoveryBoard    = "Mac-7BA5B2D9E42DDD94"
)

// Media contains local, verified installation media, not the installed system disk.
type Media struct {
	OpenCore string
	Recovery string
	VMHide   string
	Lilu     string
}

// The pinned downloader calls os.get_terminal_size without a fallback in its
// verifier. Supply geometry only when no terminal exists; leave all upstream
// chunk and RSA signature checks intact. Python's isolated mode also prevents
// PYTHONOPTIMIZE from disabling the verifier's assert statements.
const terminalGeometry = `import os
_get_terminal_size = os.get_terminal_size
def _terminal_size(fd=1):
    try:
        return _get_terminal_size(fd)
    except OSError:
        return os.terminal_size((80, 24))
os.get_terminal_size = _terminal_size
`

const appleRecoveryPython = terminalGeometry + `import runpy, sys
script, action, directory = sys.argv[1:]
if action == "verify":
    module = runpy.run_path(script, run_name="corral_recovery")
    module["verify_image"](os.path.join(directory, "BaseSystem.dmg"), os.path.join(directory, "BaseSystem.chunklist"))
else:
    sys.argv = [script, "--action", "download", "--board-id", "Mac-7BA5B2D9E42DDD94", "--mlb", "00000000000000000", "--os-type", "default", "--outdir", directory, "--basename", "BaseSystem"]
    runpy.run_path(script, run_name="__main__")
`

// PrepareMedia prepares Sequoia recovery media without modifying any cluster.
// cacheDir itself is the release-specific cache directory. Existing Apple media
// requires matching request provenance; unknown legacy caches fail closed.
// New downloads live in .apple-recovery and retain their request across retries.
// python3, dmg2img and qemu-img must be supplied by the caller through PATH;
// qemu-img checks cached images for external dependencies on every reuse.
// dmg2img is unnecessary when an intact standalone conversion is cached.
func PrepareMedia(ctx context.Context, cacheDir string) (Media, error) {
	if err := ctx.Err(); err != nil {
		return Media{}, err
	}
	if strings.TrimSpace(cacheDir) == "" {
		return Media{}, errors.New("macOS media cache directory is required")
	}
	dir, err := filepath.Abs(cacheDir)
	if err != nil {
		return Media{}, fmt.Errorf("resolve macOS media cache: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Media{}, fmt.Errorf("create macOS media cache: %w", err)
	}
	unlock, err := lockMediaCache(ctx, dir)
	if err != nil {
		return Media{}, err
	}
	defer unlock()
	python, err := mediaTool("python3")
	if err != nil {
		return Media{}, err
	}
	script := filepath.Join(dir, "fetch-macOS-v2.py")
	if err := ensurePinned(ctx, http.DefaultClient, osxKVMBaseURL+"fetch-macOS-v2.py", script, downloaderSHA256); err != nil {
		return Media{}, err
	}
	media := Media{OpenCore: filepath.Join(dir, "OpenCore.qcow2"), Recovery: filepath.Join(dir, "Recovery.qcow2")}
	if err := ensurePinned(ctx, http.DefaultClient, osxKVMBaseURL+"OpenCore/OpenCore.qcow2", media.OpenCore, openCoreSHA256); err != nil {
		return Media{}, err
	}
	media.VMHide, media.Lilu, err = prepareKexts(ctx, dir)
	if err != nil {
		return Media{}, err
	}
	appleDir, err := prepareAppleRecovery(ctx, python, script, dir)
	if err != nil {
		return Media{}, err
	}
	dmg := filepath.Join(appleDir, "BaseSystem.dmg")
	dmgHash, err := fileSHA256(ctx, dmg)
	if err != nil {
		return Media{}, err
	}
	receiptPath := filepath.Join(dir, "Recovery.qcow2.verified.json")
	if ok, err := completedRecovery(ctx, media.Recovery, receiptPath, dmgHash); err != nil {
		return Media{}, err
	} else if ok {
		return media, nil
	}
	if err := convertRecovery(ctx, dmg, media.Recovery, receiptPath, dmgHash); err != nil {
		return Media{}, err
	}
	return media, nil
}

func mediaTool(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("macOS media requires %s in PATH; provide it before running corral macos prepare: %w", name, err)
	}
	return path, nil
}

type appleRecoveryProvenance struct {
	Version    int    `json:"version"`
	Release    string `json:"release"`
	Board      string `json:"board_id"`
	MLB        string `json:"mlb"`
	OSType     string `json:"os_type"`
	Downloader string `json:"downloader_sha256"`
	DMG        string `json:"dmg_sha256,omitempty"`
	Chunks     string `json:"chunklist_sha256,omitempty"`
}

func recoveryRequest() appleRecoveryProvenance {
	return appleRecoveryProvenance{Version: 1, Release: recoveryRelease, Board: recoveryBoard, MLB: "00000000000000000", OSType: "default", Downloader: downloaderSHA256}
}

func readRecoveryProvenance(ctx context.Context, dir string, requireComplete bool) error {
	body, err := os.ReadFile(filepath.Join(dir, "recovery-request.json"))
	if err != nil {
		return fmt.Errorf("Apple recovery cache has unknown provenance; preserve it as recovery material and use a fresh Sequoia cache: %w", err)
	}
	var receipt appleRecoveryProvenance
	if err := json.Unmarshal(body, &receipt); err != nil {
		return fmt.Errorf("invalid Apple recovery provenance: %w", err)
	}
	request := receipt
	request.DMG, request.Chunks = "", ""
	if request != recoveryRequest() {
		return errors.New("Apple recovery cache belongs to a different release/request; use a fresh Sequoia cache")
	}
	for _, item := range []struct{ name, hash string }{{"BaseSystem.dmg", receipt.DMG}, {"BaseSystem.chunklist", receipt.Chunks}} {
		if item.hash == "" && !requireComplete {
			continue
		}
		got, err := fileSHA256(ctx, filepath.Join(dir, item.name))
		if err != nil {
			return err
		}
		if !validSHA256(item.hash) || got != item.hash {
			return errors.New("Apple recovery files differ from their release provenance; preserve the cache and use a fresh directory")
		}
	}
	return nil
}

func bindRecoveryProvenance(ctx context.Context, dir string) error {
	receipt := recoveryRequest()
	var err error
	receipt.DMG, err = fileSHA256(ctx, filepath.Join(dir, "BaseSystem.dmg"))
	if err != nil {
		return err
	}
	receipt.Chunks, err = fileSHA256(ctx, filepath.Join(dir, "BaseSystem.chunklist"))
	if err != nil {
		return err
	}
	return writePrivateJSON(ctx, filepath.Join(dir, "recovery-request.json"), receipt)
}

func prepareAppleRecovery(ctx context.Context, python, script, dir string) (string, error) {
	rootDMG := filepath.Join(dir, "BaseSystem.dmg")
	rootChunks := filepath.Join(dir, "BaseSystem.chunklist")
	hasDMG, err := mediaFileExists(rootDMG)
	if err != nil {
		return "", err
	}
	hasChunks, err := mediaFileExists(rootChunks)
	if err != nil {
		return "", err
	}
	if hasDMG || hasChunks {
		if !hasDMG || !hasChunks {
			return "", fmt.Errorf("incomplete existing Apple recovery cache: both %s and %s are required; restore the missing file or move both aside to download again", rootDMG, rootChunks)
		}
		if err := readRecoveryProvenance(ctx, dir, true); err != nil {
			return "", err
		}
		if err := runMediaCommand(ctx, python, "-I", "-u", "-c", appleRecoveryPython, script, "verify", dir); err != nil {
			return "", fmt.Errorf("existing Apple recovery media failed verification; preserve or move aside %s and %s before downloading again: %w", rootDMG, rootChunks, err)
		}
		return dir, nil
	}
	stage := filepath.Join(dir, ".apple-recovery")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return "", err
	}
	// A canceled downloader can leave a complete pair. Reverify it before
	// making another network request; partial pairs are never converted.
	stageDMG, err := mediaFileExists(filepath.Join(stage, "BaseSystem.dmg"))
	if err != nil {
		return "", err
	}
	stageChunks, err := mediaFileExists(filepath.Join(stage, "BaseSystem.chunklist"))
	if err != nil {
		return "", err
	}
	requestPath := filepath.Join(stage, "recovery-request.json")
	hasRequest, err := mediaFileExists(requestPath)
	if err != nil {
		return "", err
	}
	if hasRequest || stageDMG || stageChunks {
		if err := readRecoveryProvenance(ctx, stage, false); err != nil {
			return "", err
		}
	} else if err := writePrivateJSON(ctx, requestPath, recoveryRequest()); err != nil {
		return "", err
	}
	if stageDMG && stageChunks {
		if err := runMediaCommand(ctx, python, "-I", "-u", "-c", appleRecoveryPython, script, "verify", stage); err == nil {
			if err := bindRecoveryProvenance(ctx, stage); err != nil {
				return "", err
			}
			return stage, nil
		} else if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	if err := runMediaCommand(ctx, python, "-I", "-u", "-c", appleRecoveryPython, script, "download", stage); err != nil {
		return "", fmt.Errorf("Apple Sequoia recovery download/verification failed (partial files retained in %s; rerun to retry): %w", stage, err)
	}
	if err := bindRecoveryProvenance(ctx, stage); err != nil {
		return "", err
	}
	return stage, nil
}

func mediaFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("media cache path is not a regular file: %s", path)
	}
	return true, nil
}

// Each bundle is reconstructed from its checksum-verified release archive.
// Extract only regular bundle members, never archive paths or symlinks.
func prepareKexts(ctx context.Context, dir string) (string, string, error) {
	root := filepath.Join(dir, "kexts")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	unlock, err := lockMediaCache(ctx, root)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	for _, item := range []struct{ name, url, hash string }{
		{"VMHide", vmHideURL, vmHideSHA256}, {"Lilu", liluURL, liluSHA256},
	} {
		archive := filepath.Join(root, item.name+"-RELEASE.zip")
		if err := ensurePinned(ctx, http.DefaultClient, item.url, archive, item.hash); err != nil {
			return "", "", err
		}
		if err := extractKext(ctx, archive, root, item.name+".kext"); err != nil {
			return "", "", err
		}
	}
	return filepath.Join(root, "VMHide.kext"), filepath.Join(root, "Lilu.kext"), nil
}

func extractKext(ctx context.Context, archive, dir, bundle string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	work, err := os.MkdirTemp(dir, ".kext-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	foundInfo, foundBinary := false, false
	for _, member := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(member.Name, bundle+"/") {
			continue
		}
		if member.Name == bundle+"/" && member.FileInfo().IsDir() {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(member.Name))
		if clean != strings.TrimSuffix(member.Name, "/") || !strings.HasPrefix(clean, bundle+"/") {
			return fmt.Errorf("unsafe kext archive member %q", member.Name)
		}
		path := filepath.Join(work, filepath.FromSlash(clean))
		if member.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
			continue
		}
		if !member.Mode().IsRegular() || member.UncompressedSize64 > 32<<20 {
			return fmt.Errorf("unsupported kext archive member %q", member.Name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		input, err := member.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, contextReader{ctx, input})
		_ = input.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		foundInfo = foundInfo || clean == bundle+"/Contents/Info.plist"
		foundBinary = foundBinary || clean == bundle+"/Contents/MacOS/"+strings.TrimSuffix(bundle, ".kext")
	}
	if !foundInfo || !foundBinary {
		return fmt.Errorf("release archive lacks complete %s", bundle)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target := filepath.Join(dir, bundle)
	if _, err := os.Lstat(target); err == nil {
		// Never replace an already published tree underneath another VM's
		// personalization. Compare every file and reject local modifications.
		expected := make(map[string]string)
		if err := filepath.WalkDir(filepath.Join(work, bundle), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(filepath.Join(work, bundle), path)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				expected[relative] = ""
				return nil
			}
			hash, err := fileSHA256(ctx, path)
			expected[relative] = hash
			return err
		}); err != nil {
			return err
		}
		err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(target, path)
			if err != nil {
				return err
			}
			want, exists := expected[relative]
			if !exists || (want == "") != entry.IsDir() || (!entry.IsDir() && !entry.Type().IsRegular()) {
				return errors.New("staged kext tree differs from pinned release")
			}
			if want != "" {
				got, err := fileSHA256(ctx, path)
				if err != nil {
					return err
				}
				if got != want {
					return errors.New("staged kext file differs from pinned release")
				}
			}
			delete(expected, relative)
			return ctx.Err()
		})
		if err != nil || len(expected) != 0 {
			return fmt.Errorf("staged %s differs from pinned release; move the bundle aside and retry (comparison: %v)", target, err)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(filepath.Join(work, bundle), target)
}

func ensurePinned(ctx context.Context, client *http.Client, url, path, want string) error {
	exists, err := mediaFileExists(path)
	if err != nil {
		return err
	}
	if exists {
		got, err := fileSHA256(ctx, path)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("SHA256 mismatch for cached %s: got %s, expected %s; move it aside and retry to download the pinned revision", path, got, want)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", url, resp.Status)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), contextReader{ctx, resp.Body}); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("SHA256 mismatch downloading %s: got %s, expected %s", url, got, want)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type recoveryReceipt struct {
	Version        int    `json:"version"`
	DMGSHA256      string `json:"dmg_sha256"`
	RecoverySHA256 string `json:"recovery_sha256"`
}

func completedRecovery(ctx context.Context, image, receiptPath, dmgHash string) (bool, error) {
	body, err := os.ReadFile(receiptPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var receipt recoveryReceipt
	if json.Unmarshal(body, &receipt) != nil || receipt.Version != 1 || receipt.DMGSHA256 != dmgHash {
		return false, nil
	}
	exists, err := mediaFileExists(image)
	if err != nil || !exists {
		return false, err
	}
	got, err := fileSHA256(ctx, image)
	if err != nil || got != receipt.RecoverySHA256 {
		return false, err
	}
	qemu, err := mediaTool("qemu-img")
	if err != nil {
		return false, err
	}
	return standaloneQCOW2(ctx, qemu, image)
}

func convertRecovery(ctx context.Context, dmg, image, receiptPath, dmgHash string) error {
	qemu, err := mediaTool("qemu-img")
	if err != nil {
		return err
	}
	dmg2img, err := mediaTool("dmg2img")
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(image), ".recovery-convert-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	raw := filepath.Join(work, "BaseSystem.raw")
	if err := runMediaCommand(ctx, dmg2img, dmg, raw); err != nil {
		return fmt.Errorf("convert Apple DMG to raw (verified DMG retained): %w", err)
	}
	if err := convertRecoveryRaw(ctx, qemu, raw, image); err != nil {
		return err
	}
	hash, err := fileSHA256(ctx, image)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(recoveryReceipt{1, dmgHash, hash}, "", "  ")
	if err != nil {
		return err
	}
	receipt := filepath.Join(work, "receipt.json")
	if err := os.WriteFile(receipt, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(receipt, receiptPath)
}

// A hash of a qcow2 file does not cover its backing image or external data
// file. Inspect these dependencies even for a receipt-matching cache entry.
// qemu-img's documented ImageInfo / ImageInfoSpecificQCow2 JSON describes both.
func standaloneQCOW2(ctx context.Context, qemu, image string) (bool, error) {
	body, err := exec.CommandContext(ctx, qemu, "info", "--output=json", "-f", "qcow2", image).Output()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return false, fmt.Errorf("inspect recovery image dependencies: %w", err)
	}
	var info struct {
		Format          string `json:"format"`
		BackingFilename string `json:"backing-filename"`
		FullBacking     string `json:"full-backing-filename"`
		FormatSpecific  struct {
			Type string `json:"type"`
			Data struct {
				DataFile string `json:"data-file"`
			} `json:"data"`
		} `json:"format-specific"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, fmt.Errorf("decode recovery image dependencies: %w", err)
	}
	if info.Format != "qcow2" || info.FormatSpecific.Type != "qcow2" {
		return false, errors.New("recovery image inspection did not identify qcow2 metadata")
	}
	return info.BackingFilename == "" && info.FullBacking == "" && info.FormatSpecific.Data.DataFile == "", nil
}

func convertRecoveryRaw(ctx context.Context, qemu, raw, image string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	exists, err := mediaFileExists(image)
	if err != nil {
		return err
	}
	if exists {
		standalone, err := standaloneQCOW2(ctx, qemu, image)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		// Logical comparison alone would also accept an overlay backed by
		// the verified raw image. Only adopt a dependency-free container.
		if err == nil && standalone {
			if err := runMediaCommand(ctx, qemu, "compare", "-f", "raw", "-F", "qcow2", raw, image); err == nil {
				return nil
			} else if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	output := filepath.Join(filepath.Dir(raw), "Recovery.qcow2")
	if err := runMediaCommand(ctx, qemu, "convert", "-f", "raw", "-O", "qcow2", "-c", raw, output); err != nil {
		return fmt.Errorf("convert recovery raw image to qcow2 (verified DMG retained): %w", err)
	}
	if err := runMediaCommand(ctx, qemu, "check", "-f", "qcow2", output); err != nil {
		return fmt.Errorf("validate converted recovery image: %w", err)
	}
	standalone, err := standaloneQCOW2(ctx, qemu, output)
	if err != nil {
		return err
	}
	if !standalone {
		return errors.New("converted recovery image has external dependencies")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(output, image)
}

// contextReader makes local hashing/copying cancellable as well as network IO.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func fileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, contextReader{ctx, f}); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runMediaCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	// Downloader progress is large and includes temporary Apple asset URLs.
	// Keep full output off the error path while retaining the final diagnosis.
	var output mediaCommandOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s", filepath.Base(name), err, strings.TrimSpace(string(output.data)))
	}
	return nil
}

// Bounded command output avoids keeping megabytes of progress updates in RAM.
type mediaCommandOutput struct{ data []byte }

func (w *mediaCommandOutput) Write(p []byte) (int, error) {
	const max = 4096
	n := len(p)
	if n >= max {
		w.data = append(w.data[:0], p[n-max:]...)
	} else {
		if len(w.data)+n > max {
			copy(w.data, w.data[len(w.data)+n-max:])
			w.data = w.data[:max-n]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}
