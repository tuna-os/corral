//go:build !unix

package web

import (
	"os/exec"

	"golang.org/x/net/websocket"
)

// bridgeConsolePTY has no pseudo-terminal to open off Unix. The only such
// build is the js/wasm demo (#284), where the browser cannot open the
// console WebSocket anyway, so the session just ends.
func bridgeConsolePTY(ws *websocket.Conn, cmd *exec.Cmd) {}
