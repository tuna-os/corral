# ADR-0011: Exposing Corral to Prometheus

**Status:** accepted
**Date:** 2026-08-01

## Context

Every backend that Corral aggregates already has metrics, and none of them answer
the question an operator of *this* tool has. KubeVirt has kube-state-metrics and
`kubevirt_vmi_*`; Proxmox has `pve-exporter`; libvirt has `libvirt-exporter`;
QEMU-under-systemd and Incus have whatever you build yourself. If you set up four
exporters, you get four disjoint views, and each one labels its series in its
own vocabulary. None of them knows that `web-1` on KubeVirt and `web-2` on
Proxmox are the same application stack. That fact lives in Corral's folders
(ADR-0008) and nowhere else.

So the value Corral adds is not "metrics about VMs". It is **the fleet as one
series set, labelled by the axes that Corral knows about and the backends do
not**. The axes are the backend and context that an instance is on, and the
pool that an operator put it in. One more axis is the health of Corral's own
view of the world.

Two facts constrain the design more than anything else:

- **Corral has no database.** Corral builds the inventory for each request. It
  fans out to every configured context concurrently: `fleet.List` runs
  `kubectl`, `incus list`, `virsh`, `systemctl`, and HTTPS calls to PVE. That is
  fine at a human's click rate and actively bad at a scraper's.
- **Partial failure is the normal state.** A context that is down is not an
  outage of Corral. `fleet.List` already returns healthy inventory alongside a
  per-context error map. Metrics must preserve that distinction, and must not
  report a down context as zero VMs.

## Decision

### `GET /metrics` on `corral web`, in the text exposition format

Not a separate `corral exporter` daemon. The web server already holds the
inventory, the folder tree, the task log, and the doctor. An exporter process
would rebuild all four and then disagree with the UI about the fleet. That is a
worse failure than no metrics endpoint at all.

Served at `/metrics`, outside `/api`, because that is where scrapers look.

### Served from a cached snapshot, with the cache age exposed as a metric

A scrape must never fan out to the backends. A timer refreshes a snapshot, and
`/metrics` renders whatever the last snapshot holds. So a scrape is a string
build over in-memory data. It costs the cluster nothing, however many
Prometheis point at it.

The cost of that is staleness, so the staleness is itself a metric:

    corral_collection_age_seconds
    corral_collection_duration_seconds
    corral_collection_success

Without the age, a frozen collector would be indistinguishable from a stable
fleet. Every scrape would stay green while the metrics describe an hour-old
world. When Corral publishes the age, an operator can alert on
`corral_collection_age_seconds > 120` and know the difference. `corral_collection_success` covers the case where
the collector ran and failed outright.

### Labels are the axes Corral knows and the backends do not

    corral_instance_info{name,backend,context,namespace,node,pool,template,bootc}
    corral_instance_running{name,backend,context,namespace,pool}
    corral_instance_ready{...}
    corral_instance_cpu_cores{...}
    corral_instance_memory_bytes{...}
    corral_instances{backend,context,state}       # fleet-level counts
    corral_pool_instances{pool}
    corral_pool_running{pool}

`context` is the name the operator gave that context in config, not the
backend's own context string. The two differ. `fleet.List` keys its per-context
errors by the config name, while the instances it returns carry the backend
context. With the raw value, `corral_backend_up{context="kubevirt"}` and
`corral_instance_running{context=""}` would describe one cluster under two
labels that no query could relate. A context absent from the config (a peer, or
one removed since) keeps its raw value and does not vanish.

`pool` is the key label, and the reason that this endpoint exists instead of
four exporters. It is the only label that spans backends. It makes
`sum by (pool) (corral_instance_running)` mean "is my application stack up"
across a KubeVirt cluster and a Proxmox host at once. An instance in no pool
carries `pool=""`, and Corral does not omit it. If Corral dropped it, the pool
sum would silently understate the fleet.

Corral emits the per-instance series for every instance, which is the usual
cardinality trade. A fleet that Corral is likely to manage has hundreds of
guests, not millions of HTTP routes. The fleet size bounds the series count,
and an operator who wants only aggregates can drop the instance metrics at
scrape config. We state this here and do not assume it. If someone points
Corral at a 50,000-VM estate, `corral_instances` still works, and they should
drop the per-instance series first.

### Backend reachability is a first-class metric, not an absence

    corral_backend_up{context,backend}
    corral_backend_error{context,backend}     # 1 when the last collection failed

`fleet.List`'s error map becomes series, and Corral does not flatten it into
"fewer VMs". A KubeVirt context that is unreachable must not look like a
KubeVirt context with nothing in it. That is precisely the alert you want, and
a naive output makes it invisible.

### Doctor checks are gauges

    corral_check{name,backend,context,severity} 1|0

`pkg/doctor` already produces a structured list of named checks with severities
and a fixable flag. When Corral exposes them, "KubeVirt's CDI is missing" can
page someone. It does not wait until someone notices it in a browser tab. The
severity label lets an alert rule distinguish `required` from `info` without a
hardcoded list of check names.

Doctor runs on its own, slower timer than the inventory: its checks shell out
more heavily and change far less often.

### Task counters

    corral_tasks_total{action,status}

Corral derives it from the existing task-log ring. So it is a gauge over a
bounded window, not a true monotonic counter, because the ring drops its oldest
entries. That is a real caveat, so the metric name and its documentation say
what it is. A counter that silently decreases breaks `rate()`, and that break is
hard to debug from the graph.

### No client_golang

The text exposition format is a stable, line-oriented format that this package
writes in about a hundred lines. `client_golang` brings a global registry, a
protobuf dependency, and a collector model. That model instruments a process as
it runs: it increments counters at the point of work. Corral's metrics are
none of that: they are a projection of a snapshot, computed all at once, with
no state of their own to register. The library would be a new dependency
for the easy part, and its model would fit badly.

The cost of this choice is that we own the escape logic. Label values here
include instance names and doctor detail strings, which contain quotes and
backslashes. It is one function, and it has tests that feed it exactly those.

## Consequences

- Grafana dashboards become possible, and Corral does not have to ship one. The
  label set is the API. `pool` is what makes a cross-backend dashboard
  expressible at all.
- The snapshot timer is a background goroutine that does real work (a full
  `fleet.List`), whether or not anyone scrapes. The timer is off unless you pass
  `--metrics`. So a `corral web` on a laptop does not poll all five backends
  forever.
- Alerts on Corral's own health become possible. `corral_collection_success
  == 0` and `corral_backend_up == 0` are the two that matter, and neither is
  expressible today.
- Authentication is the web server's existing concern. `/metrics` sits behind
  the same middleware as everything else. So a tailnet-gated Corral has a
  tailnet-gated metrics endpoint, and a scraper needs to be on the tailnet. That
  is the right default. We deliberately do not provide a `--metrics-anonymous`
  escape hatch until someone has the deployment that needs it.

## Alternatives considered

**A separate `corral exporter` binary.** Rejected: it would duplicate inventory,
folders, and doctor, and then disagree with the UI. The two views of the fleet
must come from one collection.

**Scrape-time collection.** Rejected. A 15-second scrape interval would run
`kubectl get vms` against every context four times a minute forever, and two
Prometheis would double it. The cache is not an optimisation here; it is what
makes the endpoint safe to expose.

**Push to a Pushgateway.** Rejected. Corral is a long-lived server, not a batch
job. The Pushgateway's staleness semantics are exactly the trap that
`corral_collection_age_seconds` exists to avoid.

**Reuse of the KubeVirt metrics that already exist.** They are good, and an
operator who runs only KubeVirt should use them. They cannot answer a
cross-backend question, and cross-backend is the one question that Corral is in
a unique position to answer.

## Not in scope

Historical storage is not in scope (Prometheus is the historian). Nor are alert
rules and dashboards as shipped artifacts, or per-guest CPU and memory
*utilisation*. The existing CPU ring is KubeVirt-only, and a
`corral_instance_cpu_used` that silently covers only one backend of five would be
worse than absent. When the `Observer` family covers
every backend, that becomes a straightforward addition on the same snapshot.
