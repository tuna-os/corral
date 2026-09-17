package vmtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tuna-os/corral/pkg/bootc"
	"github.com/tuna-os/corral/pkg/shell"
)

// Building the derived image: podman, and remora when it is installed.

var runner shell.Runner = shell.Real{}

// SetRunner overrides the command runner (for unit tests).
func SetRunner(r shell.Runner) { runner = r }

// lookPath is a seam so a test can pretend remora is or is not installed.
var lookPath = exec.LookPath

// SetLookPath overrides binary lookup (for tests). Passing nil restores the
// real one.
func SetLookPath(f func(string) (string, error)) {
	if f == nil {
		lookPath = exec.LookPath
		return
	}
	lookPath = f
}

// DerivedTag is the local tag the derived image is built as. Per run, so two
// concurrent tests on one runner do not overwrite each other.
func DerivedTag(name string) string { return "localhost/corral-vmtest/" + name + ":latest" }

// podmanCmd builds the podman invocation, under sudo when asked. Root podman
// storage matters: `bootc install` runs privileged and pulls from the store it
// can see, so the derived image has to be built into the same one.
func podmanCmd(sudo bool, args ...string) (string, []string) {
	if sudo && os.Geteuid() != 0 {
		return "sudo", append([]string{"-n", "podman"}, args...)
	}
	return "podman", args
}

// Layered describes what the layering step produced.
type Layered struct {
	// Image is what to install: the derived tag, or the original reference
	// when nothing had to be layered.
	Image string `json:"image"`
	// Base is the reference under test, always.
	Base string `json:"base"`
	// Derived reports whether a layer was built at all.
	Derived bool `json:"derived"`
	// Engine is what generated the Containerfile: "remora" or "builtin".
	Engine string `json:"engine,omitempty"`
	// PackageManager is what the base image turned out to use.
	PackageManager string `json:"packageManager,omitempty"`
	// ContextDir holds the Containerfile and the overlay, kept for the
	// artifacts: the generated Containerfile is the record of what was added.
	ContextDir string `json:"contextDir,omitempty"`
}

// BuildLayer builds the derived image for spec, or reports that none is
// needed. contextDir is where the build context is written — inside the run's
// artifact directory, so a failed build leaves the exact Containerfile behind.
func BuildLayer(spec *Spec, authorizedKey, contextDir string, progress func(string)) (Layered, error) {
	result := Layered{Image: spec.Bootc, Base: spec.Bootc}
	if !spec.needsLayer() {
		report(progress, "no users, packages or hooks requested — testing %s exactly as published", spec.Bootc)
		return result, nil
	}

	ctx, err := newBuildContext(spec, spec.Bootc, authorizedKey)
	if err != nil {
		return result, err
	}
	if err := ctx.write(contextDir); err != nil {
		return result, fmt.Errorf("writing the build context: %w", err)
	}
	result.ContextDir = contextDir
	result.Derived = true

	// The base image has to be local before anything can be read out of it.
	if err := fetchBase(spec.Bootc, spec.Sudo, progress); err != nil {
		return result, err
	}

	pm, err := DetectPackageManager(spec.Bootc, spec.Sudo)
	if err != nil && len(spec.Packages) > 0 {
		return result, fmt.Errorf("%w — set packages: [] or name the manager the base uses", err)
	}
	result.PackageManager = pm.Name

	engine := spec.LayerEngine
	if engine == EngineAuto {
		engine = EngineBuiltin
		if _, err := lookPath("remora"); err == nil && pm.Name != "" {
			engine = EngineRemora
		}
	}
	if engine == EngineRemora {
		if err := generateWithRemora(ctx, pm, contextDir, progress); err != nil {
			// remora is the richer generator, not a required one. Falling back
			// keeps the run alive, and says why in the log rather than only in
			// the result.
			report(progress, "remora could not generate the Containerfile (%v); using the built-in generator", err)
			engine = EngineBuiltin
		}
	}
	if engine == EngineBuiltin {
		containerfile := builtinContainerfile(ctx, pm)
		if err := os.WriteFile(filepath.Join(contextDir, "Containerfile"), []byte(containerfile), 0o644); err != nil {
			return result, err
		}
	}
	result.Engine = engine

	tag := DerivedTag(spec.Name)
	report(progress, "building %s from %s (%s engine)", tag, spec.Bootc, engine)
	name, args := podmanCmd(spec.Sudo, "build", "--tag", tag, "--file",
		filepath.Join(contextDir, "Containerfile"), contextDir)
	if out, err := runner.Run(name, args...); err != nil {
		return result, fmt.Errorf("building the derived image: %s", commandOutput(out, err))
	}
	result.Image = tag
	return result, nil
}

// generateWithRemora writes a remora manifest into the context and asks remora
// to render the Containerfile.
//
// remora's build_files/ and system_files/ are the same shape this package
// writes, so the context needs nothing else. What remora adds is package
// managers (portage among them), per-manager cache mounts, and a resolved
// lockfile that makes an unchanged rebuild free.
func generateWithRemora(ctx *buildContext, pm packageManager, contextDir string, progress func(string)) error {
	manifest, err := remoraManifest(ctx)
	if err != nil {
		return err
	}
	if pm.Name != "" {
		// Tell remora rather than let it probe: it would run its own podman
		// container to find out what this already knows.
		manifest = strings.TrimRight(manifest, "\n") + "\npackage_manager: " + pm.Name + "\n"
	}
	if err := os.WriteFile(filepath.Join(contextDir, "remora.yaml"), []byte(manifest), 0o644); err != nil {
		return err
	}
	report(progress, "generating the Containerfile with remora")
	out, err := runner.Run("remora", "generate", "--dir", contextDir)
	if err != nil {
		return fmt.Errorf("remora generate: %s", commandOutput(out, err))
	}
	if _, err := os.Stat(filepath.Join(contextDir, "Containerfile")); err != nil {
		return fmt.Errorf("remora generate wrote no Containerfile")
	}
	return nil
}

// probeCommand is the placeholder argv for a `podman create` whose container
// is never started. See bootc.DetectBackend for why it is needed: a bootc OS
// image ships no CMD and no ENTRYPOINT, and podman refuses to create a
// container from such an image without one.
var probeCommand = []string{"/corral-probe-does-not-execute"}

// fetchBase makes sure the base image is in local storage, without insisting
// it come from a registry.
//
// A `localhost/` reference already is local, and pulling it is the one way to
// fail: podman reads it as a registry called "localhost" and dials
// https://localhost/v2/. Building an image and then testing it is a normal CI
// shape, so the layer builder has to accept the result of that build. The disk
// builder in pkg/bootc has always done this; this is the same guard, applied
// where a spec that asks for users, packages or a hook would otherwise die
// with a network error.
func fetchBase(image string, sudo bool, progress func(string)) error {
	if bootc.IsLocalRef(image) {
		if !imageExists(image, sudo) {
			// Worth its own message: a mistyped local tag otherwise surfaces
			// later as a confusing build failure against a missing base.
			return fmt.Errorf("%s is not in local podman storage — build or tag it first, "+
				"or name an image a registry can serve", image)
		}
		report(progress, "using %s from local storage", image)
		return nil
	}
	report(progress, "pulling %s", image)
	name, args := podmanCmd(sudo, "pull", image)
	out, err := runner.Run(name, args...)
	if err == nil {
		return nil
	}
	// A pull that fails over an image already in storage is a network problem,
	// not a missing image — the offline-runner case.
	if imageExists(image, sudo) {
		report(progress, "could not pull %s (%s); using the copy in local storage",
			image, firstLine(commandOutput(out, err)))
		return nil
	}
	return fmt.Errorf("podman pull %s: %s", image, commandOutput(out, err))
}

// imageExists reports whether podman already holds the image.
func imageExists(image string, sudo bool) bool {
	name, args := podmanCmd(sudo, "image", "exists", image)
	_, err := runner.Run(name, args...)
	return err == nil
}

func firstLine(msg string) string {
	if line, _, found := strings.Cut(msg, "\n"); found {
		return line
	}
	return msg
}

// DetectPackageManager reports how the image installs software, by looking for
// the binary in the image filesystem.
//
// Probed with `podman cp` and never executed: Universal Blue images ship Rust
// uutils, where running a binary through `podman --entrypoint` misdetects
// exactly the images this matters most for. pkg/bootc detects its storage
// backend the same way and for the same reason.
func DetectPackageManager(image string, sudo bool) (packageManager, error) {
	name, args := podmanCmd(sudo, append([]string{"create", image}, probeCommand...)...)
	out, err := runner.Run(name, args...)
	if err != nil {
		return packageManager{}, fmt.Errorf("podman create %s: %s", image, commandOutput(out, err))
	}
	container := strings.TrimSpace(lastLine(string(out)))
	defer func() {
		rm, rmArgs := podmanCmd(sudo, "rm", "-f", container)
		_, _ = runner.Run(rm, rmArgs...)
	}()

	for _, candidate := range packageManagers {
		cp, cpArgs := podmanCmd(sudo, "cp", container+":"+candidate.Probe, "-")
		if _, err := runner.Run(cp, cpArgs...); err == nil {
			return candidate, nil
		}
	}
	return packageManager{}, fmt.Errorf("cannot tell how %s installs packages (looked for dnf, zypper, apt, pacman, apk)", image)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func commandOutput(out []byte, err error) string {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return msg
	}
	return err.Error()
}

func report(progress func(string), format string, args ...any) {
	if progress != nil {
		progress(fmt.Sprintf(format, args...))
	}
}
