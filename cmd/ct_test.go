package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/shell"
)

func TestCtCmd_Subcommands(t *testing.T) {
	for _, want := range []string{"create", "list", "start", "stop", "delete", "console"} {
		c, _, err := ctCmd.Find([]string{want})
		if err != nil || c == ctCmd {
			t.Errorf("ct: missing subcommand %q", want)
		}
	}
}

func TestCtCreateCmd_RequiresImage(t *testing.T) {
	ctImage = ""
	err := ctCreateCmd.RunE(ctCreateCmd, []string{"web1"})
	if err == nil {
		t.Error("expected an error when --image is missing")
	}
}

func TestCtCreateCmd_Flags(t *testing.T) {
	for _, name := range []string{"image", "cpu", "mem", "disk", "storage-class", "privileged", "devcontainer", "devcontainer-ready-timeout"} {
		if ctCreateCmd.Flags().Lookup(name) == nil {
			t.Errorf("ct create: missing --%s flag", name)
		}
	}
}

func TestCtCreateCmd_Success(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	ct.SetRunner(fake)
	defer ct.SetRunner(shell.Real{})

	ctImage = "debian:bookworm"
	ctNamespace = "corral-ct"
	ctCPU = 1
	ctMem = "512Mi"
	ctDisk = "5Gi"
	defer func() { ctImage, ctNamespace = "", "" }()

	if err := ctCreateCmd.RunE(ctCreateCmd, []string{"web1"}); err != nil {
		t.Fatalf("ct create: %v", err)
	}
	applies := 0
	for _, c := range fake.Calls() {
		if c.Name == "kubectl" && len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
		}
	}
	if applies != 2 {
		t.Errorf("expected 2 applies (PVC + pod), got %d", applies)
	}
}

func TestCtListCmd_Empty(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"get", "pvc", "-A", "-l", "corral.dev/ct=true", "-o", "json"}, `{"items":[]}`, nil)
	ct.SetRunner(fake)
	defer ct.SetRunner(shell.Real{})

	if err := ctListCmd.RunE(ctListCmd, nil); err != nil {
		t.Fatalf("ct list: %v", err)
	}
}

// resetCtCreateFlags restores ctCreateCmd's package-level flag vars to their
// zero values, so one test's --devcontainer/--image doesn't leak into the
// next (these tests call RunE directly rather than through a fresh
// cobra.Execute(), so nothing else resets them between tests).
func resetCtCreateFlags(t *testing.T) {
	t.Helper()
	ctImage, ctNamespace, ctDevcontainer = "", "", ""
	ctCPU, ctPrivileged = 0, false
	t.Cleanup(func() {
		ctImage, ctNamespace, ctDevcontainer = "", "", ""
		ctCPU, ctPrivileged = 0, false
	})
}

func writeDevcontainerJSON(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing devcontainer.json: %v", err)
	}
	return path
}

func TestCtCreateCmd_DevcontainerResolvesImage(t *testing.T) {
	resetCtCreateFlags(t)
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	ct.SetRunner(fake)
	defer ct.SetRunner(shell.Real{})

	ctNamespace = "corral-ct"
	ctDevcontainer = writeDevcontainerJSON(t, `{"image": "debian:12"}`)

	if err := ctCreateCmd.RunE(ctCreateCmd, []string{"myproj"}); err != nil {
		t.Fatalf("ct create --devcontainer: %v", err)
	}

	var sawImage bool
	for _, c := range fake.Calls() {
		if c.Name == "kubectl" && strings.Contains(c.Stdin, `"debian:12"`) {
			sawImage = true
		}
	}
	if !sawImage {
		t.Error("expected the devcontainer.json's image to reach the applied manifest")
	}
}

func TestCtCreateCmd_DevcontainerMissingImageErrors(t *testing.T) {
	resetCtCreateFlags(t)
	ctDevcontainer = writeDevcontainerJSON(t, `{}`)

	err := ctCreateCmd.RunE(ctCreateCmd, []string{"myproj"})
	if err == nil {
		t.Fatal("expected an error for a devcontainer.json with no image")
	}
	if !strings.Contains(err.Error(), "no usable image") {
		t.Errorf("error should explain there's no usable image, got: %v", err)
	}
}

func TestCtCreateCmd_DevcontainerBuildWithoutImageErrors(t *testing.T) {
	resetCtCreateFlags(t)
	ctDevcontainer = writeDevcontainerJSON(t, `{"build": {"dockerfile": "Dockerfile"}}`)

	err := ctCreateCmd.RunE(ctCreateCmd, []string{"myproj"})
	if err == nil {
		t.Fatal("expected an error for build.dockerfile without --image")
	}
	if !strings.Contains(err.Error(), "build.dockerfile") {
		t.Errorf("error should mention build.dockerfile, got: %v", err)
	}
}

func TestCtCreateCmd_DevcontainerExplicitImageOverridesJSON(t *testing.T) {
	resetCtCreateFlags(t)
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	ct.SetRunner(fake)
	defer ct.SetRunner(shell.Real{})

	ctNamespace = "corral-ct"
	ctImage = "fedora:40"
	ctDevcontainer = writeDevcontainerJSON(t, `{"image": "debian:12"}`)

	if err := ctCreateCmd.RunE(ctCreateCmd, []string{"myproj"}); err != nil {
		t.Fatalf("ct create --devcontainer --image: %v", err)
	}
	for _, c := range fake.Calls() {
		if c.Name == "kubectl" && strings.Contains(c.Stdin, `"debian:12"`) {
			t.Error("explicit --image should override the devcontainer.json's image")
		}
	}
}

func TestCtCreateCmd_DevcontainerNotFoundErrors(t *testing.T) {
	resetCtCreateFlags(t)
	ctDevcontainer = t.TempDir() // no devcontainer.json in here

	if err := ctCreateCmd.RunE(ctCreateCmd, []string{"myproj"}); err == nil {
		t.Error("expected an error when --devcontainer points at a dir with no devcontainer.json")
	}
}

func TestCtNamespaceOrDefault(t *testing.T) {
	ctNamespace = "custom-ns"
	if got := ctNamespaceOrDefault(); got != "custom-ns" {
		t.Errorf("got %q, want custom-ns", got)
	}
	ctNamespace = ""
	if got := ctNamespaceOrDefault(); got == "" {
		t.Error("expected a non-empty default namespace")
	}
}
