package web

import (
	"net/http"

	"github.com/tuna-os/corral/pkg/demo"
)

// EnableDemo plugs the in-memory fake cluster (pkg/demo) into every backend
// seam plus this package's own runner. Must be called before Serve.
func EnableDemo() {
	defaultRunner = demo.Enable()
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
