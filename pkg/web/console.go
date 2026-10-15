package web

import (
	"io"
	"net/http"
	"os/exec"

	"golang.org/x/net/websocket"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/libvirt"
	"github.com/tuna-os/corral/pkg/shell"
)

// consoleDialer is the seam vncBridge/rdpBridge open their connections
// through — swapped for a fake in tests.
var consoleDialer kubevirt.ConsoleDialer = kubevirt.RealConsoleDialer{}

// vncBridge proxies a binary websocket (noVNC) to the VM's VNC console.
func vncBridge(ws *websocket.Conn) {
	defer func() { _ = ws.Close() }()
	ns, name := ws.Request().PathValue("ns"), ws.Request().PathValue("name")
	if ns == "" || name == "" {
		return
	}
	if bridgePeerConsole(ws, "vnc") {
		return
	}

	// Local QEMU VMs (#91 Phase 2): their VNC server is a plain TCP listener
	// on this host — dial it directly instead of the virtctl proxy.
	var conn io.ReadWriteCloser
	var err error
	if ns == localNS {
		conn, err = dialLocalVNC(name)
	} else if ns == "libvirt" {
		conn, err = libvirt.NewClient(ws.Request().URL.Query().Get("context")).DialVNC(name)
	} else if context := ws.Request().URL.Query().Get("context"); context != "" {
		conn, err = (kubevirt.RealConsoleDialer{Context: context}).Dial(ns, name, kubevirt.VNC)
	} else {
		conn, err = consoleDialer.Dial(ns, name, kubevirt.VNC)
	}
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	ws.PayloadType = websocket.BinaryFrame
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(conn, ws); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ws, conn); done <- struct{}{} }()
	<-done
}

// ttyBridge proxies a binary websocket (xterm.js) to a terminal — `virtctl
// console` for a VM, or `kubectl exec` for a CT (#50: CTs have no
// framebuffer/serial console, only exec/attach). Tries the VM path first
// since that's the common case; falls back to CT only when no VM by that
// name exists.
func ttyBridge(ws *websocket.Conn) {
	defer func() { _ = ws.Close() }()
	ns, name := ws.Request().PathValue("ns"), ws.Request().PathValue("name")
	if ns == "" || name == "" {
		return
	}
	if bridgePeerConsole(ws, "tty") {
		return
	}
	if ns == "incus" {
		remote := rURLContext(ws.Request())
		if remote == "" {
			remote = config.IncusRemote()
		}
		bridgeConsolePTY(ws, exec.Command("incus", "exec", remote+":"+name, "--", "bash"))
		return
	}

	isVM := kubevirt.NewClientForContext(ns, ws.Request().URL.Query().Get("context")).VMExists(name)

	ws.PayloadType = websocket.BinaryFrame
	if isVM {
		bridgeConsolePipes(ws, shell.CommandForContext(ws.Request().URL.Query().Get("context"), "virtctl", "console", name, "-n", ns))
		return
	}
	// kubectl exec's -t requires a real local TTY on its end — it checks
	// isatty on this process's stdin, which a bare os/exec pipe fails (kubectl
	// errors "Unable to use a TTY - input is not a terminal or the right kind
	// of file"). virtctl console has no such check (it's not an exec session,
	// it's a virtio-serial device), which is why the VM path above can use
	// plain pipes but this one needs a real pty.
	shellCmd, err := ct.ExecCommand(name, ns)
	if err != nil {
		return
	}
	args := append([]string{"exec", "-i", "-t", name, "-n", ns, "--"}, shellCmd...)
	bridgeConsolePTY(ws, shell.CommandForContext(ws.Request().URL.Query().Get("context"), "kubectl", args...))
}

func rURLContext(r *http.Request) string { return r.URL.Query().Get("context") }

// bridgeConsolePipes wires cmd's stdin/stdout to ws via plain OS pipes —
// fine for commands (like virtctl console) that don't check isatty.
func bridgeConsolePipes(ws *websocket.Conn, cmd *exec.Cmd) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stdin, ws); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ws, stdout); done <- struct{}{} }()
	<-done
}
