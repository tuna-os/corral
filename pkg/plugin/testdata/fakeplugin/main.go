// Command fakeplugin is a minimal plugin binary used by the handshake
// round-trip test: it implements the --corral-plugin-metadata contract the
// same way every first-party plugin does, through the shared SDK.
package main

import (
	"fmt"
	"os"

	"github.com/tuna-os/corral/pkg/plugin/sdk"
)

func main() {
	if sdk.HandleMetadata(sdk.Metadata{
		Name:              "fake",
		Version:           "0.1.0",
		Description:       "handshake round-trip fixture",
		Capabilities:      []string{"cli-command"},
		SupportedBackends: []string{"all"},
	}) {
		return
	}
	fmt.Fprintln(os.Stderr, "fakeplugin: no metadata flag")
	os.Exit(1)
}
