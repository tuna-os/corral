// Package rfb is a minimal RFB (VNC) client for console input: it completes
// the handshake, sends key and pointer events, and confirms the server took
// them. It decodes no pixels — `corral screenshot` reads the screen through
// each backend's own capture path — so it stays small enough to read in one
// sitting.
//
// The servers it targets are QEMU's built-in VNC server, reached directly or
// through KubeVirt's vnc subresource. Those speak RFB 3.8 with security type
// None; older 3.3 and 3.7 servers are accepted too because the difference is
// a few bytes of handshake.
package rfb

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Client is one RFB session after a completed handshake.
type Client struct {
	conn   net.Conn
	reader *bufio.Reader

	// Width and Height are the framebuffer size from ServerInit: the bounds
	// a pointer event must stay inside.
	Width, Height int
	// Name is the desktop name the server announced.
	Name string

	bytesPerPixel int
}

// Security types and message numbers from RFC 6143.
const (
	securityInvalid = 0
	securityNone    = 1

	msgSetEncodings             = 2
	msgFramebufferUpdateRequest = 3
	msgKeyEvent                 = 4
	msgPointerEvent             = 5

	msgFramebufferUpdate   = 0
	msgSetColourMapEntries = 1
	msgBell                = 2
	msgServerCutText       = 3

	encodingRaw = 0
)

// HandshakeTimeout bounds the handshake and each Sync, so a proxy that
// accepts the connection and never forwards it fails instead of hanging.
var HandshakeTimeout = 15 * time.Second

// Handshake runs the RFB handshake on conn as a shared client, so the VNC
// server itself keeps any other viewer connected (a proxy in front of it,
// such as KubeVirt's, may still allow one session at a time). The caller
// keeps ownership of conn until Handshake succeeds; after that, Close closes
// it.
func Handshake(conn net.Conn) (*Client, error) {
	c := &Client{conn: conn, reader: bufio.NewReader(conn)}
	_ = conn.SetDeadline(time.Now().Add(HandshakeTimeout))
	if err := c.handshake(); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

func (c *Client) handshake() error {
	version := make([]byte, 12)
	if _, err := io.ReadFull(c.reader, version); err != nil {
		return fmt.Errorf("rfb: reading server version: %w", err)
	}
	var major, minor int
	if _, err := fmt.Sscanf(string(version), "RFB %03d.%03d\n", &major, &minor); err != nil || major != 3 {
		return fmt.Errorf("rfb: server sent %q, not an RFB 3.x version", version)
	}
	// Answer with the highest version both sides know. A 3.x server newer
	// than 3.8 is answered as 3.8, as the protocol asks.
	switch {
	case minor >= 8:
		minor = 8
	case minor == 7:
	default:
		minor = 3
	}
	if _, err := fmt.Fprintf(c.conn, "RFB 003.%03d\n", minor); err != nil {
		return fmt.Errorf("rfb: sending client version: %w", err)
	}

	if minor == 3 {
		// 3.3: the server chooses the security type alone.
		var chosen uint32
		if err := binary.Read(c.reader, binary.BigEndian, &chosen); err != nil {
			return fmt.Errorf("rfb: reading security type: %w", err)
		}
		if chosen == securityInvalid {
			return c.failure("server refused the connection")
		}
		if chosen != securityNone {
			return fmt.Errorf("rfb: server requires security type %d; only None (no password) is supported", chosen)
		}
	} else {
		count, err := c.reader.ReadByte()
		if err != nil {
			return fmt.Errorf("rfb: reading security types: %w", err)
		}
		if count == 0 {
			return c.failure("server refused the connection")
		}
		types := make([]byte, count)
		if _, err := io.ReadFull(c.reader, types); err != nil {
			return fmt.Errorf("rfb: reading security types: %w", err)
		}
		if !containsByte(types, securityNone) {
			return fmt.Errorf("rfb: server offers security types %v; only None (no password) is supported", types)
		}
		if _, err := c.conn.Write([]byte{securityNone}); err != nil {
			return fmt.Errorf("rfb: choosing security type: %w", err)
		}
		// 3.8 reports the result of every security type, None included.
		if minor == 8 {
			var result uint32
			if err := binary.Read(c.reader, binary.BigEndian, &result); err != nil {
				return fmt.Errorf("rfb: reading security result: %w", err)
			}
			if result != 0 {
				return c.failure("security handshake failed")
			}
		}
	}

	// ClientInit: shared, so the server does not drop other viewers for us.
	if _, err := c.conn.Write([]byte{1}); err != nil {
		return fmt.Errorf("rfb: sending ClientInit: %w", err)
	}

	var init struct {
		Width, Height uint16
		BitsPerPixel  uint8
		_             [15]byte // rest of PIXEL_FORMAT
		NameLength    uint32
	}
	if err := binary.Read(c.reader, binary.BigEndian, &init); err != nil {
		return fmt.Errorf("rfb: reading ServerInit: %w", err)
	}
	if init.NameLength > 1<<16 {
		return fmt.Errorf("rfb: ServerInit name length %d is not plausible", init.NameLength)
	}
	name := make([]byte, init.NameLength)
	if _, err := io.ReadFull(c.reader, name); err != nil {
		return fmt.Errorf("rfb: reading desktop name: %w", err)
	}
	if init.BitsPerPixel == 0 || init.BitsPerPixel%8 != 0 {
		return fmt.Errorf("rfb: ServerInit has %d bits per pixel", init.BitsPerPixel)
	}
	c.Width, c.Height, c.Name = int(init.Width), int(init.Height), string(name)
	c.bytesPerPixel = int(init.BitsPerPixel) / 8

	// Ask for Raw only, so the one update Sync reads has a size this client
	// can compute without decoding anything.
	setEncodings := []byte{msgSetEncodings, 0, 0, 1, 0, 0, 0, encodingRaw}
	if _, err := c.conn.Write(setEncodings); err != nil {
		return fmt.Errorf("rfb: sending SetEncodings: %w", err)
	}
	return nil
}

// failure reads the reason string a 3.7+ server sends after refusing, and a
// 3.3 server's equivalent, into the error.
func (c *Client) failure(what string) error {
	var n uint32
	if err := binary.Read(c.reader, binary.BigEndian, &n); err != nil || n > 1<<16 {
		return fmt.Errorf("rfb: %s", what)
	}
	reason := make([]byte, n)
	if _, err := io.ReadFull(c.reader, reason); err != nil {
		return fmt.Errorf("rfb: %s", what)
	}
	return fmt.Errorf("rfb: %s: %s", what, reason)
}

// KeyEvent presses (down) or releases a key, named by its X11 keysym.
func (c *Client) KeyEvent(keysym uint32, down bool) error {
	msg := make([]byte, 8)
	msg[0] = msgKeyEvent
	if down {
		msg[1] = 1
	}
	binary.BigEndian.PutUint32(msg[4:], keysym)
	if _, err := c.conn.Write(msg); err != nil {
		return fmt.Errorf("rfb: sending key event: %w", err)
	}
	return nil
}

// PointerEvent moves the pointer to (x, y) in framebuffer pixels with the
// buttons in mask held: bit 0 is the left button, bit 1 the middle, bit 2
// the right. Positions outside the framebuffer are refused, because the
// server would clamp them to an edge and the click would land somewhere the
// caller did not ask for.
func (c *Client) PointerEvent(x, y int, mask uint8) error {
	if err := c.InBounds(x, y); err != nil {
		return err
	}
	msg := make([]byte, 6)
	msg[0] = msgPointerEvent
	msg[1] = mask
	binary.BigEndian.PutUint16(msg[2:], uint16(x))
	binary.BigEndian.PutUint16(msg[4:], uint16(y))
	if _, err := c.conn.Write(msg); err != nil {
		return fmt.Errorf("rfb: sending pointer event: %w", err)
	}
	return nil
}

// InBounds reports whether (x, y) is a pixel of the framebuffer.
func (c *Client) InBounds(x, y int) error {
	if x < 0 || y < 0 || x >= c.Width || y >= c.Height {
		return fmt.Errorf("position (%d, %d) is outside the %dx%d screen", x, y, c.Width, c.Height)
	}
	return nil
}

// Sync returns once the server has handled every event sent before it. RFB
// has no acknowledgement for input, but a server answers messages in order,
// so the reply to a one-pixel FramebufferUpdateRequest proves the events
// ahead of it arrived. Without this, closing right after the last key-up can
// drop it in a proxy, and the guest sees a key held down.
func (c *Client) Sync() error {
	_ = c.conn.SetDeadline(time.Now().Add(HandshakeTimeout))
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	// Non-incremental, so the server replies even when nothing changed.
	req := []byte{msgFramebufferUpdateRequest, 0, 0, 0, 0, 0, 0, 1, 0, 1}
	if _, err := c.conn.Write(req); err != nil {
		return fmt.Errorf("rfb: sending update request: %w", err)
	}
	for {
		kind, err := c.reader.ReadByte()
		if err != nil {
			return fmt.Errorf("rfb: waiting for the server to confirm input: %w", err)
		}
		switch kind {
		case msgFramebufferUpdate:
			return c.skipUpdate()
		case msgSetColourMapEntries:
			var head struct {
				_            uint8
				First, Count uint16
			}
			if err := binary.Read(c.reader, binary.BigEndian, &head); err != nil {
				return fmt.Errorf("rfb: reading colour map: %w", err)
			}
			if err := c.discard(6 * int(head.Count)); err != nil {
				return err
			}
		case msgBell:
		case msgServerCutText:
			var head struct {
				_      [3]byte
				Length uint32
			}
			if err := binary.Read(c.reader, binary.BigEndian, &head); err != nil {
				return fmt.Errorf("rfb: reading cut text: %w", err)
			}
			if err := c.discard(int(head.Length)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("rfb: server sent unknown message type %d", kind)
		}
	}
}

// skipUpdate reads past one FramebufferUpdate of Raw rectangles.
func (c *Client) skipUpdate() error {
	var head struct {
		_     uint8
		Rects uint16
	}
	if err := binary.Read(c.reader, binary.BigEndian, &head); err != nil {
		return fmt.Errorf("rfb: reading framebuffer update: %w", err)
	}
	for range int(head.Rects) {
		var rect struct {
			X, Y, W, H uint16
			Encoding   int32
		}
		if err := binary.Read(c.reader, binary.BigEndian, &rect); err != nil {
			return fmt.Errorf("rfb: reading update rectangle: %w", err)
		}
		if rect.Encoding != encodingRaw {
			return fmt.Errorf("rfb: server sent encoding %d, which was not requested", rect.Encoding)
		}
		if err := c.discard(int(rect.W) * int(rect.H) * c.bytesPerPixel); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) discard(n int) error {
	if _, err := c.reader.Discard(n); err != nil {
		return fmt.Errorf("rfb: reading server message: %w", err)
	}
	return nil
}

// Close ends the session.
func (c *Client) Close() error {
	err := c.conn.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func containsByte(haystack []byte, needle byte) bool {
	for _, b := range haystack {
		if b == needle {
			return true
		}
	}
	return false
}
