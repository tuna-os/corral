# ADR-0003: Identity source for the web UI (Tailscale headers, not OIDC)

**Status:** accepted for Tailscale-only deployments; extended by ADR-0006
**Date:** 2026-06-21

## Context

corral-web mutates a real cluster (start/stop/delete VMs, create disks, live
migrate). Until now it had no authentication: anyone who could reach the pod had
full control. We want to (1) show *who* uses the UI and (2) split
read-only from admin, without a new identity provider to run.

corral-web is reachable **only** through the Tailscale operator Ingress
(`ingressClassName: tailscale`). Tailscale already authenticates every
connection at the network layer (WireGuard + tailnet ACLs) and the operator
proxy forwards the caller's verified identity as request headers:

- `Tailscale-User-Login` — the tailnet login (e.g. `alice@github`)
- `Tailscale-User-Name` — display name

The proxy ends the tunnel and sets these headers itself. So no client
can spoof them: traffic that didn't traverse the Tailscale proxy never gets
them, and the proxy overwrites any client-supplied values.

## Decision

**Use the identity headers from the Tailscale ingress as the identity source.** Do not
build or demand OIDC for this slice.

- The server reads `Tailscale-User-Login` / `-Name` per request (`caller()`).
- The operator sets an admin allowlist out-of-band via the `CORRAL_ADMINS`
  environment variable (comma/space separated tailnet logins).
- Authorization is **deny-by-default for mutations when an allowlist exists**:
  - `CORRAL_ADMINS` **unset/empty** → single-user / open mode: every caller is
    an admin (preserves prior behaviour; safe for a personal cluster).
  - `CORRAL_ADMINS` **set** → only listed logins may send requests that change state;
    everyone else is read-only.
- Enforcement is an `adminGate` middleware. Safe methods (GET/HEAD/OPTIONS)
  always pass. Methods that change state (POST/DELETE/PATCH/PUT) need an admin, else
  `403`. This is **defense in depth** — the SPA also hides the controls that change
  state, but the server is the boundary.
- `GET /api/whoami` exposes `{login, name, admin, enforced}` so the UI can show
  the logged-in user and switch to read-only mode.

### Trust boundary

The server trusts the headers **only** because of a guarantee in the deployment.
All traffic arrives via the Tailscale operator proxy (no other Ingress/Service
path to the pod). If corral-web is ever reachable by a second path, this assumption breaks
and the gate must move behind a proxy that does authentication. Documented as a risk on
the deployment manifest.

## Alternatives considered

- **OIDC (Dex/Keycloak/cloud IdP):** the "correct" long-term answer. It is also
  the path to real per-VM RBAC (see ADR-0001's K8s-RBAC → Proxmox-privilege
  mapping). Rejected for this slice: it needs an IdP, client registration, and
  session management. That is disproportionate when Tailscale already proves
  who the user is.
  Revisit when corral-web grows beyond a single tailnet or needs SSO.
- **mTLS client certs:** strong but operationally heavy (cert distribution);
  redundant with Tailscale's existing device identity.
- **Kubernetes `TokenReview` / impersonation:** ties UI auth to kube tokens,
  which tailnet users don't hold; out of scope.

## Consequences

- Zero new infrastructure; works the moment `CORRAL_ADMINS` is set.
- Identity is only as strong as the tailnet ACLs and the single-ingress
  assumption above.
- A natural upgrade path remains. Later, swap `caller()` for an OIDC-session
  lookup, with no change to the gate or the UI contract.
