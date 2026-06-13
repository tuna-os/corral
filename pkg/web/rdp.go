package web

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"golang.org/x/net/websocket"
)

// RDP support. Windows VMs run RDP natively; modern Linux desktops expose it
// too (gnome-remote-desktop, xrdp/FreeRDP). Corral detects an open 3389 on
// the VM's pod IP and offers an RDP path for any VM that answers — see
// docs/adr/0002-browser-rdp-via-ironrdp.md for where this is headed
// (in-browser IronRDP client over this bridge).

// rdpDial is the probe dialer — a seam so tests can point it at a local
// listener instead of a pod IP.
var rdpDial = func(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 1500*time.Millisecond)
}

// handleRDPCheck reports whether the VM answers on TCP 3389.
// GET /api/vms/{ns}/{name}/rdp → {"open": bool, "ip": "…"}
func handleRDPCheck(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	info, ok := vmiIndex()[ns+"/"+name]
	if !ok || info.IP == "" {
		jsonResp(w, http.StatusOK, map[string]any{"open": false, "reason": "VM is not running or has no IP"})
		return
	}
	conn, err := rdpDial(net.JoinHostPort(info.IP, "3389"))
	if conn != nil {
		conn.Close()
	}
	jsonResp(w, http.StatusOK, map[string]any{"open": err == nil, "ip": info.IP})
}

// rdpBridge proxies a binary websocket to the VM's RDP port through
// `virtctl port-forward`, the same pattern as the VNC bridge. This is a raw
// RDP-over-websocket transport: anything that can speak RDP over a websocket
// (an IronRDP-based client, a local wsproxy) can use it.
func rdpBridge(ws *websocket.Conn) {
	defer ws.Close()
	ns, name := ws.Request().PathValue("ns"), ws.Request().PathValue("name")
	if ns == "" || name == "" {
		return
	}

	port, err := freePort()
	if err != nil {
		return
	}
	proxy := exec.Command("virtctl", "port-forward", "vm/"+name,
		fmt.Sprintf("%d:3389", port), "-n", ns)
	proxy.Stdout = io.Discard
	proxy.Stderr = io.Discard
	if err := proxy.Start(); err != nil {
		return
	}
	defer func() {
		proxy.Process.Kill()
		proxy.Wait()
	}()

	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if conn == nil {
		return
	}
	defer conn.Close()

	ws.PayloadType = websocket.BinaryFrame
	done := make(chan struct{}, 2)
	go func() { io.Copy(conn, ws); done <- struct{}{} }()
	go func() { io.Copy(ws, conn); done <- struct{}{} }()
	<-done
}
