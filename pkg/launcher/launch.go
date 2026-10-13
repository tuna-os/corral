package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// LaunchOpts is the composed launcher invocation. Either ManifestPath or
// explicit Image coordinates are required — there is no default asset, and
// package/image qualification remains the caller's prerequisite.
type LaunchOpts struct {
	// VM identity and sizing.
	Name string
	Mem  string
	CPU  int
	Disk string

	// Image source: one of ManifestPath or explicit Image.
	ManifestPath string
	Image        *ManifestImage

	// Guest binding. Empty Arch defaults to the manifest arch (which is
	// always explicit). Firmware empty defaults to the manifest firmware.
	Arch     string
	Firmware string

	// Emulation scopes. Both default false: native or refusal.
	AllowEmulation bool
	AllowCrossArch bool

	// Web server port on 127.0.0.1. 0 = any free loopback port.
	WebPort int

	// Timeouts.
	VMTimeout  time.Duration
	WebTimeout time.Duration

	// StopVM stops the VM on launcher exit. Default false: the VM survives
	// web shutdown, like `corral web` today.
	StopVM bool

	// Force recreates an existing VM. Default false: an existing VM name
	// is refused rather than clobbered.
	Force bool

	// VMHome scopes Prepare's cache and preflight paths (for tests).
	// Empty = real home. It does NOT isolate VM lifecycle: EnsureVM,
	// start/stop and readiness always use the process-global qemu.VMHome,
	// which tests isolate via qemu.SetStateDirs instead.
	VMHome string

	// Ctx cancels the launch (download, waits). Nil uses
	// context.Background. Set by the CLI from its signal context.
	Ctx context.Context

	// PreflightProbes overrides preflight probes (for tests). Nil uses
	// the real probes. The emulation flags below always win over any
	// Allow fields carried in the override.
	PreflightProbes *PreflightOpts

	// WebHandler serves the UI. Nil uses the real dashboard mux. Tests
	// inject a stub (healthz only); the launcher never runs without one.
	WebHandler http.Handler

	// OpenBrowser opens the console URL. Nil uses the platform opener.
	// A false-returning stub still prints the URL.
	OpenBrowser func(url string) error
}

func launchCtx(opts LaunchOpts) context.Context {
	if opts.Ctx != nil {
		return opts.Ctx
	}
	return context.Background()
}

// LaunchResult is what the launcher owns after a successful launch: the
// verified image path, the VM name, the owned web server and the console URL.
type LaunchResult struct {
	ImagePath string
	VMName    string
	WebAddr   string
	Console   string
	Emulated  bool
	Server    *OwnedServer
	// Started reports whether this invocation started the VM (as opposed
	// to reusing an already-running one). Token is the ownership token
	// for the running state this invocation started, valid only when
	// Started is true. Shutdown's --stop-vm stop is token-owned on this
	// basis: a VM replaced after launch is refused, never killed.
	Started bool
	Token   qemu.StartToken
}

// ResolveImage returns the effective manifest image for opts, enforcing the
// no-default-asset rule: exactly one of ManifestPath or Image must be set,
// and the result is validated fail-closed.
func ResolveImage(opts LaunchOpts) (ManifestImage, error) {
	var im ManifestImage
	if opts.ManifestPath != "" && opts.Image != nil {
		return im, fmt.Errorf("pass exactly one of --manifest or explicit --image coordinates, not both")
	}
	if opts.ManifestPath != "" {
		m, err := ParseManifest(opts.ManifestPath)
		if err != nil {
			return im, err
		}
		im = m.Image
	} else if opts.Image != nil {
		im = *opts.Image
		if err := im.Validate(); err != nil {
			return im, fmt.Errorf("invalid explicit image: %w", err)
		}
	} else {
		return im, fmt.Errorf("no image: pass --manifest <file> or explicit --image/--sha256/--size/--arch/--firmware (there is no default Tuna artifact — package qualification is a prerequisite, see the launcher help)")
	}
	// Caller arch/firmware override the manifest only when explicitly set;
	// a mismatch is refused rather than silently rebound.
	if opts.Arch != "" && opts.Arch != im.Arch {
		return im, fmt.Errorf("arch %q does not match manifest arch %q — refusing to boot a mismatched disk", opts.Arch, im.Arch)
	}
	if opts.Firmware != "" && opts.Firmware != im.Firmware {
		return im, fmt.Errorf("firmware %q does not match manifest firmware %q — refusing to boot a mismatched disk", opts.Firmware, im.Firmware)
	}
	return im, nil
}

// Prepare verifies the image is admitted (downloaded + digest-bound) and the
// host passes preflight, without creating or starting anything. It is the
// unit-testable half of Launch: pure verification, no lifecycle.
func Prepare(opts LaunchOpts) (imagePath string, im ManifestImage, err error) {
	im, err = ResolveImage(opts)
	if err != nil {
		return "", im, err
	}
	vmHome := opts.VMHome
	if vmHome == "" {
		vmHome = qemu.VMHome()
	}
	probes := PreflightOpts{}
	if opts.PreflightProbes != nil {
		probes = *opts.PreflightProbes
	}
	probes.AllowEmulation = opts.AllowEmulation
	probes.AllowCrossArch = opts.AllowCrossArch
	// One full preflight under the caller's explicit policy: every gate
	// runs, and the error names the real failure — there is no second
	// pass whose success could be discarded for the strict error.
	if _, err := Preflight(vmHome, im, probes); err != nil {
		return "", im, err
	}
	imagePath, err = Admit(vmHome, im, DownloadOpts{Ctx: launchCtx(opts)})
	if err != nil {
		return "", im, err
	}
	// Measured conversion capacity: the admitted bytes are in hand now, so
	// the converted guest copy they imply (plus headroom) must fit — the
	// preflight estimate above used the manifest size, this inspects the
	// admitted image's virtual size, which is what conversion demand
	// follows. Creation re-checks again immediately before converting.
	if err := verifyAdmittedCapacity(launchCtx(opts), vmHome, imagePath, im, probes.TemplateInspect, probes.FreeBytes); err != nil {
		return "", im, err
	}
	return imagePath, im, nil
}

// verifyAdmittedCapacity proves the admitted image's virtual-disk
// conversion plus headroom fits. The bound is the VIRTUAL size from
// qemu-img info, not the admitted file's (possibly highly compressed)
// length: conversion materializes dense guest extents. inspectProbe
// overrides the inspect (for tests); nil uses the real qemu-img lookup.
// freeProbe overrides the statfs (for tests); nil uses the real one.
func verifyAdmittedCapacity(ctx context.Context, vmHome, imagePath string, im ManifestImage, inspectProbe func(context.Context, string, string) (uint64, error), freeProbe func(string) (uint64, error)) error {
	if inspectProbe == nil {
		inspectProbe = defaultTemplateInspect
	}
	virtual, err := inspectProbe(ctx, im.Arch, imagePath)
	if err != nil {
		return fmt.Errorf("cannot bound conversion demand for %s: %w", imagePath, err)
	}
	if freeProbe == nil {
		freeProbe = freeBytesOn
	}
	free, err := freeProbe(vmHome)
	if err != nil {
		return fmt.Errorf("cannot read free disk space: %w", err)
	}
	need := virtual + 2*1024*1024*1024
	if free < need {
		st, _ := os.Stat(imagePath)
		var fileGB float64
		if st != nil {
			fileGB = float64(st.Size()) / 1e9
		}
		return fmt.Errorf("need %.1f GB free for the %.1f GB virtual disk's converted guest copy and 2 GB headroom, have %.1f GB (admitted file is %.3f GB — compressed size does not bound conversion demand)",
			float64(need)/1e9, float64(virtual)/1e9, float64(free)/1e9, fileGB)
	}
	return nil
}

// EnsureVM creates the VM when missing (verified image as its template) and
// reports whether it created. An existing VM is reused only when its
// recorded creation source, guest arch, firmware, loopback transport and
// command record all match the current manifest and invocation — otherwise
// it is refused, never silently rebound. Force recreates (a running VM is
// stopped and proven stopped first; an unstoppable one refuses).
func EnsureVM(opts LaunchOpts, imagePath string, im ManifestImage) (created bool, err error) {
	name := opts.Name
	if err := qemu.ValidateVMName(name); err != nil {
		return false, err
	}
	if qemu.Exists(name) && !opts.Force {
		if err := checkExistingVM(name, im, opts); err != nil {
			return false, err
		}
		return false, nil
	}
	mem := opts.Mem
	if mem == "" {
		mem = "4G"
	}
	cpu := opts.CPU
	if cpu == 0 {
		cpu = 2
	}
	disk := opts.Disk
	if disk == "" {
		disk = "20G"
	}
	createOpts := types.CreateOpts{
		Name:           name,
		Mem:            mem,
		CPU:            cpu,
		Disk:           disk,
		QCOW:           imagePath,
		Force:          opts.Force,
		UEFI:           im.Firmware == "uefi",
		GuestArch:      im.Arch,
		Loopback:       true,
		AllowEmulation: opts.AllowEmulation,
		AllowCrossArch: opts.AllowCrossArch,
		SourceURL:      im.URL,
		SourceSHA256:   im.SHA256,
		SourceSize:     im.Size,
		SourceFormat:   im.EffectiveFormat(),
		Ctx:            launchCtx(opts),
	}
	if err := qemu.Create(createOpts); err != nil {
		return false, err
	}
	return true, nil
}

func checkExistingVM(name string, im ManifestImage, opts LaunchOpts) error {
	data, err := qemu.Info(name)
	if err != nil {
		return fmt.Errorf("VM %q exists but its metadata is unreadable: %w", name, err)
	}
	if err := checkManifestBytes(data, name, im, opts); err != nil {
		return err
	}
	if err := qemu.ValidateCommand(name); err != nil {
		return fmt.Errorf("VM %q exists but %v — pass --force to recreate it", name, err)
	}
	return nil
}

// checkManifestBytes binds recorded creation-source/arch/firmware/transport
// metadata to the current manifest and invocation. It checks the bytes it
// is given, never re-reading: admission runs it against the exact bytes the
// definition token digests, so the manifest binding and the token pin the
// same snapshot.
func checkManifestBytes(data []byte, name string, im ManifestImage, opts LaunchOpts) error {
	// Minimal decode: only the fields the launcher binds. Unknown or legacy
	// (pre-arch, pre-provenance) VMs are refused with a --force hint, never
	// assumed. qemu.ValidateCommand below binds the rest (machine, argv,
	// transports, firmware, unit); the digest here pins the SOURCE image,
	// which must never substitute for that device/transport validation.
	var meta struct {
		GuestArch    string `json:"guest_arch"`
		Firmware     string `json:"firmware"`
		SourceSHA256 string `json:"source_sha256"`
		SourceSize   int64  `json:"source_size"`
		SourceFormat string `json:"source_format"`
		Loopback     bool   `json:"loopback"`
		Bind         string `json:"tailscale_ip"`
		Emulated     bool   `json:"emulated"`
		Accel        string `json:"accel"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("VM %q exists but its metadata is invalid — pass --force to recreate it", name)
	}
	if meta.GuestArch == "" {
		return fmt.Errorf("VM %q predates guest-arch binding — pass --force to recreate it for %s/%s", name, im.Arch, im.Firmware)
	}
	if meta.GuestArch != im.Arch || meta.Firmware != im.Firmware {
		return fmt.Errorf("VM %q is %s/%s but the manifest wants %s/%s — pass --force to recreate it",
			name, meta.GuestArch, meta.Firmware, im.Arch, im.Firmware)
	}
	if meta.SourceSHA256 == "" {
		return fmt.Errorf("VM %q predates creation-source binding — pass --force to recreate it from this manifest's image", name)
	}
	if meta.SourceSHA256 != im.SHA256 || meta.SourceSize != im.Size {
		return fmt.Errorf("VM %q was created from a different image (sha256 %.12s…, %d bytes) than the manifest's (%.12s…, %d bytes) — pass --force to recreate it",
			name, meta.SourceSHA256, meta.SourceSize, im.SHA256, im.Size)
	}
	// A different download URL for identical bytes is legitimately
	// reusable (digest equality governs); a different container format is
	// not. Empty records predate format binding and mean qcow2, matching
	// EffectiveFormat's default.
	metaFormat := meta.SourceFormat
	if metaFormat == "" {
		metaFormat = "qcow2"
	}
	if metaFormat != im.EffectiveFormat() {
		return fmt.Errorf("VM %q was created from format %q but the manifest binds %q — pass --force to recreate it",
			name, metaFormat, im.EffectiveFormat())
	}
	// Loopback is a transport fact, not just a flag: the recorded bind
	// address must BE loopback, since the launcher dials loopback only.
	if !meta.Loopback || meta.Bind != qemu.Loopback {
		return fmt.Errorf("VM %q is not loopback-bound but the launcher dials loopback only — pass --force to recreate it", name)
	}
	// Emulation opt-in is scope-correct, mirroring ResolveGuestSpec: a
	// cross-arch guest needs --allow-cross-arch, a same-arch TCG guest
	// needs --allow-emulation. Either flag no longer admits either scope.
	if meta.Emulated || meta.Accel == "tcg" {
		if cross := meta.GuestArch != runtime.GOARCH; cross && !opts.AllowCrossArch {
			return fmt.Errorf("VM %q is a cross-arch (%s-on-%s) emulated VM — pass --allow-cross-arch to reuse it, or --force to recreate it", name, meta.GuestArch, runtime.GOARCH)
		} else if !cross && !opts.AllowEmulation {
			return fmt.Errorf("VM %q is an emulated (TCG) VM — pass --allow-emulation to reuse it, or --force to recreate it", name)
		}
	}
	return nil
}

// EnsureVMAdmitted is EnsureVM plus admission capture: after the VM is
// created or reuse-validated, the admitted definition's identity is pinned
// (manifest binding against the exact bytes the token digests, validity
// plus loaded-unit proof, all under one lifecycle-lock hold). The token
// travels to StartAndWaitAdmitted, which refuses anything that changed
// since — a force-create landing between ensure and start no longer starts
// the replacement while claiming the original manifest was verified.
func EnsureVMAdmitted(opts LaunchOpts, imagePath string, im ManifestImage) (bool, qemu.DefinitionToken, error) {
	var tok qemu.DefinitionToken
	created, err := EnsureVM(opts, imagePath, im)
	if err != nil {
		return false, tok, err
	}
	tok, err = qemu.AdmitDefinition(opts.Name, func(meta []byte) error {
		return checkManifestBytes(meta, opts.Name, im, opts)
	})
	if err != nil {
		return created, tok, err
	}
	return created, tok, nil
}

// StartAndWait starts the VM (no-op when already running) and waits for
// loopback VNC readiness.
func StartAndWait(name string, timeout time.Duration) error {
	_, _, err := StartAndWaitCtx(context.Background(), name, timeout)
	return err
}

// StartAndWaitCtx is StartAndWait under ctx (launcher shutdown cancels the
// wait) and additionally reports whether this call started the VM plus the
// ownership token for the running state it started. Failure cleanup owns
// exactly the started case — a VM this call did not start is somebody
// else's running state and must be left alone — and the token binds that
// cleanup stop to the captured identity, so a VM replaced between start
// and cleanup is refused rather than killed. The token is valid only when
// started is true.
//
// On error the started/token outcome is still reported, never zeroed: a
// start that launched a child but failed to verify it (started=true with
// an error) must reach cleanup with its outcome intact, or the child it
// started is orphaned behind a false "did not start".
func StartAndWaitCtx(ctx context.Context, name string, timeout time.Duration) (bool, qemu.StartToken, error) {
	tok, started, err := qemu.StartOwnedTokenCtx(ctx, name)
	if err != nil {
		return started, tok, err
	}
	if err := WaitVMReady(name, ReadinessOpts{Timeout: timeout, Ctx: ctx}); err != nil {
		return started, tok, err
	}
	return started, tok, nil
}

// StartAndWaitAdmitted is StartAndWaitCtx for an admitted definition: the
// start proceeds only when the live definition still equals the token
// EnsureVMAdmitted captured, so a replacement between ensure and start
// refuses instead of starting under the original manifest's claim. The
// started/token outcome discipline matches StartAndWaitCtx exactly —
// reported on error, never zeroed.
func StartAndWaitAdmitted(ctx context.Context, name string, admitted qemu.DefinitionToken, timeout time.Duration) (bool, qemu.StartToken, error) {
	tok, started, err := qemu.StartOwnedTokenAdmitted(name, admitted)
	if err != nil {
		return started, tok, err
	}
	if err := WaitVMReady(name, ReadinessOpts{Timeout: timeout, Ctx: ctx}); err != nil {
		return started, tok, err
	}
	return started, tok, nil
}

// CleanupOwnedStart stops a VM this invocation started when a later phase
// failed, leaving it stopped on disk for inspection (never deleted, never
// silently stranded running). A VM this invocation did not start — a reused
// already-running guest — is somebody else's running state and is left
// exactly as it was. The stop is token-owned: if the VM was replaced since
// this run started it, the stranger is refused, never stopped. The
// VM-survives default applies to successful sessions only, via Shutdown.
//
// It returns the cleanup failure, if any, so the caller can join it with
// the primary error: a failed retirement must surface alongside the phase
// failure that triggered cleanup, not hide behind a stderr warning alone.
func CleanupOwnedStart(name string, started bool, tok qemu.StartToken) error {
	if !started {
		fmt.Fprintf(os.Stderr, "VM %q left as it was (this run did not start it).\n", name)
		return nil
	}
	fmt.Fprintf(os.Stderr, "Stopping VM %q (this run started it; leaving it stopped on disk for inspection)...\n", name)
	if err := qemu.StopOwned(name, tok); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not stop VM %q: %v — it may still be running; stop it with: corral stop %s\n", name, err, name)
		return fmt.Errorf("could not stop VM %q this run started: %w (it may still be running; stop it with: corral stop %s)", name, err, name)
	}
	return nil
}

// ServeAndOpen starts the owned loopback web server, waits for readiness,
// and opens the console URL. The opener's error is returned alongside the
// result — the URL is always printed by the caller so a headless host is
// never stranded.
func ServeAndOpen(vmName string, opts LaunchOpts) (*LaunchResult, error) {
	return ServeAndOpenCtx(context.Background(), vmName, opts)
}

// ServeAndOpenCtx is ServeAndOpen under ctx (launcher shutdown cancels the
// web wait and the shutdown below it).
func ServeAndOpenCtx(ctx context.Context, vmName string, opts LaunchOpts) (*LaunchResult, error) {
	handler := opts.WebHandler
	if handler == nil {
		h, err := defaultWebHandler()
		if err != nil {
			return nil, err
		}
		handler = h
	}
	srv, err := ServeOwned(handler, opts.WebPort)
	if err != nil {
		return nil, err
	}
	webTimeout := opts.WebTimeout
	if webTimeout <= 0 {
		webTimeout = 30 * time.Second
	}
	if err := srv.WaitReadyCtx(ctx, webTimeout); err != nil {
		// The server started but never became ready: shut it down on a
		// FRESH bounded context (ctx may already be cancelled, and a
		// cancelled shutdown context would abandon the retirement) and
		// report both the readiness failure and any shutdown failure —
		// an unverified shutdown must not hide behind the readiness error.
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownBound)
		shutErr := srv.Shutdown(shutCtx)
		cancel()
		if shutErr != nil {
			return nil, errors.Join(err, fmt.Errorf("web shutdown: %w", shutErr))
		}
		return nil, err
	}
	console := ConsoleURL(srv.Addr, vmName)
	open := opts.OpenBrowser
	if open == nil {
		open = OpenBrowser
	}
	openErr := open(console)
	res := &LaunchResult{VMName: vmName, WebAddr: srv.Addr, Console: console, Server: srv}
	if openErr != nil {
		return res, fmt.Errorf("console ready at %s (%v)", console, openErr)
	}
	return res, nil
}

// shutdownBound bounds each owned cleanup: the web retirement gets this long
// on its own context, and the VM stop follows with its own internal bounds.
const shutdownBound = 10 * time.Second

// Shutdown stops what the launcher owns: always the web server (verified),
// plus the VM only when StopVM is set. The VM-survives default is
// deliberate and documented — closing the console never stops the guest.
// The VM stop is token-bound where this invocation started the VM: a
// replacement after launch is refused loudly, never stopped. A reused VM
// this invocation did not start is stopped by name — the user named this
// VM for --stop-vm, same as an explicit `corral stop`.
func Shutdown(res *LaunchResult, stopVM bool) error {
	return ShutdownCtx(context.Background(), res, stopVM)
}

// ShutdownCtx is Shutdown under ctx. A failed web shutdown never skips an
// explicitly requested VM stop: each independent cleanup is attempted on
// its own bounded context, and both failures are reported when both fail.
func ShutdownCtx(ctx context.Context, res *LaunchResult, stopVM bool) error {
	if res == nil || res.Server == nil {
		return nil
	}
	webCtx, cancel := context.WithTimeout(ctx, shutdownBound)
	webErr := res.Server.Shutdown(webCtx)
	cancel()
	var vmErr error
	if stopVM {
		if res.Started {
			if err := qemu.StopOwnedCtx(ctx, res.VMName, res.Token); err != nil {
				vmErr = fmt.Errorf("stopping VM %q: %w", res.VMName, err)
			}
		} else if err := qemu.StopCtx(ctx, res.VMName); err != nil {
			vmErr = fmt.Errorf("stopping VM %q: %w", res.VMName, err)
		}
	} else {
		_, _ = fmt.Fprintf(os.Stderr, "VM %q keeps running — stop it with: corral stop %s\n", res.VMName, res.VMName)
	}
	return errors.Join(webErr, vmErr)
}
