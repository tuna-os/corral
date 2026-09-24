# ADR-0007: Core owns fleet primitives; plugins own optional workflows

Status: accepted

## Decision

The main binary owns the stable platform seam. This seam has contexts and
defaults, canonical instance identity, inventory, lifecycle dispatch, capability
reports, federation transports, doctor probes, and CLI/TUI/web presentation.
These must work before an extension is installed because every workflow needs
the same unambiguous target selection and safety rules.

Standalone executables own optional or high-dependency workflows: auth, disk
backup, bootc builds, GPU policy, Proxmox compatibility, schedules, snapshot
retention, and Windows installation. They use `corral.plugin/v1`, declare
permissions and supported backends, and may depend on core `pkg/` adapters.
External plugins use the same contract as first-party plugins.

Core adopts Incus because it is a compute backend, not a workflow.
`corral-incus` remains only as a compatibility executable, and the marketplace
excludes it. OIDC, Basic Auth, and passkeys remain in `corral-auth`; their
identity and cryptography dependencies do not belong in the main CLI binary.
The VDI experiment remains outside this decision, and the platform-completion
work does not build it.

Some first-party plugin functionality is now also reachable from the web
binary through shared packages. That is a compatibility surface, not permission
to silently make a marketplace plugin universal. UI controls must follow
instance capabilities and plugin `supportedBackends`. Issues #129–#134 track
the missing adapters.

## Marketplace and contribution model

Marketplaces are indexes of standalone binaries with signatures or checksums,
not Go modules that Corral loads into its address space. Multiple sources,
provenance, pinned versions, compatibility ranges, immutable artifacts, explicit
permission consent, and atomic rollback make external contribution possible.
Third-party code does not get the trust of the main process. Publication does
not imply a runtime sandbox; operating-system and cluster permissions remain the actual
security boundary.

## Consequences

- A new compute backend needs a core adapter and doctor probe.
- A new optional workflow should begin as a plugin.
- Reusable operations for a backend belong under `pkg/`, never under `cmd/`.
- Plugins must fail clearly on unsupported selected contexts.
- We measure the growth of the core binary from stripped artifacts. Optional
  auth and workflow dependencies remain isolated in their plugin binaries.
