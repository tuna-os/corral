package rfb

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func init() { EventGap = 0 }

// fakeServer is the server side of an in-memory RFB session, shaped like
// QEMU's VNC server: version 3.8 (or what the test sets), security None, a
// 32-bit pixel format.
type fakeServer struct {
	conn   net.Conn
	r      *bufio.Reader
	t      *testing.T
	width  uint16
	height uint16
	name   string
	// closed is set once the client hung up; reads then return zeros.
	closed bool
}

func newSession(t *testing.T) (*fakeServer, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return &fakeServer{conn: server, r: bufio.NewReader(server), t: t,
		width: 1280, height: 800, name: "QEMU (tuna)"}, client
}

func (s *fakeServer) write(b []byte) {
	if _, err := s.conn.Write(b); err != nil {
		s.closed = true
	}
}

// read returns n bytes, or zeros once the client has hung up: a test that
// expects a refusal closes the pipe while the server still waits.
func (s *fakeServer) read(n int) []byte {
	b := make([]byte, n)
	if _, err := io.ReadFull(s.r, b); err != nil {
		s.closed = true
	}
	return b
}

func (s *fakeServer) serverInit() {
	init := make([]byte, 24)
	binary.BigEndian.PutUint16(init[0:], s.width)
	binary.BigEndian.PutUint16(init[2:], s.height)
	init[4] = 32 // bits per pixel
	init[5] = 24 // depth
	binary.BigEndian.PutUint32(init[20:], uint32(len(s.name)))
	s.write(append(init, s.name...))
}

// handshake38 plays QEMU's side of a 3.8 handshake and returns the shared
// flag the client sent in ClientInit.
func (s *fakeServer) handshake38() byte {
	s.write([]byte("RFB 003.008\n"))
	if got := string(s.read(12)); got != "RFB 003.008\n" {
		s.t.Errorf("client version = %q, want 3.8", got)
	}
	s.write([]byte{1, securityNone})
	if got := s.read(1)[0]; got != securityNone {
		s.t.Errorf("client chose security %d, want None", got)
	}
	s.write([]byte{0, 0, 0, 0}) // SecurityResult OK
	shared := s.read(1)[0]
	s.serverInit()
	if got := s.read(8); got[0] != msgSetEncodings || binary.BigEndian.Uint16(got[2:]) != 1 ||
		binary.BigEndian.Uint32(got[4:]) != encodingRaw {
		s.t.Errorf("SetEncodings = %v, want Raw only", got)
	}
	return shared
}

// event is one decoded client input message.
type event struct {
	kind   byte
	down   bool
	keysym uint32
	mask   uint8
	x, y   int
}

func (e event) String() string {
	if e.kind == msgKeyEvent {
		return fmt.Sprintf("key %#x down=%v", e.keysym, e.down)
	}
	return fmt.Sprintf("pointer (%d,%d) mask=%d", e.x, e.y, e.mask)
}

// readUntilSync collects input events up to the client's Sync request and
// answers it with a one-rectangle Raw update, after a bell and cut text a
// real server may interleave.
func (s *fakeServer) readUntilSync() []event {
	var events []event
	for {
		kind := s.read(1)[0]
		if s.closed {
			return events
		}
		switch kind {
		case msgKeyEvent:
			b := s.read(7)
			events = append(events, event{kind: kind, down: b[0] == 1, keysym: binary.BigEndian.Uint32(b[3:])})
		case msgPointerEvent:
			b := s.read(5)
			events = append(events, event{kind: kind, mask: b[0],
				x: int(binary.BigEndian.Uint16(b[1:])), y: int(binary.BigEndian.Uint16(b[3:]))})
		case msgFramebufferUpdateRequest:
			b := s.read(9)
			if b[0] != 0 {
				s.t.Errorf("Sync sent an incremental request; the server may never answer it")
			}
			s.write([]byte{msgBell})
			s.write([]byte{msgServerCutText, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c'})
			// QEMU rounds a dirty region up to 16 pixels wide.
			update := []byte{msgFramebufferUpdate, 0, 0, 1, 0, 0, 0, 0, 0, 16, 0, 1, 0, 0, 0, 0}
			s.write(append(update, make([]byte, 16*4)...))
			return events
		default:
			s.t.Errorf("unexpected client message %d", kind)
			return events
		}
	}
}

func handshakeWith(t *testing.T, serve func(*fakeServer)) (*Client, error) {
	t.Helper()
	s, conn := newSession(t)
	done := make(chan struct{})
	go func() { defer close(done); serve(s) }()
	c, err := Handshake(conn)
	<-done
	return c, err
}

func TestHandshake38(t *testing.T) {
	var shared byte
	c, err := handshakeWith(t, func(s *fakeServer) { shared = s.handshake38() })
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if c.Width != 1280 || c.Height != 800 || c.Name != "QEMU (tuna)" {
		t.Errorf("ServerInit = %dx%d %q, want 1280x800 \"QEMU (tuna)\"", c.Width, c.Height, c.Name)
	}
	if shared != 1 {
		t.Errorf("ClientInit shared = %d, want 1 so an open web console stays connected", shared)
	}
}

// A 3.3 server picks the security type itself and sends no result.
func TestHandshake33(t *testing.T) {
	c, err := handshakeWith(t, func(s *fakeServer) {
		s.write([]byte("RFB 003.003\n"))
		if got := string(s.read(12)); got != "RFB 003.003\n" {
			t.Errorf("client version = %q, want 3.3", got)
		}
		s.write([]byte{0, 0, 0, securityNone})
		s.read(1)
		s.width, s.height = 1024, 768
		s.serverInit()
		s.read(8)
	})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if c.Width != 1024 || c.Height != 768 {
		t.Errorf("size = %dx%d, want 1024x768", c.Width, c.Height)
	}
}

func TestHandshakeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve func(*fakeServer)
		want  string
	}{
		{"not RFB", func(s *fakeServer) { s.write([]byte("SSH-2.0-xyz\n")) }, "not an RFB 3.x version"},
		{"password only", func(s *fakeServer) {
			s.write([]byte("RFB 003.008\n"))
			s.read(12)
			s.write([]byte{1, 2}) // VNC authentication
		}, "only None"},
		{"refused with reason", func(s *fakeServer) {
			s.write([]byte("RFB 003.008\n"))
			s.read(12)
			s.write([]byte{0, 0, 0, 0, 8})
			s.write([]byte("too many"))
		}, "too many"},
		{"security failed", func(s *fakeServer) {
			s.write([]byte("RFB 003.008\n"))
			s.read(12)
			s.write([]byte{1, securityNone})
			s.read(1)
			s.write([]byte{0, 0, 0, 1, 0, 0, 0, 4})
			s.write([]byte("nope"))
		}, "security handshake failed: nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handshakeWith(t, tc.serve)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Handshake = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// A server that accepts and then says nothing fails the handshake instead of
// hanging the command.
func TestHandshakeTimesOut(t *testing.T) {
	old := HandshakeTimeout
	HandshakeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { HandshakeTimeout = old })
	_, conn := newSession(t)
	if _, err := Handshake(conn); err == nil {
		t.Fatal("a silent server should fail the handshake")
	}
}

// session runs fn against a client that completed a 3.8 handshake and
// returns the input events the server saw before fn's Sync.
func session(t *testing.T, fn func(*Client) error) ([]event, error) {
	t.Helper()
	s, conn := newSession(t)
	events := make(chan []event, 1)
	go func() {
		s.handshake38()
		events <- s.readUntilSync()
	}()
	c, err := Handshake(conn)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	if err := c.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return <-events, nil
}

func TestPressHoldsInOrderAndReleasesInReverse(t *testing.T) {
	syms, err := Keysyms([]string{"ctrl", "alt", "f2"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := session(t, func(c *Client) error { return c.Press(syms) })
	if err != nil {
		t.Fatal(err)
	}
	want := []event{
		{kind: msgKeyEvent, down: true, keysym: 0xffe3},
		{kind: msgKeyEvent, down: true, keysym: 0xffe9},
		{kind: msgKeyEvent, down: true, keysym: 0xffbf},
		{kind: msgKeyEvent, keysym: 0xffbf},
		{kind: msgKeyEvent, keysym: 0xffe9},
		{kind: msgKeyEvent, keysym: 0xffe3},
	}
	assertEvents(t, events, want)
}

func TestClickMovesThenPressesAndReleases(t *testing.T) {
	events, err := session(t, func(c *Client) error { return c.Click(1236, 12, ButtonLeft, false) })
	if err != nil {
		t.Fatal(err)
	}
	assertEvents(t, events, []event{
		{kind: msgPointerEvent, x: 1236, y: 12},
		{kind: msgPointerEvent, x: 1236, y: 12, mask: ButtonLeft},
		{kind: msgPointerEvent, x: 1236, y: 12},
	})
}

func TestDoubleClickClicksTwice(t *testing.T) {
	events, err := session(t, func(c *Client) error { return c.Click(10, 20, ButtonRight, true) })
	if err != nil {
		t.Fatal(err)
	}
	assertEvents(t, events, []event{
		{kind: msgPointerEvent, x: 10, y: 20},
		{kind: msgPointerEvent, x: 10, y: 20, mask: ButtonRight},
		{kind: msgPointerEvent, x: 10, y: 20},
		{kind: msgPointerEvent, x: 10, y: 20, mask: ButtonRight},
		{kind: msgPointerEvent, x: 10, y: 20},
	})
}

// A position off the screen is refused before anything is sent: the server
// would clamp it to an edge and click somewhere else.
func TestPointerOutsideTheScreenIsRefused(t *testing.T) {
	for _, pos := range [][2]int{{1280, 10}, {10, 800}, {-1, 0}} {
		_, err := session(t, func(c *Client) error { return c.Click(pos[0], pos[1], ButtonLeft, false) })
		if err == nil || !strings.Contains(err.Error(), "outside the 1280x800 screen") {
			t.Errorf("Click%v = %v, want an outside-the-screen refusal", pos, err)
		}
	}
}

func TestKeysymsRefusesUnknownNames(t *testing.T) {
	if _, err := Keysyms([]string{"ctrl", "hyper"}); err == nil || !strings.Contains(err.Error(), `"hyper"`) {
		t.Errorf("Keysyms with an unknown name = %v, want it named", err)
	}
	if _, err := Keysyms(nil); err == nil {
		t.Error("Keysyms with no names should fail")
	}
}

// A typed character goes out as its own keysym under the modifiers, as a VNC
// viewer sends it; Return and Tab keep their key's keysym.
func TestCharKeysyms(t *testing.T) {
	for _, tc := range []struct {
		r    rune
		keys []string
		want []uint32
	}{
		{'T', []string{"shift", "t"}, []uint32{0xffe1, 'T'}},
		{'!', []string{"shift", "1"}, []uint32{0xffe1, '!'}},
		{'a', []string{"a"}, []uint32{'a'}},
		{' ', []string{"spc"}, []uint32{' '}},
		{'\n', []string{"ret"}, []uint32{0xff0d}},
		{'\t', []string{"tab"}, []uint32{0xff09}},
	} {
		got, err := CharKeysyms(tc.r, tc.keys)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("CharKeysyms(%q, %v) = %#x, %v; want %#x", tc.r, tc.keys, got, err, tc.want)
		}
	}
}

// The names `corral key` documents map to the keysyms of those keys.
func TestKeysymTable(t *testing.T) {
	for name, want := range map[string]uint32{
		"ret": 0xff0d, "esc": 0xff1b, "tab": 0xff09, "spc": 0x20, "backspace": 0xff08,
		"delete": 0xffff, "shift": 0xffe1, "ctrl": 0xffe3, "alt": 0xffe9,
		"meta_l": 0xffeb, "meta_r": 0xffec, "f1": 0xffbe, "f12": 0xffc9, "f13": 0xffca,
		"up": 0xff52, "down": 0xff54, "left": 0xff51, "right": 0xff53,
		"home": 0xff50, "end": 0xff57, "pgup": 0xff55, "pgdn": 0xff56,
		"a": 'a', "z": 'z', "0": '0', "9": '9', "minus": '-', "slash": '/',
		"kp_0": 0xffb0, "kp_enter": 0xff8d,
	} {
		got, ok := Keysym(name)
		if !ok || got != want {
			t.Errorf("Keysym(%q) = %#x, %v; want %#x", name, got, ok, want)
		}
	}
}

func TestButton(t *testing.T) {
	for name, want := range map[string]uint8{"left": 1, "middle": 2, "right": 4} {
		if got, err := Button(name); err != nil || got != want {
			t.Errorf("Button(%q) = %d, %v; want %d", name, got, err, want)
		}
	}
	if _, err := Button("back"); err == nil {
		t.Error("Button(back) should fail")
	}
}

func assertEvents(t *testing.T, got, want []event) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("events =\n  %v\nwant\n  %v", got, want)
	}
}
