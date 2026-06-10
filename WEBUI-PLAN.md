# Corral Web UI — Plan

**Goal:** bring as many Proxmox VE features as possible to KubeVirt, in a
modern, mobile-friendly web UI that works in tandem with the `corral`
CLI/TUI. Single Go binary, no JS build step, served locally (`corral web`)
or on the cluster itself.

Live deployment: `http://corral.manatee-basking.ts.net` (Tailscale operator,
manifest at [`deploy/corral-web.yaml`](deploy/corral-web.yaml)).
Code: [`pkg/web/`](pkg/web/), command
[`cmd/web.go`](cmd/web.go).

## Architecture

```
corral web (Go, net/http)
├── embedded SPA (vanilla JS + CSS, //go:embed — no node, no bundler)
│     noVNC (CDN)  → graphical console
│     xterm.js (CDN) → serial console
├── REST API  /api/vms, /api/nodes, /api/vms/{ns}/{name}/{action}
├── WS bridge /api/vnc/{ns}/{name} ↔ virtctl vnc --proxy-only
├── WS bridge /api/tty/{ns}/{name} ↔ virtctl console
└── shells out to kubectl/virtctl (same as CLI/TUI — no client-go)
```

**Tandem operation:** the web server reuses `pkg/kubevirt` and the same
registry (`~/.local/share/tailvm/registry.json`) as the CLI/TUI, so a VM
created in the browser is immediately `corral ssh`-able and vice versa.
In-cluster the registry is absent (stateless pod) — cluster state itself is
the source of truth there.

**Deployment modes:**
1. **Local** — `corral web` (default `127.0.0.1:8006`; bind the Tailscale IP
   to share with the tailnet).
2. **On-cluster** — `ghcr.io/hanthor/corral` image (alpine + corral +
   kubectl + virtctl), ServiceAccount with a scoped ClusterRole, Service
   annotated `tailscale.com/expose` → `corral.<tailnet>.ts.net`. Built and
   pushed by CI (`.github/workflows/ci.yml`).

## Proxmox feature map

| Proxmox feature | KubeVirt equivalent | Status |
|---|---|---|
| Server view tree (datacenter → node → guest) | nodes + VMIs grouped by node | ✅ shipped |
| Guest summary (status, resources, IP) | VM/VMI merge | ✅ shipped |
| noVNC console | `virtctl vnc` over WS bridge | ✅ shipped |
| xterm.js serial console | `virtctl console` over WS bridge | ✅ shipped |
| Start / shutdown / reboot | virtctl start/stop/restart | ✅ shipped |
| Pause / resume | virtctl pause/unpause | ✅ shipped |
| Live migration | `virtctl migrate` | ✅ shipped (button) |
| Create VM wizard | container-disk / ISO (CDI) / bootc / PVC sources | ✅ shipped (single dialog) |
| Build OS from container (no Proxmox equivalent!) | bootc disk built on-cluster, live build log in UI | ✅ shipped |
| Hardware view | parse VM spec (disks, volumes, firmware) | ✅ shipped (read-only) |
| Delete guest + storage | DeleteVM (PVCs, DataVolumes, proxy) | ✅ shipped |
| Mobile-usable UI | responsive drawer layout | ✅ shipped |
| Resource graphs (CPU/mem/IO) | metrics-server / Prometheus scrape | ⏳ phase 2 |
| Snapshots | VirtualMachineSnapshot CRD (needs snapshot controller + CSI) | ⏳ phase 2 |
| Clone | VirtualMachineClone CRD | ⏳ phase 2 |
| Hardware editing (add disk/NIC, resize) | patch VM spec, hotplug volumes | ⏳ phase 2 |
| Storage view (pools → PVCs) | StorageClasses + PVC browser | ⏳ phase 2 |
| Templates | golden PVCs / DataSources + clone-on-create | ⏳ phase 3 |
| Backup / restore | VM export API / Velero | ⏳ phase 3 |
| Firewall | NetworkPolicy editor | ⏳ phase 3 |
| Users / permissions / realms | none (single-operator, tailnet-gated) | ❌ out of scope |
| HA groups | covered by Kubernetes itself | ❌ not needed |
| LXC containers | plain pods — different UI problem | ❌ out of scope |
| Task log | recent K8s events for VM objects | ⏳ phase 2 |
| ISO upload | CDI upload proxy (`virtctl image-upload`) | ⏳ phase 3 |

## Phase 2 (next)

1. **Events tab** — `kubectl get events --field-selector involvedObject.name=<vm>`
   rendered as Proxmox's task log; surfaces scheduling/import errors fast.
2. **Metrics** — `kubectl top pod` for virt-launcher pods (works once
   metrics-server is installed); sparkline on the summary tab.
3. **Snapshots tab** — list/create/revert `VirtualMachineSnapshot` (requires
   deploying the snapshot controller; local-path CSI has no snapshot support,
   so this may need a storage upgrade — document the gap in the UI).
4. **Volume hotplug** — `virtctl addvolume/removevolume` from the Hardware tab.

## Phase 3 (later)

- Templates: mark a PVC as golden → CDI clone on create.
- ISO library: upload via CDI upload proxy, browse existing DataVolumes.
- VM export / backup buttons (KubeVirt export API).
- NetworkPolicy editor ("Firewall" tab).
- Optional auth (basic auth / Tailscale identity headers via serve) for the
  on-cluster deployment — today it relies entirely on tailnet membership.

## Security posture

- No authentication built in; reachable only via tailnet (operator-proxied
  Service or a Tailscale-IP bind). Never expose on a public interface.
- In-cluster ServiceAccount is scoped: VM/VMI lifecycle + their subresources,
  CDI DataVolumes, PVCs, nodes read, plus delete rights for the per-VM proxy
  resources. No secrets access.
- The websocket bridges shell out to `virtctl` per connection; connections
  die with the page, and the child process is killed on disconnect.
