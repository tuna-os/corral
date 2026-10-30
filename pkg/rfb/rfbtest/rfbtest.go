// Package rfbtest is an in-memory RFB server for tests of console input: it
// plays QEMU's side of the handshake and records the key and pointer events
// a client sends, so a test can assert on exactly what a guest would see.
package rfbtest

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

// Event is one input event the server received.
type Event struct {
	// Key events.
	Key    bool
	Down   bool
	Keysym uint32
	// Pointer events.
	Mask uint8
	X, Y int
}

func (e Event) String() string {
	if e.Key {
		state := "up"
		if e.Down {
			state = "down"
		}
		return fmt.Sprintf("key %#x %s", e.Keysym, state)
	}
	return fmt.Sprintf("pointer (%d,%d) mask=%d", e.X, e.Y, e.Mask)
}

// Server records every session opened against it.
type Server struct {
	Width, Height int
	// Relative makes the server report a relative pointer, as QEMU does
	// while the guest is not reading its tablet.
	Relative bool

	mu       sync.Mutex
	events   []Event
	sessions int
}

// NewServer returns a server with a width x height framebuffer.
func NewServer(width, height int) *Server {
	return &Server{Width: width, Height: height}
}

// Conn opens a session: it returns the client end of an in-memory connection
// and serves the other end until the client hangs up.
func (s *Server) Conn() net.Conn {
	client, server := net.Pipe()
	s.mu.Lock()
	s.sessions++
	s.mu.Unlock()
	go s.serve(server)
	return client
}

// Events returns every input event received so far, across sessions.
func (s *Server) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

// Sessions reports how many connections were opened.
func (s *Server) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

func (s *Server) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return b, err
	}
	// Writes go through their own goroutine, as through a socket buffer: a
	// net.Pipe write blocks until the client reads, and a client sends its
	// events before it reads anything.
	out := make(chan []byte, 64)
	defer close(out)
	go func() {
		for b := range out {
			if _, err := conn.Write(b); err != nil {
				for range out {
				}
				return
			}
		}
	}()

	// ProtocolVersion, security None, SecurityResult OK.
	out <- []byte("RFB 003.008\n")
	if _, err := read(12); err != nil {
		return
	}
	out <- []byte{1, 1}
	if _, err := read(1); err != nil {
		return
	}
	out <- []byte{0, 0, 0, 0}
	// ClientInit, then ServerInit with a 32-bit pixel format.
	if _, err := read(1); err != nil {
		return
	}
	name := "rfbtest"
	init := make([]byte, 24, 24+len(name))
	binary.BigEndian.PutUint16(init[0:], uint16(s.Width))
	binary.BigEndian.PutUint16(init[2:], uint16(s.Height))
	init[4], init[5] = 32, 24
	binary.BigEndian.PutUint32(init[20:], uint32(len(name)))
	out <- append(init, name...)

	for {
		kind, err := r.ReadByte()
		if err != nil {
			return
		}
		switch kind {
		case 2: // SetEncodings: QEMU reports the pointer type at once.
			head, err := read(3)
			if err != nil {
				return
			}
			encodings, err := read(4 * int(binary.BigEndian.Uint16(head[1:])))
			if err != nil {
				return
			}
			for i := 0; i < len(encodings); i += 4 {
				if int32(binary.BigEndian.Uint32(encodings[i:])) == -257 {
					out <- s.pointerType()
				}
			}
		case 3: // FramebufferUpdateRequest: one Raw pixel.
			if _, err := read(9); err != nil {
				return
			}
			out <- []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}
		case 4: // KeyEvent
			b, err := read(7)
			if err != nil {
				return
			}
			s.record(Event{Key: true, Down: b[0] == 1, Keysym: binary.BigEndian.Uint32(b[3:])})
		case 5: // PointerEvent
			b, err := read(5)
			if err != nil {
				return
			}
			s.record(Event{Mask: b[0], X: int(binary.BigEndian.Uint16(b[1:])), Y: int(binary.BigEndian.Uint16(b[3:]))})
		default:
			return
		}
	}
}

// pointerType is QEMU's pointer type change update: one rectangle of
// encoding -257 whose x is 1 for absolute, 0 for relative.
func (s *Server) pointerType() []byte {
	absolute := byte(1)
	if s.Relative {
		absolute = 0
	}
	msg := []byte{0, 0, 0, 1, 0, absolute, 0, 0}
	msg = binary.BigEndian.AppendUint16(msg, uint16(s.Width))
	msg = binary.BigEndian.AppendUint16(msg, uint16(s.Height))
	pointerType := int32(-257)
	return binary.BigEndian.AppendUint32(msg, uint32(pointerType))
}

func (s *Server) record(e Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

// Typed returns the characters a guest would receive from the key events,
// modelled on QEMU's VNC server with a US keymap, so a test reads back what
// `corral type` typed:
//
//   - A printable keysym presses its physical key, and Shift held at that
//     moment decides which of the key's two characters the guest gets: '!'
//     sent without Shift arrives as '1'.
//   - A letter keysym whose case disagrees with Shift makes QEMU toggle Caps
//     Lock first (its lock-key sync), so 'a' sent with Shift arrives as 'a'.
//   - Return arrives as a newline.
func Typed(events []Event) string {
	const shiftL, shiftR, ret = 0xffe1, 0xffe2, 0xff0d
	shiftOf := map[rune]rune{
		'1': '!', '2': '@', '3': '#', '4': '$', '5': '%', '6': '^', '7': '&',
		'8': '*', '9': '(', '0': ')', '-': '_', '=': '+', '[': '{', ']': '}',
		'\\': '|', ';': ':', '\'': '"', ',': '<', '.': '>', '/': '?', '`': '~',
	}
	keyOf := map[rune]rune{}
	for key, shifted := range shiftOf {
		keyOf[shifted] = key
	}
	var out []rune
	shift, capsLock := false, false
	for _, e := range events {
		if !e.Key {
			continue
		}
		switch {
		case e.Keysym == shiftL || e.Keysym == shiftR:
			shift = e.Down
		case !e.Down:
		case e.Keysym == ret:
			out = append(out, '\n')
		case e.Keysym >= 'A' && e.Keysym <= 'Z' || e.Keysym >= 'a' && e.Keysym <= 'z':
			r := rune(e.Keysym)
			upper := r <= 'Z'
			if upper != (shift != capsLock) {
				capsLock = !capsLock
			}
			lower := r | 0x20
			if shift != capsLock {
				lower -= 'a' - 'A'
			}
			out = append(out, lower)
		case e.Keysym >= 0x20 && e.Keysym < 0x7f:
			key := rune(e.Keysym)
			if k, ok := keyOf[key]; ok {
				key = k
			}
			if s, ok := shiftOf[key]; ok && shift {
				key = s
			}
			out = append(out, key)
		}
	}
	return string(out)
}
