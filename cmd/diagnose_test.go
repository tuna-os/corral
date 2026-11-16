package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tuna-os/corral/pkg/qemu"
)

// The frame diagnose inspects must not get a fixed name in the shared temp
// directory, where another local user can plant a symlink under that name.
func TestDiagScreenshotPath_StaysInVMState(t *testing.T) {
	got := diagScreenshotPath("web")
	if want := filepath.Join(qemu.VMHome(), "web"); filepath.Dir(got) != want {
		t.Errorf("diagnose screenshot = %s, want a file in %s", got, want)
	}
	if filepath.Dir(got) == filepath.Clean(os.TempDir()) {
		t.Errorf("diagnose screenshot %s is in the shared temp directory", got)
	}
}
