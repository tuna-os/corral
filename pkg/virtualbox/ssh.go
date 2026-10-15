package virtualbox

// SSH: VirtualBox has no address of its own to hand out, so Corral finds the
// guest's sshd one of two ways, in this order:
//
//  1. a NAT port-forward rule whose guest port is 22 (showvminfo reports it as
//     Forwarding(N)="name,tcp,hostip,hostport,guestip,22"), reached on the
//     host side — the usual setup, since a NAT guest is unreachable otherwise;
//  2. the guest's first IPv4 from Guest Additions
//     (guestproperty /VirtualBox/GuestInfo/Net/0/V4/IP), on port 22 — a
//     bridged or host-only guest is reachable there directly.

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/tuna-os/corral/pkg/shell"
)

// guestIPProperty is the Guest Additions property for the first NIC's IPv4.
const guestIPProperty = "/VirtualBox/GuestInfo/Net/0/V4/IP"

// Address returns the guest's own IPv4 as Guest Additions reports it.
func (c Client) Address(name string) (string, error) {
	out, err := c.run("guestproperty", "get", name, guestIPProperty)
	if err != nil {
		return "", fmt.Errorf("virtualbox guestproperty %s: %w", name, err)
	}
	if v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "Value:"); ok {
		if ip := strings.TrimSpace(v); ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("virtualbox: %q reports no guest address; install Guest Additions in the guest, or add a NAT port-forward to guest port 22 (VBoxManage modifyvm %s --natpf1 ssh,tcp,,2222,,22)", name, name)
}

// SSHEndpoint returns where the guest's sshd is reachable from this host.
func (c Client) SSHEndpoint(name string) (string, int, error) {
	out, err := c.run("showvminfo", name, "--machinereadable")
	if err != nil {
		return "", 0, fmt.Errorf("virtualbox showvminfo %s: %w", name, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || !strings.HasPrefix(k, "Forwarding(") {
			continue
		}
		// name,proto,hostip,hostport,guestip,guestport
		rule := strings.Split(strings.Trim(v, `"`), ",")
		if len(rule) != 6 || rule[1] != "tcp" || rule[5] != "22" {
			continue
		}
		port, err := strconv.Atoi(rule[3])
		if err != nil || port <= 0 {
			continue
		}
		host := rule[2]
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		return host, port, nil
	}
	ip, err := c.Address(name)
	if err != nil {
		return "", 0, err
	}
	return ip, 22, nil
}

// SSH opens an ssh session to the guest's endpoint. A non-zero port other
// than 22 overrides the discovered one, as with the other backends.
func (c Client) SSH(name, user, identity, command string, port int, password string, localForwards []string) error {
	host, found, err := c.SSHEndpoint(name)
	if err != nil {
		return err
	}
	if port == 0 || port == 22 {
		port = found
	}
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh not found in PATH")
	}
	args := sshArgs(user, identity, command, port, host, localForwards)
	if password != "" {
		return shell.RunWithSSHPass(password, sshBin, args...)
	}
	cmd := exec.Command(sshBin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// sshArgs builds the ssh(1) argv, factored out for unit testing.
func sshArgs(user, identity, command string, port int, host string, localForwards []string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-p", strconv.Itoa(port),
	}
	if identity != "" {
		args = append(args, "-i", identity)
	}
	for _, fwd := range localForwards {
		args = append(args, "-L", fwd)
	}
	target := host
	if user != "" {
		target = user + "@" + host
	}
	args = append(args, target)
	if command != "" {
		args = append(args, command)
	}
	return args
}
