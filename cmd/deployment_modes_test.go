package cmd

// Deployment-mode manifest tests: the files docs/deployment-modes.md points
// at must stay true. The quadlet unit, the Tailscale ingress manifest, and
// the generated systemd unit are all checked here so docs and reality cannot
// drift apart. None of these tests needs live infra (no VPN, no cluster).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// quadletPath finds the quadlet generator binary for a dry-run conversion.
func quadletPath() string {
	if p, err := exec.LookPath("quadlet"); err == nil {
		return p
	}
	for _, p := range []string{
		"/usr/libexec/podman/quadlet",
		"/usr/lib/podman/quadlet",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// sectionKeys parses an ini-style unit file into group -> keys.
func sectionKeys(t *testing.T, path string) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string][]string{}
	group := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			group = line
			continue
		}
		if group == "" {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			out[group] = append(out[group], strings.TrimSpace(line[:i]))
		}
	}
	return out
}

func TestQuadletUnitFile(t *testing.T) {
	path := filepath.Join("..", "containers", "corral-web.container")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read quadlet unit: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"Image=ghcr.io/tuna-os/corral:latest",
		"ContainerName=corral-web",
		"PublishPort=127.0.0.1:8006:8006",
		"WantedBy=default.target",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("quadlet unit missing %q", want)
		}
	}
	sections := sectionKeys(t, path)
	// Restart is a systemd [Service] key. Podman 4.x rejects it in
	// [Container] ("unsupported key"), so the dry-run below would fail.
	for _, k := range sections["[Container]"] {
		if k == "Restart" {
			t.Error("[Container] must not carry Restart= (unsupported key); put it in [Service]")
		}
	}
	found := false
	for _, k := range sections["[Service]"] {
		if k == "Restart" {
			found = true
		}
	}
	if !found {
		t.Error("[Service] must carry Restart=")
	}

	// Full conversion through the real generator when it exists.
	q := quadletPath()
	if q == "" {
		t.Skip("no quadlet binary; structural checks above still ran")
	}
	home := t.TempDir()
	unitDir := filepath.Join(home, ".config", "containers", "systemd")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "corral-web.container"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(q, "-dryrun", "-user")
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quadlet -dryrun: %v\n%s", err, out)
	}
	// Newer generators resolve the user home via the password database
	// instead of $HOME, so they never see the staged file ("No files
	// parsed"). That is an environment limit, not a unit bug: the
	// structural checks above still ran.
	if strings.Contains(string(out), "No files parsed") {
		t.Skip("quadlet ignored the staged HOME; structural checks above still ran")
	}
	if strings.Contains(string(out), "unsupported key") {
		t.Errorf("quadlet reports unsupported keys:\n%s", out)
	}
	if !strings.Contains(string(out), "corral-web.service") {
		t.Errorf("quadlet did not generate corral-web.service:\n%s", out)
	}
}

// loadManifests splits a multi-doc YAML manifest into generic maps.
// Only the fields the VPN preflight cares about are asserted below.

func loadManifests(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var docs []map[string]any
	for _, part := range strings.Split(string(raw), "\n---") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(part), &doc); err != nil {
			t.Fatalf("parse manifest doc: %v", err)
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

func TestTailscaleIngressManifest(t *testing.T) {
	// Homelab/VPN mode trusts one fact: every byte reaches corral-web
	// through the Tailscale operator Ingress. A second ingress path would
	// break the identity-header trust (see docs/adr/0003-identity-source.md).
	docs := loadManifests(t, filepath.Join("..", "deploy", "corral-web.yaml"))
	ingresses := 0
	for _, doc := range docs {
		kind, _ := doc["kind"].(string)
		if kind != "Ingress" {
			continue
		}
		ingresses++
		spec, _ := doc["spec"].(map[string]any)
		if spec == nil {
			t.Fatal("Ingress has no spec")
		}
		class, _ := spec["ingressClassName"].(string)
		if class != "tailscale" {
			t.Errorf("ingressClassName = %q, want tailscale", class)
		}
	}
	if ingresses != 1 {
		t.Errorf("found %d Ingress docs, want exactly 1 (no second path)", ingresses)
	}

	// The probes supervisors rely on must stay wired.
	text, err := os.ReadFile(filepath.Join("..", "deploy", "corral-web.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"path: /healthz",
		"path: /readyz",
		"Do not add a second, non-Tailscale ingress path",
	} {
		if !strings.Contains(string(text), want) {
			t.Errorf("corral-web.yaml missing %q", want)
		}
	}
}

func TestGeneratedWebUnitVerifies(t *testing.T) {
	// Mode 4 (24/7 vs on-demand) documents `corral web service install`.
	// The unit it installs must at least parse under systemd.
	// systemd-analyze also checks that ExecStart points at a real file,
	// so the test pins the unit to an executable that exists here.
	execPath := "/usr/bin/corral"
	if _, err := os.Stat(execPath); err != nil {
		execPath, err = os.Executable()
		if err != nil {
			t.Fatal(err)
		}
	}
	unit := renderWebUnit(execPath+" web --addr 127.0.0.1:8006", false)
	path := filepath.Join(t.TempDir(), "corral-web.service")
	if err := os.WriteFile(path, []byte(unit), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze; structural checks in TestRenderWebUnit_* still ran")
	}
	if out, err := exec.Command("systemd-analyze", "verify", path).CombinedOutput(); err != nil {
		t.Errorf("systemd-analyze verify: %v\n%s", err, out)
	}
}
