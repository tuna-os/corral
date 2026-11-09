package macos

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMacserialOutputRejectsAmbiguousIdentifiers(t *testing.T) {
	const pair = "C02YL000JV3Q | C02900000GUJG36CB"
	serial, mlb, err := parseMacserialOutput(pair + "\n")
	if err != nil || serial != "C02YL000JV3Q" || mlb != "C02900000GUJG36CB" {
		t.Fatalf("cannot parse upstream pair: %v", err)
	}
	for _, invalid := range []string{"", "random opaque serial", pair + "\n" + pair, "SHORT | C02900000GUJG36CB", pair + " | extra"} {
		if _, _, err := parseMacserialOutput(invalid); err == nil {
			t.Fatalf("accepted ambiguous generator output %q", invalid)
		}
	}
}

func TestPersistedOpenCoreIdentityNeverRegeneratesOnCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	identity := openCoreIdentity{1, "iMac19,1", "C02YL000JV3Q", "C02900000GUJG36CB", "00112233-4455-4677-8899-AABBCCDDEEFF", "021122334455"}
	if err := writePrivateJSON(context.Background(), path, identity); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOpenCoreIdentity(path)
	if err != nil || loaded == nil || *loaded != identity {
		t.Fatalf("identity did not round-trip: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity is not private: %v", err)
	}
	if err := os.WriteFile(path, []byte("{interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := loadOpenCoreIdentity(path); err == nil || loaded != nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("corrupt identity did not require recovery: %v", err)
	}
}

func TestPersonalizeOpenCoreRejectsLostIdentityBeforeTools(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.qcow2")
	identityDir := filepath.Join(dir, "vm")
	if err := os.WriteFile(source, []byte("immutable source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(identityDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(identityDir, "OpenCore.qcow2"), []byte("previous identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PersonalizeOpenCore(context.Background(), source, identityDir); err == nil || !strings.Contains(err.Error(), "restore the original identity") {
		t.Fatalf("lost identity was not protected: %v", err)
	}
}

func TestPersonalizeOpenCoreCanceledBeforeChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(t.TempDir(), "not-created")
	if _, err := PersonalizeOpenCore(ctx, "source", dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled personalization changed filesystem: %v", err)
	}
}

func TestEFIPartitionOffsetReadsGPTInsteadOfAssumingOneMiB(t *testing.T) {
	for _, start := range []uint64{2048, 4096} {
		path := filepath.Join(t.TempDir(), "OpenCore.raw")
		image := make([]byte, 8192*512)
		copy(image[512:], "EFI PART")
		binary.LittleEndian.PutUint64(image[512+72:], 2)
		binary.LittleEndian.PutUint32(image[512+80:], 1)
		binary.LittleEndian.PutUint32(image[512+84:], 128)
		copy(image[1024:], []byte{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b})
		binary.LittleEndian.PutUint64(image[1024+32:], start)
		binary.LittleEndian.PutUint64(image[1024+40:], 8191)
		if err := os.WriteFile(path, image, 0o600); err != nil {
			t.Fatal(err)
		}
		if offset, err := efiPartitionOffset(path); err != nil || offset != int64(start*512) {
			t.Fatalf("wrong EFI offset: %d, %v", offset, err)
		}
		binary.LittleEndian.PutUint64(image[1024+40:], 8192)
		if err := os.WriteFile(path, image, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := efiPartitionOffset(path); err == nil {
			t.Fatal("accepted out-of-bounds EFI partition")
		}
	}
}

func TestPersonalizePlistPreservesUnrelatedConfiguration(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config.plist")
	identityPath := filepath.Join(dir, "identity.json")
	identity := openCoreIdentity{1, "iMac19,1", "C02YL000JV3Q", "C02900000GUJG36CB", "00112233-4455-4677-8899-AABBCCDDEEFF", "021122334455"}
	if err := writePrivateJSON(context.Background(), identityPath, identity); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual transformer with a small independent plist, rather
	// than copying any upstream configuration or mocking command output.
	fixture := `import plistlib, sys
plist = {"PlatformInfo": {"Generic": {"SystemProductName": "iMac19,1", "ProcessorType": 17, "SystemSerialNumber": "shared", "MLB": "shared", "ROM": b"shared", "SystemUUID": "shared"}}, "Kernel": {"Add": [{"BundlePath": "keep.kext"}]}, "NVRAM": {"Add": {"example": {"boot-args": "keep-me"}}}}
with open(sys.argv[1], "wb") as stream:
    plistlib.dump(plist, stream)
`
	if err := runMediaCommand(context.Background(), python, "-I", "-c", fixture, config); err != nil {
		t.Fatal(err)
	}
	if err := runMediaCommand(context.Background(), python, "-I", "-c", personalizePlistPython, config, "personalize", identityPath); err != nil {
		t.Fatal(err)
	}
	inspect := `import json, plistlib, sys
with open(sys.argv[1], "rb") as stream:
    plist = plistlib.load(stream)
generic = plist["PlatformInfo"]["Generic"]
if generic["ROM"] != bytes.fromhex("021122334455"):
    raise RuntimeError("ROM was not binary plist data")
del generic["ROM"]
print(json.dumps(plist))
`
	output, err := privateCommandOutput(context.Background(), python, "-I", "-c", inspect, config)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		PlatformInfo struct {
			Generic struct {
				SystemSerialNumber string
				MLB                string
				SystemUUID         string
				ProcessorType      int
			}
		}
		Kernel struct{ Add []struct{ BundlePath string } }
		NVRAM  struct{ Add map[string]map[string]string }
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	generic := result.PlatformInfo.Generic
	if generic.SystemSerialNumber != identity.Serial || generic.MLB != identity.MLB || generic.SystemUUID != identity.UUID || generic.ProcessorType != 17 ||
		len(result.Kernel.Add) != 1 || result.Kernel.Add[0].BundlePath != "keep.kext" || result.NVRAM.Add["example"]["boot-args"] != "keep-me" {
		t.Fatal("personalization changed unrelated fields or failed to set identity")
	}
}

func writePersonalizedFixture(t *testing.T, dir string) (string, string) {
	t.Helper()
	ctx := context.Background()
	source := filepath.Join(dir, "source.qcow2")
	image := filepath.Join(dir, "OpenCore.qcow2")
	if err := os.WriteFile(source, []byte("immutable source image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("published personalized image"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := openCoreIdentity{1, "iMac19,1", "C02YL000JV3Q", "C02900000GUJG36CB", "00112233-4455-4677-8899-AABBCCDDEEFF", "021122334455"}
	identityPath := filepath.Join(dir, "identity.json")
	if err := writePrivateJSON(ctx, identityPath, identity); err != nil {
		t.Fatal(err)
	}
	sourceHash, err := fileSHA256(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	identityHash, err := fileSHA256(ctx, identityPath)
	if err != nil {
		t.Fatal(err)
	}
	imageHash, err := fileSHA256(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(ctx, image+".verified.json", openCoreReceipt{Version: 2, SourceSHA256: sourceHash, IdentitySHA256: identityHash, ImageSHA256: imageHash, VMHideSHA256: vmHideSHA256, LiluSHA256: liluSHA256}); err != nil {
		t.Fatal(err)
	}
	return source, image
}

func TestPersonalizeOpenCoreRejectsChangedValidIdentityBeforeSourceInvalidation(t *testing.T) {
	for _, changeSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-source", true: "changed-source"}[changeSource], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			source, image := writePersonalizedFixture(t, dir)
			identityPath := filepath.Join(dir, "identity.json")
			identity, err := loadOpenCoreIdentity(identityPath)
			if err != nil {
				t.Fatal(err)
			}
			// Still syntactically valid: this must not be treated as a cache
			// miss that silently replaces the already published VM identity.
			identity.ROM = "026655443322"
			if err := writePrivateJSON(ctx, identityPath, identity); err != nil {
				t.Fatal(err)
			}
			if changeSource {
				if err := os.WriteFile(source, []byte("new upstream source image"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := make(map[string]string)
			for _, path := range []string{identityPath, image, image + ".verified.json"} {
				hash, err := fileSHA256(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = hash
			}
			t.Setenv("PATH", t.TempDir())
			if _, err := PersonalizeOpenCore(ctx, source, dir); err == nil || !strings.Contains(err.Error(), "restore the original identity.json") {
				t.Fatalf("changed valid identity did not require restoration: %v", err)
			}
			for path, want := range before {
				if got, err := fileSHA256(ctx, path); err != nil || got != want {
					t.Fatalf("identity collision modified %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
}

func TestPersonalizedOpenCoreCacheReusesIdentityAndSourceInvalidationDoesNotReplaceIt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source, image := writePersonalizedFixture(t, dir)
	identityPath := filepath.Join(dir, "identity.json")
	original, err := fileSHA256(ctx, identityPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if got, err := PersonalizeOpenCore(ctx, source, dir); err != nil || got != image {
		t.Fatalf("valid completed cache was not reused: %s, %v", got, err)
	}
	if ok, err := completedOpenCore(ctx, image, image+".verified.json", identityPath, mediaDigest("replacement source")); err != nil || ok {
		t.Fatalf("source invalidation rejected original identity or reused wrong source: %t, %v", ok, err)
	}
	if got, err := fileSHA256(ctx, identityPath); err != nil || got != original {
		t.Fatalf("source invalidation modified persisted identity: %v", err)
	}
}

func TestVerifyPersonalizedOpenCoreRequiresOriginalReceiptIdentityAndImage(t *testing.T) {
	for _, mutation := range []string{"none", "missing-identity", "invalid-identity", "changed-identity", "missing-image", "changed-image", "missing-receipt", "invalid-receipt", "invalid-source-hash"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			source, image := writePersonalizedFixture(t, dir)
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			identityPath := filepath.Join(dir, "identity.json")
			receiptPath := image + ".verified.json"
			var err error
			switch mutation {
			case "missing-identity":
				err = os.Remove(identityPath)
			case "invalid-identity":
				err = os.WriteFile(identityPath, []byte("{invalid"), 0o600)
			case "changed-identity":
				var identity *openCoreIdentity
				identity, err = loadOpenCoreIdentity(identityPath)
				if err == nil {
					identity.ROM = "026655443322"
					err = writePrivateJSON(ctx, identityPath, identity)
				}
			case "missing-image":
				err = os.Remove(image)
			case "changed-image":
				err = os.WriteFile(image, []byte("replacement personalized image"), 0o600)
			case "missing-receipt":
				err = os.Remove(receiptPath)
			case "invalid-receipt":
				err = os.WriteFile(receiptPath, []byte("{invalid"), 0o600)
			case "invalid-source-hash":
				var body []byte
				body, err = os.ReadFile(receiptPath)
				if err == nil {
					var receipt openCoreReceipt
					err = json.Unmarshal(body, &receipt)
					if err == nil {
						receipt.SourceSHA256 = "not-a-digest"
						err = writePrivateJSON(ctx, receiptPath, receipt)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// This API must neither need build tools nor recreate lost media.
			t.Setenv("PATH", t.TempDir())
			got, hash, err := verifyPersonalizedOpenCore(ctx, dir)
			if mutation == "none" {
				want, hashErr := fileSHA256(ctx, image)
				if err != nil || hashErr != nil || got != image || hash != want {
					t.Fatalf("intact published image rejected without source: %s, %s, %v", got, hash, err)
				}
			} else if err == nil || got != "" || hash != "" {
				t.Fatalf("invalid persisted state accepted: %s, %s, %v", got, hash, err)
			}
			if mutation == "missing-image" {
				if _, err := os.Stat(image); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("verification recreated missing image: %v", err)
				}
			}
		})
	}
}

func TestVerifyPersonalizedOpenCoreHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(t.TempDir(), "missing")
	if _, _, err := verifyPersonalizedOpenCore(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verification created identity directory: %v", err)
	}
}

func TestOpenCoreDependencyRefreshInvalidatesCacheButNotPublishedUpload(t *testing.T) {
	for _, mutation := range []string{"legacy", "vmhide", "lilu"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			source, image := writePersonalizedFixture(t, dir)
			receiptPath := image + ".verified.json"
			body, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			var receipt openCoreReceipt
			if err := json.Unmarshal(body, &receipt); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "legacy":
				receipt.Version = 1
				receipt.VMHideSHA256, receipt.LiluSHA256 = "", ""
			case "vmhide":
				receipt.VMHideSHA256 = mediaDigest("older VMHide release")
			case "lilu":
				receipt.LiluSHA256 = mediaDigest("older Lilu release")
			}
			if err := writePrivateJSON(ctx, receiptPath, receipt); err != nil {
				t.Fatal(err)
			}
			sourceHash, err := fileSHA256(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			identityPath := filepath.Join(dir, "identity.json")
			identityBefore, err := os.ReadFile(identityPath)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := completedOpenCore(ctx, image, receiptPath, identityPath, sourceHash); err != nil || ok {
				t.Fatalf("outdated EFI treated as current: %t, %v", ok, err)
			}
			// Upload-bound old images remain authoritative, even after the
			// source or current dependency pins have changed.
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", t.TempDir())
			if got, hash, err := verifyPersonalizedOpenCore(ctx, dir); err != nil || got != image || hash != receipt.ImageSHA256 {
				t.Fatalf("original upload bytes became unavailable: %s, %s, %v", got, hash, err)
			}
			identityAfter, err := os.ReadFile(identityPath)
			if err != nil || string(identityAfter) != string(identityBefore) {
				t.Fatalf("dependency invalidation changed identity: %v", err)
			}
		})
	}
}

func TestAppleServicesConfigurationPreservesIdentitySIPAndBootArguments(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	for _, liluVersion := range []string{"1.6.8", "1.7.2"} {
		t.Run(liluVersion, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "config.plist")
			lilu := filepath.Join(dir, "lilu.plist")
			vmhide := filepath.Join(dir, "vmhide.plist")
			fixture := `import plistlib, sys
config, lilu, vmhide, version = sys.argv[1:]
plist = {"PlatformInfo": {"Generic": {"SystemProductName": "iMac19,1", "SystemUUID": "unchanged", "ROM": b"private", "ProcessorType": 17}}, "Kernel": {"Add": [{"BundlePath": "Lilu.kext", "Enabled": True}, {"BundlePath": "keep.kext", "Enabled": True}, {"BundlePath": "VMHide.kext", "Enabled": False}]}, "NVRAM": {"Add": {"7C436110-AB2A-4BBB-A880-FE41995C9F82": {"csr-active-config": b"\0\0\0\0", "boot-args": "quiet"}}}}
for path, value in [(config, plist), (config + ".before", plist), (lilu, {"CFBundleIdentifier": "as.vit9696.Lilu", "CFBundleVersion": version}), (vmhide, {"OSBundleLibraries": {"as.vit9696.Lilu": "1.7.0"}})]:
    with open(path, "wb") as stream:
        plistlib.dump(value, stream)
`
			if err := runMediaCommand(context.Background(), python, "-I", "-c", fixture, config, lilu, vmhide, liluVersion); err != nil {
				t.Fatal(err)
			}
			err := runMediaCommand(context.Background(), python, "-I", "-c", appleServicesPlistPython, config, lilu, vmhide)
			if liluVersion == "1.6.8" {
				if err == nil {
					t.Fatal("incompatible immutable-source Lilu accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			inspect := `import plistlib, sys
with open(sys.argv[1], "rb") as stream:
    actual = plistlib.load(stream)
with open(sys.argv[1] + ".before", "rb") as stream:
    before = plistlib.load(stream)
assert actual["PlatformInfo"] == before["PlatformInfo"], "hardware identity changed"
assert actual["NVRAM"] == before["NVRAM"], "SIP or boot arguments changed"
entries = actual["Kernel"]["Add"]
assert entries[:2] == before["Kernel"]["Add"][:2], "existing kext load order changed"
vmhide = entries[2]
assert len(entries) == 3 and vmhide["BundlePath"] == "VMHide.kext" and vmhide["Enabled"]
assert vmhide["MinKernel"] == "24.0.0" and vmhide["MaxKernel"] == "" and vmhide["Arch"] == "x86_64"
assert vmhide["ExecutablePath"] == "Contents/MacOS/VMHide" and vmhide["PlistPath"] == "Contents/Info.plist"
`
			if err := runMediaCommand(context.Background(), python, "-I", "-c", inspect, config); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenerateOpenCoreIdentityRefusesRepeatedSerial(t *testing.T) {
	dir := t.TempDir()
	stub := `#!/bin/sh
n=$(cat "$MACSERIAL_CALLS" 2>/dev/null || echo 0)
n=$((n + 1)); printf '%s' "$n" > "$MACSERIAL_CALLS"
if [ "$MACSERIAL_DETERMINISTIC" = 1 ] || [ $((n % 2)) = 1 ]; then
    printf 'C02YL000JV3Q | C02900000GUJG36CB\n'
else
    printf 'C02YM111JV3Q | C02911111GUJG36CB\n'
fi
`
	if err := os.WriteFile(filepath.Join(dir, "macserial"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MACSERIAL_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("MACSERIAL_DETERMINISTIC", "1")
	if _, err := generateOpenCoreIdentity(context.Background(), "iMac19,1"); err == nil || !strings.Contains(err.Error(), "same serial twice") {
		t.Fatalf("deterministic generator was accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "calls")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACSERIAL_DETERMINISTIC", "0")
	identity, err := generateOpenCoreIdentity(context.Background(), "iMac19,1")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Serial != "C02YL000JV3Q" || identity.MLB != "C02900000GUJG36CB" {
		t.Fatalf("varying generator was not used: %+v", identity)
	}
}
