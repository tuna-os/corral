//go:build bootc

package kubevirt

import (
	"strings"
	"testing"

	"github.com/hanthor/corral/pkg/shell"
)

func TestBootcRegistryMirror_EnvOverrideAndDisable(t *testing.T) {
	t.Setenv("CORRAL_REGISTRY_MIRROR", "my-cache:5000")
	if got := bootcRegistryMirror(); got != "my-cache:5000" {
		t.Errorf("explicit host = %q, want my-cache:5000", got)
	}
	for _, off := range []string{"off", "none", "FALSE", "0"} {
		t.Setenv("CORRAL_REGISTRY_MIRROR", off)
		if got := bootcRegistryMirror(); got != "" {
			t.Errorf("%q should disable the mirror, got %q", off, got)
		}
	}
}

func TestBootcRegistryMirror_AutoDetect(t *testing.T) {
	t.Setenv("CORRAL_REGISTRY_MIRROR", "") // default: detect the cache Service

	// Cache present → default-on, returns its cluster DNS.
	present := shell.NewFake()
	present.AddResponseKV("kubectl", []string{"get", "svc", "registry-cache", "-n", "corral", "-o", "jsonpath={.metadata.name}"}, "registry-cache", nil)
	SetPackageRunner(present)
	defer SetPackageRunner(shell.Real{})
	if got := bootcRegistryMirror(); got != "registry-cache.corral.svc.cluster.local:5000" {
		t.Errorf("with cache deployed, mirror = %q", got)
	}

	// Cache absent → no mirror (no regression for clusters without it).
	absent := shell.NewFake() // unregistered → error
	SetPackageRunner(absent)
	if got := bootcRegistryMirror(); got != "" {
		t.Errorf("without cache, mirror should be empty, got %q", got)
	}
}

func TestBuilderScript_MirrorWiring(t *testing.T) {
	// Always writes the drop-in path so podman picks it up.
	t.Setenv("CORRAL_REGISTRY_MIRROR", "off")
	off := builderScript("ghcr.io/x/y:tag", "ssh-ed25519 AAAA")
	if !strings.Contains(off, "/etc/containers/registries.conf.d/corral-mirror.conf") {
		t.Error("builder cloud-init should always include the mirror drop-in path")
	}
	if strings.Contains(off, "[[registry.mirror]]") {
		t.Error("disabled mirror should leave an empty drop-in (no registry stanza)")
	}

	t.Setenv("CORRAL_REGISTRY_MIRROR", "cache:5000")
	on := builderScript("ghcr.io/x/y:tag", "ssh-ed25519 AAAA")
	if !strings.Contains(on, `location = "cache:5000"`) || !strings.Contains(on, `prefix = "ghcr.io"`) {
		t.Errorf("enabled mirror should inject the ghcr.io→cache stanza:\n%s", on)
	}
}
