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
	if !strings.Contains(provisionDiskScript, "chroot \"$MNT\" /bin/bash") {
		t.Error("provision script must execute inside the installed root")
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
