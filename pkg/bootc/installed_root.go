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
//
// setfiles validates every file_contexts entry before it labels anything.
// Without -c it validates against the policy the running kernel loaded, which
// is the builder's (Fedora 42 on KubeVirt), not the image's: an image whose
// policy is newer (Fedora 43+, ELN, rawhide) labels types the builder's policy
// lacks, and setfiles exits 255 on "invalid context". -c validates against
// the image's own binary policy instead.
//
// Validation passing is not enough: setfiles then asks the builder kernel to
// set each context, and the kernel refuses a type its loaded policy does not
// know with EINVAL ("Could not set context for /etc/kmscon:  Invalid
// argument"), labels everything else, and exits nonzero. That is expected:
// such a type is unmapped on the builder, as it already was for `bootc
// install`'s own labeling ("Context ...:kmscon_conf_t:s0 is not valid (left
// unmapped)" on the same builder), and only the guest's own policy can map
// it. The file keeps the label it had, and everything the builder policy does
// know (passwd, shadow, group, home directories) is still relabeled. The
// runner therefore accepts a nonzero setfiles whose entire stderr is such
// EINVAL lines (setfilesTolerant), and fails on anything else: another errno,
// an "invalid context", or a nonzero exit with no diagnosis.
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
chroot "$R" /bin/sh -c '` + setfilesTolerant + `
  T=$(sed -n "s/^SELINUXTYPE=//p" /etc/selinux/config 2>/dev/null || true)
  FC=/etc/selinux/$T/contexts/files/file_contexts
  if [ -n "$T" ] && [ -f "$FC" ] && command -v setfiles >/dev/null 2>&1; then
    POLICY=
    for f in /etc/selinux/$T/policy/policy.*; do
      if [ -f "$f" ]; then POLICY=$f; fi
    done
    if [ -n "$POLICY" ]; then
      corral_setfiles -c "$POLICY" "$FC" /etc /var
    else
      corral_setfiles "$FC" /etc /var
    fi
  fi
'
`

// setfilesTolerant defines corral_setfiles, which runs the setfiles command
// it is given and treats one failure as success: a nonzero exit whose whole
// stderr is "Could not set context for <path>:  Invalid argument" lines (the
// builder kernel does not know the image's type). stderr is always passed
// through. It is POSIX sh and must not contain a single quote, because the
// runner embeds it in a single-quoted chroot script.
const setfilesTolerant = `
  corral_setfiles() {
    E=$(mktemp "${TMPDIR:-/tmp}/setfiles.err.XXXXXX") || return 1
    RC=0
    "$@" 2>"$E" || RC=$?
    cat "$E" >&2
    if [ "$RC" -ne 0 ] && [ -s "$E" ]; then
      OTHER=$(grep -vc "^setfiles: Could not set context for .*:  *Invalid argument$" "$E" || true)
      if [ "$OTHER" = 0 ]; then
        echo "corral: setfiles could not set the contexts above on the builder kernel; they keep the label bootc install gave them" >&2
        RC=0
      fi
    fi
    rm -f "$E"
    return "$RC"
  }
`
