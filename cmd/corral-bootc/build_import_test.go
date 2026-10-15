//go:build bootc

package main

// Table tests for buildAndImport, the off-cluster bootc flow (#130). The
// seam is two package vars in main.go — newBuilder and targetFor —
// overridden here with fakes, so no test touches podman or backend storage.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/bootc"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/types"
)

type fakeBuilder struct {
	sudoSeen bool
	reqSeen  bootc.BuildRequest
	availErr error
	buildErr error
	result   bootc.BuildResult
}

func (f *fakeBuilder) Available() error { return f.availErr }

type fakeTarget struct {
	requires  []string
	importErr error
	imports   int
	refSeen   types.InstanceRef
	diskSeen  string
	optsSeen  bootc.CreateOpts
}

func (f *fakeBuilder) Build(req bootc.BuildRequest, progress func(string)) (bootc.BuildResult, error) {
	f.reqSeen = req
	if progress != nil {
		progress("building " + req.Dest)
	}
	return f.result, f.buildErr
}

func (f *fakeTarget) Requires() []string { return f.requires }

func (f *fakeTarget) Import(ref types.InstanceRef, disk string, opts bootc.CreateOpts) error {
	f.imports++
	f.refSeen, f.diskSeen, f.optsSeen = ref, disk, opts
	return f.importErr
}

// runBuildAndImport swaps in the fakes, captures stderr (buildAndImport
// reports prerequisites and progress there), and runs the flow.
func runBuildAndImport(t *testing.T, fb *fakeBuilder, ft *fakeTarget, useRealTarget bool, target config.ContextConfig, image, sshKey, disk string, useSudo bool) (string, error) {
	t.Helper()
	oldBuilder, oldTarget := newBuilder, targetFor
	newBuilder = func(sudo bool) bootc.Builder {
		fb.sudoSeen = sudo
		return fb
	}
	if !useRealTarget {
		targetFor = func(string) (bootc.Target, error) { return ft, nil }
	}
	t.Cleanup(func() { newBuilder, targetFor = oldBuilder, oldTarget })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	os.Stderr = w
	runErr := buildAndImport("myvm", target, image, sshKey, disk, "4G", 2, "", nil, useSudo)
	_ = w.Close()
	os.Stderr = oldErr
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func TestBuildAndImport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	qemuTarget := config.ContextConfig{Name: "local", Backend: "qemu", Context: "local"}

	t.Run("missing image", func(t *testing.T) {
		fb, ft := &fakeBuilder{}, &fakeTarget{}
		_, err := runBuildAndImport(t, fb, ft, false, qemuTarget, "", "ssh-ed25519 AAAAtest", "20G", false)
		if err == nil || !strings.Contains(err.Error(), "--image is required") {
			t.Fatalf("err = %v, want missing --image error", err)
		}
	})

	t.Run("unsupported backend", func(t *testing.T) {
		fb := &fakeBuilder{}
		// Real TargetFor: incus is a documented rejection (see TargetFor).
		_, err := runBuildAndImport(t, fb, nil, true,
			config.ContextConfig{Name: "x", Backend: "incus"}, "quay.io/fedora/fedora-bootc:41", "ssh-ed25519 AAAAtest", "20G", false)
		if err == nil || !strings.Contains(err.Error(), "incus") {
			t.Fatalf("err = %v, want incus unsupported error", err)
		}
	})

	t.Run("unavailable builder reports target requirements", func(t *testing.T) {
		fb := &fakeBuilder{availErr: errors.New("podman not found")}
		ft := &fakeTarget{requires: []string{"qemu-img", "libvirt daemon"}}
		stderr, err := runBuildAndImport(t, fb, ft, false, qemuTarget, "quay.io/fedora/fedora-bootc:41", "ssh-ed25519 AAAAtest", "20G", true)
		if err == nil || !strings.Contains(err.Error(), "podman not found") {
			t.Fatalf("err = %v, want podman error", err)
		}
		for _, want := range []string{"the qemu target also needs", "qemu-img", "libvirt daemon"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q:\n%s", want, stderr)
			}
		}
		if !fb.sudoSeen {
			t.Error("sudo flag not forwarded to builder constructor")
		}
		if ft.imports != 0 {
			t.Errorf("imports = %d, want 0 (must stop before build)", ft.imports)
		}
	})

	t.Run("build failure", func(t *testing.T) {
		fb := &fakeBuilder{buildErr: errors.New("bootc install failed")}
		ft := &fakeTarget{}
		_, err := runBuildAndImport(t, fb, ft, false, qemuTarget, "quay.io/fedora/fedora-bootc:41", "", "20G", false)
		if err == nil || !strings.Contains(err.Error(), "bootc install failed") {
			t.Fatalf("err = %v, want build error", err)
		}
		if ft.imports != 0 {
			t.Errorf("imports = %d, want 0 (must stop before import)", ft.imports)
		}
		if fb.reqSeen.SSHKey != "" {
			t.Errorf("build SSHKey = %q, want empty (no ~/.ssh keys under temp HOME)", fb.reqSeen.SSHKey)
		}
	})

	t.Run("import failure still cleans up the built disk", func(t *testing.T) {
		built := filepath.Join(t.TempDir(), "myvm.raw")
		if err := os.WriteFile(built, []byte("disk"), 0o600); err != nil {
			t.Fatal(err)
		}
		fb := &fakeBuilder{result: bootc.BuildResult{Path: built}}
		ft := &fakeTarget{importErr: errors.New("virsh define failed")}
		_, err := runBuildAndImport(t, fb, ft, false, qemuTarget, "quay.io/fedora/fedora-bootc:41", "ssh-ed25519 AAAAtest", "20G", false)
		if err == nil || !strings.Contains(err.Error(), "virsh define failed") {
			t.Fatalf("err = %v, want import error", err)
		}
		if _, statErr := os.Stat(built); !os.IsNotExist(statErr) {
			t.Errorf("built disk %s not removed after import failure", built)
		}
	})

	t.Run("success imports with resolved ref and opts, then cleans up", func(t *testing.T) {
		built := filepath.Join(t.TempDir(), "myvm.raw")
		if err := os.WriteFile(built, []byte("disk"), 0o600); err != nil {
			t.Fatal(err)
		}
		fb := &fakeBuilder{result: bootc.BuildResult{Path: built, Format: "raw"}}
		ft := &fakeTarget{requires: []string{"qemu-img"}}
		// Empty disk exercises the 20G default.
		stderr, err := runBuildAndImport(t, fb, ft, false,
			config.ContextConfig{Name: "local", Backend: "qemu", Context: "myctx"},
			"quay.io/fedora/fedora-bootc:41", "ssh-ed25519 AAAAtest", "", false)
		if err != nil {
			t.Fatalf("buildAndImport: %v", err)
		}
		if ft.imports != 1 {
			t.Fatalf("imports = %d, want 1", ft.imports)
		}
		wantRef := types.InstanceRef{Backend: "qemu", Context: "myctx", Name: "myvm"}
		if ft.refSeen != wantRef {
			t.Errorf("ref = %+v, want %+v", ft.refSeen, wantRef)
		}
		if ft.diskSeen != built {
			t.Errorf("disk = %q, want built path %q", ft.diskSeen, built)
		}
		if ft.optsSeen.CPU != 2 || ft.optsSeen.Memory != "4G" || ft.optsSeen.Disk != "20G" || ft.optsSeen.SSHKey != "ssh-ed25519 AAAAtest" {
			t.Errorf("opts = %+v, want CPU=2 Memory=4G Disk=20G + ssh key", ft.optsSeen)
		}
		if !strings.HasSuffix(fb.reqSeen.Dest, "myvm.raw") || fb.reqSeen.Size != "20G" || fb.reqSeen.Image != "quay.io/fedora/fedora-bootc:41" {
			t.Errorf("build request = %+v, want Dest *.raw Size 20G resolved image", fb.reqSeen)
		}
		if _, statErr := os.Stat(built); !os.IsNotExist(statErr) {
			t.Errorf("built disk %s not removed after success", built)
		}
		if !strings.Contains(stderr, "created") {
			t.Errorf("stderr missing creation notice:\n%s", stderr)
		}
	})
}
