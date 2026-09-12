package vmtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testSpec() *Spec {
	spec := &Spec{
		Bootc:        "quay.io/fedora/fedora-bootc:41",
		RootPassword: "rootpw",
		Users: []User{{
			Name:              "tester",
			Password:          "hunter2",
			Sudo:              true,
			Groups:            []string{"video"},
			SSHAuthorizedKeys: []string{"ssh-ed25519 AAAAtester tester@example"},
		}},
		Packages: []string{"jq", "htop"},
		ExtraRun: []string{"dnf config-manager --set-enabled crb"},
		Files:    []File{{Path: "/etc/corral-test.conf", Content: "hello\n", Mode: "0600"}},
		Provision: []Provision{
			{Mode: ImageMode, Script: "echo built-at-build-time"},
			{Mode: "system", Script: "systemctl is-active sshd"},
		},
	}
	spec.WithDefaults()
	return spec
}

func TestNewBuildContext(t *testing.T) {
	ctx, err := newBuildContext(testSpec(), "quay.io/fedora/fedora-bootc:41", "ssh-ed25519 AAAArun run@corral")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}

	if ctx.Base != "quay.io/fedora/fedora-bootc:41" {
		t.Errorf("base = %q", ctx.Base)
	}
	// The spec's own file, the hook, and the unit.
	for _, want := range []string{
		"/etc/corral-test.conf",
		"/usr/libexec/corral-postboot",
		"/usr/lib/systemd/system/" + PostBootUnit,
	} {
		if _, ok := ctx.SystemFiles[want]; !ok {
			t.Errorf("the overlay is missing %s", want)
		}
	}
	if mode := ctx.SystemFiles["/etc/corral-test.conf"].Mode; mode != 0o600 {
		t.Errorf("file mode = %o, want 600", mode)
	}
	if mode := ctx.SystemFiles["/usr/libexec/corral-postboot"].Mode; mode != 0o755 {
		t.Errorf("hook mode = %o, want 755 — a hook that cannot execute stops the run at first boot", mode)
	}

	// Build scripts run in name order: accounts, services, then the spec's own.
	want := []string{"10-corral-accounts.sh", "20-corral-services.sh", "50-corral-provision-01.sh"}
	got := ctx.scriptNames()
	if len(got) != len(want) {
		t.Fatalf("build scripts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("build script %d = %s, want %s", i, got[i], want[i])
		}
	}
	// The build-time script is in the layer; the boot-time one is in the hook.
	if !strings.Contains(ctx.BuildScripts["50-corral-provision-01.sh"], "built-at-build-time") {
		t.Error("the image-mode script should be a build script")
	}
	if !strings.Contains(ctx.SystemFiles["/usr/libexec/corral-postboot"].Content, "systemctl is-active sshd") {
		t.Error("the boot-mode script should be in the post-boot hook")
	}
}

func TestNewBuildContext_BadFileMode(t *testing.T) {
	spec := &Spec{Files: []File{{Path: "/etc/x", Mode: "not-a-mode"}}}
	if _, err := newBuildContext(spec, "base", ""); err == nil {
		t.Fatal("expected an error for a mode that is not octal")
	}
}

func TestBuildContextWrite(t *testing.T) {
	dir := t.TempDir()
	ctx, err := newBuildContext(testSpec(), "base", "ssh-ed25519 AAAArun run@corral")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	if err := ctx.write(dir); err != nil {
		t.Fatalf("write: %v", err)
	}

	hook := filepath.Join(dir, "system_files", "usr", "libexec", "corral-postboot")
	info, err := os.Stat(hook)
	if err != nil {
		t.Fatalf("the hook was not written: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("hook mode on disk = %o, want 755", info.Mode().Perm())
	}
	conf := filepath.Join(dir, "system_files", "etc", "corral-test.conf")
	if data, err := os.ReadFile(conf); err != nil || string(data) != "hello\n" {
		t.Errorf("overlay file = %q, %v", data, err)
	}
	script := filepath.Join(dir, "build_files", "10-corral-accounts.sh")
	info, err = os.Stat(script)
	if err != nil {
		t.Fatalf("the account script was not written: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("build script mode = %o, want 755", info.Mode().Perm())
	}
}

func TestAccountScript(t *testing.T) {
	spec := testSpec()
	script, err := accountScript(spec, "ssh-ed25519 AAAArun run@corral")
	if err != nil {
		t.Fatalf("accountScript: %v", err)
	}

	// The run's key reaches root and the created account, because the probe
	// logs in with it and no password helps a non-interactive client.
	if !strings.Contains(script, "corral_authorize root /root 'ssh-ed25519 AAAArun run@corral'") {
		t.Error("root should be authorized for the run's key")
	}
	if !strings.Contains(script, "corral_authorize 'tester' /var/home/tester 'ssh-ed25519 AAAArun run@corral'") {
		t.Error("the created user should be authorized for the run's key")
	}
	if !strings.Contains(script, "AAAAtester") {
		t.Error("the spec's own authorized key should be installed too")
	}
	// /var/home, not /home: on a bootc system /home is a symlink into /var, and
	// only /var from the image reaches the installed system.
	if strings.Contains(script, " /home/tester") {
		t.Error("home directories belong under /var/home on a bootc system")
	}
	if !strings.Contains(script, "corral_admin 'tester'") {
		t.Error("a sudo user should be added to the administrator group and sudoers")
	}
	// The group name is resolved in the guest, so it must not be inside the
	// quoted group list — a single-quoted substitution would create a group
	// literally called $(corral_admin_group).
	if strings.Contains(script, "'video,$(corral_admin_group)'") {
		t.Error("the administrator group must be resolved by the guest, not quoted into a list")
	}
	if !strings.Contains(script, `group="$(corral_admin_group)"`) {
		t.Error("the administrator group is named by the base image, not by us")
	}

	// Passwords are hashed on the host. A plain one would sit in the image.
	for _, plain := range []string{"hunter2", "rootpw"} {
		if strings.Contains(script, plain) {
			t.Errorf("the script contains the plain password %q — it must only carry a hash", plain)
		}
	}
	if strings.Count(script, "corral_setpass root '$6$") != 1 {
		t.Errorf("root's password should be a SHA-512 crypt hash:\n%s", script)
	}
	if strings.Count(script, "corral_setpass 'tester' '$6$") != 1 {
		t.Errorf("the user's password should be a SHA-512 crypt hash:\n%s", script)
	}
	// And the shell that applies them must not mangle a hash with $ in it.
	if !strings.Contains(script, `usermod -p "$hash" "$name"`) {
		t.Error("the helper should apply the hash with usermod")
	}
}

func TestServiceScript(t *testing.T) {
	withPassword := serviceScript(testSpec())
	if !strings.Contains(withPassword, "systemctl enable "+PostBootUnit) {
		t.Error("the post-boot unit must be enabled")
	}
	if !strings.Contains(withPassword, "sshd.service ssh.service") {
		t.Error("sshd is named differently per base, so both names are tried")
	}
	if !strings.Contains(withPassword, "PasswordAuthentication yes") {
		t.Error("a spec with passwords should turn password logins on")
	}
	if !strings.Contains(withPassword, "PermitRootLogin yes") {
		t.Error("a root password is no use without PermitRootLogin")
	}

	// And nothing is loosened when no password was asked for.
	plain := serviceScript(&Spec{})
	if strings.Contains(plain, "PasswordAuthentication") {
		t.Error("sshd must be left as the image ships it when no password was set")
	}
}

func TestPostBootScript(t *testing.T) {
	script := postBootScript([]string{"echo one", "exit 3"})
	for _, want := range []string{StatusFile, HookLogFile, ReadyMarker, HookOKMarker, HookFailMarker, "trap report EXIT"} {
		if !strings.Contains(script, want) {
			t.Errorf("the hook is missing %q", want)
		}
	}
	if !strings.Contains(script, "echo one") || !strings.Contains(script, "exit 3") {
		t.Error("the hook should carry both scripts")
	}
	// Every script gets its own guarded block, so the first failure is the
	// reported one and the rest do not run.
	if strings.Count(script, "CORRAL_SCRIPT_EOF") != 4 {
		t.Errorf("expected two heredoc-delimited scripts:\n%s", script)
	}
}

func TestPostBootUnit(t *testing.T) {
	unit := postBootUnitFile()
	if !strings.Contains(unit, "journal+console") {
		t.Error("the hook's output must reach the console: it is all a guest with no SSH has")
	}
	if !strings.Contains(unit, "After=network-online.target multi-user.target sshd.service") {
		t.Error("the hook runs after the network and sshd")
	}
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Error("the unit must be wanted by something, or enabling it does nothing")
	}
}

func TestBuiltinContainerfile(t *testing.T) {
	ctx, err := newBuildContext(testSpec(), "quay.io/fedora/fedora-bootc:41", "ssh-ed25519 AAAArun")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	file := builtinContainerfile(ctx, packageManagers[0]) // dnf

	if !strings.HasPrefix(file, "# Generated by corral vmtest.") {
		t.Error("the generated file should say it is generated")
	}
	for _, want := range []string{
		"FROM quay.io/fedora/fedora-bootc:41",
		"RUN dnf config-manager --set-enabled crb",
		"dnf -y install jq htop",
		"COPY system_files/ /",
		"COPY build_files/ /tmp/corral-build/",
		"/tmp/corral-build/10-corral-accounts.sh",
		"bootc container lint",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the Containerfile is missing %q:\n%s", want, file)
		}
	}
	// extraRun comes before the packages: it is what makes them installable.
	if strings.Index(file, "config-manager") > strings.Index(file, "dnf -y install") {
		t.Error("extraRun must run before the package install")
	}
}

func TestBuiltinContainerfile_NothingExtra(t *testing.T) {
	spec := &Spec{}
	spec.WithDefaults()
	ctx, err := newBuildContext(spec, "base:latest", "")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	file := builtinContainerfile(ctx, packageManager{})
	if strings.Contains(file, "install") {
		t.Errorf("no packages means no install line:\n%s", file)
	}
	// The hook and its unit are still layered: they are what report readiness.
	if !strings.Contains(file, "COPY system_files/ /") {
		t.Error("the overlay is always copied — it carries the hook")
	}
}

func TestRemoraManifest(t *testing.T) {
	ctx, err := newBuildContext(testSpec(), "base:latest", "")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	manifest, err := remoraManifest(ctx)
	if err != nil {
		t.Fatalf("remoraManifest: %v", err)
	}
	for _, want := range []string{"base: base:latest", "- jq", "extra_run:", "image: localhost/corral-vmtest:latest"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("the remora manifest is missing %q:\n%s", want, manifest)
		}
	}
}

func TestHashPassword(t *testing.T) {
	hash, err := hashPassword("hunter2")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$6$") {
		t.Errorf("hash = %q, want a SHA-512 crypt hash", hash)
	}
	if strings.Contains(hash, "hunter2") {
		t.Error("the hash contains the password")
	}
	// Salted, so two hashes of one password differ.
	other, err := hashPassword("hunter2")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if hash == other {
		t.Error("hashes should be salted")
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":    "'plain'",
		"it's":     `'it'\''s'`,
		"a b":      "'a b'",
		"$(rm -r)": "'$(rm -r)'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestShebang(t *testing.T) {
	if got := shebang("echo hi"); !strings.HasPrefix(got, "#!/usr/bin/env bash") {
		t.Errorf("a script with no shebang should get one: %q", got)
	}
	if !strings.Contains(shebang("echo hi"), "set -euo pipefail") {
		t.Error("a generated script should stop at the first failure")
	}
	own := "#!/bin/sh\necho hi"
	if got := shebang(own); got != own {
		t.Errorf("a script with its own shebang must be left alone: %q", got)
	}
}

func TestDerivedTag(t *testing.T) {
	if got := DerivedTag("gate"); got != "localhost/corral-vmtest/gate:latest" {
		t.Errorf("DerivedTag = %q", got)
	}
}

// The account script runs inside the image, whose coreutils may be Rust
// uutils. uutils' `install -d /root/.ssh` fails with "cannot create directory
// '/root': File exists", which took out a whole CI run. Nothing generated here
// may use `install -d` again.
func TestGeneratedShell_NoInstallD(t *testing.T) {
	ctx, err := newBuildContext(testSpec(), "base", "ssh-ed25519 AAAArun")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	for name, script := range ctx.BuildScripts {
		if line, found := findCommand(script, "install -d"); found {
			t.Errorf("%s uses `install -d`, which Rust uutils refuses when the parent exists: %s", name, line)
		}
	}
	for path, file := range ctx.SystemFiles {
		if line, found := findCommand(file.Content, "install -d"); found {
			t.Errorf("%s uses `install -d`, which Rust uutils refuses when the parent exists: %s", path, line)
		}
	}
	// And the directories it needs are still created, with their modes.
	accounts := ctx.BuildScripts["10-corral-accounts.sh"]
	for _, want := range []string{`mkdir -p "$home/.ssh"`, `chmod 0700 "$home/.ssh"`, "mkdir -p /etc/sudoers.d"} {
		if !strings.Contains(accounts, want) {
			t.Errorf("the account script is missing %q", want)
		}
	}
}

// The generated shell has to be valid shell. `bash -n` parses it without
// running it, which catches an unbalanced heredoc or quote in a script that
// otherwise only fails four minutes into a container build.
func TestGeneratedShell_Parses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("no bash: %v", err)
	}
	ctx, err := newBuildContext(testSpec(), "base", "ssh-ed25519 AAAArun")
	if err != nil {
		t.Fatalf("newBuildContext: %v", err)
	}
	scripts := map[string]string{}
	for name, script := range ctx.BuildScripts {
		scripts[name] = shebang(script)
	}
	scripts["corral-postboot"] = ctx.SystemFiles["/usr/libexec/corral-postboot"].Content

	for name, script := range scripts {
		path := filepath.Join(t.TempDir(), "script.sh")
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bash, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s is not valid shell: %v\n%s\n%s", name, err, out, script)
		}
	}
}

// findCommand reports whether a script runs the given command, ignoring the
// comment lines that may name it while telling the reader not to use it.
func findCommand(script, command string) (string, bool) {
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, command) {
			return trimmed, true
		}
	}
	return "", false
}
