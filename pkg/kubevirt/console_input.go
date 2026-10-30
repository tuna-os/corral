package kubevirt

import (
	"fmt"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/rfb"
)

// Console input: keys, text, and pointer events at a running VMI's display,
// sent over the same VNC console the web UI's noVNC opens (virtctl vnc
// --proxy-only, then RFB to QEMU's VNC server). It reaches a guest that has
// no SSH yet: a greeter, a LUKS prompt, a desktop under test.

// consoleInputDialer is the seam console input dials through. Nil means the
// real virtctl proxy for the client's context; tests set a fake that hands
// back one end of a net.Pipe.
var consoleInputDialer ConsoleDialer

// SetConsoleDialer overrides how console input reaches a VM's VNC server
// (for unit tests). Nil restores the real virtctl proxy.
func SetConsoleDialer(d ConsoleDialer) { consoleInputDialer = d }

// comboGap is the pause between key combinations, as between characters
// when typing. The QMP path waits the same 30 ms.
var comboGap = 30 * time.Millisecond

// openVNC dials the VMI's VNC console and completes the RFB handshake.
func (c *Client) openVNC(name string) (*rfb.Client, error) {
	dialer := consoleInputDialer
	if dialer == nil {
		if _, err := c.ensureVirtctl(); err != nil {
			return nil, err
		}
		dialer = RealConsoleDialer{Context: c.Context}
	}
	conn, err := dialer.Dial(c.Namespace, name, VNC)
	if err != nil {
		return nil, fmt.Errorf("opening the VNC console of %s: %w", name, err)
	}
	session, err := rfb.Handshake(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("VNC console of %s: %w (is the VM running?)", name, err)
	}
	return session, nil
}

// withVNC runs fn on one VNC session and waits for the server to confirm
// every event before it closes the session, so the last key-up is not lost.
func (c *Client) withVNC(name string, fn func(*rfb.Client) error) error {
	session, err := c.openVNC(name)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()
	if err := fn(session); err != nil {
		return err
	}
	return session.Sync()
}

// PressKeysyms presses each combination of X11 keysyms in turn, on one VNC
// session: rfb.Keysyms maps `corral key` names, rfb.CharKeysyms typed text.
func (c *Client) PressKeysyms(name string, combos [][]uint32) error {
	return c.withVNC(name, func(session *rfb.Client) error {
		for i, combo := range combos {
			if i > 0 {
				time.Sleep(comboGap)
			}
			if err := session.Press(combo); err != nil {
				return err
			}
		}
		return nil
	})
}

// MovePointer moves the pointer to (x, y) in framebuffer pixels.
func (c *Client) MovePointer(name string, x, y int) error {
	if err := c.requireTablet(name); err != nil {
		return err
	}
	return c.withVNC(name, func(session *rfb.Client) error {
		if err := session.RequireAbsolute(); err != nil {
			return err
		}
		return session.PointerEvent(x, y, 0)
	})
}

// Click moves the pointer to (x, y) and clicks button (left, middle, or
// right) there, twice when double is set.
func (c *Client) Click(name string, x, y int, button string, double bool) error {
	mask, err := rfb.Button(button)
	if err != nil {
		return err
	}
	if err := c.requireTablet(name); err != nil {
		return err
	}
	return c.withVNC(name, func(session *rfb.Client) error {
		if err := session.RequireAbsolute(); err != nil {
			return err
		}
		return session.Click(x, y, mask, double)
	})
}

// requireTablet refuses pointer input to a VMI without a tablet. QEMU's VNC
// server turns an absolute position into relative motion for a plain mouse,
// so the click would land somewhere else and the command would still report
// success.
func (c *Client) requireTablet(name string) error {
	out, err := c.runner().Run("kubectl", "get", "vmi", name, "-n", c.Namespace,
		"-o", "jsonpath={.spec.domain.devices.inputs[*].type}")
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("reading the input devices of %s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("reading the input devices of %s: %w", name, err)
	}
	for _, kind := range strings.Fields(string(out)) {
		if kind == "tablet" {
			return nil
		}
	}
	return fmt.Errorf("%s has no tablet input device, so a pointer position cannot be placed exactly; "+
		"add one under spec.template.spec.domain.devices.inputs "+
		"({type: tablet, bus: usb, name: tablet}) and restart the VM", name)
}
