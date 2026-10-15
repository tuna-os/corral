package plugin

// Signer is the keyless (Sigstore/Fulcio) identity a source's artifacts must
// be signed by. Both fields match exactly: a regular expression is something a
// workflow in a fork can satisfy (docs/enterprise-readiness.md).
type Signer struct {
	Issuer   string `json:"issuer"`
	Identity string `json:"identity"`
}

// FirstPartySigner is the trust anchor for DefaultMarketplaceURL: the
// publish-plugins job of tuna-os/corral's CI on main. It is compiled in
// because an anchor an index could assert about itself would anchor nothing.
// TestFirstPartySignerMatchesWorkflow keeps it equal to SIGNER_IDENTITY in
// .github/workflows/ci.yml.
var FirstPartySigner = Signer{
	Issuer:   "https://token.actions.githubusercontent.com",
	Identity: "https://github.com/tuna-os/corral/.github/workflows/ci.yml@refs/heads/main",
}

const (
	// Sidecar suffixes publish-plugins uploads next to every artifact. They
	// are a URL convention rather than index fields because Corral decodes
	// the index with DisallowUnknownFields: a new field would break every
	// installed client.
	signatureBundleSuffix = ".sigstore.json"
	sbomBundleSuffix      = ".sbom.sigstore.json"
	spdxPredicateType     = "https://spdx.dev/Document"
	maxBundleBytes        = 32 << 20 // the SBOM attestation embeds the whole SBOM
)

// Valid reports whether both halves of the identity are set.
func (s Signer) Valid() bool { return s.Issuer != "" && s.Identity != "" }
