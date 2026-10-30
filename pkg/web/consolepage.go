package web

import (
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
)

// The standalone console page: GET /console/{ns}/{name} shows one VM's VNC
// console and nothing else, at a URL a script can build or read from
// `corral console`. It reuses /api/vnc/{ns}/{name}, so the websocket keeps
// its origin check, the VDI holder gate, and the tailnet identity gate.

//go:embed console_page.html
var consolePageHTML string

var consolePageTemplate = template.Must(template.New("console").Parse(consolePageHTML))

// consolePathSegment is the shape of a namespace or VM name in a console page
// URL. It covers Kubernetes names, local QEMU names, the "local" and
// "libvirt" namespaces, and peer namespaces (peer-<base64url>).
var consolePathSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// consolePage is what the page template renders. Scale is "one" (1:1 pixels)
// or "fit" (scaled to the window).
type consolePage struct {
	Namespace, Name, Context, Scale string
}

func handleConsolePage(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !consolePathSegment.MatchString(ns) || !consolePathSegment.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	scale := r.URL.Query().Get("scale")
	switch scale {
	case "":
		scale = "one"
	case "one", "fit":
	default:
		http.Error(w, fmt.Sprintf("scale %q is not supported: use one or fit", scale), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	page := consolePage{Namespace: ns, Name: name, Context: r.URL.Query().Get("context"), Scale: scale}
	if err := consolePageTemplate.Execute(w, page); err != nil {
		slog.WarnContext(r.Context(), "render console page", "error", err)
	}
}
