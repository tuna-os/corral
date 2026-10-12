package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Load reads config.yaml, which users sync between machines in dotfiles repos
// and hand-edit: its bytes are adversarial input by construction. A file that
// fails to parse must be an error, never a panic — a corrupt config that
// crashes `corral` also blocks the command that would fix it.
//
// Run longer with: go test -run xxx -fuzz FuzzLoad ./pkg/config/
func FuzzLoad(f *testing.F) {
	for _, seed := range []string{
		"",
		"{}\n",
		"default:\n  backend: kubevirt\n",
		"default:\n  backend: kubevirt\n  context: test\nincus:\n  remote: local\nkubevirt:\n  context: kind-kind\nlibvirt:\n  uri: qemu:///system\n",
		"contexts:\n- name: lab\n  backend: proxmox\n  context: pve1\n  proxmox:\n    host: https://pve1:8006\n    user: root@pam\npeers:\n- name: lab\n  url: https://lab:8443\nfolders:\n- path: /dev\n  members:\n  - kubevirt/default/default/vm\n",
		"contexts: nope\n",
		"peers: [unclosed\n",
		"\t-tabs-are-not-yaml\n",
		"a: *undefined-alias\n",
		"a: &x [1, 2\nb: *x\n",
		"key: !!binary |\n  not really binary at all\n",
		"\xff\xfe\x00binary\n",
		"long: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
		"folders:\n- path: /\n  members: [a/b/c/d, peer/p/kubevirt/e/f/g]\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			return
		}
		// A config that loads must also save: Set* callers reload and write
		// back, so a loadable-but-unmarshalable document corrupts on the
		// next settings change instead of at the edit that caused it.
		if cfg == nil {
			t.Errorf("Load of %d bytes succeeded with a nil Config", len(data))
			return
		}
		if _, err := yaml.Marshal(cfg); err != nil {
			t.Errorf("Load of %d bytes succeeded, but re-marshal fails: %v", len(data), err)
		}
	})
}
