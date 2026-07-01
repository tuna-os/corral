package qemu

import "github.com/tuna-os/corral/pkg/types"

// Backend is a zero-size adapter satisfying types.Backend — a method-set
// wrapper over this package's existing functions. No logic lives here; it
// exists so qemu can be depended on through types.Backend the same way
// *kubevirt.Client already is.
type Backend struct{}

var _ types.Backend = Backend{}

func (Backend) ListVMs() ([]types.VM, error)       { return List() }
func (Backend) VMExists(name string) bool          { return Exists(name) }
func (Backend) StartVM(name string) error          { return Start(name) }
func (Backend) StopVM(name string) error           { return Stop(name) }
func (Backend) DeleteVM(name string) error         { return Delete(name) }
func (Backend) VMInfo(name string) ([]byte, error) { return Info(name) }
func (Backend) Viewer(name string) error           { return Viewer(name) }
func (Backend) Logs(name string) error             { return Logs(name) }

func (Backend) SSH(name, username, identityFile, command string, port int, password string) error {
	return SSH(name, username, identityFile, command, port, password)
}
