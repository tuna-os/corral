package create

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

func (r request) runLocalBootcCreate(name string) error {
	vmDir := filepath.Join(qemu.VMHome(), name)
	if qemu.Exists(name) && !r.Force {
		return fmt.Errorf("VM %q already exists. Use --force to overwrite", name)
	}
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("creating VM directory: %w", err)
	}

	diskSize := r.Disk
	if diskSize == "" {
		diskSize = "20G"
	}
	mem := r.Mem
	if mem == "" {
		mem = "4G"
	}
	cpu := r.CPU
	if cpu == 0 {
		cpu = 2
	}

	_ = qemu.RecordCreating(name, types.CreateOpts{
		Name: name,
		CPU:  cpu,
		Mem:  mem,
		Disk: diskSize,
	})

	var created bool
	defer func() {
		if !created {
			_ = os.RemoveAll(vmDir)
		}
	}()

	diskPath := filepath.Join(vmDir, "disk.raw")
	out, err := exec.Command("truncate", "-s", diskSize, diskPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("creating raw disk: %s: %w", string(out), err)
	}

	out, err = exec.Command("sudo", "losetup", "-fP", diskPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup failed: %s: %w", string(out), err)
	}

	out, err = exec.Command("sudo", "losetup", "-a").CombinedOutput()
	if err != nil {
		return fmt.Errorf("listing loop devices: %s: %w", string(out), err)
	}
	var loopDev string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, filepath.Base(diskPath)) {
			loopDev = strings.Split(line, ":")[0]
			break
		}
	}
	if loopDev == "" {
		return fmt.Errorf("could not find loop device for %s", diskPath)
	}
	defer func() { _ = exec.Command("sudo", "losetup", "-d", loopDev).Run() }()

	if r.ProvisionScript != "" {
		provFile := filepath.Join(vmDir, "provision.sh")
		if err := os.WriteFile(provFile, []byte(r.ProvisionScript), 0755); err != nil {
			return err
		}
		defer func() { _ = os.Remove(provFile) }()
	}

	sshKey := kubevirt.LoadSSHPublicKey()
	if sshKey == "" {
		return fmt.Errorf("no SSH public key found")
	}
	keyFile := filepath.Join(vmDir, "id_rsa.pub")
	if err := os.WriteFile(keyFile, []byte(sshKey), 0644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(keyFile) }()

	fmt.Printf("Building bootc image locally onto %s...\n", loopDev)
	// Backend-aware install (mirrors pkg/kubevirt/bootc.go's builder). ostree/
	// server images ship bootupctl and install to xfs with --generic-image (so
	// the disk boots the removable EFI path under plain QEMU). composefs images
	// (Universal Blue / TunaOS desktops: systemd-boot without GRUB payloads)
	// require fs-verity (ext4 when available, otherwise btrfs). Installing the
	// ostree/xfs way drops to dracut emergency mode at initrd-switch-root
	// (tuna-os sailfin desktop Gate). They also need the real kernel/initrd
	// re-extracted over bootc's EROFS-zeroed ESP copies, and the SSH key written
	// into the deployment (--root-ssh-authorized-keys is a no-op on composefs).
	cmd := exec.Command("sudo", "podman", "run", "--privileged", "--pid=host", "--security-opt", "label=disable",
		"-v", "/dev:/dev", "-v", vmDir+":/output:Z",
		r.Bootc, "sh", "-c",
		localBootcScript(loopDev, r.ProvisionScript, r.Kargs))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("local bootc build failed: %w", err)
	}

	qcowPath := filepath.Join(vmDir, "disk.qcow2")
	out, err = exec.Command("qemu-img", "convert", "-O", "qcow2", diskPath, qcowPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("converting to qcow2: %s: %w", string(out), err)
	}
	_ = os.Remove(diskPath)

	// ExistingDisk: the qcow2 we just built IS the boot disk — without it
	// qemu.Create would recreate disk.qcow2 empty and the VM would boot
	// into nothing.
	useUEFI := r.Firmware != "bios"
	vsock := r.Vsock || r.VsockCID != 0
	if r.VsockCID != 0 {
		vsock = true
	}
	if err := r.QEMUCreate(types.CreateOpts{
		Name:         name,
		Mem:          r.Mem,
		CPU:          r.CPU,
		Disk:         r.Disk,
		Force:        true,
		ExistingDisk: true,
		UEFI:         useUEFI,
		Vsock:        vsock,
		VsockCID:     r.VsockCID,
		TPM:          r.TPM,
	}); err != nil {
		return err
	}
	created = true
	if r.Registry != nil {
		_ = r.Registry.Set(name, types.RegistryEntry{Backend: "qemu"})
	}
	return nil
}
