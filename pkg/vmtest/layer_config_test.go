package vmtest

import "testing"

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

func TestDerivedTag(t *testing.T) {
	if got := DerivedTag("gate"); got != "localhost/corral-vmtest/gate:latest" {
		t.Errorf("DerivedTag = %q", got)
	}
}
