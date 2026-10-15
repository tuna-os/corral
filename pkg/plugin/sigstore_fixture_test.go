//go:build !js

package plugin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata/sigstore are real output of the publish-plugins
// job (release plugins-1c0562a32338821c1380c2ba97f3b8760cb50b22): the Sigstore bundles of one plugin binary,
// with the public-good trusted root of that day. This test proves, offline,
// that what CI publishes is what Install accepts, with the production
// verifier, bundle parser and trusted-root loading.
const (
	fixtureArtifact = "corral-auth-linux-amd64"
	fixtureDigest   = "dd337ae9d7d91c61b770c2e8a378f849bc22f83e716c650b8f23ac6662eec385"
)

func TestVerifySigstore_RealCIBundles(t *testing.T) {
	dir := filepath.Join("testdata", "sigstore")
	t.Setenv("CORRAL_SIGSTORE_TRUSTED_ROOT", filepath.Join(dir, "trusted_root.json"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	url := srv.URL + "/" + fixtureArtifact

	sbom, err := verifySigstore(srv.Client(), url, fixtureDigest, FirstPartySigner)
	if err != nil {
		t.Fatalf("CI-published bundles do not verify: %v", err)
	}
	if !strings.Contains(string(sbom), `"spdxVersion"`) {
		t.Errorf("attested SBOM is not an SPDX document: %.200s", sbom)
	}

	fork := Signer{Issuer: FirstPartySigner.Issuer, Identity: strings.Replace(FirstPartySigner.Identity, "tuna-os/corral", "attacker/corral", 1)}
	if _, err := verifySigstore(srv.Client(), url, fixtureDigest, fork); err == nil {
		t.Error("bundles verified against a fork's identity")
	}
	branch := Signer{Issuer: FirstPartySigner.Issuer, Identity: strings.Replace(FirstPartySigner.Identity, "refs/heads/main", "refs/heads/feature", 1)}
	if _, err := verifySigstore(srv.Client(), url, fixtureDigest, branch); err == nil {
		t.Error("bundles verified against another ref")
	}
	if _, err := verifySigstore(srv.Client(), url, strings.Repeat("0", 64), FirstPartySigner); err == nil {
		t.Error("bundles verified for another artifact digest")
	}
}
