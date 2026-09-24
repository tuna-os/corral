# ADR-0001: K8s RBAC → Proxmox privilege mapping

**Status:** accepted  
**Date:** 2026-06-11

## Context

The compatibility layer for the Proxmox API must return plausible RBAC
responses to the `/access/users`, `/access/groups`, and `/access/roles`
endpoints. Then Proxmox ecosystem tools (Terraform providers, Ansible modules)
can complete their initialization without errors.

KubeVirt clusters use Kubernetes RBAC (ClusterRoles, ClusterRoleBindings,
ServiceAccounts) — a fundamentally different model from Proxmox's flat
user@realm / privilege-string system.

This ADR documents the translation we chose.

## Decision

### User mapping

Proxmox users are `user@realm` strings. We map K8s identities as follows:

| K8s identity | Proxmox user | Rationale |
|---|---|---|
| `root@pam` (static) | `root@pam` | Built-in superuser — always present |
| ClusterRoleBinding subject `kind: User` | `subject@k8s` or `subject@pve` | Realm derived from apiGroup |
| ClusterRoleBinding subject `kind: ServiceAccount` | `sa-name@sa` | ServiceAccounts are machine identities |
| ClusterRoleBinding subject `kind: Group` | → group, not user | Groups get their own endpoint |

On clusters where RBAC queries fail (e.g. insufficient permissions), the
endpoint falls back and returns only `root@pam`.

### Group mapping

The endpoint takes the Proxmox groups from the subjects of each
`ClusterRoleBinding` with `kind: Group`. If no groups exist, the endpoint
returns a synthetic `Administrators` group. Tools that expect at least one
group then don't break.

### Role mapping

K8s ClusterRoles use (apiGroups, resources, verbs) tuples. Proxmox uses
privilege strings like `VM.Allocate`, `Datastore.Audit`. We define four
roles with a fixed set of privileges. The roles approximate the most common RBAC profiles in K8s:

| Proxmox role | K8s equivalent | Privileges granted |
|---|---|---|
| `Administrator` | `cluster-admin` | All 24 Proxmox privileges |
| `Operator` | `admin` | VM.* + Datastore.* + Sys.Audit + Sys.Console |
| `Viewer` | `view` | VM.Audit, Datastore.Audit, Sys.Audit |
| `NoAccess` | (default) | None |

We do **not** dynamically enumerate every K8s ClusterRole and translate it
rule-by-rule. That mapping is lossy (K8s rules are per-resource, Proxmox
privileges are per-category). Also, the Proxmox ecosystem tools do not
meaningfully enforce or gate on specific privilege strings. They validate
that a role set exists.

### Privilege taxonomy

The 24 Proxmox privileges we expose:

```
VM.Allocate           VM.Audit           VM.Clone
VM.Config.CDROM       VM.Config.CPU      VM.Config.Disk
VM.Config.Memory      VM.Config.Network  VM.Config.Options
VM.Console            VM.Migrate         VM.Monitor
VM.PowerMgmt          VM.Snapshot
Datastore.Allocate    Datastore.AllocateSpace
Datastore.AllocateTemplate  Datastore.Audit
Sys.Audit             Sys.Console        Sys.Modify
Sys.PowerMgmt         Sys.Syslog
Permissions.Modify
```

## Consequences

- **Positive**: Some tools in the Proxmox ecosystem enumerate users/roles during
  initialization (bpg Terraform provider, proxmoxer Python client). These
  tools get plausible responses and complete without errors.
- **Positive**: The static role mapping is simple to reason about and
  needs no RBAC reconciliation loop.
- **Negative**: There is no dynamic sync between K8s RBAC changes and the
  Proxmox API view. Users created/removed via `kubectl` will not appear
  until the next API request rebuilds the list from live state.
- **Negative**: We do not enforce Proxmox privileges at the API layer.
  Tailnet membership + K8s RBAC do the auth enforcement. The
  Proxmox privilege strings are presentation-only.
