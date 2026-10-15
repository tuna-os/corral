//go:build js

package plugin

import (
	"fmt"
	"net/http"
)

// verifySigstore is unavailable in the js/wasm demo build, which keeps
// sigstore-go out of the WebAssembly binary. It fails closed: a source with a
// trust anchor cannot install plugins from that build.
func verifySigstore(*http.Client, string, string, Signer) ([]byte, error) {
	return nil, fmt.Errorf("Sigstore verification is not available in the js/wasm build")
}
