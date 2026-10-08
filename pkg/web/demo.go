package web

import (
	"encoding/binary"
	"io"
	"net/http"

	"github.com/tuna-os/corral/pkg/demo"
	"github.com/tuna-os/corral/pkg/folder"
	"golang.org/x/net/websocket"
)

// demoEnabled is consumed by the next newMux (demo console); demoMode stays
// set for handlers that must not touch real state, such as PUT /api/theme.
var (
	demoEnabled bool
	demoMode    bool
)

// EnableDemo plugs the in-memory fake cluster (pkg/demo) into every backend
// seam plus this package's own runner. Must be called before Serve.
func EnableDemo() {
	demoEnabled = true
	demoMode = true
	defaultRunner = demo.Enable()
	SetFolderStore(folder.NewStore(folder.NewMemoryBackend()))
}

// serveDemoVNC performs enough of RFB 3.8 to establish a no-auth console.
// The framebuffer intentionally remains black: demo mode proves browser and
// websocket console plumbing without pretending to run a guest display.
func serveDemoVNC(ws *websocket.Conn) {
	ws.PayloadType = websocket.BinaryFrame
	if _, err := ws.Write([]byte("RFB 003.008\n")); err != nil {
		return
	}
	version := make([]byte, 12)
	if _, err := io.ReadFull(ws, version); err != nil {
		return
	}
	if _, err := ws.Write([]byte{1, 1}); err != nil { // one security type: None
		return
	}
	choice := make([]byte, 1)
	if _, err := io.ReadFull(ws, choice); err != nil || choice[0] != 1 {
		return
	}
	if _, err := ws.Write([]byte{0, 0, 0, 0}); err != nil { // SecurityResult OK
		return
	}
	shared := make([]byte, 1)
	if _, err := io.ReadFull(ws, shared); err != nil {
		return
	}

	name := []byte("Corral demo console")
	init := make([]byte, 24+len(name))
	binary.BigEndian.PutUint16(init[0:2], 800)
	binary.BigEndian.PutUint16(init[2:4], 600)
	init[4], init[5], init[6], init[7] = 32, 24, 0, 1 // true-colour RGB888
	binary.BigEndian.PutUint16(init[8:10], 255)
	binary.BigEndian.PutUint16(init[10:12], 255)
	binary.BigEndian.PutUint16(init[12:14], 255)
	init[14], init[15], init[16] = 16, 8, 0
	binary.BigEndian.PutUint32(init[20:24], uint32(len(name)))
	copy(init[24:], name)
	if _, err := ws.Write(init); err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, ws)
}

// DemoHandler returns the full dashboard, wired to the fake cluster, as a
// plain http.Handler with nothing listening. The browser-only demo site
// (#284, web-demo/) runs it inside WebAssembly and feeds it the page's
// requests from a service worker, since a browser cannot open a socket.
func DemoHandler() (http.Handler, error) {
	EnableDemo()
	applyCLITheme()
	mux, err := newMux()
	if err != nil {
		return nil, err
	}
	startMetricSampler()
	return mux, nil
}
