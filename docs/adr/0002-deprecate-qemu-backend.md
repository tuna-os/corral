# Deprecate the qemu backend (soft)

Corral repositions from a "two backends, laptop + cluster" tool to a KubeVirt
VM manager. The qemu backend is **soft-deprecated**: it stays in the tree and
keeps working for existing local VMs, but receives no new features, is dropped
from docs and marketing, and is never surfaced in the web UI. Actual code
removal is a separate, later decision.

Why: the web UI — the focus of ongoing work — is already kubevirt-only, and the
operator's real usage (bootc images on the cluster) always has a cluster
present. Maintaining a second backend in every web feature (console, snapshots,
migrate, GPU…) would roughly double surface for a path no longer used.

Consequence: corral now effectively **requires a KubeVirt cluster**; the "quick
VM on your laptop, no infra" story is retired. README and the "same five
commands everywhere / two backends" framing need rewriting. The `Backend`
concept in `CONTEXT.md` collapses to one primary value plus a frozen legacy.
