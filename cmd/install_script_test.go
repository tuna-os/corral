package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// standInCurl answers the downloads of scripts/install.sh from two local
// files: the binary, and the SHA256SUMS file beside it.
const standInCurl = `#!/bin/sh
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
case "$url" in
  */SHA256SUMS) cat "$FAKE_SUMS" > "$out" ;;
  *.sigstore.json) exit 22 ;;
  *) cat "$FAKE_BINARY" > "$out" ;;
esac
`

// runInstallScript runs scripts/install.sh with a PATH that holds only the
// tools the script needs, plus hashTool when it is not empty. The download
// has the content of binary, and SHA256SUMS records sum for it. It returns
// the path the script installs to, its output, and its error.
func runInstallScript(t *testing.T, hashTool, sum string) (string, string, error) {
	t.Helper()
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skipf("install.sh has no build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed")
	}
	bin := t.TempDir()
	tools := []string{"uname", "id", "mkdir", "mktemp", "rm", "awk", "chmod", "install", "head", "basename", "cat"}
	if hashTool != "" {
		tools = append(tools, hashTool)
	}
	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not installed", tool)
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(standInCurl), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	binary := filepath.Join(work, "download")
	if err := os.WriteFile(binary, []byte(installScriptBinary), 0o644); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(work, "SHA256SUMS")
	asset := "corral-" + runtime.GOOS + "-" + runtime.GOARCH
	if err := os.WriteFile(sums, []byte(sum+"  "+asset+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := exec.Command(sh, filepath.Join(root, "scripts", "install.sh"))
	cmd.Env = []string{
		"PATH=" + bin, "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "SHELL=/bin/unknown",
		"CORRAL_INSTALL_DIR=" + dir, "FAKE_BINARY=" + binary, "FAKE_SUMS=" + sums,
	}
	out, err := cmd.CombinedOutput()
	return filepath.Join(dir, "corral"), string(out), err
}

const installScriptBinary = "#!/bin/sh\necho stand-in corral\n"

func installScriptBinarySum() string {
	sum := sha256.Sum256([]byte(installScriptBinary))
	return hex.EncodeToString(sum[:])
}

// installScriptHashTool is a checksum tool of this host that install.sh
// knows: sha256sum on Linux, shasum on macOS.
func installScriptHashTool(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"sha256sum", "shasum"} {
		if _, err := exec.LookPath(tool); err == nil {
			return tool
		}
	}
	t.Skip("no sha256sum or shasum installed")
	return ""
}

// The checksum is the one check that install.sh always makes: the Sigstore
// check runs only when cosign is installed. A host with no checksum tool must
// thus get a refusal, not an install with no check at all.
func TestInstallScript_RefusesWithoutAChecksumTool(t *testing.T) {
	installed, out, err := runInstallScript(t, "", installScriptBinarySum())
	if err == nil {
		t.Errorf("install.sh succeeded with no checksum tool:\n%s", out)
	}
	if !strings.Contains(out, "refusing to install an unverified binary") {
		t.Errorf("output does not say that the install is refused:\n%s", out)
	}
	if _, statErr := os.Stat(installed); statErr == nil {
		t.Errorf("%s was installed with no checksum verification", installed)
	}
}

func TestInstallScript_ChecksumDecidesTheInstall(t *testing.T) {
	tool := installScriptHashTool(t)

	installed, out, err := runInstallScript(t, tool, installScriptBinarySum())
	if err != nil {
		t.Fatalf("install.sh with a matching checksum: %v\n%s", err, out)
	}
	if got, readErr := os.ReadFile(installed); readErr != nil || string(got) != installScriptBinary {
		t.Errorf("installed file = %q, %v; want the downloaded binary", got, readErr)
	}

	installed, out, err = runInstallScript(t, tool, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(out, "checksum mismatch") {
		t.Errorf("install.sh with a wrong checksum = %v, want a checksum mismatch:\n%s", err, out)
	}
	if _, statErr := os.Stat(installed); statErr == nil {
		t.Errorf("%s was installed with a wrong checksum", installed)
	}
}
