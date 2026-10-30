package bootc

// InstalledRootRunner runs a provisioning script against a freshly installed
// bootc disk the way the booted system will see it. It is POSIX sh and must
// run inside the bootc image's own container, because it reuses that
// container's /usr as the deployment's /usr.
//
// Usage: sh runner <physical-root> <script>
//
// <physical-root> is the mounted root partition `bootc install` wrote. That
// partition is not a usable chroot: an ostree install keeps the deployment
// under ostree/deploy/<stateroot>/deploy/<checksum>.<serial>, and a composefs
// install keeps only an EROFS image plus state/deploy/<id>/etc and
// state/os/default/var. Account changes must land in the deployment's
// mutable /etc and the stateroot's /var (not the image's /usr), or they vanish
// on the first boot. The runner therefore assembles a booted-system view on a
// tmpfs — image /usr, deployment /etc, stateroot /var, the image's top-level
// symlinks (home -> var/home, bin -> usr/bin, ...) — and chroots the script
// into it. Afterwards it restores SELinux labels on /etc and /var from the
// image's policy: tools such as useradd replace /etc/shadow by rename, and a
// file the build left unlabeled breaks logins on an enforcing guest.
const InstalledRootRunner = `set -eu
P=$1
SCRIPT=$2
ETC=
VAR=
if [ -d "$P/state/os/default/var" ]; then
  VAR=$P/state/os/default/var
  for d in "$P"/state/deploy/*/etc; do
    if [ -d "$d" ]; then ETC=$d; break; fi
  done
else
  for d in "$P"/ostree/deploy/*/deploy/*; do
    if [ -d "$d/etc" ]; then
      ETC=$d/etc
      VAR=${d%/deploy/*}/var
      break
    fi
  done
fi
if [ -z "$ETC" ] || [ ! -d "$VAR" ]; then
  echo "corral: no bootc deployment found under $P" >&2
  exit 1
fi
R=$(mktemp -d /tmp/corral-root.XXXXXX)
cleanup() {
  umount -R "$R" 2>/dev/null || umount -l "$R" 2>/dev/null || true
  rmdir "$R" 2>/dev/null || true
}
trap cleanup EXIT
mount -t tmpfs -o mode=0755 corral-root "$R"
for e in /*; do
  [ -L "$e" ] && cp -P "$e" "$R/"
done
mkdir -p "$R/usr" "$R/etc" "$R/var" "$R/dev" "$R/proc" "$R/run" "$R/tmp"
mount --rbind /usr "$R/usr"
mount --bind "$ETC" "$R/etc"
mount --bind "$VAR" "$R/var"
mount --rbind /dev "$R/dev"
mount -t proc proc "$R/proc"
mount -t tmpfs -o mode=0755 tmpfs "$R/run"
mount -t tmpfs -o mode=1777 tmpfs "$R/tmp"
cp "$SCRIPT" "$R/tmp/corral-provision.sh"
chmod 0700 "$R/tmp/corral-provision.sh"
if [ -x "$R/usr/bin/bash" ]; then SH=/usr/bin/bash; else SH=/bin/sh; fi
chroot "$R" "$SH" /tmp/corral-provision.sh
chroot "$R" /bin/sh -c '
  T=$(sed -n "s/^SELINUXTYPE=//p" /etc/selinux/config 2>/dev/null || true)
  FC=/etc/selinux/$T/contexts/files/file_contexts
  if [ -n "$T" ] && [ -f "$FC" ] && command -v setfiles >/dev/null 2>&1; then
    setfiles "$FC" /etc /var
  fi
'
`
