package cmd

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/export"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/virtualbox"
)

// pause, unpause, and scale reach the VM's own backend adapter, so a
// VirtualBox VM gets VBoxManage, not a KubeVirt-only refusal.
func TestOpsReachTheVirtualBoxAdapter(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() error
		want string
	}{
		{"pause", func() error { return pauseCmd.RunE(pauseCmd, []string{"web"}) }, "VBoxManage controlvm web pause"},
		{"unpause", func() error { return unpauseCmd.RunE(unpauseCmd, []string{"web"}) }, "VBoxManage controlvm web resume"},
		{"scale", func() error {
			scaleCPU, scaleMem = 2, ""
			t.Cleanup(func() { scaleCPU, scaleMem = 0, "" })
			return scaleCmd.RunE(scaleCmd, []string{"web"})
		}, "VBoxManage modifyvm web --cpus 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := remoteVMScratch(t, "web", types.RegistryEntry{Backend: "virtualbox"})
			fake.AddPrefixResponse("VBoxManage", "VMState=\"poweroff\"\n", nil)
			virtualbox.SetRunner(fake)
			t.Cleanup(func() { virtualbox.SetRunner(shell.Real{}) })
			if err := tc.run(); err != nil {
				t.Fatalf("%s on a VirtualBox VM: %v", tc.name, err)
			}
			if !ranCommandLine(fake, tc.want) {
				t.Errorf("%s did not run %q; calls were %v", tc.name, tc.want, fake.Calls())
			}
		})
	}
}

// A backend without the family is refused by name.
func TestOpsRefuseABackendWithoutTheFamily(t *testing.T) {
	remoteVMScratch(t, "web", types.RegistryEntry{Backend: "virtualbox"})
	for action, run := range map[string]func() error{
		"migrate": func() error { return migrateCmd.RunE(migrateCmd, []string{"web"}) },
		"tags":    func() error { return tagCmd.RunE(tagCmd, []string{"web", "prod"}) },
	} {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "virtualbox backend does not support "+action) {
			t.Errorf("%s on a VirtualBox VM = %v, want a refusal naming virtualbox", action, err)
		}
	}
}

// export goes through pkg/export: a VirtualBox VM is refused while it runs,
// by the VirtualBox adapter rather than a KubeVirt-only check.
func TestExportReachesTheBackendAdapter(t *testing.T) {
	fake := remoteVMScratch(t, "web", types.RegistryEntry{Backend: "virtualbox"})
	fake.AddPrefixResponse("VBoxManage showvminfo", "VMState=\"running\"\n", nil)
	export.SetRunner(fake)
	t.Cleanup(func() { export.SetRunner(shell.Real{}) })
	err := exportCmd.RunE(exportCmd, []string{"web"})
	if err == nil || !strings.Contains(err.Error(), "corral stop web") {
		t.Fatalf("export of a running VirtualBox VM = %v, want the adapter's refusal", err)
	}
}

// --volume names a KubeVirt PVC, so it stays KubeVirt-only.
func TestExportVolumeIsKubeVirtOnly(t *testing.T) {
	remoteVMScratch(t, "web", types.RegistryEntry{Backend: "virtualbox"})
	exportVolume = "disk"
	t.Cleanup(func() { exportVolume = "" })
	if err := exportCmd.RunE(exportCmd, []string{"web"}); err == nil || !strings.Contains(err.Error(), "only supported for KubeVirt") {
		t.Fatalf("export --volume on VirtualBox = %v", err)
	}
}
