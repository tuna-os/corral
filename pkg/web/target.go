package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tuna-os/corral/pkg/config"
)

// targetRoutePrefixes are the routes whose first two path segments name a
// guest: {ns}/{name}. Their handlers hand both segments, and for libvirt and
// Incus the ?context= value, to backend command lines. The /console/ HTML
// page runs no command and validates its own segments; the console
// WebSocket it opens is covered here.
var targetRoutePrefixes = []string{"/api/vms/", "/api/vnc/", "/api/tty/", "/api/rdp/"}

// targetGate refuses a guest route whose request values would choose more
// than the guest. Two rules:
//
//   - The namespace and name must not start with "-": they reach virsh,
//     incus, kubectl and virtctl as positional arguments, where a leading
//     dash is read as an option.
//   - For the libvirt and Incus namespaces, ?context= is a connection URI or
//     remote, not a name. It must be one the operator configured: the
//     server's default, or the context of a configured context of that
//     backend. Anything else would let the caller point the server at a
//     hypervisor of their choosing (a qemu+ssh:// URI starts ssh to it).
//
// It runs on every method, because GET routes reach these backends too.
func targetGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ns, name, ok := targetSegments(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(ns, "-") || strings.HasPrefix(name, "-") {
			errResp(w, http.StatusBadRequest, fmt.Errorf("a namespace or name must not start with '-'"))
			return
		}
		backend := ""
		switch ns {
		case "libvirt", "incus":
			backend = ns
		default:
			next.ServeHTTP(w, r)
			return
		}
		values := r.URL.Query()["context"]
		if len(values) > 1 {
			errResp(w, http.StatusBadRequest, fmt.Errorf("give at most one context"))
			return
		}
		if len(values) == 1 && !allowedBackendContext(backend, values[0]) {
			errResp(w, http.StatusBadRequest, fmt.Errorf("context %q is not a configured %s context", values[0], backend))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// targetSegments returns the {ns} and {name} segments of a guest route,
// unescaped the way the route table unescapes them.
func targetSegments(r *http.Request) (ns, name string, ok bool) {
	p := r.URL.EscapedPath()
	for _, prefix := range targetRoutePrefixes {
		rest, found := strings.CutPrefix(p, prefix)
		if !found {
			continue
		}
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 2 {
			return "", "", false
		}
		ns, err1 := url.PathUnescape(parts[0])
		name, err2 := url.PathUnescape(parts[1])
		if err1 != nil || err2 != nil {
			return "", "", false
		}
		return ns, name, true
	}
	return "", "", false
}

// allowedBackendContext reports whether value is a libvirt URI or Incus
// remote the operator configured for backend. The empty value means the
// server's default.
func allowedBackendContext(backend, value string) bool {
	if value == "" {
		return true
	}
	switch backend {
	case "libvirt":
		if value == config.LibvirtURI() {
			return true
		}
	case "incus":
		if value == config.IncusRemote() {
			return true
		}
	default:
		return false
	}
	for _, c := range config.Contexts() {
		if c.Backend == backend && c.Context != "" && c.Context == value {
			return true
		}
	}
	return false
}
