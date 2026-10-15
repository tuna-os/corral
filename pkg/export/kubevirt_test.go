package export

// Tests for the KubeVirt export adapter — the kubevirt export cell the parity
// matrix ships without a covering test. The adapter shells to real virtctl
// (vmexport download) and kubectl (primary-PVC lookup), so the doubles are
// stub binaries on PATH: kubectl answers the VM lookup, virtctl materialises
// the downloaded artifact. The sibling test/move-adapters branch covers the
// PVE adapter the same way; this file covers only KubeVirt.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/types"
)

// stubKubeTools puts recording kubectl/virtctl stubs on PATH. The kubectl
// stub answers `get vm` with pvcJSON ("" for a container-disk VM); the
// virtctl stub materialises any --output= file. VIRTCTL_FAIL=1 makes vmexport
// fail. Returns the file kubectl/virtctl argv lands in.
func stubKubeTools(t *testing.T, pvcJSON string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	kubectl := "#!/bin/sh\n" +
		"echo \"kubectl $@\" >> \"$ARGV_LOG\"\n" +
		"case \"$*\" in *\"get vm\"*) printf '%s' \"$PVC_JSON\";; esac\n" +
		"exit 0\n"
	virtctl := "#!/bin/sh\n" +
		"echo \"virtctl $@\" >> \"$ARGV_LOG\"\n" +
		"if [ -n \"$VIRTCTL_FAIL\" ]; then echo vmexport boom >&2; exit 1; fi\n" +
		"for a in \"$@\"; do case \"$a\" in --output=*) printf 'fake-disk-bytes' > \"${a#--output=}\";; esac; done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(kubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "virtctl"), []byte(virtctl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGV_LOG", log)
	t.Setenv("PVC_JSON", pvcJSON)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

const pvcVM = `{"spec":{"template":{"spec":{"volumes":[{"name":"root","persistentVolumeClaim":{"claimName":"web-1-root"}}]}}}}`

func kubevirtRef() types.InstanceRef {
	return types.InstanceRef{Backend: "kubevirt", Namespace: "default", Name: "web-1"}
}

func TestKubeVirt_ExportsRawGzFromThePrimaryPVC(t *testing.T) {
	log := stubKubeTools(t, pvcVM)
	dest := filepath.Join(t.TempDir(), "web-1.img.gz")

	res, err := (KubeVirt{}).Export(context.Background(),
		Request{Ref: kubevirtRef(), Dest: dest, Format: RawGz}, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Format != RawGz || res.Path != dest {
		t.Errorf("result = %+v, want raw.gz at dest", res)
	}
	if res.Bytes <= 0 {
		t.Errorf("bytes = %d, want the artifact's real size", res.Bytes)
	}
	// KubeVirt refuses to export a running VMI's disk, so whatever came out
	// was taken from a stopped instance.
	if res.Consistency != snapshot.Offline {
		t.Errorf("consistency = %v, want Offline", res.Consistency)
	}
	raw, _ := os.ReadFile(log)
	argv := string(raw)
	// The volume defaults to the primary PVC; the download is gzip (raw.gz
	// ships it, qcow2 converts from a raw download instead).
	for _, want := range []string{"--vm=web-1", "--volume=web-1-root", "--format=gzip", "--port-forward"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
}

func TestKubeVirt_RefusesAnEphemeralVM(t *testing.T) {
	stubKubeTools(t, `{"spec":{"template":{"spec":{"volumes":[{"name":"root","containerDisk":{"image":"img"}}]}}}}`)
	dest := filepath.Join(t.TempDir(), "web-1.img.gz")

	_, err := (KubeVirt{}).Export(context.Background(),
		Request{Ref: kubevirtRef(), Dest: dest, Format: RawGz}, nil)
	if err == nil {
		t.Fatal("expected a refusal: a container-disk VM has no PVC to export")
	}
	if !strings.Contains(err.Error(), "no persistent disk") {
		t.Errorf("refusal %q should say there is no persistent disk", err)
	}
}

func TestKubeVirt_VmexportFailureSurfaces(t *testing.T) {
	stubKubeTools(t, pvcVM)
	t.Setenv("VIRTCTL_FAIL", "1")
	dest := filepath.Join(t.TempDir(), "web-1.img.gz")

	_, err := (KubeVirt{}).Export(context.Background(),
		Request{Ref: kubevirtRef(), Dest: dest, Format: RawGz}, nil)
	if err == nil {
		t.Fatal("expected the vmexport failure")
	}
	if !strings.Contains(err.Error(), "vmexport download") {
		t.Errorf("error %q should name the vmexport download", err)
	}
}

func TestKubeVirt_RejectsAnUnofferedFormat(t *testing.T) {
	_, err := (KubeVirt{}).Export(context.Background(),
		Request{Ref: kubevirtRef(), Dest: filepath.Join(t.TempDir(), "x"), Format: IncusTar}, nil)
	if err == nil {
		t.Fatal("expected a refusal for a format kubevirt cannot produce")
	}
}
