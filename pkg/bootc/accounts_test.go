package bootc

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAccountProvisionScript(t *testing.T) {
	const hash = "$6$example$abcdefghijklmnopqrstuv"
	script, err := AccountProvisionScript("tuna", hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"useradd --create-home --home-dir \"$home\"",
		"/var/home/$name",
		"usermod -p \"$hash\" \"$name\"",
		"name='tuna'",
		"hash='" + hash + "'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("account script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "hunter2") {
		t.Error("account script contains plaintext password")
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		if out, err := exec.Command(sh, "-n", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("account script does not parse: %v\n%s", err, out)
		}
	}
}

func TestAccountProvisionScriptValidation(t *testing.T) {
	cases := []struct {
		name string
		hash string
		want string
	}{
		{name: "", hash: "$6$hash", want: "requires --user"},
		{name: "bad/name", want: "valid login name"},
		{name: "tuna", hash: "plaintext", want: "SHA-512 crypt"},
	}
	for _, tc := range cases {
		t.Run(tc.name+tc.hash, func(t *testing.T) {
			_, err := AccountProvisionScript(tc.name, tc.hash)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestProvisionArgsMountsScriptAndDisk(t *testing.T) {
	args := LocalBuilder{}.provisionArgs(BuildRequest{
		Image: "ghcr.io/tuna-os/marlin:tuna",
		Dest:  "/tmp/disk.raw",
	}, "/tmp/provision.sh")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--privileged",
		"-v /tmp/disk.raw:/target.img",
		"-v /tmp/provision.sh:/provision.sh:ro",
		"ghcr.io/tuna-os/marlin:tuna sh -c",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("provision args missing %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(provisionDiskScript, `run_installed_root "$MNT" /provision.sh`) {
		t.Error("the local disk script must hand the root partition to InstalledRootRunner")
	}
	if strings.Contains(provisionDiskScript, `chroot "$MNT"`) {
		t.Error("the local disk script must not chroot into the raw root partition")
	}
}

// The runner and the local disk script are POSIX sh: the image's /bin/sh runs
// them, and that is not always bash.
func TestInstalledRootScriptsParse(t *testing.T) {
	for _, shell := range []string{"sh", "bash"} {
		bin, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		for name, script := range map[string]string{"runner": InstalledRootRunner, "disk": provisionDiskScript} {
			if out, err := exec.Command(bin, "-n", "-c", script).CombinedOutput(); err != nil {
				t.Errorf("%s: %s script does not parse: %v\n%s", shell, name, err, out)
			}
		}
	}
}

// Account changes must land where the booted system reads them: the
// deployment's /etc and the stateroot's /var, for both bootc backends, with
// SELinux labels restored from the image's own policy.
func TestInstalledRootRunnerTargetsDeployment(t *testing.T) {
	for _, want := range []string{
		`"$P"/ostree/deploy/*/deploy/*`, // ostree deployment
		`VAR=${d%/deploy/*}/var`,        // ostree stateroot /var
		`"$P"/state/deploy/*/etc`,       // composefs deployment /etc
		`VAR=$P/state/os/default/var`,   // composefs /var
		`mount --rbind /usr "$R/usr"`,   // the image's /usr
		`mount --bind "$ETC" "$R/etc"`,
		`mount --bind "$VAR" "$R/var"`,
		`chroot "$R" "$SH" /tmp/corral-provision.sh`,
		// validated against the image's policy, not the builder kernel's,
		// through the wrapper that tolerates only unmapped contexts
		`corral_setfiles -c "$POLICY" "$FC" /etc /var`,
		`corral_setfiles "$FC" /etc /var`,
	} {
		if !strings.Contains(InstalledRootRunner, want) {
			t.Errorf("runner missing %q", want)
		}
	}
}

// A partition with no bootc deployment is a hard error, not a silent no-op.
func TestInstalledRootRunnerRejectsNonDeployment(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	out, err := exec.Command(sh, "-c", InstalledRootRunner, "runner", t.TempDir(), "/dev/null").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "no bootc deployment found") {
		t.Fatalf("err = %v, out = %s; want a missing-deployment failure", err, out)
	}
}

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$6$") {
		t.Fatalf("hash = %q, want SHA-512 crypt", hash)
	}
	if strings.Contains(hash, "hunter2") {
		t.Fatal("hash contains the plaintext password")
	}
}

func TestValidateLoginUser(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"tuna", true},
		{"_svc", true},
		{"a-b_c9", true},
		{"machine$", true},
		{strings.Repeat("a", 32), true},
		{strings.Repeat("a", 33), false},
		{"", false},
		{"Tuna", false},
		{"1tuna", false},
		{"-tuna", false},
		{"tu$na", false},
		{"tuna$$", false},
		{"tuna\n", false},
		{"tuna name", false},
		{"../etc", false},
		{"tuna;id", false},
	}
	for _, tc := range cases {
		err := ValidateLoginUser(tc.name)
		if tc.ok && err != nil {
			t.Errorf("ValidateLoginUser(%q) = %v, want nil", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("ValidateLoginUser(%q) = nil, want an error", tc.name)
		}
	}
}

func TestValidatePasswordHash(t *testing.T) {
	cases := []struct {
		hash string
		ok   bool
	}{
		{"", true},
		{"$6$example$abcdefghijklmnopqrstuv", true},
		{"$6$rounds=5000$example$abcdefghijklmnopqrstuv", true},
		{"$6$" + strings.Repeat("a", 16) + "$abc./", true},
		{"$6$" + strings.Repeat("a", 17) + "$abc", false},
		{"$5$example$abcdefghijklmnopqrstuv", false},
		{"$6$example$", false},
		{"$6$$abcdef", false},
		{"$6$rounds=x$example$abcdef", false},
		{"plaintext", false},
		{"$6$example$abc'; id; '", false},
		{"$6$example$abcdef\n", false},
		{"$6$example$abc def", false},
	}
	for _, tc := range cases {
		err := ValidatePasswordHash(tc.hash)
		if tc.ok && err != nil {
			t.Errorf("ValidatePasswordHash(%q) = %v, want nil", tc.hash, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("ValidatePasswordHash(%q) = nil, want an error", tc.hash)
		}
	}
}

func TestAccountProvisionScriptWithoutUserIsEmpty(t *testing.T) {
	script, err := AccountProvisionScript("", "")
	if err != nil || script != "" {
		t.Fatalf("AccountProvisionScript(\"\", \"\") = %q, %v; want empty script and no error", script, err)
	}
}

func TestAccountProvisionScriptWithoutHashLeavesPasswordUntouched(t *testing.T) {
	script, err := AccountProvisionScript("tuna", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "hash=''\n") {
		t.Errorf("account script should carry an empty hash:\n%s", script)
	}
}

func TestHashPasswordProducesAcceptedUniqueHashes(t *testing.T) {
	first, err := HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{first, second} {
		if err := ValidatePasswordHash(hash); err != nil {
			t.Errorf("HashPassword produced %q, rejected by ValidatePasswordHash: %v", hash, err)
		}
	}
	if first == second {
		t.Errorf("two hashes of the same password are identical (%q): the salt is not random", first)
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	if got, want := shellQuote(`a'b`), `'a'\''b'`; got != want {
		t.Errorf("shellQuote = %s, want %s", got, want)
	}
	if got := shellQuote(""); got != "''" {
		t.Errorf("shellQuote(\"\") = %s, want ''", got)
	}
}
