package virtualbox

import (
	"errors"
	"strings"
	"testing"
)

const natInfo = `name="web"
VMState="running"
Forwarding(0)="http,tcp,,8080,,80"
Forwarding(1)="ssh,tcp,,2222,,22"
`

func TestSSHEndpointPrefersTheNATForward(t *testing.T) {
	f := fakeRunner(t)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "web", "--machinereadable"}, natInfo, nil)
	host, port, err := NewClient("").SSHEndpoint("web")
	if err != nil || host != "127.0.0.1" || port != 2222 {
		t.Fatalf("SSHEndpoint = %s:%d, %v; want 127.0.0.1:2222", host, port, err)
	}
	if len(f.Calls()) != 1 {
		t.Errorf("guestproperty was read despite a forward: %v", f.Calls())
	}
}

func TestSSHEndpointFallsBackToGuestAdditions(t *testing.T) {
	f := fakeRunner(t)
	f.AddResponseKV("VBoxManage", []string{"showvminfo", "web", "--machinereadable"}, runningInfo, nil)
	f.AddResponseKV("VBoxManage", []string{"guestproperty", "get", "web", guestIPProperty}, "Value: 192.168.56.10\n", nil)
	host, port, err := NewClient("").SSHEndpoint("web")
	if err != nil || host != "192.168.56.10" || port != 22 {
		t.Fatalf("SSHEndpoint = %s:%d, %v; want 192.168.56.10:22", host, port, err)
	}
}

func TestAddressWithoutGuestAdditionsNamesTheRemedy(t *testing.T) {
	f := fakeRunner(t)
	f.AddResponseKV("VBoxManage", []string{"guestproperty", "get", "web", guestIPProperty}, "No value set!\n", nil)
	_, err := NewClient("").Address("web")
	if err == nil || !strings.Contains(err.Error(), "--natpf1") {
		t.Fatalf("Address = %v, want a remedy", err)
	}
}

func TestAddressCommandFailure(t *testing.T) {
	f := fakeRunner(t)
	f.AddResponseKV("VBoxManage", []string{"guestproperty", "get", "web", guestIPProperty}, "", errors.New("no such VM"))
	if _, err := NewClient("").Address("web"); err == nil {
		t.Fatal("a failed guestproperty read returned an address")
	}
}

func TestSSHArgs(t *testing.T) {
	got := strings.Join(sshArgs("core", "/k", "uptime", 2222, "127.0.0.1", []string{"8080:localhost:80"}), " ")
	want := "-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 -i /k -L 8080:localhost:80 core@127.0.0.1 uptime"
	if got != want {
		t.Errorf("sshArgs = %q\nwant      %q", got, want)
	}
}
