package macos

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type openCoreIdentity struct {
	Version     int    `json:"version"`
	ProductName string `json:"system_product_name"`
	Serial      string `json:"system_serial_number"`
	MLB         string `json:"mlb"`
	UUID        string `json:"system_uuid"`
	ROM         string `json:"rom"`
}

type openCoreReceipt struct {
	Version        int    `json:"version"`
	SourceSHA256   string `json:"source_sha256"`
	IdentitySHA256 string `json:"identity_sha256"`
	ImageSHA256    string `json:"image_sha256"`
	VMHideSHA256   string `json:"vmhide_sha256,omitempty"`
	LiluSHA256     string `json:"lilu_sha256,omitempty"`
}

var (
	identitySerialPattern = regexp.MustCompile(`^[A-Z0-9]{11,12}$`)
	identityMLBPattern    = regexp.MustCompile(`^(?:[A-Z0-9]{13}|[A-Z0-9]{17})$`)
	identityUUIDPattern   = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$`)
)

const personalizePlistPython = `import json, plistlib, sys
config, action, identity_path = sys.argv[1:]
with open(config, "rb") as stream:
    plist = plistlib.load(stream)
platform = plist["PlatformInfo"]
generic = platform["Generic"]
if action == "model":
    print(generic["SystemProductName"])
else:
    with open(identity_path, "r") as stream:
        identity = json.load(stream)
    if generic["SystemProductName"] != identity["system_product_name"]:
        raise RuntimeError("OpenCore model differs from persisted identity; refusing identity migration")
    generic["SystemSerialNumber"] = identity["system_serial_number"]
    generic["MLB"] = identity["mlb"]
    generic["SystemUUID"] = identity["system_uuid"]
    generic["ROM"] = bytes.fromhex(identity["rom"])
    platform["Automatic"] = True
    platform["UpdateDataHub"] = True
    platform["UpdateNVRAM"] = True
    platform["UpdateSMBIOS"] = True
    with open(config, "wb") as stream:
        plistlib.dump(plist, stream, sort_keys=False)
`

// VMHide 2.0.0 declares Lilu >=1.7.0; the immutable OSX-KVM image has
// 1.6.8. Replace it only in the private clone, retaining its load position.
// OpenCore injects these release kexts; no csr-active-config or boot args change.
const appleServicesPlistPython = `import plistlib, sys
config, lilu_info, vmhide_info = sys.argv[1:]
with open(lilu_info, "rb") as stream:
    lilu = plistlib.load(stream)
with open(vmhide_info, "rb") as stream:
    vmhide = plistlib.load(stream)
def version(value):
    return tuple(int(part) for part in value.split("."))
if lilu["CFBundleIdentifier"] != "as.vit9696.Lilu" or version(lilu["CFBundleVersion"]) < (1, 7, 0):
    raise RuntimeError("VMHide requires Lilu 1.7.0 or newer")
if version(lilu["CFBundleVersion"]) < version(vmhide["OSBundleLibraries"]["as.vit9696.Lilu"]):
    raise RuntimeError("Lilu is older than the VMHide dependency")
with open(config, "rb") as stream:
    plist = plistlib.load(stream)
entries = plist["Kernel"]["Add"]
lilu_entries = [entry for entry in entries if entry["BundlePath"] == "Lilu.kext"]
if len(lilu_entries) != 1 or not lilu_entries[0]["Enabled"]:
    raise RuntimeError("OpenCore must load exactly one enabled Lilu before VMHide")
entries[:] = [entry for entry in entries if entry["BundlePath"] != "VMHide.kext"]
entries.append({"Arch": "x86_64", "BundlePath": "VMHide.kext", "Comment": "VMHide 2.0.0 RELEASE", "Enabled": True, "ExecutablePath": "Contents/MacOS/VMHide", "MaxKernel": "", "MinKernel": "24.0.0", "PlistPath": "Contents/Info.plist"})
with open(config, "wb") as stream:
    plistlib.dump(plist, stream, sort_keys=False)
`

// PersonalizeOpenCore creates a private per-VM clone without modifying source.
// identityDir must be stable for that VM and backed up with its installed disk:
// deleting it must not be used as a way to retry Apple account sign-in. Identity
// is persisted before image publication and never regenerated on a failed retry.
// New clones require qemu-img, mcopy, python3 and upstream macserial in PATH.
// macserial generates model-compatible identifiers, not Apple-issued hardware
// identities, and does not guarantee Apple account/service acceptance.
func PersonalizeOpenCore(ctx context.Context, source, identityDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(identityDir) == "" {
		return "", errors.New("OpenCore source and persistent per-VM identity directory are required")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	dir, err := filepath.Abs(identityDir)
	if err != nil {
		return "", err
	}
	image := filepath.Join(dir, "OpenCore.qcow2")
	if source == image {
		return "", errors.New("personalized OpenCore must be separate from immutable source media")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	unlock, err := lockMediaCache(ctx, dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	sourceHash, err := fileSHA256(ctx, source)
	if err != nil {
		return "", err
	}
	identityPath := filepath.Join(dir, "identity.json")
	receiptPath := filepath.Join(dir, "OpenCore.qcow2.verified.json")
	identity, err := loadOpenCoreIdentity(identityPath)
	if err != nil {
		return "", err
	}
	if identity != nil {
		if err := os.Chmod(identityPath, 0o600); err != nil {
			return "", err
		}
		ok, err := completedOpenCore(ctx, image, receiptPath, identityPath, sourceHash)
		if err != nil {
			return "", err
		}
		if ok {
			if err := os.Chmod(image, 0o600); err != nil {
				return "", err
			}
			return image, nil
		}
	} else {
		for _, path := range []string{image, receiptPath} {
			if exists, err := mediaFileExists(path); err != nil {
				return "", err
			} else if exists {
				return "", errors.New("personalized OpenCore exists without identity.json; restore the original identity backup instead of generating a replacement")
			}
		}
	}
	// Dependency refreshes start with the verified private image, not the
	// shared template: this preserves PlatformInfo.Generic byte-for-byte in
	// plist values, including fields outside the persisted identifier set.
	cloneSource := source
	if exists, err := mediaFileExists(image); err != nil {
		return "", err
	} else if exists {
		if _, _, err := verifyPersonalizedOpenCore(ctx, dir); err != nil {
			return "", err
		}
		body, err := os.ReadFile(receiptPath)
		if err != nil {
			return "", err
		}
		var receipt openCoreReceipt
		if err := json.Unmarshal(body, &receipt); err != nil {
			return "", err
		}
		if receipt.SourceSHA256 != sourceHash {
			return "", errors.New("OpenCore source changed for an existing identity; refusing an unsupported template migration")
		}
		cloneSource = image
	}
	qemu, err := mediaTool("qemu-img")
	if err != nil {
		return "", err
	}
	mcopy, err := mediaTool("mcopy")
	if err != nil {
		return "", err
	}
	python, err := mediaTool("python3")
	if err != nil {
		return "", err
	}
	vmhide, lilu, err := prepareKexts(ctx, filepath.Dir(source))
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(dir, ".personalize-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(work) }()
	raw := filepath.Join(work, "OpenCore.raw")
	if err := runMediaCommand(ctx, qemu, "convert", "-f", "qcow2", "-O", "raw", cloneSource, raw); err != nil {
		return "", fmt.Errorf("extract immutable OpenCore clone: %w", err)
	}
	offset, err := efiPartitionOffset(raw)
	if err != nil {
		return "", err
	}
	fatImage := fmt.Sprintf("%s@@%d", raw, offset)
	config := filepath.Join(work, "config.plist")
	if err := runMediaCommand(ctx, mcopy, "-i", fatImage, "::EFI/OC/config.plist", config); err != nil {
		return "", fmt.Errorf("extract OpenCore configuration: %w", err)
	}
	if identity == nil {
		modelOutput, err := privateCommandOutput(ctx, python, "-I", "-c", personalizePlistPython, config, "model", identityPath)
		if err != nil {
			return "", err
		}
		identity, err = generateOpenCoreIdentity(ctx, strings.TrimSpace(string(modelOutput)))
		if err != nil {
			return "", err
		}
		if err := writePrivateJSON(ctx, identityPath, identity); err != nil {
			return "", err
		}
	}
	if _, err := privateCommandOutput(ctx, python, "-I", "-c", personalizePlistPython, config, "personalize", identityPath); err != nil {
		return "", fmt.Errorf("personalize OpenCore without changing persisted identity: %w", err)
	}
	if _, err := privateCommandOutput(ctx, python, "-I", "-c", appleServicesPlistPython, config, filepath.Join(lilu, "Contents", "Info.plist"), filepath.Join(vmhide, "Contents", "Info.plist")); err != nil {
		return "", fmt.Errorf("configure release Apple-services kexts without changing SIP: %w", err)
	}
	// mcopy overwrites the dependency's existing members; the staged release
	// bundles contain only their published regular files, no debug symbols.
	for _, bundle := range []string{lilu, vmhide} {
		if err := runMediaCommand(ctx, mcopy, "-s", "-o", "-i", fatImage, bundle, "::EFI/OC/Kexts/"); err != nil {
			return "", fmt.Errorf("install private Apple-services dependency: %w", err)
		}
	}
	if err := runMediaCommand(ctx, mcopy, "-o", "-i", fatImage, config, "::EFI/OC/config.plist"); err != nil {
		return "", fmt.Errorf("write private OpenCore configuration: %w", err)
	}
	output := filepath.Join(work, "OpenCore.qcow2")
	if err := runMediaCommand(ctx, qemu, "convert", "-f", "raw", "-O", "qcow2", "-c", raw, output); err != nil {
		return "", err
	}
	if err := runMediaCommand(ctx, qemu, "check", "-f", "qcow2", output); err != nil {
		return "", err
	}
	if err := os.Chmod(output, 0o600); err != nil {
		return "", err
	}
	imageHash, err := fileSHA256(ctx, output)
	if err != nil {
		return "", err
	}
	identityHash, err := fileSHA256(ctx, identityPath)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(output, image); err != nil {
		return "", err
	}
	receipt := openCoreReceipt{Version: 2, SourceSHA256: sourceHash, IdentitySHA256: identityHash, ImageSHA256: imageHash, VMHideSHA256: vmHideSHA256, LiluSHA256: liluSHA256}
	if err := writePrivateJSON(ctx, receiptPath, receipt); err != nil {
		return "", err
	}
	return image, nil
}

func loadOpenCoreIdentity(path string) (*openCoreIdentity, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var identity openCoreIdentity
	if json.Unmarshal(body, &identity) != nil || !validOpenCoreIdentity(identity) {
		return nil, errors.New("persisted OpenCore identity is invalid; restore identity.json from backup, do not regenerate identifiers for an existing VM")
	}
	return &identity, nil
}

func validOpenCoreIdentity(identity openCoreIdentity) bool {
	rom, err := hex.DecodeString(identity.ROM)
	return identity.Version == 1 && identity.ProductName != "" &&
		identitySerialPattern.MatchString(identity.Serial) && identityMLBPattern.MatchString(identity.MLB) &&
		identityUUIDPattern.MatchString(identity.UUID) && err == nil && len(rom) == 6 && rom[0]&3 == 2
}

// macAddress formats ROM as the colon-separated NIC address macOS reports for
// en0. validOpenCoreIdentity guarantees six bytes with a unicast,
// locally administered first octet.
func (identity openCoreIdentity) macAddress() string {
	rom := strings.ToLower(identity.ROM)
	parts := make([]string, 0, 6)
	for i := 0; i < len(rom); i += 2 {
		parts = append(parts, rom[i:i+2])
	}
	return strings.Join(parts, ":")
}

func generateOpenCoreIdentity(ctx context.Context, model string) (*openCoreIdentity, error) {
	if model == "" || strings.ContainsAny(model, "\r\n\x00") {
		return nil, errors.New("source OpenCore configuration does not contain a usable SystemProductName")
	}
	macserial, err := mediaTool("macserial")
	if err != nil {
		return nil, fmt.Errorf("unique OpenCore identity requires official OpenCorePkg macserial (Linux release binary may be named macserial.linux; expose it as macserial in PATH): %w", err)
	}
	output, err := privateCommandOutput(ctx, macserial, "--generate", "--model", model, "--num", "1")
	if err != nil {
		return nil, err
	}
	serial, mlb, err := parseMacserialOutput(string(output))
	if err != nil {
		return nil, err
	}
	// A macserial without a working randomness source repeats one serial for
	// every VM, and Apple rejects the duplicate as already in use. Official
	// OpenCorePkg builds never collide; a repeat proves a broken build.
	repeat, err := privateCommandOutput(ctx, macserial, "--generate", "--model", model, "--num", "1")
	if err != nil {
		return nil, err
	}
	repeatSerial, _, err := parseMacserialOutput(string(repeat))
	if err != nil {
		return nil, err
	}
	if repeatSerial == serial {
		return nil, errors.New("macserial returned the same serial twice; its randomness source is broken: use the official OpenCorePkg release and never persist a duplicate identity")
	}
	var uuid [16]byte
	var rom [6]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(rom[:]); err != nil {
		return nil, err
	}
	uuid[6] = uuid[6]&0x0f | 0x40
	uuid[8] = uuid[8]&0x3f | 0x80
	rom[0] = rom[0]&0xfc | 0x02
	uuidHex := strings.ToUpper(hex.EncodeToString(uuid[:]))
	formattedUUID := uuidHex[:8] + "-" + uuidHex[8:12] + "-" + uuidHex[12:16] + "-" + uuidHex[16:20] + "-" + uuidHex[20:]
	return &openCoreIdentity{1, model, serial, mlb, formattedUUID, strings.ToUpper(hex.EncodeToString(rom[:]))}, nil
}

func parseMacserialOutput(output string) (string, string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 1 {
		parts := strings.Split(lines[0], "|")
		if len(parts) == 2 {
			serial, mlb := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if identitySerialPattern.MatchString(serial) && identityMLBPattern.MatchString(mlb) {
				return serial, mlb, nil
			}
		}
	}
	return "", "", errors.New("macserial did not return exactly one valid serial/MLB pair; no identity was persisted")
}

// Do not include stdout/stderr from a generator or plist operation in errors:
// they may contain the private machine identifiers.
func privateCommandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("%s identity operation failed: %w (output withheld to protect identifiers)", filepath.Base(name), err)
	}
	return out, nil
}

func completedOpenCore(ctx context.Context, image, receiptPath, identityPath, sourceHash string) (bool, error) {
	body, err := os.ReadFile(receiptPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var receipt openCoreReceipt
	if json.Unmarshal(body, &receipt) != nil || (receipt.Version != 1 && receipt.Version != 2) {
		return false, nil
	}
	identityHash, err := fileSHA256(ctx, identityPath)
	if err != nil {
		return false, err
	}
	if identityHash != receipt.IdentitySHA256 {
		return false, errors.New("persisted OpenCore identity differs from its image receipt; restore the original identity.json backup, do not replace identifiers for an existing VM")
	}
	if receipt.SourceSHA256 != sourceHash || receipt.Version != 2 ||
		receipt.VMHideSHA256 != vmHideSHA256 || receipt.LiluSHA256 != liluSHA256 {
		return false, nil
	}
	exists, err := mediaFileExists(image)
	if err != nil || !exists {
		return false, err
	}
	imageHash, err := fileSHA256(ctx, image)
	return imageHash == receipt.ImageSHA256, err
}

// verifyPersonalizedOpenCore checks a previously published identity and image
// without rebuilding it, changing permissions, or requiring source media.
// Upload resumption must use these exact bytes, not silently repair the cache.
func verifyPersonalizedOpenCore(ctx context.Context, identityDir string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(identityDir) == "" {
		return "", "", errors.New("persistent per-VM identity directory is required")
	}
	dir, err := filepath.Abs(identityDir)
	if err != nil {
		return "", "", err
	}
	image := filepath.Join(dir, "OpenCore.qcow2")
	identityPath := filepath.Join(dir, "identity.json")
	identity, err := loadOpenCoreIdentity(identityPath)
	if err != nil {
		return "", "", err
	}
	if identity == nil {
		return "", "", errors.New("personalized OpenCore identity is missing; restore the original identity.json backup")
	}
	body, err := os.ReadFile(image + ".verified.json")
	if err != nil {
		return "", "", fmt.Errorf("read personalized OpenCore receipt; restore the original identity and image backup: %w", err)
	}
	var receipt openCoreReceipt
	if json.Unmarshal(body, &receipt) != nil || (receipt.Version != 1 && receipt.Version != 2) ||
		!validSHA256(receipt.SourceSHA256) || !validSHA256(receipt.IdentitySHA256) || !validSHA256(receipt.ImageSHA256) ||
		(receipt.Version == 2 && (!validSHA256(receipt.VMHideSHA256) || !validSHA256(receipt.LiluSHA256))) {
		return "", "", errors.New("personalized OpenCore receipt is invalid; restore the original identity and image backup")
	}
	identityHash, err := fileSHA256(ctx, identityPath)
	if err != nil {
		return "", "", err
	}
	if identityHash != receipt.IdentitySHA256 {
		return "", "", errors.New("persisted OpenCore identity differs from its image receipt; restore the original identity.json backup, do not replace identifiers for an existing VM")
	}
	imageHash, err := fileSHA256(ctx, image)
	if err != nil {
		return "", "", fmt.Errorf("read personalized OpenCore image; restore the original identity and image backup: %w", err)
	}
	if imageHash != receipt.ImageSHA256 {
		return "", "", errors.New("personalized OpenCore image differs from its receipt; restore the original identity and image backup")
	}
	return image, imageHash, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func writePrivateJSON(ctx context.Context, path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".identity-json-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(body, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// Read the GPT rather than relying on the current upstream image's 1 MiB
// offset. GUIDs in a GPT entry use mixed-endian encoding on disk.
func efiPartitionOffset(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	var header [92]byte
	if _, err := file.ReadAt(header[:], 512); err != nil {
		return 0, fmt.Errorf("read OpenCore GPT: %w", err)
	}
	if string(header[:8]) != "EFI PART" {
		return 0, errors.New("OpenCore raw image has no GPT header")
	}
	tableLBA := binary.LittleEndian.Uint64(header[72:80])
	count := binary.LittleEndian.Uint32(header[80:84])
	entrySize := binary.LittleEndian.Uint32(header[84:88])
	if tableLBA > uint64(info.Size()/512) || count == 0 || count > 4096 || entrySize < 128 || entrySize > 4096 {
		return 0, errors.New("OpenCore GPT partition table is out of bounds")
	}
	var efiGUID = []byte{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b}
	entry := make([]byte, entrySize)
	for i := uint32(0); i < count; i++ {
		offset := int64(tableLBA*512) + int64(i)*int64(entrySize)
		if _, err := file.ReadAt(entry, offset); err != nil {
			return 0, fmt.Errorf("read OpenCore GPT partition: %w", err)
		}
		if bytes.Equal(entry[:16], efiGUID) {
			start := binary.LittleEndian.Uint64(entry[32:40])
			end := binary.LittleEndian.Uint64(entry[40:48])
			if start == 0 || end < start || end >= uint64(info.Size()/512) {
				return 0, errors.New("OpenCore EFI partition is out of bounds")
			}
			return int64(start * 512), nil
		}
	}
	return 0, errors.New("OpenCore GPT does not contain an EFI system partition")
}
