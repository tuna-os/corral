//go:build unix

package web

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/net/websocket"
)

// bridgeConsolePTY wires cmd to a real pseudo-terminal — needed for
// commands (like kubectl exec -t) that check isatty on their own stdin.
func bridgeConsolePTY(ws *websocket.Conn, cmd *exec.Cmd) {
	f, err := pty.Start(cmd)
	if err != nil {
		return
	}
	defer func() {
		f.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	done := make(chan struct{}, 2)
	go func() { io.Copy(f, ws); done <- struct{}{} }()
	go func() { io.Copy(ws, f); done <- struct{}{} }()
	<-done
}
