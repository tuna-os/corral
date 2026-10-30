package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/rfb"
	"github.com/tuna-os/corral/pkg/rfb/rfbtest"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

type consoleDialer struct{ server *rfbtest.Server }

func (d consoleDialer) Dial(_, _ string, _ kubevirt.Console) (net.Conn, error) {
	return d.server.Conn(), nil
}

// kubevirtConsole registers a KubeVirt VM named web whose VNC console is an
// in-memory RFB server with a 1280x800 screen.
func kubevirtConsole(t *testing.T) (*rfbtest.Server, *shell.Fake) {
	t.Helper()
	fake := kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "corral-vms"})
	server := rfbtest.NewServer(1280, 800)
	kubevirt.SetConsoleDialer(consoleDialer{server})
	oldGap := rfb.EventGap
	rfb.EventGap = 0
	t.Cleanup(func() { kubevirt.SetConsoleDialer(nil); rfb.EventGap = oldGap })
	return server, fake
}

func withJSON(t *testing.T) {
	t.Helper()
	old := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = old })
}

func consoleFlags(t *testing.T, enter bool, click string, double bool) {
	t.Helper()
	typeEnter, pointerClick, pointerDouble = enter, click, double
	t.Cleanup(func() { typeEnter, pointerClick, pointerDouble = false, "", false })
}

func decodeJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q is not JSON: %v", out, err)
	}
	return got
}

func TestKeyOnKubeVirtPressesOverVNC(t *testing.T) {
	server, _ := kubevirtConsole(t)
	withJSON(t)
	out := captureStdout(t, func() {
		if err := keyCmd.RunE(keyCmd, []string{"web", "ctrl", "alt", "f2"}); err != nil {
			t.Fatalf("key on a KubeVirt VM: %v", err)
		}
	})
	want := "[key 0xffe3 down key 0xffe9 down key 0xffbf down key 0xffbf up key 0xffe9 up key 0xffe3 up]"
	if got := fmt.Sprint(server.Events()); got != want {
		t.Errorf("events = %s\nwant %s", got, want)
	}
	got := decodeJSON(t, out)
	if got["vm"] != "web" || got["action"] != "key" || fmt.Sprint(got["keys"]) != "[ctrl alt f2]" {
		t.Errorf("--json = %v", got)
	}
}

// --enter types Return on the same session, and --json reports the length,
// never the text: the text is often a passphrase.
func TestTypeOnKubeVirtTypesOverVNC(t *testing.T) {
	server, _ := kubevirtConsole(t)
	withJSON(t)
	consoleFlags(t, true, "", false)
	out := captureStdout(t, func() {
		if err := typeCmd.RunE(typeCmd, []string{"web", "Secret!1"}); err != nil {
			t.Fatalf("type on a KubeVirt VM: %v", err)
		}
	})
	if got := rfbtest.Typed(server.Events()); got != "Secret!1\n" {
		t.Errorf("guest saw %q, want %q", got, "Secret!1\n")
	}
	if server.Sessions() != 1 {
		t.Errorf("opened %d sessions, want the text and Return on one", server.Sessions())
	}
	if strings.Contains(out, "Secret") {
		t.Errorf("--json printed the typed text: %s", out)
	}
	got := decodeJSON(t, out)
	if got["action"] != "type" || got["characters"] != float64(8) || got["enter"] != true {
		t.Errorf("--json = %v", got)
	}
}

func TestPointerOnKubeVirtClicks(t *testing.T) {
	server, fake := kubevirtConsole(t)
	fake.AddResponse("kubectl get vmi web -n corral-vms -o jsonpath={.spec.domain.devices.inputs[*].type}", "tablet", nil)
	withJSON(t)
	consoleFlags(t, false, "", true)
	out := captureStdout(t, func() {
		if err := pointerCmd.RunE(pointerCmd, []string{"web", "1236", "12"}); err != nil {
			t.Fatalf("pointer on a KubeVirt VM: %v", err)
		}
	})
	want := "[pointer (1236,12) mask=0 pointer (1236,12) mask=1 pointer (1236,12) mask=0 " +
		"pointer (1236,12) mask=1 pointer (1236,12) mask=0]"
	if got := fmt.Sprint(server.Events()); got != want {
		t.Errorf("events = %s\nwant %s", got, want)
	}
	got := decodeJSON(t, out)
	if got["action"] != "pointer" || got["x"] != float64(1236) || got["y"] != float64(12) ||
		got["button"] != "left" || got["double"] != true {
		t.Errorf("--json = %v", got)
	}
}

// Without --click the pointer only moves.
func TestPointerWithoutClickOnlyMoves(t *testing.T) {
	server, fake := kubevirtConsole(t)
	fake.AddResponse("kubectl get vmi web -n corral-vms -o jsonpath={.spec.domain.devices.inputs[*].type}", "keyboard tablet", nil)
	consoleFlags(t, false, "", false)
	if err := pointerCmd.RunE(pointerCmd, []string{"web", "640", "400"}); err != nil {
		t.Fatalf("pointer move: %v", err)
	}
	if got := fmt.Sprint(server.Events()); got != "[pointer (640,400) mask=0]" {
		t.Errorf("events = %s, want one move", got)
	}
}

func TestPointerRefusesBadArguments(t *testing.T) {
	kubevirtConsole(t)
	for _, tc := range []struct {
		args  []string
		click string
		want  string
	}{
		{[]string{"web", "-1", "0"}, "", "x must be"},
		{[]string{"web", "1", "top"}, "", "y must be"},
		{[]string{"web", "1", "1"}, "back", "--click must be left, middle, or right"},
	} {
		consoleFlags(t, false, tc.click, false)
		err := pointerCmd.RunE(pointerCmd, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("pointer %v --click %q = %v, want %q", tc.args, tc.click, err, tc.want)
		}
	}
}

// Local QEMU types through QMP, not VNC: the refusal names the missing
// monitor socket, so the call reached the QMP path.
func TestKeyOnQEMUUsesQMP(t *testing.T) {
	server, _ := kubevirtConsole(t)
	if err := registryStore.Set("local", types.RegistryEntry{Backend: "qemu"}); err != nil {
		t.Fatal(err)
	}
	err := keyCmd.RunE(keyCmd, []string{"local", "ret"})
	if err == nil || !strings.Contains(err.Error(), "no QMP socket") {
		t.Fatalf("key on a QEMU VM = %v, want the QMP path", err)
	}
	if server.Sessions() != 0 {
		t.Error("key on a QEMU VM opened a VNC session")
	}
}

// QEMU VMs have no tablet, so pointer is refused by backend name.
func TestPointerOnQEMUIsRefused(t *testing.T) {
	remoteVMScratch(t, "local", types.RegistryEntry{Backend: "qemu"})
	consoleFlags(t, false, "left", false)
	err := pointerCmd.RunE(pointerCmd, []string{"local", "1", "1"})
	if err == nil || !strings.Contains(err.Error(), "qemu backend does not support pointer") {
		t.Fatalf("pointer on a QEMU VM = %v", err)
	}
}

// A backend with no console input is refused by name for all three.
func TestConsoleInputRefusesOtherBackends(t *testing.T) {
	remoteVMScratch(t, "web", types.RegistryEntry{Backend: "incus", Context: "lab"})
	consoleFlags(t, false, "", false)
	for _, run := range []struct {
		action string
		err    error
	}{
		{"key", keyCmd.RunE(keyCmd, []string{"web", "ret"})},
		{"type", typeCmd.RunE(typeCmd, []string{"web", "x"})},
		{"pointer", pointerCmd.RunE(pointerCmd, []string{"web", "1", "1"})},
	} {
		want := "incus backend does not support " + run.action
		if run.err == nil || !strings.Contains(run.err.Error(), want) {
			t.Errorf("%s on an Incus VM = %v, want %q", run.action, run.err, want)
		}
	}
}
