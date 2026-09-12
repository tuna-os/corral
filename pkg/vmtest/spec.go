// Package vmtest runs a bootable container image as a local VM and reports
// whether it works — one command, an exit code, and artifacts a human can read
// afterwards.
//
// It is the Lima-shaped front door for testing bootc images in CI: point it at
// an image reference, and it builds the disk, layers in the test accounts and
// packages the test needs, boots the VM, waits for the guest to say it is
// ready, runs assertions inside it, and collects the boot log, the screenshots
// and a JSON result. What a caller does after that is ordinary SSH against a
// running machine.
//
// The work it replaces is real. TunaOS gates its image promotion on
// scripts/iso-e2e.sh — four thousand lines of bash that boots QEMU, tails a
// serial console for a readiness marker, drives the monitor socket to type at
// a console, screendumps frames and measures their standard deviation to catch
// a desktop that never painted. Every one of those moves is a method here, and
// the parts that were bash-and-ImageMagick are Go over the QMP socket corral
// already opens.
//
// Three properties are deliberate:
//
//   - **The published image is not modified.** Users, passwords, packages and
//     hooks go into a *derived* local image (see layer.go), built on top of the
//     reference under test. With no customisation requested, nothing is
//     derived and the image boots exactly as published.
//   - **Every failure leaves evidence.** The serial console, the framebuffer
//     and the post-boot hook's own output are captured as the run goes, not
//     collected at the end, because the failures worth debugging are the ones
//     where the guest never became reachable.
//   - **A skipped check is a failed run.** A missing tool, an unreadable
//     console, a blank screen where a desktop was promised: each is reported
//     as itself, never as success.
package vmtest

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Spec is one test run. It parses from YAML in the Lima shape — `bootc:`,
// `cpus:`, `memory:`, `disk:`, `provision:` mean what a Lima file means by
// them — with the fields a test needs and a Lima file has no word for.
type Spec struct {
	// Name is the VM's name. Defaults to DefaultName.
	Name string `yaml:"name"`
	// Bootc is the image under test: an OCI reference, or a catalog name the
	// caller has already resolved.
	Bootc string `yaml:"bootc"`

	CPUs   int    `yaml:"cpus"`
	Memory string `yaml:"memory"`
	Disk   string `yaml:"disk"`

	// Timeout bounds the whole boot phase — build excluded, since a
	// multi-gigabyte pull is not the guest's fault.
	Timeout Duration `yaml:"timeout"`

	// SSHUser is who the readiness probe and the checks log in as. Defaults to
	// root, which is the account bootc install injects a key for.
	SSHUser string `yaml:"sshUser"`

	// ReadyMarker is a regular expression matched against the guest's serial
	// console. When set, the run is ready once it matches — instead of, not as
	// well as, the SSH probe. An image that ships sshd off can still be
	// tested: it only has to say something.
	ReadyMarker string `yaml:"readyMarker"`

	// Users are accounts created in the derived image. A test that has to log
	// in as a human needs one, and bootc's own install only knows about root.
	Users []User `yaml:"users"`

	// RootPassword sets root's password in the derived image. Empty leaves it
	// locked, which is what the published image says.
	RootPassword string `yaml:"rootPassword"`

	// Packages are layered into the derived image with the base image's own
	// package manager. This is the "install software" half of the job: a test
	// that needs jq or a driver should not need a purpose-built image.
	Packages []string `yaml:"packages"`

	// ExtraRun are verbatim shell lines that run in the derived image *before*
	// the package install — enabling a repository, mostly. Same field name and
	// role as remora's manifest, so a manifest can move between the two.
	ExtraRun []string `yaml:"extraRun"`

	// Files are written into the derived image.
	Files []File `yaml:"files"`

	// Provision are scripts, in Lima's shape. Mode decides when each runs:
	// "image" at build time in the derived image, anything else (Lima's
	// "system", "boot", "user", or empty) in the booted guest as the post-boot
	// hook.
	Provision []Provision `yaml:"provision"`

	// Checks are commands run over SSH once the guest is ready. All must exit
	// zero. They are the assertions: "is the desktop up", "did any unit fail".
	Checks []string `yaml:"checks"`

	// Screenshots controls framebuffer capture.
	Screenshots Screenshots `yaml:"screenshots"`

	// Artifacts is the directory for the console log, the frames and
	// result.json. Defaults to DefaultArtifactDir.
	Artifacts string `yaml:"artifacts"`

	// Keep leaves the VM running when the run finishes, so the caller can run
	// its own tests against it. This is the default — the point of the command
	// is to hand over a booting system.
	Keep bool `yaml:"keep"`

	// Sudo runs podman under sudo. bootc install needs root, and a CI runner
	// that is not root needs to say how to become it.
	Sudo bool `yaml:"sudo"`

	// LayerEngine picks what generates the derived image's Containerfile:
	// "remora" (tuna-os/remora, richer — six package managers, lockfiles),
	// "builtin", or "auto" (remora when installed).
	LayerEngine string `yaml:"layerEngine"`
}

// User is a test account in the derived image.
type User struct {
	Name     string `yaml:"name"`
	Password string `yaml:"password"`
	// Sudo adds the account to the base image's administrator group (wheel, or
	// sudo on Debian bases — whichever exists).
	Sudo              bool     `yaml:"sudo"`
	Groups            []string `yaml:"groups"`
	Shell             string   `yaml:"shell"`
	SSHAuthorizedKeys []string `yaml:"sshAuthorizedKeys"`
}

// File is written into the derived image.
type File struct {
	Path    string `yaml:"path"`
	Content string `yaml:"content"`
	Mode    string `yaml:"mode"`
}

// Provision is one script, in Lima's shape.
type Provision struct {
	Mode   string `yaml:"mode"`
	Script string `yaml:"script"`
}

// ImageMode is the Provision mode that runs at build time rather than in the
// booted guest. It is corral's own: Lima has no build step to name.
const ImageMode = "image"

// Screenshots controls framebuffer capture during the run.
type Screenshots struct {
	// Interval between frames while the guest boots. Zero uses
	// DefaultFrameInterval; a negative value turns capture off.
	Interval Duration `yaml:"interval"`
	// Video assembles the captured frames into a WebM timelapse with ffmpeg.
	// Skipped, with a note in the result, when ffmpeg is absent.
	Video bool `yaml:"video"`
	// RequirePaint fails the run when the final frame is blank. Use it for a
	// desktop image, where an unpainted screen is the failure that a passing
	// SSH probe hides.
	RequirePaint bool `yaml:"requirePaint"`
}

// Defaults for a run that says nothing.
const (
	DefaultName        = "corral-vmtest"
	DefaultArtifactDir = "corral-vmtest-out"
	DefaultCPUs        = 2
	DefaultMemory      = "4G"
	DefaultDisk        = "20G"
	DefaultTimeout     = 15 * time.Minute
	DefaultSSHUser     = "root"
	// DefaultFrameInterval is a compromise between a timelapse worth watching
	// and a QMP round trip every second for fifteen minutes.
	DefaultFrameInterval = 5 * time.Second
	// SerialKarg makes the guest write its boot log to the emulated serial
	// port, where the harness reads it. Without it the console log is empty
	// and a boot that never reaches SSH says nothing at all.
	SerialKarg = "console=ttyS0,115200n8"
)

// Duration is a time.Duration that parses from YAML as "90s" or "15m".
type Duration time.Duration

// UnmarshalYAML accepts a duration string or a bare number of seconds.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if parsed, err := time.ParseDuration(raw); err == nil {
		*d = Duration(parsed)
		return nil
	}
	var seconds int
	if _, err := fmt.Sscanf(raw, "%d", &seconds); err == nil {
		*d = Duration(time.Duration(seconds) * time.Second)
		return nil
	}
	return fmt.Errorf("%q is not a duration like 90s or 15m", raw)
}

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Load reads a spec from a YAML file.
func Load(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return Parse(data)
}

// Parse reads a spec from YAML. Unknown fields are refused rather than
// ignored: a misspelled `packages:` that silently installs nothing wastes a
// whole CI run before anyone notices.
func Parse(data []byte) (*Spec, error) {
	var spec Spec
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&spec); err != nil {
		return nil, fmt.Errorf("parsing the test spec: %w", err)
	}
	return &spec, nil
}

// WithDefaults fills in what the spec left unsaid.
func (s *Spec) WithDefaults() {
	if s.Name == "" {
		s.Name = DefaultName
	}
	if s.CPUs <= 0 {
		s.CPUs = DefaultCPUs
	}
	if s.Memory == "" {
		s.Memory = DefaultMemory
	}
	s.Memory = normalizeSize(s.Memory, DefaultMemory)
	if s.Disk == "" {
		s.Disk = DefaultDisk
	}
	s.Disk = normalizeSize(s.Disk, DefaultDisk)
	if s.Timeout <= 0 {
		s.Timeout = Duration(DefaultTimeout)
	}
	if s.SSHUser == "" {
		s.SSHUser = DefaultSSHUser
	}
	if s.Artifacts == "" {
		s.Artifacts = DefaultArtifactDir
	}
	if s.Screenshots.Interval == 0 {
		s.Screenshots.Interval = Duration(DefaultFrameInterval)
	}
	if s.LayerEngine == "" {
		s.LayerEngine = EngineAuto
	}
}

// Validate reports what would make the run impossible, all of it at once —
// a build that fails ten minutes in on the second mistake is a wasted run.
func (s *Spec) Validate() error {
	var problems []string
	if strings.TrimSpace(s.Bootc) == "" {
		problems = append(problems, "no image to test: set bootc: in the spec, or pass --bootc")
	}
	if s.ReadyMarker != "" {
		if _, err := regexp.Compile(s.ReadyMarker); err != nil {
			problems = append(problems, fmt.Sprintf("readyMarker %q is not a valid regular expression: %v", s.ReadyMarker, err))
		}
	}
	for i, u := range s.Users {
		if strings.TrimSpace(u.Name) == "" {
			problems = append(problems, fmt.Sprintf("users[%d] has no name", i))
		}
		if strings.ContainsAny(u.Name, " \t:\n") {
			problems = append(problems, fmt.Sprintf("users[%d] name %q contains a character an account name cannot have", i, u.Name))
		}
	}
	for i, f := range s.Files {
		if !strings.HasPrefix(f.Path, "/") {
			problems = append(problems, fmt.Sprintf("files[%d] path %q is not absolute", i, f.Path))
		}
	}
	switch s.LayerEngine {
	case EngineAuto, EngineRemora, EngineBuiltin:
	default:
		problems = append(problems, fmt.Sprintf("layerEngine %q is not one of auto, remora, builtin", s.LayerEngine))
	}
	if len(problems) > 0 {
		return fmt.Errorf("this test spec cannot run:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// readyPattern compiles the serial readiness marker, or returns nil when the
// run gates on SSH instead.
func (s *Spec) readyPattern() (*regexp.Regexp, error) {
	if s.ReadyMarker == "" {
		return nil, nil
	}
	// Matched against a whole line so the result can quote what it found.
	return regexp.Compile(".*" + s.ReadyMarker + ".*")
}

// imageScripts and bootScripts split provision entries by when they run.
func (s *Spec) imageScripts() []string { return s.scriptsFor(true) }
func (s *Spec) bootScripts() []string  { return s.scriptsFor(false) }

func (s *Spec) scriptsFor(image bool) []string {
	var out []string
	for _, p := range s.Provision {
		if strings.TrimSpace(p.Script) == "" {
			continue
		}
		if (strings.EqualFold(p.Mode, ImageMode)) == image {
			out = append(out, p.Script)
		}
	}
	return out
}

// needsLayer reports whether anything asked for would change the image. When
// nothing does, the image under test is booted exactly as published.
func (s *Spec) needsLayer() bool {
	return len(s.Users) > 0 || s.RootPassword != "" || len(s.Packages) > 0 ||
		len(s.ExtraRun) > 0 || len(s.Files) > 0 ||
		len(s.imageScripts()) > 0 || len(s.bootScripts()) > 0
}

// normalizeSize turns Lima's sizes into the ones pkg/qemu and bootc use:
// "4GiB", "4GB" and "4G" are the same memory, and a bare number is mebibytes.
// Anything that is not a size at all falls back rather than reaching QEMU as a
// value it will reject.
func normalizeSize(value, fallback string) string {
	upper := strings.ToUpper(strings.TrimSpace(value))
	upper = strings.TrimSuffix(strings.TrimSuffix(upper, "IB"), "B")
	if upper == "" {
		return fallback
	}
	digits := strings.TrimRight(upper, "KMGT")
	unit := upper[len(digits):]
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return fallback
	}
	if unit == "" {
		unit = "M"
	}
	return digits + unit
}
