//go:build !js

package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	testIssuer   = "https://token.actions.githubusercontent.com"
	testIdentity = "https://github.com/example/plugins/.github/workflows/release.yml@refs/heads/main"
)

var testSigner = Signer{Issuer: testIssuer, Identity: testIdentity}

// signedServer is a marketplace artifact host backed by a virtual Sigstore.
// Bundles are served as opaque tokens; loadBundle maps each token to the
// entity the virtual Sigstore produced for it.
type signedServer struct {
	t        *testing.T
	vs       *ca.VirtualSigstore
	artifact []byte
	entities map[string]verify.SignedEntity
	files    map[string]string // path -> bundle token; absent path -> 404
	srv      *httptest.Server
}

func newSignedServer(t *testing.T, artifact []byte) *signedServer {
	t.Helper()
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	s := &signedServer{t: t, vs: vs, artifact: artifact, entities: map[string]verify.SignedEntity{}, files: map[string]string{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/corral-demo" {
			_, _ = w.Write(s.artifact)
			return
		}
		token, ok := s.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(token))
	}))
	t.Cleanup(s.srv.Close)

	oldMaterial, oldOptions, oldLoad := trustedMaterial, verifierOptions, loadBundle
	trustedMaterial = func() (root.TrustedMaterial, error) { return vs, nil }
	// The virtual Sigstore issues no SCTs; everything else matches production.
	verifierOptions = []verify.VerifierOption{verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1)}
	loadBundle = func(data []byte) (verify.SignedEntity, error) {
		e, ok := s.entities[string(data)]
		if !ok {
			return nil, os.ErrInvalid
		}
		return e, nil
	}
	t.Cleanup(func() { trustedMaterial, verifierOptions, loadBundle = oldMaterial, oldOptions, oldLoad })
	return s
}

func (s *signedServer) serve(path, token string, entity verify.SignedEntity) {
	s.entities[token] = entity
	s.files[path] = token
}

// signArtifact publishes a signature bundle over blob as the artifact's.
func (s *signedServer) signArtifact(identity string, blob []byte) {
	e, err := s.vs.Sign(identity, testIssuer, blob)
	if err != nil {
		s.t.Fatal(err)
	}
	s.serve("/corral-demo"+signatureBundleSuffix, "sig", e)
}

// attestSBOM publishes an in-toto attestation naming subject as the artifact's.
func (s *signedServer) attestSBOM(identity string, subject []byte, predicateType string) {
	sum := sha256.Sum256(subject)
	stmt, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []any{map[string]any{"name": "corral-demo", "digest": map[string]string{"sha256": hex.EncodeToString(sum[:])}}},
		"predicateType": predicateType,
		"predicate":     map[string]any{"spdxVersion": "SPDX-2.3", "name": "corral-demo"},
	})
	if err != nil {
		s.t.Fatal(err)
	}
	e, err := s.vs.Attest(identity, testIssuer, stmt)
	if err != nil {
		s.t.Fatal(err)
	}
	s.serve("/corral-demo"+sbomBundleSuffix, "sbom", e)
}

func (s *signedServer) entry() *Entry {
	sum := sha256.Sum256(s.artifact)
	e := v2Entry("demo", Build{URL: s.srv.URL + "/corral-demo", SHA256: hex.EncodeToString(sum[:])})
	signer := testSigner
	e.Signer = &signer
	return e
}

func TestInstall_SigstoreVerified(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CORRAL_PLUGIN_DIR", dir)
	artifact := []byte("#!/bin/sh\necho signed\n")
	s := newSignedServer(t, artifact)
	s.signArtifact(testIdentity, artifact)
	s.attestSBOM(testIdentity, artifact, spdxPredicateType)

	if err := s.entry().Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "corral-demo")); err != nil {
		t.Fatalf("plugin not installed: %v", err)
	}
	state, err := ReadState("demo")
	if err != nil {
		t.Fatal(err)
	}
	if state.Signer == nil || *state.Signer != testSigner {
		t.Errorf("state signer = %+v, want %+v", state.Signer, testSigner)
	}
	sbom, err := os.ReadFile(sbomPath("demo"))
	if err != nil {
		t.Fatalf("SBOM not stored: %v", err)
	}
	if !strings.Contains(string(sbom), `"spdxVersion":"SPDX-2.3"`) {
		t.Errorf("stored SBOM = %s", sbom)
	}
	sum := sha256.Sum256(sbom)
	if state.SBOMSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("state SBOM digest = %s, want digest of stored SBOM", state.SBOMSHA256)
	}

	if err := Remove("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sbomPath("demo")); !os.IsNotExist(err) {
		t.Errorf("Remove left the SBOM behind: %v", err)
	}
}

func TestInstall_SigstoreRefusals(t *testing.T) {
	artifact := []byte("#!/bin/sh\necho signed\n")
	other := []byte("#!/bin/sh\necho something else\n")
	cases := []struct {
		name  string
		setup func(*signedServer)
		want  string
	}{
		{"signed by another identity", func(s *signedServer) {
			s.signArtifact("https://github.com/attacker/fork/.github/workflows/release.yml@refs/heads/main", artifact)
			s.attestSBOM(testIdentity, artifact, spdxPredicateType)
		}, "signature"},
		{"signature over another artifact", func(s *signedServer) {
			s.signArtifact(testIdentity, other)
			s.attestSBOM(testIdentity, artifact, spdxPredicateType)
		}, "signature"},
		{"no signature bundle", func(s *signedServer) {
			s.attestSBOM(testIdentity, artifact, spdxPredicateType)
		}, "404"},
		{"no SBOM attestation", func(s *signedServer) {
			s.signArtifact(testIdentity, artifact)
		}, "SBOM attestation"},
		{"SBOM attested by another identity", func(s *signedServer) {
			s.signArtifact(testIdentity, artifact)
			s.attestSBOM("https://github.com/attacker/fork/.github/workflows/release.yml@refs/heads/main", artifact, spdxPredicateType)
		}, "SBOM attestation"},
		{"SBOM for another artifact", func(s *signedServer) {
			s.signArtifact(testIdentity, artifact)
			s.attestSBOM(testIdentity, other, spdxPredicateType)
		}, "SBOM attestation"},
		{"attestation that is not an SBOM", func(s *signedServer) {
			s.signArtifact(testIdentity, artifact)
			s.attestSBOM(testIdentity, artifact, "https://slsa.dev/provenance/v1")
		}, "predicate type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CORRAL_PLUGIN_DIR", dir)
			s := newSignedServer(t, artifact)
			tc.setup(s)
			err := s.entry().Install()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Install error = %v, want one mentioning %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "corral-demo")); !os.IsNotExist(err) {
				t.Errorf("refused plugin was installed anyway (stat err %v)", err)
			}
			if _, err := os.Stat(statePath("demo")); !os.IsNotExist(err) {
				t.Errorf("refused plugin left install state (stat err %v)", err)
			}
		})
	}
}

func TestSourceTrustAnchor(t *testing.T) {
	if got := (Source{URL: DefaultMarketplaceURL}).TrustAnchor(); got == nil || *got != FirstPartySigner {
		t.Errorf("built-in marketplace anchor = %+v, want %+v", got, FirstPartySigner)
	}
	if got := (Source{URL: "https://plugins.example.com/index.json"}).TrustAnchor(); got != nil {
		t.Errorf("third-party source without a pin has anchor %+v, want none", got)
	}
	pinned := Source{URL: DefaultMarketplaceURL, Signer: &testSigner}
	if got := pinned.TrustAnchor(); got == nil || *got != testSigner {
		t.Errorf("operator pin = %+v, want %+v", got, testSigner)
	}
}

// TestFetchSource_SetsSignerFromSourceNotIndex: the anchor comes from the
// source; an index cannot name its own signer.
func TestFetchSource_SetsSignerFromSourceNotIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(v2Index("demo", "https://example.com/corral-demo", strings.Repeat("a", 64))))
	}))
	defer srv.Close()

	idx, err := fetchSource(Source{Name: "plain", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if idx.Plugins[0].Signer != nil {
		t.Errorf("unpinned source produced signer %+v", idx.Plugins[0].Signer)
	}
	idx, err = fetchSource(Source{Name: "pinned", URL: srv.URL, Enabled: true, Signer: &testSigner})
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Plugins[0].Signer; got == nil || *got != testSigner {
		t.Errorf("pinned source produced signer %+v, want %+v", got, testSigner)
	}
}

func TestAddSource_RejectsHalfSigner(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := AddSource(Source{Name: "half", URL: "https://plugins.example.com/index.json", Signer: &Signer{Identity: testIdentity}})
	if err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("AddSource error = %v, want a missing-issuer error", err)
	}
}

// TestFirstPartySignerMatchesWorkflow keeps the compiled-in anchor and the
// identity publish-plugins signs and self-verifies with in step: a drift on
// either side would make every first-party install fail closed.
func TestFirstPartySignerMatchesWorkflow(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	start := strings.Index(workflow, "\n  publish-plugins:\n")
	if start < 0 {
		t.Fatal("ci.yml has no publish-plugins job")
	}
	job := workflow[start+1:]
	// The job ends where the next two-space-indented key (the next job) starts.
	if loc := regexp.MustCompile(`\n  [a-z][a-z0-9-]*:\n`).FindStringIndex(job); loc != nil {
		job = job[:loc[0]]
	}
	wantIdentity := strings.Replace(FirstPartySigner.Identity, "tuna-os/corral", "${{ github.repository }}", 1)
	for _, want := range []string{
		"SIGNER_IDENTITY: " + wantIdentity,
		"SIGNER_ISSUER: " + FirstPartySigner.Issuer,
		"id-token: write",
		"github.ref == 'refs/heads/main'",
		"cosign sign-blob --bundle \"$bin" + signatureBundleSuffix + "\"",
		"--bundle \"$bin" + sbomBundleSuffix + "\"",
		"--type spdxjson",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("publish-plugins job does not contain %q", want)
		}
	}
	if !strings.HasPrefix(DefaultMarketplaceURL, "https://github.com/tuna-os/corral/") {
		t.Errorf("DefaultMarketplaceURL %s is not published by the repository FirstPartySigner names", DefaultMarketplaceURL)
	}
}
