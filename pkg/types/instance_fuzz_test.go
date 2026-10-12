package types

import (
	"strings"
	"testing"
)

// ParseInstanceRef decodes instance selectors that arrive from peers and CLI
// arguments, not from Corral: a federated peer names whatever instance it
// wants, and a malformed selector must be an error, never a panic or a
// silently wrong identity. A parse that succeeds must also be reversible —
// the string form is the VM's ID (see SetIdentity), so a value that does not
// survive a round trip would address two different instances before and
// after a save.
//
// Run longer with: go test -run xxx -fuzz FuzzInstanceRef ./pkg/types/
func FuzzInstanceRef(f *testing.F) {
	for _, seed := range []string{
		"",
		"kubevirt/default/default/my-vm",
		"peer/lab/kubevirt/default/default/my-vm",
		"libvirt/qemu+ssh://alice@hypervisor/system/libvirt/desktop/dev",
		"peer/lab/libvirt/qemu+ssh://alice@hypervisor/system/libvirt/desktop%2Fdev",
		"qemu///my-vm",
		"a/b/c/d",
		"peer/a/b/c/d/e",
		"backend/context/namespace/name/extra",
		"peer/only",
		"peer",
		"///",
		" / / / ",
		"%zz",
		"%2F%2F",
		"peer/%2F/%2F/%2F/%2F/%2F",
		"kubevirt/%00/default/vm",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseInstanceRef(s)
		if err != nil {
			return
		}
		// A successful parse must be a usable identity: backend and name are
		// what Validate enforces, and String is what the registry stores.
		if err := got.Validate(); err != nil {
			t.Errorf("ParseInstanceRef(%q) = %+v, which fails Validate: %v", s, got, err)
		}
		if strings.TrimSpace(got.Backend) == "" || strings.TrimSpace(got.Name) == "" {
			t.Errorf("ParseInstanceRef(%q) = %+v, with a blank backend or name", s, got)
		}
		// ... and it must round-trip: re-parsing the string form yields the
		// same ref, so an ID written to the registry addresses the same
		// instance when it is read back.
		round, err := ParseInstanceRef(got.String())
		if err != nil {
			t.Errorf("ParseInstanceRef(%q) = %+v, whose String %q does not re-parse: %v",
				s, got, got.String(), err)
		} else if round != got {
			t.Errorf("ParseInstanceRef(%q) round trip: got %+v, want %+v", s, round, got)
		}
	})
}
