# ADR-0006: Optional authentication is a gateway plugin

**Status:** accepted
**Date:** 2026-07-29

## Context

ADR-0003 uses identity headers asserted by Tailscale ingress. Corral now also
supports ingress-agnostic and peer deployments, where that single trust path
does not always exist. If the core took in OIDC, sessions, and WebAuthn, the
main binary would grow and every deployment would depend on optional auth.

## Decision

Ship `corral-auth` as a separate reverse-proxy plugin in front of `corral web`.
It uses `coreos/go-oidc` for discovery and ID-token verification. It uses the
authorization code flow of OAuth2, with PKCE and nonce. It keeps sessions in
encrypted Gorilla cookies. It removes client-supplied identity headers and sets
the existing identity contract only after authentication. Go's reverse proxy keeps
API streams and WebSocket upgrades intact.

Tailscale ingress remains a supported identity adapter, and it is
first-class. Deployments choose either a trusted Tailscale-only path or the
auth gateway; KubeVirt and the core web server remain ingress-agnostic.

Passkeys belong in this plugin with `go-webauthn`, but they need a credential
store, RP ID/origin, enrollment bootstrap, and recovery policy. The project does
not show them as complete until those pieces ship.

## Consequences

- The main Corral binary does not link OIDC/session dependencies.
- Operators can upgrade the gateway independently and use it again for Corral peers.
- When the upstream listener of Corral trusts the identity headers from the
  gateway, it must not be publicly reachable.
