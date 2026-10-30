//go:build bootc

package kubevirt

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/bootc"
	yaml "gopkg.in/yaml.v3"
)

// The builder VM must render as a valid VirtualMachine that attaches the target
// disk as a block device and whose cloud-init carries the composefs-aware
// install recipe (backend detection, --filesystem, the EROFS initrd fixup, the
// manual composefs key injection, and the console/sshd kargs).
func TestRenderBuilderVM(t *testing.T) {
	vm := generateBuilderVM("e2e-x-bootc-builder", "corral-vms",
		"e2e-x-bootc-disk", "e2e-x-bootc-builder-cloudinit", "quay.io/centos-bootc/centos-bootc:stream9", nil, "")

	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("builder VM not marshalable: %v", err)
	}
	js := string(data)

	if vm["kind"] != "VirtualMachine" {
		t.Errorf("builder must be a VirtualMachine, got %v", vm["kind"])
	}
	// The target disk is attached by serial so the guest sees virtio-target.
	if !strings.Contains(js, `"serial":"target"`) {
		t.Errorf("builder VM missing target disk serial")
	}
	// cloud-init comes from a Secret (script exceeds the 2 KB inline limit).
	if !strings.Contains(js, `"secretRef"`) || !strings.Contains(js, "e2e-x-bootc-builder-cloudinit") {
		t.Errorf("builder VM must reference the cloud-init secret")
	}

	// The recipe lives in the Secret's userdata.
	sec := generateBuilderSecret("e2e-x-bootc-builder-cloudinit", "corral-vms",
		"ghcr.io/projectbluefin/dakota:testing", "ssh-ed25519 AAAATESTKEY user@host", "", nil)
	script := sec["stringData"].(map[string]any)["userdata"].(string)
	for _, want := range []string{
		"ghcr.io/projectbluefin/dakota:testing", // image substituted
		"ssh-ed25519 AAAATESTKEY user@host",     // key substituted
		"bootc install to-disk",                 // VM-builder path
		"CORRAL_BACKEND=",                       // backend detection result
		"bootupctl",                             // ostree signal (has bootupd)
		"systemd-bootx64.efi",                   // composefs signal (systemd-boot, no bootupd)
		"--composefs-backend",                   // composefs branch
		"--generic-image",                       // ostree branch (removable bootloader)
		"--filesystem",                          // fs selection (btrfs vs xfs)
		"bootc_composefs-",                      // composefs initrd fixup target
		"state/os/default/var/roothome/.ssh",    // composefs manual key injection
		"systemd.wants=sshd.service",            // sshd karg
		"systemd.wants=ssh.service",             // Debian/Ubuntu sshd unit name
		"CORRAL_BUILD_OK",                       // success marker
	} {
		if !strings.Contains(script, want) {
			t.Errorf("builder cloud-init missing %q", want)
		}
	}
}

// Each --karg reaches the builder's `bootc install to-disk` as its own
// single-quoted word, so it is in the boot entry from the first boot. The
// cloud-config stays valid YAML with a quote in a value.
func TestBuilderScript_Kargs(t *testing.T) {
	t.Setenv("CORRAL_REGISTRY_MIRROR", "off")
	script := builderScript("ghcr.io/tuna-os/yellowfin:gnome", "ssh-ed25519 AAAA", "",
		[]string{"tunaos.countme=0", "console=ttyS0,115200n8", "it's=1"})
	want := `bootc install to-disk $BACKEND --filesystem "$FS" --wipe --karg 'tunaos.countme=0' --karg 'console=ttyS0,115200n8' --karg 'it'\''s=1' \`
	if !strings.Contains(script, want) {
		t.Errorf("builder script missing %q:\n%s", want, script)
	}
	if strings.Contains(script, "__KARGS__") {
		t.Error("builder script still holds the __KARGS__ placeholder")
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(script), &doc); err != nil {
		t.Fatalf("builder cloud-config with kargs is not valid YAML: %v", err)
	}

	plain := builderScript("ghcr.io/tuna-os/yellowfin:gnome", "ssh-ed25519 AAAA", "", nil)
	if !strings.Contains(plain, `--filesystem "$FS" --wipe \`) || strings.Contains(plain, "--karg") || strings.Contains(plain, "__KARGS__") {
		t.Errorf("builder script without kargs must keep the plain install line:\n%s", plain)
	}
}

// The builder VM records the kargs it installs, which is what a later
// `--resume` checks. A build with no kargs records nothing.
func TestRenderBuilderVM_RecordsKargs(t *testing.T) {
	vm := generateBuilderVM("e2e-x-bootc-builder", "corral-vms", "e2e-x-bootc-disk",
		"e2e-x-bootc-builder-cloudinit", "quay.io/centos-bootc/centos-bootc:stream9",
		[]string{"tunaos.countme=0", "console=ttyS0,115200n8"}, "")
	ann := vm["metadata"].(map[string]any)["annotations"].(map[string]any)
	if got := ann["corral.bootc/kargs"]; got != "tunaos.countme=0 console=ttyS0,115200n8" {
		t.Errorf("corral.bootc/kargs = %v, want the space-joined kargs", got)
	}
	if ann["corral.bootc/image"] != "quay.io/centos-bootc/centos-bootc:stream9" {
		t.Errorf("corral.bootc/image = %v", ann["corral.bootc/image"])
	}

	none := generateBuilderVM("e2e-x-bootc-builder", "corral-vms", "e2e-x-bootc-disk",
		"e2e-x-bootc-builder-cloudinit", "quay.io/centos-bootc/centos-bootc:stream9", nil, "")
	if _, ok := none["metadata"].(map[string]any)["annotations"].(map[string]any)["corral.bootc/kargs"]; ok {
		t.Error("a build with no kargs must not record a corral.bootc/kargs annotation")
	}
}

// The final bootc VM must UEFI-boot the block disk PVC — no kernelBoot.
func TestRenderBootcFinalVM(t *testing.T) {
	vm := generateBootcVM("dak", "corral-vms", "dak-bootc-disk",
		"ghcr.io/projectbluefin/dakota:testing", "ssh-ed25519 AAAAREBUILDKEY u@h", "4G", 2, "")
	data, _ := json.Marshal(vm)
	js := string(data)

	if strings.Contains(js, "kernelBoot") {
		t.Errorf("final VM must not use kernelBoot (disk is self-bootable)")
	}
	if !strings.Contains(js, `"efi"`) {
		t.Errorf("final VM must boot via UEFI firmware")
	}
	if !strings.Contains(js, "dak-bootc-disk") {
		t.Errorf("final VM must boot the disk PVC")
	}
	// The SSH key is recorded on the VM so rebuild/upgrade/switch can re-bake it.
	if !strings.Contains(js, "corral.bootc/ssh-key") || !strings.Contains(js, "AAAAREBUILDKEY") {
		t.Errorf("final VM must record the SSH key annotation for rebuilds")
	}
}

// The builder cloud-init must stay valid YAML no matter what the provision
// script contains — embedding it verbatim once put script lines at column 0,
// which silently terminated the `content: |` block and cloud-init dropped the
// ENTIRE config ("empty cloud config"), so the build never started.
func TestBuilderSecretUserdataIsValidYAML(t *testing.T) {
	provision := "#!/bin/bash\nsystemctl enable sshd\necho 'EOF'\nkey: looks-like-yaml\n"
	secret := generateBuilderSecret("vm1", "ns1", "ghcr.io/tuna-os/yellowfin:gnome", "ssh-ed25519 AAAA test", provision, nil)
	userdata, _ := secret["stringData"].(map[string]any)["userdata"].(string)
	if userdata == "" {
		t.Fatal("empty userdata")
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(userdata), &doc); err != nil {
		t.Fatalf("builder cloud-init is not valid YAML: %v", err)
	}
	if _, ok := doc["write_files"]; !ok {
		t.Fatal("write_files missing from parsed cloud-config")
	}
	if _, ok := doc["runcmd"]; !ok {
		t.Fatal("runcmd missing from parsed cloud-config (block literal likely swallowed it)")
	}
	// The provision payload must round-trip through its base64 write_files entry.
	if !strings.Contains(userdata, base64.StdEncoding.EncodeToString([]byte(provision))) {
		t.Fatal("provision script not embedded base64-encoded")
	}
}

func TestRenderBuilderVM_NodePinned(t *testing.T) {
	vm := generateBuilderVM("e2e-x-bootc-builder", "corral-vms",
		"e2e-x-bootc-disk", "e2e-x-bootc-builder-cloudinit",
		"quay.io/centos-bootc/centos-bootc:stream9", nil, "karnataka")
	spec := vm["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	sel, ok := spec["nodeSelector"].(map[string]any)
	if !ok || sel["kubernetes.io/hostname"] != "karnataka" {
		t.Fatalf("expected builder pinned to karnataka, got nodeSelector=%v", spec["nodeSelector"])
	}
}

func TestRenderBuilderVM_NoNodeMeansNoSelector(t *testing.T) {
	vm := generateBuilderVM("e2e-x-bootc-builder", "corral-vms",
		"e2e-x-bootc-disk", "e2e-x-bootc-builder-cloudinit",
		"quay.io/centos-bootc/centos-bootc:stream9", nil, "")
	spec := vm["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if _, ok := spec["nodeSelector"]; ok {
		t.Fatalf("expected no nodeSelector when node is empty, got %v", spec["nodeSelector"])
	}
}

func TestBootcVMPreservesGiBMemory(t *testing.T) {
	vm := generateBootcVM("roost", "corral-vms", "disk", "image", "key", "6Gi", 4, "node")
	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"guest":"6144Mi"`) {
		t.Fatalf("6Gi did not become 6GiB of guest memory: %s", data)
	}
}

// A --user build carries the account script and the installed-root runner as
// base64 write_files entries. The builder applies them in the image's own
// container against the mounted root partition, never by chrooting into the
// raw partition (an ostree or composefs root has no shell to chroot into).
// The password hash appears only inside the encoded script.
func TestBuilderScript_AccountProvisioning(t *testing.T) {
	t.Setenv("CORRAL_REGISTRY_MIRROR", "off")
	const hash = "$6$saltsalt$abcdefghijklmnopqrstuvwxyz"
	account, err := bootc.AccountProvisionScript("tuna-test", hash)
	if err != nil {
		t.Fatal(err)
	}
	script := builderScript("ghcr.io/tuna-os/marlin:tuna", "ssh-ed25519 AAAA", account, nil)

	var doc struct {
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(script), &doc); err != nil {
		t.Fatalf("builder cloud-config with an account is not valid YAML: %v", err)
	}
	files := map[string]string{}
	for _, f := range doc.WriteFiles {
		files[f.Path] = f.Content
	}
	decode := func(path string) string {
		t.Helper()
		raw, err := base64.StdEncoding.DecodeString(files[path])
		if err != nil {
			t.Fatalf("%s is not base64: %v", path, err)
		}
		return string(raw)
	}
	if got := decode("/root/provision.b64"); got != account {
		t.Errorf("provision.b64 does not round-trip the account script:\n%s", got)
	}
	if got := decode("/root/runner.b64"); got != bootc.InstalledRootRunner {
		t.Errorf("runner.b64 does not carry bootc.InstalledRootRunner")
	}
	build := files["/root/build.sh"]
	for _, want := range []string{
		`if [ -s /root/provision.b64 ]; then`,
		`mount /dev/disk/by-id/virtio-target-part3 /mnt/root ||`,
		`-v /mnt/root:/corral-sysroot -v /root/corral-provision:/corral-provision:ro`,
		`"$IMG" sh /corral-provision/runner.sh /corral-sysroot /corral-provision/provision.sh`,
		"CORRAL_BUILD_FAIL provision",
	} {
		if !strings.Contains(build, want) {
			t.Errorf("build.sh missing %q", want)
		}
	}
	if strings.Contains(build, "chroot /mnt/root") {
		t.Error("build.sh must not chroot into the raw root partition")
	}
	if strings.Contains(script, hash) {
		t.Error("the password hash must travel only inside the base64 account script")
	}
}
