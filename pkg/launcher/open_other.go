//go:build !unix

package launcher

import "os"

// Non-Unix fallback: plain open. No O_NONBLOCK exists here; the only such
// build is the js/wasm demo, which never admits local files.
func openFileRead(path string) (*os.File, error) {
	return os.Open(path)
}
