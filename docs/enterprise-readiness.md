# Enterprise readiness criteria

This document defines "enterprise ready" for Corral. It is the policy for
the Q4 goals in Phase 3 of the [roadmap](../ROADMAP.md). Each rule refers to
a check that exists today, or it names the issue that adds the check.

A feature is enterprise ready when it meets all of these criteria:

1. It is at the **Stable** stability level.
2. It works on at least one **Tier 1** backend, and it declares its backends.
3. CI tests its upgrade path from the previous release, and the docs describe it.
4. The artifacts that deliver it are **signed** and have an **SBOM**.
5. It has user documentation. It has an entry in the
   [guide to problems and fixes](troubleshooting.md).

The sections below define each term.

## Stability levels

Each command, REST endpoint, web screen and plugin has one stability level.
The user guide or the plugin catalog
([first-party-plugins.md](first-party-plugins.md)) shows the level.

| Level | Meaning | Change policy | Minimum evidence |
|---|---|---|---|
| Experimental | A design in progress. It can disappear. | Any release can change or remove it. | Unit tests. A label in the docs and in `--help`. |
| Preview | The design is complete. We collect feedback. | A change needs a note in the release notes. | Unit tests and conformance tests. User docs. |
| Stable | Supported for production use. | A change that is not compatible needs a deprecation period (see below). | All Preview evidence. CI end-to-end tests on a Tier 1 backend. A troubleshooting entry. |

There is no long-term support (LTS) level before 1.0. The
[support window](#support-window) section gives the reason.

### Promotion

To promote a feature, open a PR that does these steps:

1. Change the level in the docs.
2. Link the evidence for the new level (tests, CI job, docs).
3. Update this table if the feature is a new kind of surface.

To demote a feature, use the same steps. A demotion from Stable needs a note
in the release notes.

## Versioning and upgrades

Corral uses [semantic versions](https://semver.org/). The release workflow
stamps the version from the newest `v*` tag (`release-binaries` in
[ci.yml](../.github/workflows/ci.yml)).

### Before 1.0

- A minor release (0.x) can include a change that is not compatible. The
  release notes must name it, and they must give the migration steps.
- A patch release (0.x.y) has no change that is not compatible.

### From 1.0

- A change to a Stable surface that is not compatible needs a major release.
- A deprecated Stable surface stays for at least one minor release. It prints
  a warning before Corral removes it.

### Contracts that have their own version

These contracts have a version identifier. A change that is not compatible
needs a new identifier, not a change to the current one.

| Contract | Identifier | Where |
|---|---|---|
| Marketplace index | `corral.marketplace/v2` | [marketplace/schema-v2.json](../marketplace/schema-v2.json) |
| Plugin metadata handshake | `corral.plugin/v1` | [plugin-marketplace.md](plugin-marketplace.md) |
| Vendored asset manifest | `schemaVersion` field | `pkg/web/static/vendor/MANIFEST.json` |

Corral decodes the marketplace index with `DisallowUnknownFields`. Thus a
new field in a published index stops every installed Corral that reads it.
Add a field only with a new schema version. Then publish the old and the
new index together until the support window of the old clients ends.

### Upgrade path

- Corral supports an upgrade from the previous minor release to the current
  one. To skip a minor release, upgrade through each release in sequence.
- After an upgrade, Corral must read the on-disk state of the previous
  release. This state is the registry file, the `tailvm-` names in
  [SPEC.md](../SPEC.md) and the plugin state in `.installed/`. A format change needs a migration in code and
  a test that reads the old format.
- A plugin can declare the Corral versions it supports in its `corral` field.
  Corral refuses to install a plugin that does not support the version that
  it has.

## Support window

[SECURITY.md](../SECURITY.md) is the source for the support window. This
document does not change it.

- **Before 1.0:** Security fixes go to the most recent release only.
- **Response targets:** Acknowledgment in 48 hours. A status update in 5
  business days. These targets apply to all stability levels.
- **Regressions:** A regression in a Stable feature blocks the next release.
  A regression in a Preview or Experimental feature does not.

Corral is a community project. It has no paid support and no contractual
SLA. The targets above are commitments of the maintainers, not a contract.

An LTS branch needs a second line of supported releases. Before 1.0, the project
does not have the maintainer capacity for that. At 1.0, the PR that tags the
release must also update SECURITY.md and this section with the new window.

## Backend support tiers

The [backend support matrix](backend-support.md) shows what each backend can
do. The [parity table](backend-parity.md) comes from `pkg/backend.Matrix`,
and conformance tests keep it correct. The tiers below show how well CI
tests each backend.

| Tier | Requirement | Backends today | Evidence |
|---|---|---|---|
| Tier 1 | A real backend runs in CI on each push to `main`. | KubeVirt, Incus, libvirt, local QEMU | [e2e.yml](../.github/workflows/e2e.yml) (kind with emulated KubeVirt), [e2e-incus.yml](../.github/workflows/e2e-incus.yml) (Incus, libvirt and QEMU) |
| Tier 2 | Unit tests and fixture tests. No live backend in CI. | Proxmox VE | `pkg/proxmoxbe` tests |
| Tier 3 | A proposal or a work in progress. | vSphere, Nutanix, XenServer, VirtualBox | [RFC-0003](rfc/0003-enterprise-backends.md), #507 |

Rules:

- A Stable feature must have CI end-to-end tests on a Tier 1 backend.
- A feature can be Stable on one backend and Preview on a different backend.
  The docs must show the level for each backend.
- To move a backend to Tier 1, add a live CI job for it.
- A plugin must declare `supportedBackends`. "Installed" does not mean that a
  workflow works on all backends.

## Supply chain

### What CI enforces today

| Control | Where | Status |
|---|---|---|
| Actions pinned to a commit SHA, least-privilege tokens | `.github/workflows/*.yml` | Enforced |
| Vendored browser assets have a recorded source, version and SHA-256 | `pkg/web/static/vendor/MANIFEST.json`, `TestVendorManifest` (#215, #396, #504) | Enforced |
| Dependency updates | [renovate.json](../renovate.json) (#97) | Enforced |
| Plugin artifacts are immutable and have a SHA-256 in the index | `publish-plugins` job, `scripts/generate-marketplace.sh` | Enforced |
| Corral refuses an index that is not `corral.marketplace/v2` | `pkg/plugin` | Enforced. The operator can opt out for each source with `--allow-unverified`. |
| Core binaries have a `SHA256SUMS` file | `release-binaries` job | Enforced |
| Signed plugin artifacts and SBOMs, verified at install time | `publish-plugins` job, `pkg/plugin` (#505) | Enforced |
| Signed core binaries and container image, with SBOMs | `release-binaries` and `publish-image` jobs, [release.yml](../.github/workflows/release.yml), `scripts/install.sh` (#584) | Enforced |

### Requirements

The organization policy is
[tunaos#1187](https://github.com/tuna-os/tunaos/issues/1187). Corral follows
it:

- **Keyless signatures.** CI signs with Sigstore and Cosign. It uses the
  GitHub OIDC token of the workflow that publishes. Do not use a long-lived
  private key or a secret to sign.
- **Pinned identity.** A verifier pins the OIDC issuer
  (`https://token.actions.githubusercontent.com`) and the exact workflow and
  ref that publish. Do not use a regular expression that a fork can match.
- **SBOM.** Each released binary has an SPDX JSON SBOM. A signed attestation
  binds the SBOM to the digest of that binary.
- **Fail closed.** A release fails when CI cannot sign or verify an artifact. An
  install fails when the signature or the SBOM attestation is missing or not
  valid.
- **Immutable targets.** CI signs digests and immutable release assets, not
  mutable tags or channels that change.

The existing Ed25519 `signature` field in the marketplace index does not
meet these requirements. The public key comes from the same index as the
signature. Thus a person who can change the index can also change the key.
A trust anchor must come from Corral or from the operator, not from the
index.

## Feature checklist

Use this checklist in the PR that promotes a feature to Stable, or that
calls a feature enterprise ready:

- [ ] The docs show the stability level of the feature for each backend.
- [ ] CI runs end-to-end tests of the feature on a Tier 1 backend.
- [ ] A test reads the on-disk state of the previous release.
- [ ] The release notes give the upgrade steps.
- [ ] CI signs the artifacts that deliver the feature, and each has an SBOM.
- [ ] The [guide to problems and fixes](troubleshooting.md) has an entry for
  the feature.
- [ ] The feature uses only the RBAC verbs in `deploy/` that it needs.

## Phase 3 goals

| Goal | Issue | Criterion that it closes |
|---|---|---|
| VDI GA | #69 | Stability level Stable for the VDI plugin |
| Plugin signing and SBOM | #505 | Supply chain, for plugins |
| Enterprise backends | #507 | Tier 3 backends move to Tier 2 or Tier 1 |
| Structured logs and tracing | #419 | Troubleshooting evidence for Stable features |
