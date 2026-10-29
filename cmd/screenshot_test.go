package cmd

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

// writeFramePNG stands in for the PNG virtctl writes: the fake runner records
// the call but writes nothing, so the test puts the frame there itself.
func writeFramePNG(t *testing.T, path string, painted bool) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			if painted && (x/8+y/8)%2 == 0 {
				img.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func screenshotFlags(t *testing.T, out string, paint bool) {
	t.Helper()
	screenshotOutput, screenshotPaint = out, paint
	t.Cleanup(func() { screenshotOutput, screenshotPaint = "", false })
}

// A KubeVirt VM is captured through virtctl's VNC screenshot subresource and
// judged by the same frame analysis as a QEMU capture.
func TestScreenshotKubeVirtUsesVNCScreenshot(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "corral-vms"})
	out := filepath.Join(t.TempDir(), "web.png")
	writeFramePNG(t, out, true)
	screenshotFlags(t, out, true)

	stdout := captureStdout(t, func() {
		if err := screenshotCmd.RunE(screenshotCmd, []string{"web"}); err != nil {
			t.Fatalf("screenshot of a KubeVirt VM: %v", err)
		}
	})
	want := "virtctl vnc screenshot web -n corral-vms -f " + out
	if !ranCommandLine(fake, want) {
		t.Errorf("screenshot did not run %q; calls were %v", want, fake.Calls())
	}
	if !strings.Contains(stdout, "64x48") || !strings.Contains(stdout, "painted") {
		t.Errorf("report = %q, want the frame size and a painted verdict", stdout)
	}
}

// --require-paint fails a blank KubeVirt frame exactly as it fails a QEMU one.
func TestScreenshotKubeVirtRequirePaintFailsBlank(t *testing.T) {
	kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "corral-vms"})
	out := filepath.Join(t.TempDir(), "web.png")
	writeFramePNG(t, out, false)
	screenshotFlags(t, out, true)

	var err error
	captureStdout(t, func() { err = screenshotCmd.RunE(screenshotCmd, []string{"web"}) })
	if err == nil || !strings.Contains(err.Error(), "painted nothing") {
		t.Fatalf("blank KubeVirt frame with --require-paint = %v, want a painted-nothing failure", err)
	}
}

// Backends with neither a QMP monitor nor a VNC screenshot API are refused.
func TestScreenshotRefusesOtherBackends(t *testing.T) {
	remoteVMScratch(t, "web", types.RegistryEntry{Backend: "virtualbox"})
	screenshotFlags(t, filepath.Join(t.TempDir(), "web.png"), false)
	err := screenshotCmd.RunE(screenshotCmd, []string{"web"})
	if err == nil || !strings.Contains(err.Error(), "only supported for QEMU and KubeVirt") {
		t.Fatalf("screenshot of a VirtualBox VM = %v", err)
	}
}
