package create

import (
	"fmt"

	"github.com/tuna-os/corral/pkg/kubevirt"
)

// localBootcScript is the `sh -c` script the local bootc build runs inside
// the image. Each karg goes to `bootc install to-disk --karg`, single-quoted.
func localBootcScript(disk, provisionScript string, kargs []string) string {
	var provisionArg string
	if provisionScript != "" {
		provisionArg = `&& cat /output/provision.sh | chroot "$MNT" /bin/bash`
	}
	return fmt.Sprintf(`set -e
DISK=%s
if ls /usr/lib/bootupd/updates/EFI/*/grub*.efi >/dev/null 2>&1 \
  || { [ -f /usr/lib/bootupd/updates/EFI.json ] \
    && find /usr/lib/efi/grub2 -type f -name 'grub*.efi' -print -quit 2>/dev/null | grep -q . \
    && find /usr/lib/efi/shim -type f -name 'shim*.efi' -print -quit 2>/dev/null | grep -q .; }; then
  COMPOSEFS=0; FS=xfs; BACKEND=--generic-image
elif [ -f /usr/lib/systemd/boot/efi/systemd-bootx64.efi ] || [ -f /usr/lib/systemd/boot/efi/systemd-bootaa64.efi ]; then
  COMPOSEFS=1; FS=btrfs; BACKEND=--composefs-backend
  if command -v mkfs.ext4 >/dev/null 2>&1; then FS=ext4; fi
else
  COMPOSEFS=0; FS=xfs; BACKEND=--generic-image
fi
echo "corral: install backend composefs=$COMPOSEFS fs=$FS"
bootc install to-disk $BACKEND --filesystem "$FS" --wipe%s --root-ssh-authorized-keys /output/id_rsa.pub "$DISK"
udevadm settle
if [ "$COMPOSEFS" = 1 ]; then
  MNT_ESP=$(mktemp -d)
  for P in 1 2 3; do
    if mount "${DISK}p${P}" "$MNT_ESP" 2>/dev/null; then
      if [ -d "$MNT_ESP/EFI" ]; then break; fi
      umount "$MNT_ESP"
    fi
  done
  KVER=$(ls /usr/lib/modules | head -1)
  D=$(ls -d "$MNT_ESP"/EFI/Linux/bootc_composefs-* 2>/dev/null | head -1)
  if [ -n "$D" ]; then
    cp -f "/usr/lib/modules/$KVER/vmlinuz" "$D/vmlinuz"
    cp -f "/usr/lib/modules/$KVER/initramfs.img" "$D/initrd"
  fi
  sync; umount "$MNT_ESP" 2>/dev/null || true
  rm -rf "$MNT_ESP"
  MNT_ROOT=$(mktemp -d)
  mount "${DISK}p3" "$MNT_ROOT"
  SSHDIR="$MNT_ROOT/state/os/default/var/roothome/.ssh"
  mkdir -p "$SSHDIR"; chmod 700 "$SSHDIR"
  cp /output/id_rsa.pub "$SSHDIR/authorized_keys"; chmod 600 "$SSHDIR/authorized_keys"; chown -R 0:0 "$SSHDIR"
  sync; umount "$MNT_ROOT"
  rm -rf "$MNT_ROOT"
fi
udevadm settle
MNT=$(mktemp -d)
mount "${DISK}p3" "$MNT" %s && umount "$MNT"
rm -rf "$MNT"`, disk, kubevirt.BootcKargFlags(kargs), provisionArg)
}
