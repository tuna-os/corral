package logging

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"TS_AUTHKEY=tskey-auth-kAbC123-XYZ tailscale up", "TS_AUTHKEY=[REDACTED] tailscale up"},
		{"tailscale up --authkey=tskey-auth-k1-abc", "tailscale up --authkey=[REDACTED]"},
		{"key is tskey-api-k9-zzz", "key is [REDACTED]"},
		{"Authorization: PVEAPIToken=root@pam!corral=0a1b2c3d-4e5f-6789-abcd-ef0123456789",
			"Authorization: PVEAPIToken=[REDACTED]"},
		{"token root@pam!corral=0a1b2c3d-4e5f-6789-abcd-ef0123456789", "token root@pam!corral=[REDACTED]"},
		{"Authorization: Bearer abc.def.ghi", "Authorization: Bearer [REDACTED]"},
		{"https://admin:hunter2@pve.example:8006/api2", "https://admin:[REDACTED]@pve.example:8006/api2"},
		{`{"password":"hunter2","name":"web"}`, `{"password":"[REDACTED]","name":"web"}`},
		{"chpasswd:\n  password: hunter2\n", "chpasswd:\n  password: [REDACTED]\n"},
		{"--from-literal=password=hunter2", "--from-literal=password=[REDACTED]"},
		{"SSHPASS=hunter2", "SSHPASS=[REDACTED]"},
		{"https://pve/api?token=abc&node=n1", "https://pve/api?token=[REDACTED]&node=n1"},
		{"client_secret: s3cr3t", "client_secret: [REDACTED]"},
		// Not credentials: left alone.
		{"kubectl get vms -n default -o json", "kubectl get vms -n default -o json"},
		{"--request-timeout=5s", "--request-timeout=5s"},
		{"virsh -c qemu+ssh://host/system list", "virsh -c qemu+ssh://host/system list"},
		{"ssh://host:22/path", "ssh://host:22/path"},
	} {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactArgs(t *testing.T) {
	argv := []string{"tool", "--token", "abc123", "--password=hunter2", "--name", "web",
		"--auth-key", "tskey-auth-x", "-v", "--api-key", "--next", "TS_AUTHKEY=tskey-auth-y"}
	want := []string{"tool", "--token", Redacted, "--password=" + Redacted, "--name", "web",
		"--auth-key", Redacted, "-v", "--api-key", "--next", "TS_AUTHKEY=" + Redacted}
	got := RedactArgs(argv)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("RedactArgs\n got %q\nwant %q", got, want)
	}
	if argv[2] != "abc123" {
		t.Error("RedactArgs modified its input")
	}
}

func TestCommand(t *testing.T) {
	got := Command("incus", []string{"config", "set", "vm", "environment.TS_AUTHKEY=tskey-auth-k1"})
	if want := "incus config set vm environment.TS_AUTHKEY=" + Redacted; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}
