//go:build !js

package plugin

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// trustedMaterial returns the Sigstore trust roots (Fulcio CAs, Rekor and CT
// log keys). CORRAL_SIGSTORE_TRUSTED_ROOT points at a trusted_root.json for
// hosts that cannot reach the public-good TUF repository; otherwise the root
// comes from TUF, cached in the user cache directory. A variable so tests can
// substitute a virtual Sigstore.
var trustedMaterial = func() (root.TrustedMaterial, error) {
	if path := os.Getenv("CORRAL_SIGSTORE_TRUSTED_ROOT"); path != "" {
		return root.NewTrustedRootFromPath(path)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	opts := tuf.DefaultOptions().WithCachePath(filepath.Join(cache, "corral", "sigstore-tuf"))
	return root.FetchTrustedRootWithOptions(opts)
}

// verifierOptions match the Sigstore public-good deployment: certificates
// carry an SCT, and each signature has a Rekor entry. A variable so tests can
// use a virtual Sigstore, which issues no SCTs.
var verifierOptions = []verify.VerifierOption{
	verify.WithSignedCertificateTimestamps(1),
	verify.WithTransparencyLog(1),
	verify.WithObserverTimestamps(1),
}

// loadBundle parses a Sigstore bundle. A variable so tests can hand the
// verifier entities produced by a virtual Sigstore.
var loadBundle = func(data []byte) (verify.SignedEntity, error) {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(data); err != nil {
		return nil, err
	}
	return &b, nil
}

// verifySigstore checks that the artifact with the given SHA-256 digest is
// signed by signer and carries an SPDX SBOM attested by the same signer. The
// bundles are read from artifactURL plus the sidecar suffixes. It returns the
// SBOM document.
func verifySigstore(client *http.Client, artifactURL, digestHex string, signer Signer) ([]byte, error) {
	if !signer.Valid() {
		return nil, fmt.Errorf("signer identity and issuer are both required")
	}
	digest, err := hex.DecodeString(digestHex)
	if err != nil || len(digest) != 32 {
		return nil, fmt.Errorf("invalid artifact digest %q", digestHex)
	}
	material, err := trustedMaterial()
	if err != nil {
		return nil, fmt.Errorf("loading Sigstore trusted root (set CORRAL_SIGSTORE_TRUSTED_ROOT when offline): %w", err)
	}
	verifier, err := verify.NewVerifier(material, append([]verify.VerifierOption{verify.WithoutStatementPredicate()}, verifierOptions...)...)
	if err != nil {
		return nil, err
	}
	identity, err := verify.NewShortCertificateIdentity(signer.Issuer, "", signer.Identity, "")
	if err != nil {
		return nil, err
	}
	policy := verify.NewPolicy(verify.WithArtifactDigest("sha256", digest), verify.WithCertificateIdentity(identity))

	sig, err := fetchBundle(client, artifactURL+signatureBundleSuffix)
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	if _, err := verifier.Verify(sig, policy); err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}

	att, err := fetchBundle(client, artifactURL+sbomBundleSuffix)
	if err != nil {
		return nil, fmt.Errorf("SBOM attestation: %w", err)
	}
	// With an artifact digest in the policy, Verify also checks that the
	// in-toto subject names this artifact.
	if _, err := verifier.Verify(att, policy); err != nil {
		return nil, fmt.Errorf("SBOM attestation: %w", err)
	}
	return spdxPredicate(att)
}

func fetchBundle(client *http.Client, url string) (verify.SignedEntity, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if len(data) > maxBundleBytes {
		return nil, fmt.Errorf("%s exceeds %d MiB", url, maxBundleBytes>>20)
	}
	entity, err := loadBundle(data)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", url, err)
	}
	return entity, nil
}

// spdxPredicate returns the SPDX document from a verified attestation, read
// straight from the signed DSSE payload.
func spdxPredicate(att verify.SignedEntity) ([]byte, error) {
	content, err := att.SignatureContent()
	if err != nil {
		return nil, err
	}
	envelope := content.EnvelopeContent()
	if envelope == nil {
		return nil, fmt.Errorf("SBOM attestation is not an in-toto attestation")
	}
	payload, err := envelope.RawEnvelope().DecodeB64Payload()
	if err != nil {
		return nil, fmt.Errorf("SBOM attestation payload: %w", err)
	}
	var stmt struct {
		PredicateType string          `json:"predicateType"`
		Predicate     json.RawMessage `json:"predicate"`
	}
	if err := json.Unmarshal(payload, &stmt); err != nil {
		return nil, fmt.Errorf("SBOM attestation statement: %w", err)
	}
	if stmt.PredicateType != spdxPredicateType {
		return nil, fmt.Errorf("SBOM attestation has predicate type %q, want %q", stmt.PredicateType, spdxPredicateType)
	}
	predicate := bytes.TrimSpace(stmt.Predicate)
	if len(predicate) == 0 || predicate[0] != '{' {
		return nil, fmt.Errorf("SBOM attestation carries no SPDX document")
	}
	return predicate, nil
}
