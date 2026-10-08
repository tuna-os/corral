package vmtest

import (
	"io"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

// The runner's seams. Runner.Run owns the sequencing — what happens in which
// order, which failure maps to which exit code, and what is cleaned up on the
// way out — and reaches the host only through these collaborators. Run wires
// the production adapters (QEMU, bootc, the corral registry); tests wire fakes,
// so the ordering and cleanup invariants are checked without a VM.

// ImagePreparer turns a spec into a VM disk: the host check, the run's SSH
// keys, the derived image layer, and the build-and-import that creates the VM.
type ImagePreparer interface {
	Preflight(spec *Spec) (notes []string, err error)
	Keypair(dir string) (Keypair, error)
	Layer(spec *Spec, authorizedKeys, dir string, progress func(string)) (Layered, error)
	// BuildAndImport creates the VM. An error that contains "creating the
	// VM" is reported as ExitStart; any other as ExitBuild.
	BuildAndImport(spec *Spec, layered Layered, keys Keypair, progress func(string)) error
}

// VMLifecycle starts, reaches and deletes the VM under test.
type VMLifecycle interface {
	Start(name string) error
	// WaitReady blocks until the guest is up by the spec's gate, and records
	// ReadyBy and ReadyEvidence on result.
	WaitReady(spec *Spec, keys Keypair, result *Result, progress func(string)) error
	SSHEndpoint(name string) (qemu.Endpoint, error)
	WaitSSH(name, user, identityFile string, timeout time.Duration) error
	Delete(name string) error
}

// GuestProbes asks the running guest questions over SSH or its console.
type GuestProbes interface {
	Hook(spec *Spec, keys Keypair, sshReachable bool, progress func(string)) *HookResult
	Checks(spec *Spec, keys Keypair, progress func(string)) []CheckResult
	Diagnostics(spec *Spec, keys Keypair, artifacts string)
}

// FrameSource is a running framebuffer capture. stop is idempotent and must
// return only once capture has ended.
type FrameSource interface {
	stop()
	frames() []qemu.Frame
}

// EvidenceCollector writes what a reader looks at after the run.
type EvidenceCollector interface {
	StartFrames(spec *Spec, artifacts string) FrameSource
	CopySerialLog(name, artifacts string) (string, error)
	CaptureFinal(spec *Spec, artifacts, label string, result *Result) *qemu.Frame
	AssembleVideo(spec *Spec, artifacts string) (string, error)
}

// RegistryRecorder makes the VM visible to the rest of corral. It cannot fail
// the run: a problem is a warning on out.
type RegistryRecorder interface {
	Record(spec *Spec, out io.Writer)
}

// Clock is the runner's source of time, for the result's timestamps and the
// boot duration.
type Clock interface {
	Now() time.Time
}

// Runner executes a Spec against injected collaborators.
type Runner struct {
	Image    ImagePreparer
	VM       VMLifecycle
	Probes   GuestProbes
	Evidence EvidenceCollector
	Registry RegistryRecorder
	Clock    Clock
}

// NewRunner returns a Runner wired to the production adapters: a local bootc
// build, a QEMU VM, SSH probes and the corral registry.
func NewRunner() *Runner {
	return &Runner{
		Image:    hostImage{},
		VM:       qemuLifecycle{},
		Probes:   sshProbes{},
		Evidence: hostEvidence{},
		Registry: corralRegistry{},
		Clock:    systemClock{},
	}
}

// ── production adapters ──────────────────────────────────────────

type hostImage struct{}

func (hostImage) Preflight(spec *Spec) ([]string, error) { return Preflight(spec) }
func (hostImage) Keypair(dir string) (Keypair, error)    { return EnsureKeypair(dir, operatorKey()) }
func (hostImage) Layer(spec *Spec, authorizedKeys, dir string, progress func(string)) (Layered, error) {
	return BuildLayer(spec, authorizedKeys, dir, progress)
}
func (hostImage) BuildAndImport(spec *Spec, layered Layered, keys Keypair, progress func(string)) error {
	return buildAndImport(spec, layered, keys, progress)
}

type qemuLifecycle struct{}

func (qemuLifecycle) Start(name string) error { return qemu.Start(name) }
func (qemuLifecycle) WaitReady(spec *Spec, keys Keypair, result *Result, progress func(string)) error {
	return waitReady(spec, keys, result, progress)
}
func (qemuLifecycle) SSHEndpoint(name string) (qemu.Endpoint, error) { return qemu.SSHEndpoint(name) }
func (qemuLifecycle) WaitSSH(name, user, identityFile string, timeout time.Duration) error {
	return qemu.WaitSSHKey(name, user, identityFile, timeout)
}
func (qemuLifecycle) Delete(name string) error { return qemu.Delete(name) }

type sshProbes struct{}

func (sshProbes) Hook(spec *Spec, keys Keypair, sshReachable bool, progress func(string)) *HookResult {
	return hookOutcome(spec, keys, sshReachable, progress)
}
func (sshProbes) Checks(spec *Spec, keys Keypair, progress func(string)) []CheckResult {
	return runChecks(spec, keys, progress)
}
func (sshProbes) Diagnostics(spec *Spec, keys Keypair, artifacts string) {
	collectDiagnostics(spec, keys, artifacts)
}

type hostEvidence struct{}

func (hostEvidence) StartFrames(spec *Spec, artifacts string) FrameSource {
	f := newFrameRecorder(spec, artifacts)
	f.start()
	return f
}
func (hostEvidence) CopySerialLog(name, artifacts string) (string, error) {
	return copySerialLog(name, artifacts)
}
func (hostEvidence) CaptureFinal(spec *Spec, artifacts, label string, result *Result) *qemu.Frame {
	return captureFinal(spec, artifacts, label, result)
}
func (hostEvidence) AssembleVideo(spec *Spec, artifacts string) (string, error) {
	return assembleVideo(spec, artifacts)
}

type corralRegistry struct{}

func (corralRegistry) Record(spec *Spec, out io.Writer) { recordInRegistry(spec, out) }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
