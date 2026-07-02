//go:build bootc

package kubevirt

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tuna-os/corral/pkg/shell"
)

// The deploy manifest and detectRegistryCache() must agree on the Service name,
// namespace, and port — otherwise the cache deploys but the builder never finds
// it (a silent break). This pins that contract + the pull-through config.
func TestRegistryCacheManifestContract(t *testing.T) {
	data, err := os.ReadFile("../../deploy/registry-cache.yaml")
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var svcFound, proxyFound bool
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break // EOF
		}
		if doc == nil {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any)
		switch doc["kind"] {
		case "Service":
			svcFound = true
			if meta["name"] != registryCacheService {
				t.Errorf("Service name %v, code expects %q", meta["name"], registryCacheService)
			}
			if meta["namespace"] != registryCacheNamespace {
				t.Errorf("Service namespace %v, code expects %q", meta["namespace"], registryCacheNamespace)
			}
			port := firstServicePort(doc)
			if port != registryCachePort {
				t.Errorf("Service port %q, code expects %q", port, registryCachePort)
			}
		case "Deployment":
			if hasProxyRemote(doc, "https://ghcr.io") {
				proxyFound = true
			}
		}
	}
	if !svcFound {
		t.Error("manifest has no Service")
	}
	if !proxyFound {
		t.Error("manifest Deployment must set REGISTRY_PROXY_REMOTEURL=https://ghcr.io (pull-through mode)")
	}
}

func firstServicePort(doc map[string]any) string {
	spec, _ := doc["spec"].(map[string]any)
	ports, _ := spec["ports"].([]any)
	if len(ports) == 0 {
		return ""
	}
	p, _ := ports[0].(map[string]any)
	switch v := p["port"].(type) {
	case int:
		return strconv.Itoa(v)
	case string:
		return v
	}
	return ""
}

func hasProxyRemote(doc map[string]any, want string) bool {
	spec, _ := doc["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	tspec, _ := tmpl["spec"].(map[string]any)
	containers, _ := tspec["containers"].([]any)
	for _, c := range containers {
		cm, _ := c.(map[string]any)
		envs, _ := cm["env"].([]any)
		for _, e := range envs {
			em, _ := e.(map[string]any)
			if em["name"] == "REGISTRY_PROXY_REMOTEURL" && em["value"] == want {
				return true
			}
		}
	}
	return false
}

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
	off := builderScript("ghcr.io/x/y:tag", "ssh-ed25519 AAAA", "")
	if !strings.Contains(off, "/etc/containers/registries.conf.d/corral-mirror.conf") {
		t.Error("builder cloud-init should always include the mirror drop-in path")
	}
	if strings.Contains(off, "[[registry.mirror]]") {
		t.Error("disabled mirror should leave an empty drop-in (no registry stanza)")
	}

	t.Setenv("CORRAL_REGISTRY_MIRROR", "cache:5000")
	on := builderScript("ghcr.io/x/y:tag", "ssh-ed25519 AAAA", "")
	if !strings.Contains(on, `location = "cache:5000"`) || !strings.Contains(on, `prefix = "ghcr.io"`) {
		t.Errorf("enabled mirror should inject the ghcr.io→cache stanza:\n%s", on)
	}
}
