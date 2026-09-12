package vmtest

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GehirnInc/crypt/sha512_crypt"
	"gopkg.in/yaml.v3"
)

// The derived image.
//
// A test usually needs something the published image does not have: an account
// to log in as, a password, a package, a script that runs on first boot. The
// honest way to get those into a bootc system is the way a user would — build
// an image FROM it. So everything the spec asks for becomes one thin layer on
// top of the reference under test, and the reference itself is never touched.
//
// The layer's content is assembled as a build context first (files on disk,
// nothing executed), which is what makes it testable: every decision here is a
// string in a directory, and the podman invocation at the end is two lines.
//
// Two engines can turn that context into an image:
//
//   - remora (tuna-os/remora) — local layering for bootc systems. It knows six
//     package managers, resolves a lockfile so an unchanged rebuild is free,
//     and lints the result with `bootc container lint`. Used when installed.
//   - builtin — a Containerfile written here, for the common case on a runner
//     that has podman and nothing else.
//
// Both consume the same context, because remora's extension points
// (build_files/*.sh, system_files/ overlay, packages, extra_run) are exactly
// the shape this needs.

// Layer engines.
const (
	EngineAuto    = "auto"
	EngineRemora  = "remora"
	EngineBuiltin = "builtin"
)

// PostBootUnit is the systemd unit the derived image runs once the guest is up.
const PostBootUnit = "corral-postboot.service"

// Markers the guest prints on its console. They are the readiness and verdict
// signals for an image SSH cannot answer for.
const (
	// ReadyMarker is printed after the post-boot hook finishes, whatever it
	// reported. It means "this guest reached multi-user and ran our unit".
	ReadyMarker = "CORRAL_VM_READY"
	// HookOKMarker and HookFailMarker report the hook's own verdict.
	HookOKMarker   = "CORRAL_POSTBOOT_OK"
	HookFailMarker = "CORRAL_POSTBOOT_FAIL"
)

// StatusFile is where the post-boot hook records its exit code inside the
// guest. Read over SSH, so a passing boot with a failing hook is still a
// failing run.
const StatusFile = "/var/lib/corral/postboot.status"

// HookLogFile is the hook's own output inside the guest.
const HookLogFile = "/var/log/corral-postboot.log"

// buildContext is everything the derived image is made of, before anything
// runs. Paths in SystemFiles are guest-absolute; BuildScripts run in order.
type buildContext struct {
	Dir      string
	Base     string
	Packages []string
	ExtraRun []string
	// SystemFiles is the overlay copied onto / in the image.
	SystemFiles map[string]contextFile
	// BuildScripts run at the end of the build, in name order.
	BuildScripts map[string]string
}

type contextFile struct {
	Content string
	Mode    os.FileMode
}

// newBuildContext turns a spec into the layer's content. Pure: it computes
// strings, and writes nothing.
//
// authorizedKey is the harness's own public key. It is added to every account
// the spec creates as well as root, because the probe and the checks log in
// with it and a password is no use to a non-interactive client.
func newBuildContext(spec *Spec, base, authorizedKey string) (*buildContext, error) {
	ctx := &buildContext{
		Base:         base,
		Packages:     spec.Packages,
		ExtraRun:     spec.ExtraRun,
		SystemFiles:  map[string]contextFile{},
		BuildScripts: map[string]string{},
	}

	for _, f := range spec.Files {
		mode := os.FileMode(0o644)
		if f.Mode != "" {
			var parsed uint32
			if _, err := fmt.Sscanf(f.Mode, "%o", &parsed); err != nil {
				return nil, fmt.Errorf("files: %q is not an octal mode like 0644", f.Mode)
			}
			mode = os.FileMode(parsed)
		}
		ctx.SystemFiles[f.Path] = contextFile{Content: f.Content, Mode: mode}
	}

	// The post-boot hook and its unit. Always present when anything is
	// layered: the unit is what prints the readiness marker, and a run that
	// asked for no hook scripts still wants to know the guest got that far.
	ctx.SystemFiles["/usr/libexec/corral-postboot"] = contextFile{
		Content: postBootScript(spec.bootScripts()),
		Mode:    0o755,
	}
	ctx.SystemFiles["/usr/lib/systemd/system/"+PostBootUnit] = contextFile{
		Content: postBootUnitFile(),
		Mode:    0o644,
	}

	accounts, err := accountScript(spec, authorizedKey)
	if err != nil {
		return nil, err
	}
	ctx.BuildScripts["10-corral-accounts.sh"] = accounts
	ctx.BuildScripts["20-corral-services.sh"] = serviceScript(spec)
	for i, script := range spec.imageScripts() {
		ctx.BuildScripts[fmt.Sprintf("50-corral-provision-%02d.sh", i+1)] = script
	}
	return ctx, nil
}

// write materialises the context in dir.
func (c *buildContext) write(dir string) error {
	c.Dir = dir
	if err := os.MkdirAll(filepath.Join(dir, "build_files"), 0o755); err != nil {
		return err
	}
	for guestPath, file := range c.SystemFiles {
		target := filepath.Join(dir, "system_files", filepath.FromSlash(strings.TrimPrefix(guestPath, "/")))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(file.Content), file.Mode); err != nil {
			return err
		}
		// WriteFile honours the umask, and a hook that is not executable stops
		// the whole run at first boot.
		if err := os.Chmod(target, file.Mode); err != nil {
			return err
		}
	}
	for name, script := range c.BuildScripts {
		target := filepath.Join(dir, "build_files", name)
		if err := os.WriteFile(target, []byte(shebang(script)), 0o755); err != nil {
			return err
		}
		if err := os.Chmod(target, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// scriptNames returns the build scripts in the order they run.
func (c *buildContext) scriptNames() []string {
	names := make([]string, 0, len(c.BuildScripts))
	for name := range c.BuildScripts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// shebang makes a script runnable on its own, since remora invokes each
// build_files entry directly.
func shebang(script string) string {
	script = strings.TrimLeft(script, "\n")
	if strings.HasPrefix(script, "#!") {
		return script
	}
	return "#!/usr/bin/env bash\nset -euo pipefail\n" + script
}

// ── the account script ────────────────────────────────────────────

// accountScript creates the spec's users, sets passwords, and installs
// authorized_keys.
//
// It is one shell script rather than a set of Containerfile RUN lines because
// the details differ per base image and the base image is the authority:
// useradd on most, adduser on a busybox base; wheel on rpm and arch, sudo on
// debian. Asking the image at build time is shorter than guessing from the
// registry.
//
// Passwords arrive already hashed. A plain password in a build argument or a
// copied file would sit in the image's history, and /etc/shadow is where a
// hash belongs anyway.
func accountScript(spec *Spec, authorizedKey string) (string, error) {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Generated by corral vmtest — test accounts for this run.\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString(accountHelpers)

	if key := strings.TrimSpace(authorizedKey); key != "" {
		b.WriteString(fmt.Sprintf("corral_authorize root /root %s\n", shellQuote(key)))
	}
	if spec.RootPassword != "" {
		hash, err := hashPassword(spec.RootPassword)
		if err != nil {
			return "", err
		}
		b.WriteString(fmt.Sprintf("corral_setpass root %s\n", shellQuote(hash)))
	}

	for _, u := range spec.Users {
		shell := u.Shell
		if shell == "" {
			shell = "/bin/bash"
		}
		b.WriteString(fmt.Sprintf("\ncorral_adduser %s %s %s\n",
			shellQuote(u.Name), shellQuote(shell), shellQuote(strings.Join(u.Groups, ","))))
		if u.Password != "" {
			hash, err := hashPassword(u.Password)
			if err != nil {
				return "", err
			}
			b.WriteString(fmt.Sprintf("corral_setpass %s %s\n", shellQuote(u.Name), shellQuote(hash)))
		}
		keys := append([]string{}, u.SSHAuthorizedKeys...)
		if key := strings.TrimSpace(authorizedKey); key != "" {
			keys = append(keys, key)
		}
		for _, key := range keys {
			if strings.TrimSpace(key) == "" {
				continue
			}
			b.WriteString(fmt.Sprintf("corral_authorize %s /var/home/%s %s\n",
				shellQuote(u.Name), u.Name, shellQuote(strings.TrimSpace(key))))
		}
		if u.Sudo {
			// corral_admin resolves the base image's own administrator group,
			// which is why it happens in the guest and not in this string.
			b.WriteString(fmt.Sprintf("corral_admin %s\n", shellQuote(u.Name)))
		}
	}
	return b.String(), nil
}

// accountHelpers is the shell the account script calls. Kept verbatim in one
// constant so it reads as the script it is.
//
// /var/home is deliberate: on a bootc system /home is a symlink into /var, and
// /var from the image is copied in once at install time. A home directory
// created here therefore reaches the installed system, and one created on first
// boot would not survive a rebuild.
const accountHelpers = `corral_admin_group() {
  if getent group wheel >/dev/null 2>&1; then echo wheel
  elif getent group sudo >/dev/null 2>&1; then echo sudo
  else echo wheel; fi
}

corral_adduser() {
  local name="$1" shell="$2" groups="$3"
  if id "$name" >/dev/null 2>&1; then
    echo "corral: user $name already exists in the image"
  elif command -v useradd >/dev/null 2>&1; then
    useradd --create-home --home-dir "/var/home/$name" --shell "$shell" "$name"
  elif command -v adduser >/dev/null 2>&1; then
    # busybox adduser: -D skips the password prompt, -s sets the shell.
    adduser -D -h "/var/home/$name" -s "$shell" "$name"
  else
    echo "corral: this image has neither useradd nor adduser; cannot create $name" >&2
    exit 1
  fi
  if [ -n "$groups" ]; then
    if command -v usermod >/dev/null 2>&1; then
      usermod -aG "$groups" "$name"
    else
      local g
      for g in ${groups//,/ }; do addgroup "$name" "$g" 2>/dev/null || true; done
    fi
  fi
  install -d -m 0700 -o "$name" -g "$name" "/var/home/$name"
}

corral_setpass() {
  local name="$1" hash="$2"
  if command -v usermod >/dev/null 2>&1; then
    usermod -p "$hash" "$name"
  else
    # busybox: rewrite the shadow field in place.
    sed -i "s|^\($name:\)[^:]*|\1$hash|" /etc/shadow
  fi
  # An expired password would make every login a password change, which no
  # automated test can answer.
  command -v chage >/dev/null 2>&1 && chage -M -1 "$name" || true
}

corral_authorize() {
  local name="$1" home="$2" key="$3"
  install -d -m 0700 "$home/.ssh"
  printf '%s\n' "$key" >> "$home/.ssh/authorized_keys"
  chmod 0600 "$home/.ssh/authorized_keys"
  chown -R "$name:$name" "$home/.ssh" 2>/dev/null || true
}

corral_admin() {
  local name="$1" group
  group="$(corral_admin_group)"
  if command -v usermod >/dev/null 2>&1; then
    usermod -aG "$group" "$name"
  else
    addgroup "$name" "$group" 2>/dev/null || true
  fi
  corral_sudoers "$name"
}

corral_sudoers() {
  local name="$1"
  install -d -m 0750 /etc/sudoers.d
  printf '%s ALL=(ALL) NOPASSWD: ALL\n' "$name" > "/etc/sudoers.d/90-corral-$name"
  chmod 0440 "/etc/sudoers.d/90-corral-$name"
}
`

// serviceScript enables what the harness needs the guest to be running, and
// nothing else.
func serviceScript(spec *Spec) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Generated by corral vmtest.\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("systemctl enable " + PostBootUnit + "\n")
	// sshd is how the checks get in. Some images ship it disabled, and its
	// unit is sshd.service on rpm bases and ssh.service on debian ones.
	b.WriteString(`for unit in sshd.service ssh.service; do
  if systemctl enable "$unit" 2>/dev/null; then echo "corral: enabled $unit"; break; fi
done
`)
	if spec.passwordLoginWanted() {
		// Password logins are off in most bootc images. The spec asked for
		// passwords, so they are turned on for this disposable test VM — and
		// only in the derived image, never in the published one.
		b.WriteString(`install -d -m 0755 /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/30-corral-vmtest.conf <<'EOF'
# corral vmtest: this VM is a disposable test target.
PasswordAuthentication yes
EOF
`)
		if spec.RootPassword != "" {
			b.WriteString("echo 'PermitRootLogin yes' >> /etc/ssh/sshd_config.d/30-corral-vmtest.conf\n")
		}
	}
	return b.String()
}

// passwordLoginWanted reports whether any account got a password.
func (s *Spec) passwordLoginWanted() bool {
	if s.RootPassword != "" {
		return true
	}
	for _, u := range s.Users {
		if u.Password != "" {
			return true
		}
	}
	return false
}

// ── the post-boot hook ────────────────────────────────────────────

// postBootScript wraps the spec's boot-mode scripts in the reporting a harness
// can read: a status file for SSH, and markers on the console for when SSH
// never happens.
func postBootScript(scripts []string) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Generated by corral vmtest — runs once, on first boot and every boot after.\n")
	b.WriteString(`set -o pipefail
exec > >(tee -a ` + HookLogFile + `) 2>&1
mkdir -p "$(dirname ` + StatusFile + `)"
rc=0
report() {
  echo "$rc" > ` + StatusFile + `
  if [ "$rc" -eq 0 ]; then echo "` + HookOKMarker + `"; else echo "` + HookFailMarker + ` rc=$rc"; fi
  echo "` + ReadyMarker + `"
}
# The status file and the markers must be written even when a script kills the
# hook, or a harness waiting on them waits for the whole timeout instead.
trap report EXIT

echo "corral: post-boot hook starting on $(date -Is)"
`)
	for i, script := range scripts {
		b.WriteString(fmt.Sprintf("\necho 'corral: provision script %d'\n", i+1))
		b.WriteString("if ! bash -euo pipefail <<'CORRAL_SCRIPT_EOF'\n")
		b.WriteString(strings.TrimRight(script, "\n") + "\n")
		b.WriteString("CORRAL_SCRIPT_EOF\n")
		b.WriteString(fmt.Sprintf("then rc=$?; echo \"corral: provision script %d failed with $rc\"; exit $rc; fi\n", i+1))
	}
	b.WriteString("\necho 'corral: post-boot hook finished'\nexit 0\n")
	return b.String()
}

// postBootUnitFile is the unit that runs the hook.
//
// Ordering matters: after the network and after sshd, so a hook that installs
// something can reach a mirror and a harness that sees the marker knows SSH is
// already up. journal+console puts the output on the serial log, which is the
// only channel a guest with no SSH has.
func postBootUnitFile() string {
	return `[Unit]
Description=corral vmtest post-boot hook
After=network-online.target multi-user.target sshd.service
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/libexec/corral-postboot
StandardOutput=journal+console
StandardError=journal+console
TimeoutStartSec=0

[Install]
WantedBy=multi-user.target
`
}

// ── Containerfile generation ──────────────────────────────────────

// packageManager is how a base image installs software.
type packageManager struct {
	Name string
	// Probe is a path whose presence in the image identifies this manager.
	Probe string
	// Install is the shell that installs "$@"-style package arguments.
	Install string
}

// packageManagers are probed in this order. dnf before yum, apt before apk:
// only the first match is used, so a base with both is treated as the newer.
var packageManagers = []packageManager{
	{Name: "dnf", Probe: "/usr/bin/dnf", Install: "dnf -y install %s && dnf clean all"},
	{Name: "zypper", Probe: "/usr/bin/zypper", Install: "zypper --non-interactive install -y %s && zypper clean -a"},
	{Name: "apt", Probe: "/usr/bin/apt-get", Install: "apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*"},
	{Name: "pacman", Probe: "/usr/bin/pacman", Install: "pacman -Sy --noconfirm %s && pacman -Scc --noconfirm"},
	{Name: "apk", Probe: "/sbin/apk", Install: "apk add --no-cache %s"},
}

// builtinContainerfile renders the derived image.
//
// One RUN per concern, in the order that keeps the cache useful: the overlay
// and the packages change rarely, the scripts change every time someone edits
// a test. bootc container lint at the end is what upstream asks derived images
// to pass, and catching a broken layer here is cheaper than catching it as a
// disk that will not boot.
func builtinContainerfile(ctx *buildContext, pm packageManager) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by corral vmtest. Do not edit — it is rewritten on every run.\nFROM %s\n", ctx.Base)

	if len(ctx.ExtraRun) > 0 {
		b.WriteString("\n# extraRun: repositories and anything else the packages need first.\n")
		for _, line := range ctx.ExtraRun {
			if strings.TrimSpace(line) == "" {
				continue
			}
			fmt.Fprintf(&b, "RUN %s\n", line)
		}
	}

	if len(ctx.Packages) > 0 {
		fmt.Fprintf(&b, "\nRUN "+pm.Install+"\n", strings.Join(ctx.Packages, " "))
	}

	if len(ctx.SystemFiles) > 0 {
		b.WriteString("\n# The overlay: test files, the post-boot hook, its unit.\nCOPY system_files/ /\n")
	}

	if names := ctx.scriptNames(); len(names) > 0 {
		b.WriteString("\nCOPY build_files/ /tmp/corral-build/\n")
		b.WriteString("RUN set -eux; ")
		for i, name := range names {
			if i > 0 {
				b.WriteString(" \\\n  && ")
			}
			fmt.Fprintf(&b, "/tmp/corral-build/%s", name)
		}
		b.WriteString(" \\\n  && rm -rf /tmp/corral-build\n")
	}

	// bootc's own lint: it catches the layer mistakes that otherwise show up
	// as a disk that builds and does not boot. Advisory, because an older base
	// image may not ship the subcommand at all.
	b.WriteString("\nRUN bootc container lint || echo 'corral: bootc container lint unavailable in this base'\n")
	return b.String()
}

// remoraManifest renders the remora.yaml that remora generates a Containerfile
// from. Same content, different generator — remora resolves a package lockfile
// and knows package managers this does not.
func remoraManifest(ctx *buildContext) (string, error) {
	manifest := map[string]any{
		"base":  ctx.Base,
		"image": "localhost/corral-vmtest:latest",
	}
	if len(ctx.Packages) > 0 {
		manifest["packages"] = ctx.Packages
	}
	if len(ctx.ExtraRun) > 0 {
		manifest["extra_run"] = ctx.ExtraRun
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return "# Generated by corral vmtest.\n" + string(data), nil
}

// ── passwords ─────────────────────────────────────────────────────

// hashPassword returns a SHA-512 crypt hash, the format every base image this
// targets accepts in /etc/shadow.
func hashPassword(plain string) (string, error) {
	salt, err := cryptSalt()
	if err != nil {
		return "", err
	}
	hash, err := sha512_crypt.New().Generate([]byte(plain), []byte(salt))
	if err != nil {
		return "", fmt.Errorf("hashing a password: %w", err)
	}
	return hash, nil
}

// cryptSalt builds a "$6$<16 chars>$" salt from the crypt alphabet.
func cryptSalt() (string, error) {
	const alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("reading random bytes for a password salt: %w", err)
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return "$6$" + string(buf) + "$", nil
}

// shellQuote wraps a value in single quotes so a key, a hash or a name with
// anything in it survives the shell.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
