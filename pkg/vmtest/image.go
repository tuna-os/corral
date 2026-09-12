package vmtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	report(progress, "pulling %s", spec.Bootc)
	name, args := podmanCmd(spec.Sudo, "pull", spec.Bootc)
	if out, err := runner.Run(name, args...); err != nil {
		return result, fmt.Errorf("podman pull %s: %s", spec.Bootc, commandOutput(out, err))
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
	name, args = podmanCmd(spec.Sudo, "build", "--tag", tag, "--file",
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

// DetectPackageManager reports how the image installs software, by looking for
// the binary in the image filesystem.
//
// Probed with `podman cp` and never executed: Universal Blue images ship Rust
// uutils, where running a binary through `podman --entrypoint` misdetects
// exactly the images this matters most for. pkg/bootc detects its storage
// backend the same way and for the same reason.
func DetectPackageManager(image string, sudo bool) (packageManager, error) {
	name, args := podmanCmd(sudo, "create", image)
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
