package vmtest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

// fakeRunner installs a scriptable runner and restores the real one after.
func fakeRunner(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	SetRunner(fake)
	t.Cleanup(func() { SetRunner(shell.Real{}) })
	return fake
}

// fakeLookPath answers binary lookups for the length of one test.
func fakeLookPath(t *testing.T, found map[string]bool) {
	t.Helper()
	original := lookPath
	t.Cleanup(func() { SetLookPath(original) })
	SetLookPath(func(name string) (string, error) {
		if found[name] {
			return "/fake/bin/" + name, nil
		}
		return "", errors.New("not installed: " + name)
	})
}

// noRemora makes every binary lookup fail, so the builtin engine is chosen.
func noRemora(t *testing.T) {
	t.Helper()
	fakeLookPath(t, nil)
}

func TestBuildLayer_NothingToLayer(t *testing.T) {
	// The published image must be tested exactly as published when the spec
	// asks for no customisation — no derived image, no podman at all.
	fake := fakeRunner(t)
	spec := &Spec{Bootc: "quay.io/fedora/fedora-bootc:41"}
	spec.WithDefaults()

	layered, err := BuildLayer(spec, "ssh-ed25519 AAAA", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if layered.Derived {
		t.Error("nothing was asked for, so nothing should be derived")
	}
	if layered.Image != spec.Bootc {
		t.Errorf("image = %q, want the reference under test", layered.Image)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("expected no commands, ran %d", len(fake.Calls()))
	}
}

func TestBuildLayer_BuiltinEngine(t *testing.T) {
	fake := fakeRunner(t)
	noRemora(t)
	fake.AddPrefixResponse("podman pull", "", nil)
	fake.AddPrefixResponse("podman create", "cafef00d\n", nil)
	fake.AddPrefixResponse("podman cp cafef00d:/usr/bin/dnf", "", nil)
	fake.AddPrefixResponse("podman cp", "", errors.New("no such file"))
	fake.AddPrefixResponse("podman rm", "", nil)
	fake.AddPrefixResponse("podman build", "", nil)

	spec := testSpec()
	spec.Name = "gate"
	spec.LayerEngine = EngineAuto
	dir := t.TempDir()

	layered, err := BuildLayer(spec, "ssh-ed25519 AAAA", dir, nil)
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if !layered.Derived || layered.Engine != EngineBuiltin {
		t.Errorf("layered = %+v, want a builtin-derived image", layered)
	}
	if layered.Image != DerivedTag("gate") {
		t.Errorf("image = %q, want %q", layered.Image, DerivedTag("gate"))
	}
	if layered.PackageManager != "dnf" {
		t.Errorf("package manager = %q, want dnf", layered.PackageManager)
	}

	// The Containerfile is an artifact: it is the record of what was added.
	data, err := os.ReadFile(filepath.Join(dir, "Containerfile"))
	if err != nil {
		t.Fatalf("reading the generated Containerfile: %v", err)
	}
	if !strings.Contains(string(data), "FROM "+spec.Bootc) {
		t.Errorf("the Containerfile does not build on the image under test:\n%s", data)
	}

	// And the build was told where the context is.
	var built bool
	for _, call := range fake.Calls() {
		if call.Name == "podman" && len(call.Args) > 0 && call.Args[0] == "build" {
			built = true
			if !strings.Contains(strings.Join(call.Args, " "), dir) {
				t.Errorf("podman build did not get the context directory: %v", call.Args)
			}
		}
	}
	if !built {
		t.Error("podman build never ran")
	}
}

func TestBuildLayer_RemoraEngine(t *testing.T) {
	fake := fakeRunner(t)
	fakeLookPath(t, map[string]bool{"remora": true})

	dir := t.TempDir()
	fake.AddPrefixResponse("podman pull", "", nil)
	fake.AddPrefixResponse("podman create", "abc123\n", nil)
	fake.AddPrefixResponse("podman cp abc123:/usr/bin/dnf", "", nil)
	fake.AddPrefixResponse("podman cp", "", errors.New("no such file"))
	fake.AddPrefixResponse("podman rm", "", nil)
	fake.AddPrefixResponse("podman build", "", nil)
	// remora writes the Containerfile; the fake has to do that part too,
	// because the code checks that it appeared.
	fake.AddPrefixResponse("remora generate", "generated", nil)

	spec := testSpec()
	spec.LayerEngine = EngineRemora

	// First: remora ran but produced nothing, so the run falls back rather
	// than failing — remora is the richer generator, not a required one.
	layered, err := BuildLayer(spec, "ssh-ed25519 AAAA", dir, nil)
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if layered.Engine != EngineBuiltin {
		t.Errorf("engine = %q, want a fallback to builtin when remora writes no Containerfile", layered.Engine)
	}
	// The manifest it would have consumed is still written, and names the base
	// and the package manager so remora does not probe the image again.
	manifest, err := os.ReadFile(filepath.Join(dir, "remora.yaml"))
	if err != nil {
		t.Fatalf("reading remora.yaml: %v", err)
	}
	if !strings.Contains(string(manifest), "package_manager: dnf") {
		t.Errorf("the manifest should name the detected package manager:\n%s", manifest)
	}
}

func TestBuildLayer_PackagesNeedAPackageManager(t *testing.T) {
	fake := fakeRunner(t)
	noRemora(t)
	fake.AddPrefixResponse("podman pull", "", nil)
	fake.AddPrefixResponse("podman create", "deadbeef\n", nil)
	fake.AddPrefixResponse("podman cp", "", errors.New("no such file"))
	fake.AddPrefixResponse("podman rm", "", nil)

	spec := &Spec{Bootc: "example.com/mystery:1", Packages: []string{"jq"}}
	spec.WithDefaults()
	_, err := BuildLayer(spec, "", t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected an error: packages were requested and nothing can install them")
	}
	if !strings.Contains(err.Error(), "cannot tell how") {
		t.Errorf("error should say what it could not determine: %v", err)
	}
}

func TestBuildLayer_NoPackagesSurvivesUnknownBase(t *testing.T) {
	// A base with no recognised package manager is fine as long as the spec
	// wants no packages — accounts and hooks need no package manager.
	fake := fakeRunner(t)
	noRemora(t)
	fake.AddPrefixResponse("podman pull", "", nil)
	fake.AddPrefixResponse("podman create", "deadbeef\n", nil)
	fake.AddPrefixResponse("podman cp", "", errors.New("no such file"))
	fake.AddPrefixResponse("podman rm", "", nil)
	fake.AddPrefixResponse("podman build", "", nil)

	spec := &Spec{Bootc: "example.com/mystery:1", Users: []User{{Name: "tester"}}}
	spec.WithDefaults()
	layered, err := BuildLayer(spec, "", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if !layered.Derived {
		t.Error("a user was requested, so a layer was needed")
	}
}

func TestBuildLayer_PullFailureIsReported(t *testing.T) {
	fake := fakeRunner(t)
	noRemora(t)
	fake.AddPrefixResponse("podman pull", "unknown: manifest unknown", errors.New("exit 125"))

	spec := testSpec()
	_, err := BuildLayer(spec, "", t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected the pull failure to fail the layer")
	}
	if !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("the error should carry podman's own message: %v", err)
	}
}

func TestDetectPackageManager(t *testing.T) {
	for _, tc := range []struct{ probe, want string }{
		{probe: "/usr/bin/dnf", want: "dnf"},
		{probe: "/usr/bin/apt-get", want: "apt"},
		{probe: "/sbin/apk", want: "apk"},
		{probe: "/usr/bin/pacman", want: "pacman"},
		{probe: "/usr/bin/zypper", want: "zypper"},
	} {
		fake := fakeRunner(t)
		fake.AddPrefixResponse("podman create", "container1\n", nil)
		fake.AddPrefixResponse("podman cp container1:"+tc.probe, "", nil)
		fake.AddPrefixResponse("podman cp", "", errors.New("no such file"))
		fake.AddPrefixResponse("podman rm", "", nil)

		pm, err := DetectPackageManager("some/image", false)
		if err != nil {
			t.Errorf("%s: %v", tc.want, err)
			continue
		}
		if pm.Name != tc.want {
			t.Errorf("detected %q, want %q", pm.Name, tc.want)
		}
	}
}

func TestDetectPackageManager_RemovesTheProbeContainer(t *testing.T) {
	fake := fakeRunner(t)
	fake.AddPrefixResponse("podman create", "probe-container\n", nil)
	fake.AddPrefixResponse("podman cp probe-container:/usr/bin/dnf", "", nil)
	fake.AddPrefixResponse("podman cp", "", errors.New("no such file"))
	fake.AddPrefixResponse("podman rm", "", nil)

	if _, err := DetectPackageManager("some/image", false); err != nil {
		t.Fatalf("DetectPackageManager: %v", err)
	}
	var removed bool
	for _, call := range fake.Calls() {
		if strings.Contains(strings.Join(call.Args, " "), "rm -f probe-container") {
			removed = true
		}
	}
	if !removed {
		t.Error("the probe container must be removed, or a run leaks one per attempt")
	}
}

func TestPodmanCmd_Sudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, where sudo is not used")
	}
	name, args := podmanCmd(true, "build", ".")
	if name != "sudo" || args[0] != "-n" || args[1] != "podman" {
		t.Errorf("sudo invocation = %s %v", name, args)
	}
	name, args = podmanCmd(false, "build", ".")
	if name != "podman" || args[0] != "build" {
		t.Errorf("plain invocation = %s %v", name, args)
	}
}

func TestLastLine(t *testing.T) {
	if got := lastLine("a\nb\nc\n"); got != "c" {
		t.Errorf("lastLine = %q", got)
	}
	if got := lastLine("only"); got != "only" {
		t.Errorf("lastLine = %q", got)
	}
}

func TestCommandOutput_PrefersTheCommandsOwnMessage(t *testing.T) {
	if got := commandOutput([]byte(" real reason \n"), errors.New("exit 1")); got != "real reason" {
		t.Errorf("commandOutput = %q", got)
	}
	if got := commandOutput(nil, errors.New("exit 1")); got != "exit 1" {
		t.Errorf("commandOutput = %q", got)
	}
}
