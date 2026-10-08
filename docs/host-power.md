# Host power hook

Some VM hosts are not always on. Examples are an on-demand cloud instance
that stops when idle, a lab machine behind a smart plug, and a workstation
with Wake-on-LAN. The host-power hook lets the web UI show these machines and
power them on and off. Corral core contains no provider code. Any installed
plugin that declares the `host-power` capability provides it (ADR-0007).

## Contract (`corral.plugin/v1`)

The plugin lists `host-power` in its `--corral-plugin-metadata` capabilities
and provides these commands:

| Command | Output |
|---|---|
| `corral-<name> host-power list` | JSON array of `sdk.Host` on stdout |
| `corral-<name> host-power start <id>` | exit 0 once the provider accepted the request |
| `corral-<name> host-power stop <id>` | exit 0 once the provider accepted the request |

```json
[{"id": "eu-north-1/i-0abc", "name": "KubeVirt (AWS)", "node": "ip-10-20-1-12",
  "state": "stopped", "actions": ["start"], "detail": "m7i.2xlarge · eu-north-1"}]
```

`state` is one of `running`, `stopped`, `starting`, `stopping` or `unknown`.
`actions` lists the actions that a user can request now. `node` ties the host to a
Kubernetes node, so the UI can show which VMs it carries.

## Web API

- `GET /api/hostpower` returns the hosts from every capable plugin. If a
  plugin fails, the response reports it under `errors` and still shows the others.
- `POST /api/hostpower/{plugin}/{start|stop}?id=<id>` is admin-gated, like
  every mutation (`CORRAL_ADMINS`).

Each plugin call times out after 30 s.

## First-party provider: `aws-power`

`corral-aws-power` manages the EC2 instances that have the tag `corral:host-power`
(the value is the display name), optionally with `corral:node=<k8s node>`. Configure it
with `CORRAL_AWS_POWER_REGIONS` (or `AWS_REGION`) and standard AWS
credentials. Scope the credentials to `ec2:DescribeInstances` plus
`ec2:StartInstances`/`ec2:StopInstances` on the tagged instances. The plugin
also refuses to act on any instance without the tag.

Pair it with an idle-stop job, so that "power on" is the only thing a user
ever has to do.
