# Corral deployment modes

`corral web` serves the dashboard. It has no login of its own. This page shows four ways to deploy it.

Pick one mode per install. Each section gives the shape, the commands, the risks, and where to look when it breaks.

| Mode | Reach | Login path |
|---|---|---|
| Public behind a login | Internet | `corral-auth` gateway or front proxy with OIDC |
| Local PC | Loopback only | None (your user account is the boundary) |
| Homelab over VPN | Tailnet only | Tailscale headers plus `CORRAL_ADMINS` |
| 24/7 vs on-demand | Either | Same as the mode it extends |

## 1. Public web access behind a login

Never bind `corral web` to a public address. It accepts each request that reaches it.

### Shape

```text
browser -> TLS -> edge proxy -> 127.0.0.1:8443 -> corral-auth -> 127.0.0.1:8006 -> corral web
```

The edge proxy faces the net. `corral web` hears loopback traffic only. The gateway proves who calls. It strips fake headers from clients.

### Commands

Use the `corral-auth` plugin as the gateway. It supports OIDC, htpasswd basic auth, and passkeys.

```bash
go build -o corral-auth ./cmd/corral-auth
export CORRAL_AUTH_COOKIE_SECRET="$(openssl rand -base64 32)"

# Upstream hears loopback only.
corral web --addr 127.0.0.1:8006 &

# Basic auth example. Swap in OIDC flags for SSO.
# Loopback only. A TLS proxy faces the net (see below).
corral-auth --addr 127.0.0.1:8443 \
  --upstream http://127.0.0.1:8006 \
  --basic-htpasswd /etc/corral/users.htpasswd
```

| Flag | Use |
|---|---|
| `--upstream` | URL of `corral web` (keep the loopback default) |
| `--cookie-secret` | 32+ byte session key (the gateway refuses short ones) |
| `--basic-htpasswd` | Apache htpasswd file for simple logins |
| `--issuer`, `--client-id`, `--client-secret`, `--redirect-url` | OIDC provider settings |
| `--passkey-store`, `--passkey-rp-id`, `--passkey-origin` | Passkey settings |

Add `CORRAL_ADMINS` as a second lock. It names the tailnet logins that may change state. All others get read-only access.

```bash
CORRAL_ADMINS="alice@github,bob@github" corral web --addr 127.0.0.1:8006
```

Check the result at `GET /api/whoami`. It shows login, name, admin state, and whether the allowlist applies.

### TLS edge

`corral-auth` speaks plain HTTP. It must sit behind a TLS proxy on a public box. Basic-auth must never ride plaintext.

Caddy ends TLS and passes traffic to the loopback gateway. Caddy fetches and renews the certificate. The gateway still drops client headers behind the proxy.

```caddy
# /etc/caddy/Caddyfile
corral.example.com {
	reverse_proxy 127.0.0.1:8443
}
```

```bash
caddy fmt --overwrite /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
systemctl reload caddy
```

### Risks

| Threat | Guard |
|---|---|
| Direct hits on port 8006 skip the login | Bind `--addr` to loopback and filter the port |
| Basic-auth over plain HTTP leaks passwords | End TLS at the edge proxy first |
| Fake `Tailscale-User-Login` headers | The gateway drops client headers and sets its own |
| Stolen session cookie | 32+ byte secret, HTTPS at the edge, 8h sessions |
| Open mutations for each visitor | Set `CORRAL_ADMINS` so guests stay read-only |

Background: [ADR-0003](adr/0003-identity-source.md) sets the header trust rules. [ADR-0006](adr/0006-optional-auth-gateway-plugin.md) defines the gateway split.

### When it breaks

- 401 at the edge means bad or missing credentials. Test `curl -u user:pass` against [the gateway health endpoint](https://host:8443/healthz).
- 403 from `/api/*` means the caller has no admin rights. Check `/api/whoami` and `CORRAL_ADMINS`.
- The gateway refuses to start with no provider. Set one of OIDC, `--basic-htpasswd`, or `--passkey-store`.
- For dead pods and bad probes see [corral-web-health](runbooks/corral-web-health.md).

## 2. Local PC

Run the dashboard on your own box for daily use. Keep it on loopback.

### Bare binary

```bash
corral web
# Dashboard at http://127.0.0.1:8006
```

Add `--demo` to tour the UI with fake data. Add `--metrics` to expose `/metrics` for a local scraper.

### Container

Build and smoke-test the image with the checked-in script. It needs `podman` or `docker`.

```bash
./scripts/smoke-web-image.sh
```

Run it for daily use with plain `podman` or `docker` commands.

```bash
podman run -d --name corral-web -p 127.0.0.1:8006:8006 ghcr.io/tuna-os/corral:latest
docker run -d --name corral-web -p 127.0.0.1:8006:8006 ghcr.io/tuna-os/corral:latest
```

Both publish loopback only. Both answer `/healthz` and `/readyz` on port 8006.

### Quadlet unit

For an always-on local dashboard use the Quadlet unit in `containers/corral-web.container`. It pins the image, the loopback port, and a named volume for the registry.

```bash
mkdir -p ~/.config/containers/systemd
cp containers/corral-web.container ~/.config/containers/systemd/
systemctl --user daemon-reload
systemctl --user enable --now corral-web.service
journalctl --user -u corral-web.service
```

CI checks the unit file on each push. The test runs the real generator where it can.

### When it breaks

- Port clash on 8006 means an old copy still holds it. Stop the old one first.
- A fresh container loses its registry unless the volume exists. Keep the `Volume=` line.
- `quadlet --dryrun` flags bad keys before you install. Run it after each edit.

## 3. Homelab server over VPN

Serve the dashboard from your homelab and reach it through Tailscale only. No public ports open at all.

### Shape

```text
your device -> tailnet (WireGuard) -> Tailscale Ingress -> corral-web Service -> pod
```

`deploy/corral-web.yaml` ships this shape. One `Ingress` with class `tailscale` fronts the `corral-web` Service. TLS ends at the operator. The manifest holds a single ingress on purpose.

### Commands

```bash
kubectl apply -f deploy/corral-web.yaml
kubectl -n corral rollout status deploy/corral-web
kubectl -n corral get ingress corral-web
```

Open [the MagicDNS address](https://corral.<tailnet>.ts.net) with `<tailnet>` swapped for your tailnet name. Tailscale MagicDNS resolves it. No DNS records to add by hand.

Lock mutations down to your own logins.

```bash
kubectl -n corral set env deploy/corral-web CORRAL_ADMINS="alice@github"
```

The pod reads `Tailscale-User-Login` from the proxy and grants admin rights to listed logins only. Unlisted tailnet users see a read-only UI.

### Preflight without live infra

`TestTailscaleIngressManifest` guards the trust base in CI. It asserts one `Ingress`, class `tailscale`, live `/healthz` and `/readyz` probes, and the no-second-path note.

```bash
go test ./cmd/ -run TestTailscaleIngressManifest -count=1
```

Manual gate: the steps above need a live tailnet and a cluster. The test pins the manifest. A person still must apply it and open the MagicDNS URL.

### When it breaks

- 404 from the tailnet name means the operator has no `Ingress`. Check `tailscale status` on the cluster egress node.
- Empty identity at `/api/whoami` means traffic skipped the proxy. Check for a second route to the pod.
- 503 on `/readyz` means a bad state dir. See [corral-web-health](runbooks/corral-web-health.md).
- `corral_backend_up == 0` means the pod lost the cluster API. Check kubeconfig context and node links.

## 4. 24/7 vs on-demand web operation

Run the dashboard around the clock or start it when you need it. Both use the same binary and the same probes.

### Always on

Use the container from mode 2 or the cluster `Deployment` from mode 3. Both restart on crash and both expose `/healthz` plus `/readyz` to their supervisor.

Resource shape from the manifest: 50m CPU and 64Mi RAM asked, 256Mi RAM max. The UI idles near the floor. Add `--metrics` when a scraper watches it. The flag takes one fleet pass at boot, so `/readyz` lags on slow links.

```bash
corral web --addr 127.0.0.1:8006 --metrics &
curl -s http://127.0.0.1:8006/metrics | grep corral_collection_age_seconds
```

### On demand

Use `corral web service` for a user unit that starts at login. It needs no root.

```bash
corral web service print --addr 127.0.0.1:8006
corral web service install --addr 127.0.0.1:8006
corral web service status
```

The unit uses `Restart=on-failure` with a 2s delay. `TestGeneratedWebUnitVerifies` runs `systemd-analyze verify` on its output in CI.

Poll for boot with the checked-in helper. It waits on `/healthz`, then asserts `/readyz` reports ready.

```bash
./scripts/check-web-probes.sh http://127.0.0.1:8006 30
```

`TestSupervisorProbeContract` pins the same deal in Go: 200 plus `ok` on `/healthz`, 200 plus `{"status":"ready"}` on `/readyz`, and 503 with an error field before the store exists.

Note: `corral web` has no socket activation. The code never reads `LISTEN_FDS`. On demand means start at login or by hand, not start on first TCP byte.

### When it breaks

- Slow first paint after boot is normal. Wait for `/readyz` before you judge.
- Crash loops point at a bad `--addr` or a lost state dir. Read `journalctl --user -u corral-web` or pod logs.
- Stale metrics mean `--metrics` never passed. The endpoint then always reports failure.
- Full probe and metrics triage lives in [corral-web-health](runbooks/corral-web-health.md).
