package vmtest

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/tuna-os/corral/pkg/image/build"
	"github.com/tuna-os/corral/pkg/shell"
)

// Re-exported types and functions from pkg/image/build for backwards compatibility.
type Layered = build.Layered

var (
	DerivedTag           = build.DerivedTag
	DetectPackageManager = build.DetectPackageManager
)

// BuildLayer is a compatibility wrapper around build.BuildLayer for vmtest.Spec.
func BuildLayer(spec *Spec, authorizedKey, contextDir string, progress func(string)) (Layered, error) {
	bSpec := imageSpec(spec)
	return build.BuildLayer(bSpec, authorizedKey, contextDir, progress)
}

func imageSpec(s *Spec) *build.Spec {
	if s == nil {
		return nil
	}
	return &build.Spec{
		Name:         s.Name,
		Bootc:        s.Bootc,
		Sudo:         s.Sudo,
		LayerEngine:  s.LayerEngine,
		RootPassword: s.RootPassword,
		Users:        s.Users,
		Packages:     s.Packages,
		ExtraRun:     s.ExtraRun,
		Files:        s.Files,
		ImageScripts: s.ImageScripts(),
		BootScripts:  s.BootScripts(),
	}
}

func report(progress func(string), format string, args ...any) {
	if progress != nil {
		progress(fmt.Sprintf(format, args...))
	}
}

var runner shell.Runner = shell.Real{}

// SetRunner overrides the command runner (for unit tests).
func SetRunner(r shell.Runner) {
	runner = r
	build.SetRunner(r)
}

// lookPath is a seam so a test can pretend binaries are or are not installed.
var lookPath = exec.LookPath

// SetLookPath overrides binary lookup (for tests). Passing nil restores the
// real one.
func SetLookPath(f func(string) (string, error)) {
	if f == nil {
		lookPath = exec.LookPath
		build.SetLookPath(nil)
		return
	}
	lookPath = f
	build.SetLookPath(f)
}

func commandOutput(out []byte, err error) string {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return msg
	}
	return err.Error()
}
