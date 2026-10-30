package backend

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/rfb"
	"github.com/tuna-os/corral/pkg/rfb/rfbtest"
	"github.com/tuna-os/corral/pkg/types"
)

// Which backends drive a guest's console. Keyboarder and Clicker are not
// Families, so the matrix does not pin them; this does, so a backend gaining
// or losing console input is a visible change rather than a silent one.
func TestConsoleInputCapabilities(t *testing.T) {
	keyboard := map[string]bool{"kubevirt": true, "qemu": true}
	pointer := map[string]bool{"kubevirt": true}
	for _, backend := range Backends {
		adapter, err := probe(backend)
		if err != nil {
			t.Fatalf("probing %s: %v", backend, err)
		}
		if _, ok := adapter.(Keyboarder); ok != keyboard[backend] {
			t.Errorf("%s implements Keyboarder = %v, want %v", backend, ok, keyboard[backend])
		}
		if _, ok := adapter.(Clicker); ok != pointer[backend] {
			t.Errorf("%s implements Clicker = %v, want %v", backend, ok, pointer[backend])
		}
	}
}

type rfbDialer struct{ server *rfbtest.Server }

func (d rfbDialer) Dial(_, _ string, _ kubevirt.Console) (net.Conn, error) {
	return d.server.Conn(), nil
}

// kubevirtKeyboard routes a KubeVirt adapter's console to an in-memory RFB
// server.
func kubevirtKeyboard(t *testing.T) (Keyboarder, *rfbtest.Server) {
	t.Helper()
	server := rfbtest.NewServer(1280, 800)
	kubevirt.SetConsoleDialer(rfbDialer{server})
	oldGap := rfb.EventGap
	rfb.EventGap = 0
	t.Cleanup(func() { kubevirt.SetConsoleDialer(nil); rfb.EventGap = oldGap })

	adapter, err := For(types.InstanceRef{Backend: "kubevirt", Name: "desk", Namespace: "corral-vms"})
	if err != nil {
		t.Fatal(err)
	}
	return adapter.(Keyboarder), server
}

// KubeVirt types through the same US-keyboard table as the QMP path: shifted
// characters are wrapped in Shift, and a character the table cannot type is
// refused before anything is sent.
func TestKubeVirtTypeTextUsesTheQEMUKeyTable(t *testing.T) {
	keyboarder, server := kubevirtKeyboard(t)

	text := "Tuna-Q! a@b.c ~{x}\n"
	if err := keyboarder.TypeText("desk", text); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	// Typed models QEMU's Caps Lock sync: a lowercase keysym under Shift
	// would read back as lowercase here, as it did on a live guest.
	if got := rfbtest.Typed(server.Events()); got != text {
		t.Errorf("guest saw %q, want %q", got, text)
	}
	events := server.Events()
	if len(events) < 2 || events[0].Keysym != 0xffe1 || !events[0].Down || events[1].Keysym != 'T' {
		t.Errorf("first events = %v, want Shift_L down, then the keysym of T itself", events[:2])
	}

	sessions := server.Sessions()
	err := keyboarder.TypeText("desk", "café")
	if err == nil || !strings.Contains(err.Error(), "no US-keyboard mapping") {
		t.Errorf("TypeText with é = %v, want the QMP path's refusal", err)
	}
	if server.Sessions() != sessions {
		t.Error("a refused character still opened the console")
	}
}

// An unknown key name presses nothing at all, not even the known keys before
// it.
func TestKubeVirtPressKeysRefusesUnknownKeys(t *testing.T) {
	keyboarder, server := kubevirtKeyboard(t)
	err := keyboarder.PressKeys("desk", "ctrl", "hyper")
	if err == nil || !strings.Contains(err.Error(), `"hyper"`) {
		t.Fatalf("PressKeys = %v, want the unknown key named", err)
	}
	if server.Sessions() != 0 {
		t.Error("a refused key still opened the console")
	}

	if err := keyboarder.PressKeys("desk", "ctrl", "alt", "f2"); err != nil {
		t.Fatalf("PressKeys: %v", err)
	}
	want := "[key 0xffe3 down key 0xffe9 down key 0xffbf down key 0xffbf up key 0xffe9 up key 0xffe3 up]"
	if got := fmt.Sprint(server.Events()); got != want {
		t.Errorf("events = %s\nwant %s", got, want)
	}
}
