package web

import "github.com/tuna-os/corral/pkg/demo"

// EnableDemo plugs the in-memory fake cluster (pkg/demo) into every backend
// seam plus this package's own runner. Must be called before Serve.
func EnableDemo() {
	defaultRunner = demo.Enable()
}
