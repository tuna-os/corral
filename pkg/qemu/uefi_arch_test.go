package qemu

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func clearOVMFEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CORRAL_OVMF_CODE", "CORRAL_OVMF_VARS",
		"CORRAL_OVMF_CODE_AMD64", "CORRAL_OVMF_VARS_AMD64",
		"CORRAL_OVMF_CODE_ARM64", "CORRAL_OVMF_VARS_ARM64",
	} {
		t.Setenv(k, "")
	}
}

func TestCandidateFirmwaresForArch_Differ(t *testing.T) {
	clearOVMFEnv(t)
	amd := candidateFirmwaresForArch("amd64")
	arm := candidateFirmwaresForArch("arm64")
	if len(amd) == 0 || len(arm) == 0 {
		t.Fatalf("both arches need candidates: amd=%d arm=%d", len(amd), len(arm))
	}
	amdJoined := strings.Join(func() []string {
		var out []string
		for _, c := range amd {
			out = append(out, c.code)
		}
		return out
	}(), " ")
	armJoined := strings.Join(func() []string {
		var out []string
		for _, c := range arm {
			out = append(out, c.code)
		}
		return out
	}(), " ")
	if !strings.Contains(amdJoined, "x86_64") && !strings.Contains(amdJoined, "OVMF") {
		t.Errorf("amd64 candidates look wrong: %s", amdJoined)
	}
	if !strings.Contains(armJoined, "aarch64") && !strings.Contains(armJoined, "AAVMF") {
		t.Errorf("arm64 candidates look wrong: %s", armJoined)
	}
	if amdJoined == armJoined {
		t.Error("amd64 and arm64 must not share one firmware list")
	}
}

func writeFirmwarePair(t *testing.T, codeBody, varsBody string) (code, vars string) {
	t.Helper()
	tmp := t.TempDir()
	code = filepath.Join(tmp, "code.fd")
	vars = filepath.Join(tmp, "vars.fd")
	if err := os.WriteFile(code, []byte(codeBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vars, []byte(varsBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return code, vars
}

func TestFindUEFIFirmwareForArch_GenericOverrideIsHostArchOnly(t *testing.T) {
	clearOVMFEnv(t)
	code, vars := writeFirmwarePair(t, "c", "v")
	t.Setenv("CORRAL_OVMF_CODE", code)
	t.Setenv("CORRAL_OVMF_VARS", vars)
	host := runtime.GOARCH

	// Same-arch guests honor the generic override.
	gotCode, gotVars, err := FindUEFIFirmwareForArch(host)
	if err != nil {
		t.Fatalf("%s: %v", host, err)
	}
	if gotCode != code || gotVars != vars {
		t.Errorf("%s: override not honored: %q %q", host, gotCode, gotVars)
	}

	// Cross-arch guests must NOT receive the host's firmware: the generic
	// override is ignored and the guest-arch system list (or an error)
	// governs.
	other := "arm64"
	if host == "arm64" {
		other = "amd64"
	}
	gotCode, _, err = FindUEFIFirmwareForArch(other)
	if err == nil && gotCode == code {
		t.Errorf("%s: generic host override leaked into a cross-arch lookup", other)
	}
}

func TestFindUEFIFirmwareForArch_ArchOverrideWins(t *testing.T) {
	clearOVMFEnv(t)
	// A generic override pointing at the host firmware plus an
	// arch-specific override for the cross arch: each arch resolves to
	// its own files.
	hostCode, hostVars := writeFirmwarePair(t, "host-code", "host-vars")
	other := "arm64"
	suffix := "ARM64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
		suffix = "AMD64"
	}
	otherCode, otherVars := writeFirmwarePair(t, "other-code", "other-vars")
	t.Setenv("CORRAL_OVMF_CODE", hostCode)
	t.Setenv("CORRAL_OVMF_VARS", hostVars)
	t.Setenv("CORRAL_OVMF_CODE_"+suffix, otherCode)
	t.Setenv("CORRAL_OVMF_VARS_"+suffix, otherVars)

	gotCode, gotVars, err := FindUEFIFirmwareForArch(other)
	if err != nil {
		t.Fatalf("%s: %v", other, err)
	}
	if gotCode != otherCode || gotVars != otherVars {
		t.Errorf("%s: arch override not honored: %q %q", other, gotCode, gotVars)
	}
	gotCode, _, err = FindUEFIFirmwareForArch(runtime.GOARCH)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	if gotCode != hostCode {
		t.Errorf("host arch must keep the generic override: %q", gotCode)
	}
}

func TestFindUEFIFirmwareForArch_RefusesEmptyAndMissing(t *testing.T) {
	clearOVMFEnv(t)
	// Pin the ARCH-SPECIFIC override for the arch under test: the generic
	// override is deliberately host-only, so a generic pin plus an amd64
	// lookup would fall through to the system list (and real distro
	// firmware) on an arm64 host — host-dependent. The arch override wins
	// on every host.
	t.Setenv("CORRAL_OVMF_CODE_AMD64", "/nonexistent/code.fd")
	t.Setenv("CORRAL_OVMF_VARS_AMD64", "/nonexistent/vars.fd")
	if _, _, err := FindUEFIFirmwareForArch("amd64"); err == nil {
		t.Error("missing firmware must be an error, not empty paths")
	}
	if UEFIAvailableForArch("amd64") {
		t.Error("UEFIAvailableForArch must be false when files are missing")
	}
	// Empty files are not firmware: the override names only the empty
	// file, so lookup must refuse it rather than return it. The
	// arch-specific pins above are cleared first so the generic override
	// is what the same-arch lookup sees on every host.
	t.Setenv("CORRAL_OVMF_CODE_AMD64", "")
	t.Setenv("CORRAL_OVMF_VARS_AMD64", "")
	tmp := t.TempDir()
	empty := filepath.Join(tmp, "empty.fd")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORRAL_OVMF_CODE", empty)
	t.Setenv("CORRAL_OVMF_VARS", "")
	gotCode, _, err := FindUEFIFirmwareForArch(runtime.GOARCH)
	if err == nil && gotCode == empty {
		t.Error("empty firmware file must be refused")
	}
}

func TestCopyFile_RefusesOverlong(t *testing.T) {
	orig := maxFirmwareCopy
	maxFirmwareCopy = 16
	t.Cleanup(func() { maxFirmwareCopy = orig })
	src := filepath.Join(t.TempDir(), "big.fd")
	if err := os.WriteFile(src, []byte("more than sixteen bytes of not-firmware"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "vars.fd")
	if err := copyFile(src, dst); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("overlong copy must refuse, got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("refused copy must not leave a destination file")
	}
	// Within bounds copies exactly.
	small := filepath.Join(t.TempDir(), "small.fd")
	if err := os.WriteFile(small, []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst2 := filepath.Join(t.TempDir(), "vars2.fd")
	if err := copyFile(small, dst2); err != nil {
		t.Fatalf("bounded copy: %v", err)
	}
	got, _ := os.ReadFile(dst2)
	if string(got) != "tiny" {
		t.Errorf("copied bytes = %q", got)
	}
}
