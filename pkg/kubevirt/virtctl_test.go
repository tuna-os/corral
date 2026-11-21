package kubevirt

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The pods that run this script hold cluster credentials, so the download
// must be checked against a digest compiled into Corral before it can run.
func TestVirtctlInstallScript_ChecksDigestBeforeChmod(t *testing.T) {
	s := VirtctlInstallScript("/tmp/virtctl")
	hexDigest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, arch := range []string{"amd64", "arm64"} {
		sum := virtctlSHA256[arch]
		if !hexDigest.MatchString(sum) {
			t.Errorf("virtctlSHA256[%q] = %q, want a SHA-256 in lowercase hex", arch, sum)
		}
		if !strings.Contains(s, "VIRTCTL_ARCH="+arch+" VIRTCTL_SHA256="+sum) {
			t.Errorf("script does not bind %s to its digest:\n%s", arch, s)
		}
	}
	for _, want := range []string{
		"curl -fsSL -o /tmp/virtctl",
		"/download/" + VirtctlVersion + "/virtctl-" + VirtctlVersion + "-linux-${VIRTCTL_ARCH}",
		`"$(sha256sum < /tmp/virtctl | cut -d' ' -f1)" != "$VIRTCTL_SHA256"`,
		"rm -f /tmp/virtctl",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
	check := strings.Index(s, "sha256sum")
	chmod := strings.Index(s, "chmod +x /tmp/virtctl")
	if check < 0 || chmod < 0 || check > chmod {
		t.Errorf("the digest check must come before chmod +x:\n%s", s)
	}
}

// Runs the generated script with a stand-in curl. A download with the
// expected digest becomes executable; any other download is removed and the
// script fails, with and without `sh -e`.
func TestVirtctlInstallScript_Run(t *testing.T) {
	for _, tool := range []string{"sh", "sha256sum", "cut", "uname"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required to run the script", tool)
		}
	}
	good := []byte("the release file\n")
	digest := sha256.Sum256(good)
	sum := hex.EncodeToString(digest[:])
	sums := map[string]string{"amd64": sum, "arm64": sum}

	cases := []struct {
		name     string
		curl     string // body of the stand-in curl; "$3" is the -o target
		wantFail bool
	}{
		{"expected digest", `printf 'the release file\n' > "$3"`, false},
		{"replaced file", `printf 'something else\n' > "$3"`, true},
		{"empty file", `: > "$3"`, true},
		{"HTTP error", `printf 'partial' > "$3"; exit 22`, true},
	}
	for _, tc := range cases {
		for _, flags := range []string{"-c", "-ec"} {
			t.Run(tc.name+" sh "+flags, func(t *testing.T) {
				dir := t.TempDir()
				bin := filepath.Join(dir, "bin")
				if err := os.Mkdir(bin, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\n"+tc.curl+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(dir, "virtctl")
				cmd := exec.Command("sh", flags, virtctlInstallScript(dest, "v0.0.0", sums))
				cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				out, err := cmd.CombinedOutput()
				info, statErr := os.Stat(dest)
				if tc.wantFail {
					if err == nil {
						t.Errorf("script succeeded, want a failure:\n%s", out)
					}
					if statErr == nil {
						t.Errorf("%s is still there after a failed check (mode %v)", dest, info.Mode())
					}
					return
				}
				if err != nil {
					t.Fatalf("script failed: %v\n%s", err, out)
				}
				if statErr != nil || info.Mode()&0o111 == 0 {
					t.Errorf("virtctl is not installed as an executable: %v", statErr)
				}
			})
		}
	}
}

// The VNC proxy's init container installs virtctl through the checked
// script, for the node's architecture.
func TestGenerateProxyDeployment_InitContainerChecksVirtctl(t *testing.T) {
	deploy := GenerateProxyDeployment("bluefin", "default", []int{22, 5900})
	pod := deploy["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	inits := pod["initContainers"].([]map[string]any)
	if len(inits) != 1 {
		t.Fatalf("want one init container, got %d", len(inits))
	}
	command := inits[0]["command"].([]string)
	script := command[len(command)-1]
	if !strings.Contains(script, VirtctlInstallScript("/tmp/virtctl")) {
		t.Errorf("init container does not use the checked virtctl install:\n%s", script)
	}
	if strings.Contains(script, "linux-amd64") {
		t.Errorf("init container hard-codes amd64:\n%s", script)
	}
}

// The corral web image ships the virtctl that ci.yml downloads, and the
// in-cluster pods download VirtctlVersion and check it against
// virtctlSHA256. The two must be one release: a bump on one side only would
// run two virtctl versions against one cluster, and the digests here would no
// longer be those of the image's virtctl. ci.yml selects every job when it
// changes (.github/scripts/ci-scope.sh), so this runs on the PRs that can
// cause the drift.
func TestVirtctlPinMatchesWorkflow(t *testing.T) {
	read := func(parts ...string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	workflow := read(".github", "workflows", "ci.yml")
	script := read("scripts", "smoke-web-image.sh")

	// The workflow-wide env block is indented two spaces; job-level keys such
	// as the e2e job's KUBEVIRT_VERSION are indented further.
	workflowEnv := func(name string) string {
		t.Helper()
		m := regexp.MustCompile(`(?m)^  `+name+`: (\S+)$`).FindAllStringSubmatch(workflow, -1)
		if len(m) != 1 {
			t.Fatalf("ci.yml: want one workflow-level %s, found %d", name, len(m))
		}
		return m[0][1]
	}
	scriptDefault := func(name string) string {
		t.Helper()
		m := regexp.MustCompile(name + `="\$\{` + name + `:-([^}]+)\}"`).FindStringSubmatch(script)
		if m == nil {
			t.Fatalf("scripts/smoke-web-image.sh: no default for %s", name)
		}
		return m[1]
	}

	if got := workflowEnv("VIRTCTL_VERSION"); got != VirtctlVersion {
		t.Errorf("ci.yml VIRTCTL_VERSION = %s, VirtctlVersion = %s: change them together, with virtctlSHA256", got, VirtctlVersion)
	}
	if got := scriptDefault("VIRTCTL_VERSION"); got != VirtctlVersion {
		t.Errorf("scripts/smoke-web-image.sh VIRTCTL_VERSION default = %s, VirtctlVersion = %s", got, VirtctlVersion)
	}
	if got, want := scriptDefault("KUBECTL_VERSION"), workflowEnv("KUBECTL_VERSION"); got != want {
		t.Errorf("scripts/smoke-web-image.sh KUBECTL_VERSION default = %s, ci.yml KUBECTL_VERSION = %s", got, want)
	}

	// Every digest the workflow checks virtctl against is the one compiled in.
	for _, m := range regexp.MustCompile(`(?m)^\s*([0-9a-f]{64})  build/([a-z0-9]+)/virtctl$`).FindAllStringSubmatch(workflow, -1) {
		if want := virtctlSHA256[m[2]]; m[1] != want {
			t.Errorf("ci.yml checks build/%s/virtctl against %s, virtctlSHA256[%q] = %q", m[2], m[1], m[2], want)
		}
	}
}
