package rfb

import (
	"fmt"
	"time"
)

// Console input takes QEMU's key names (the QKeyCode vocabulary `corral key`
// has always used over QMP) so one command line drives a local QEMU VM and a
// KubeVirt VM the same way. RFB carries X11 keysyms, so each name maps to the
// keysym of that physical key on a US keyboard. QEMU's VNC server turns the
// keysym back into the same key, which is what a VNC viewer relies on.

// keysyms maps QEMU qcode names to X11 keysyms. Letters and digits are added
// in init.
var keysyms = map[string]uint32{
	// Modifiers.
	"shift": 0xffe1, "shift_r": 0xffe2,
	"ctrl": 0xffe3, "ctrl_r": 0xffe4,
	"alt": 0xffe9, "alt_r": 0xffea,
	"meta_l": 0xffeb, "meta_r": 0xffec, // Super_L, Super_R
	"menu": 0xff67, "compose": 0xff20,
	"caps_lock": 0xffe5, "num_lock": 0xff7f, "scroll_lock": 0xff14,

	// Editing and whitespace.
	"esc": 0xff1b, "backspace": 0xff08, "tab": 0xff09, "ret": 0xff0d,
	"spc": 0x0020, "insert": 0xff63, "delete": 0xffff,

	// Navigation.
	"home": 0xff50, "end": 0xff57, "pgup": 0xff55, "pgdn": 0xff56,
	"left": 0xff51, "up": 0xff52, "right": 0xff53, "down": 0xff54,

	// System.
	"print": 0xff61, "sysrq": 0xff15, "pause": 0xff13,
	"help": 0xff6a, "undo": 0xff65, "again": 0xff66, "find": 0xff68, "stop": 0xff69,

	// Punctuation, unshifted.
	"minus": '-', "equal": '=', "bracket_left": '[', "bracket_right": ']',
	"semicolon": ';', "apostrophe": '\'', "grave_accent": '`', "backslash": '\\',
	"comma": ',', "dot": '.', "slash": '/',

	// Keypad. QEMU names keypad multiply "asterisk".
	"kp_divide": 0xffaf, "kp_multiply": 0xffaa, "asterisk": 0xffaa,
	"kp_subtract": 0xffad, "kp_add": 0xffab, "kp_enter": 0xff8d,
	"kp_decimal": 0xffae, "kp_comma": 0xffac, "kp_equals": 0xffbd,

	// Media and launch keys (XF86 keysyms).
	"audiomute": 0x1008ff12, "volumedown": 0x1008ff11, "volumeup": 0x1008ff13,
	"audioplay": 0x1008ff14, "audiostop": 0x1008ff15,
	"audioprev": 0x1008ff16, "audionext": 0x1008ff17,
	"power": 0x1008ff2a, "sleep": 0x1008ff2f, "wake": 0x1008ff2b,
	"mail": 0x1008ff19, "calculator": 0x1008ff1d, "computer": 0x1008ff33,
	"ac_home": 0x1008ff18, "ac_back": 0x1008ff26, "ac_forward": 0x1008ff27,
	"ac_refresh": 0x1008ff29, "ac_bookmarks": 0x1008ff30,
	"copy": 0x1008ff57, "cut": 0x1008ff58, "paste": 0x1008ff6d, "open": 0x1008ff6b,
}

func init() {
	for r := 'a'; r <= 'z'; r++ {
		keysyms[string(r)] = uint32(r)
	}
	for r := '0'; r <= '9'; r++ {
		keysyms[string(r)] = uint32(r)
	}
	for i := range 12 {
		keysyms[fmt.Sprintf("f%d", i+1)] = 0xffbe + uint32(i)  // F1..F12
		keysyms[fmt.Sprintf("f%d", i+13)] = 0xffca + uint32(i) // F13..F24
	}
	for i := range 10 {
		keysyms[fmt.Sprintf("kp_%d", i)] = 0xffb0 + uint32(i)
	}
}

// Keysym returns the X11 keysym for a QEMU key name.
func Keysym(name string) (uint32, bool) {
	sym, ok := keysyms[name]
	return sym, ok
}

// Keysyms maps a combination of QEMU key names to keysyms, refusing the whole
// combination if one name is unknown, so nothing is half-pressed.
func Keysyms(names []string) ([]uint32, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no keys given")
	}
	out := make([]uint32, 0, len(names))
	for _, name := range names {
		sym, ok := Keysym(name)
		if !ok {
			return nil, fmt.Errorf("unknown key %q: use a QEMU key name such as ret, esc, tab, spc, ctrl, alt, shift, meta_l, f1, up, or a letter or digit", name)
		}
		out = append(out, sym)
	}
	return out, nil
}

// CharKeysyms returns the keysyms that type r, given the QEMU key names that
// produce it on a US keyboard (["shift", "t"] for 'T'). The modifiers map as
// usual, but a printable character is sent as its own keysym, as a VNC
// viewer sends it: 'T' rather than 't'. QEMU's VNC server infers Caps Lock
// from a letter keysym's case against the Shift state, so "Shift + t" makes
// it toggle Caps Lock and the guest types a lowercase t.
func CharKeysyms(r rune, keys []string) ([]uint32, error) {
	syms, err := Keysyms(keys)
	if err != nil {
		return nil, err
	}
	if r >= 0x20 && r <= 0x7e {
		syms[len(syms)-1] = uint32(r)
	}
	return syms, nil
}

// EventGap is the pause between input events. A guest's input stack can
// drop or reorder events that arrive faster than it polls, and the QMP path
// waits the same way between keys. A var so tests run without the sleeps.
var EventGap = 15 * time.Millisecond

// Press presses a key combination: every key down in order, then every key
// up in reverse, as a person holds ctrl and alt before pressing delete.
func (c *Client) Press(syms []uint32) error {
	for _, sym := range syms {
		if err := c.KeyEvent(sym, true); err != nil {
			return err
		}
		time.Sleep(EventGap)
	}
	for i := len(syms) - 1; i >= 0; i-- {
		if err := c.KeyEvent(syms[i], false); err != nil {
			return err
		}
		time.Sleep(EventGap)
	}
	return nil
}

// Pointer buttons, as RFB's button mask bits.
const (
	ButtonLeft   uint8 = 1 << 0
	ButtonMiddle uint8 = 1 << 1
	ButtonRight  uint8 = 1 << 2
)

// Button returns the mask bit for a button name: left, middle, or right.
func Button(name string) (uint8, error) {
	switch name {
	case "left":
		return ButtonLeft, nil
	case "middle":
		return ButtonMiddle, nil
	case "right":
		return ButtonRight, nil
	}
	return 0, fmt.Errorf("unknown button %q: use left, middle, or right", name)
}

// doubleClickGap separates the two clicks of a double click: long enough to
// be two presses, well inside GTK's and Qt's 400 ms double-click window.
const doubleClickGap = 80 * time.Millisecond

// Click moves the pointer to (x, y) and clicks button there, twice when
// double is set. The move comes first, with no button held, so the guest
// sees the pointer arrive and then the press, as from a real mouse.
func (c *Client) Click(x, y int, button uint8, double bool) error {
	if err := c.PointerEvent(x, y, 0); err != nil {
		return err
	}
	time.Sleep(EventGap)
	clicks := 1
	if double {
		clicks = 2
	}
	for i := range clicks {
		if i > 0 {
			time.Sleep(doubleClickGap)
		}
		if err := c.PointerEvent(x, y, button); err != nil {
			return err
		}
		time.Sleep(EventGap)
		if err := c.PointerEvent(x, y, 0); err != nil {
			return err
		}
		time.Sleep(EventGap)
	}
	return nil
}
