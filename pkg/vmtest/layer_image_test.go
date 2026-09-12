//go:build e2elayer

// The generated layer, run inside a real bootc image.
//
// This tier exists because of what it costs not to have it. Three CI runs of
// the full harness — pull, layer, disk, boot — died in the same build step for
// three variations of one shell mistake, and each round cost minutes because
// the only place the guest's own userland existed was a CI runner.
//
// It needs no KVM, no privileges and no `bootc install`: the build step that
// keeps failing is an ordinary container build. Any machine with docker or
// podman can run it, in about a minute against a cached image.
//
//	go test -tags e2elayer -timeout 10m ./pkg/vmtest/
//	CORRAL_LAYER_ENGINE=docker CORRAL_VMTEST_IMAGE=quay.io/fedora/fedora-bootc:41 \
//	  go test -tags e2elayer -timeout 10m ./pkg/vmtest/
//
// What it proves: the account script, the service script and the post-boot hook
// run to completion in the image under test, and the accounts, keys, passwords
// and units they claim to create are really there.

package vmtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// layerEngine returns the container engine to build with, or skips.
func layerEngine(t *testing.T) string {
	t.Helper()
	if named := os.Getenv("CORRAL_LAYER_ENGINE"); named != "" {
		if _, err := exec.LookPath(named); err != nil {
			t.Fatalf("CORRAL_LAYER_ENGINE=%s is not installed: %v", named, err)
		}
		return named
	}
	for _, candidate := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(candidate); err == nil {
			// A daemon that is not running is the same absence as a missing
			// binary, and says so more clearly than a build failure would.
			if out, err := exec.Command(candidate, "info").CombinedOutput(); err != nil {
				t.Logf("%s is installed but not usable: %s", candidate, strings.TrimSpace(string(out)))
				continue
			}
			return candidate
		}
	}
	t.Skip("no usable podman or docker; this tier builds a container image")
	return ""
}

func layerTestImage() string {
	if v := os.Getenv("CORRAL_VMTEST_IMAGE"); v != "" {
		return v
	}
	return "quay.io/fedora/fedora-bootc:41"
}

// The whole layer, built and inspected in the image it targets.
func TestE2ELayer_BuildsInTheRealImage(t *testing.T) {
	engine := layerEngine(t)
	image := layerTestImage()

	spec := &Spec{
		Name:         "layerprobe",
		Bootc:        image,
		RootPassword: "corral-root",
		Users: []User{{
			Name:     "tester",
			Password: "corral-test",
			Sudo:     true,
			Groups:   []string{"video"},
		}},
		Files:     []File{{Path: "/etc/corral-probe.conf", Content: "probe=1\n", Mode: "0640"}},
		Provision: []Provision{{Script: "echo the hook ran"}},
	}
	spec.WithDefaults()

	dir := t.TempDir()
	ctx, err := newBuildContext(spec, image, "ssh-ed25519 AAAAprobekey corral-vmtest")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	if err := ctx.write(dir); err != nil {
		t.Fatalf("writing the build context: %v", err)
	}
	// No packages: a package install is dnf's business, not this test's, and it
	// would put a network fetch between the code and the answer.
	containerfile := builtinContainerfile(ctx, packageManager{})
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(containerfile), 0o644); err != nil {
		t.Fatal(err)
	}

	tag := "localhost/corral-layerprobe:test"
	build := exec.Command(engine, "build", "--tag", tag, "--file", filepath.Join(dir, "Containerfile"), dir)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the layer does not build in %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(engine, "rmi", "-f", tag).Run()
	})

	// Each assertion is one thing the layer claims to have done. They run in one
	// container so a failure names the claim rather than the container.
	checks := []struct{ name, command string }{
		{"the test account exists", "id tester"},
		{"its home is under /var/home", `test -d /var/home/tester`},
		{"it is in the administrator group", `id -nG tester | tr ' ' '\n' | grep -qx wheel`},
		{"and in the group the spec asked for", `id -nG tester | tr ' ' '\n' | grep -qx video`},
		{"it has passwordless sudo", "test -f /etc/sudoers.d/90-corral-tester"},
		{"its password is a SHA-512 hash", `getent shadow tester | cut -d: -f2 | grep -q '^\$6\$'`},
		{"root has a password too", `getent shadow root | cut -d: -f2 | grep -q '^\$6\$'`},
		// The failure that cost three CI runs: root's home is behind a symlink
		// to a path the image does not ship.
		{"root's key reached the real home", `grep -q AAAAprobekey /var/roothome/.ssh/authorized_keys`},
		{"and is readable through the symlink", `grep -q AAAAprobekey /root/.ssh/authorized_keys`},
		{"its mode is right", `test "$(stat -c %a /var/roothome/.ssh)" = 700`},
		{"the user's key is installed", `grep -q AAAAprobekey /var/home/tester/.ssh/authorized_keys`},
		{"the spec's file is installed", `test "$(cat /etc/corral-probe.conf)" = probe=1`},
		{"with its mode", `test "$(stat -c %a /etc/corral-probe.conf)" = 640`},
		{"the hook is executable", "test -x /usr/libexec/corral-postboot"},
		{"the hook carries the spec's script", `grep -q 'the hook ran' /usr/libexec/corral-postboot`},
		{"the hook unit is enabled", "test -L /etc/systemd/system/multi-user.target.wants/" + PostBootUnit},
		{"sshd is enabled", `test -L /etc/systemd/system/multi-user.target.wants/sshd.service`},
		{"password logins are on", `grep -q 'PasswordAuthentication yes' /etc/ssh/sshd_config.d/30-corral-vmtest.conf`},
		{"root may log in", `grep -q 'PermitRootLogin yes' /etc/ssh/sshd_config.d/30-corral-vmtest.conf`},
		{"the build context is gone", "test ! -e /tmp/corral-build"},
	}
	for _, check := range checks {
		out, err := exec.Command(engine, "run", "--rm", tag, "bash", "-c", check.command).CombinedOutput()
		if err != nil {
			t.Errorf("%s: %v\n  %s\n  %s", check.name, err, check.command, strings.TrimSpace(string(out)))
		}
	}
}

// The hook has to run in a booted system, and this tier has no init. Running it
// by hand still proves the parts a container can show: it completes, it writes
// its status file, and it prints the markers the harness reads from the console.
func TestE2ELayer_HookRunsInTheImage(t *testing.T) {
	engine := layerEngine(t)
	image := layerTestImage()

	spec := &Spec{Name: "hookprobe", Bootc: image}
	spec.Provision = []Provision{{Script: "echo first\ntest -f /etc/os-release"}}
	spec.WithDefaults()

	dir := t.TempDir()
	ctx, err := newBuildContext(spec, image, "")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	if err := ctx.write(dir); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(dir, "system_files", "usr", "libexec", "corral-postboot")

	run := exec.Command(engine, "run", "--rm", "-v", hook+":/hook:ro", image,
		"bash", "-c", "/hook; echo rc=$?; cat "+StatusFile)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the hook failed inside %s: %v\n%s", image, err, out)
	}
	text := string(out)
	for _, want := range []string{"first", HookOKMarker, ReadyMarker, "rc=0"} {
		if !strings.Contains(text, want) {
			t.Errorf("the hook's output is missing %q:\n%s", want, text)
		}
	}
	// The status file is what the harness reads over SSH, so it has to say 0.
	if !strings.Contains(text, "rc=0\n0") {
		t.Errorf("the status file should hold 0:\n%s", text)
	}
}

// A failing script must fail the hook, and the markers must say so — the
// property the run's exit code 7 depends on.
func TestE2ELayer_HookReportsAFailure(t *testing.T) {
	engine := layerEngine(t)
	image := layerTestImage()

	spec := &Spec{Name: "hookfail", Bootc: image}
	spec.Provision = []Provision{{Script: "exit 9"}}
	spec.WithDefaults()

	dir := t.TempDir()
	ctx, err := newBuildContext(spec, image, "")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	if err := ctx.write(dir); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(dir, "system_files", "usr", "libexec", "corral-postboot")

	out, _ := exec.Command(engine, "run", "--rm", "-v", hook+":/hook:ro", image,
		"bash", "-c", "/hook; echo rc=$?; cat "+StatusFile).CombinedOutput()
	text := string(out)
	if !strings.Contains(text, HookFailMarker+" rc=9") {
		t.Errorf("the console should carry the failure marker and the code:\n%s", text)
	}
	if !strings.Contains(text, ReadyMarker) {
		t.Errorf("the readiness marker must print even when the hook fails:\n%s", text)
	}
	if !strings.Contains(text, "rc=9\n9") {
		t.Errorf("the status file should hold 9:\n%s", text)
	}
}
