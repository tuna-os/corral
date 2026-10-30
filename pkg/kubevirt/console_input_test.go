package kubevirt

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/rfb"
	"github.com/tuna-os/corral/pkg/rfb/rfbtest"
)

// vncDialer hands every console-input session to an in-memory RFB server and
// records what was dialed.
type vncDialer struct {
	server *rfbtest.Server
	err    error
	dialed []string
}

func (d *vncDialer) Dial(ns, name string, console Console) (net.Conn, error) {
	d.dialed = append(d.dialed, fmt.Sprintf("%s/%s/%d", ns, name, console))
	if d.err != nil {
		return nil, d.err
	}
	return d.server.Conn(), nil
}

func withVNCServer(t *testing.T) *vncDialer {
	t.Helper()
	d := &vncDialer{server: rfbtest.NewServer(1280, 800)}
	SetConsoleDialer(d)
	oldGap, oldCombo := rfb.EventGap, comboGap
	rfb.EventGap, comboGap = 0, 0
	t.Cleanup(func() {
		SetConsoleDialer(nil)
		rfb.EventGap, comboGap = oldGap, oldCombo
	})
	return d
}

const inputsQuery = "jsonpath={.spec.domain.devices.inputs[*].type}"

func TestPressKeysymsSendsEachComboOnOneSession(t *testing.T) {
	d := withVNCServer(t)
	c, _ := newFakeClient()
	if err := c.PressKeysyms("desk", [][]uint32{{0xffe3, 0xffe9, 0xffbf}, {0xff0d}}); err != nil {
		t.Fatalf("PressKeysyms: %v", err)
	}
	if d.server.Sessions() != 1 || len(d.dialed) != 1 || d.dialed[0] != "tailvm/desk/0" {
		t.Errorf("dialed %v (%d sessions), want one VNC session to tailvm/desk", d.dialed, d.server.Sessions())
	}
	got := fmt.Sprint(d.server.Events())
	want := "[key 0xffe3 down key 0xffe9 down key 0xffbf down key 0xffbf up key 0xffe9 up key 0xffe3 up " +
		"key 0xff0d down key 0xff0d up]"
	if got != want {
		t.Errorf("events = %s\nwant %s", got, want)
	}
}

func TestPressKeysymsReportsADeadConsole(t *testing.T) {
	d := withVNCServer(t)
	d.err = errors.New("proxy did not open")
	c, _ := newFakeClient()
	err := c.PressKeysyms("desk", [][]uint32{{0xff0d}})
	if err == nil || !strings.Contains(err.Error(), "VNC console of desk") {
		t.Fatalf("PressKeysyms = %v, want the dial failure", err)
	}
}

func TestClickNeedsATablet(t *testing.T) {
	d := withVNCServer(t)
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vmi", "desk", "-n", "tailvm", "-o", inputsQuery}, "", nil)
	err := c.Click("desk", 10, 10, "left", false)
	if err == nil || !strings.Contains(err.Error(), "no tablet") {
		t.Fatalf("Click without a tablet = %v, want a refusal", err)
	}
	if len(d.dialed) != 0 {
		t.Errorf("dialed %v for a VM without a tablet", d.dialed)
	}
}

func TestClickAndMoveWithATablet(t *testing.T) {
	d := withVNCServer(t)
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vmi", "desk", "-n", "tailvm", "-o", inputsQuery}, "tablet", nil)

	if err := c.Click("desk", 1236, 12, "right", false); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := c.MovePointer("desk", 640, 400); err != nil {
		t.Fatalf("MovePointer: %v", err)
	}
	got := fmt.Sprint(d.server.Events())
	want := "[pointer (1236,12) mask=0 pointer (1236,12) mask=4 pointer (1236,12) mask=0 pointer (640,400) mask=0]"
	if got != want {
		t.Errorf("events = %s\nwant %s", got, want)
	}

	if err := c.Click("desk", 1280, 12, "left", false); err == nil || !strings.Contains(err.Error(), "outside the 1280x800 screen") {
		t.Errorf("Click off the screen = %v, want a refusal", err)
	}
	if err := c.Click("desk", 1, 1, "back", false); err == nil || !strings.Contains(err.Error(), "unknown button") {
		t.Errorf("Click with an unknown button = %v, want a refusal", err)
	}
}
