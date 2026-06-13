# Multus CNI on Talos (secondary VM networks)

Multus runs fine on Talos: it installs as a daemonset that chains itself in
front of the existing CNI (flannel here). Talos exposes `/opt/cni/bin` and
`/etc/cni/net.d` to host-path mounts, and already ships the standard CNI
plugin binaries (`bridge`, `host-local`, …) that flannel uses — so bridge
NADs need nothing installed on the nodes.

```bash
# Install (pinned v4.2.2, thin plugin)
kubectl apply -f deploy/multus/multus-daemonset.yaml
kubectl -n kube-system rollout status ds/kube-multus-ds --timeout=5m

# Sanity: existing pod networking must be unaffected
kubectl -n kube-system get pods

# Test NAD: node-local bridge, host-local IPAM (no physical uplink)
kubectl apply -f deploy/multus/test-nad.yaml
```

Corral integration (already wired):
- `GET /api/nads` lists NetworkAttachmentDefinitions; once one exists the
  Hardware tab shows an **Add NIC** button.
- `POST /api/vms/{ns}/{name}/nics {"nad": "corral-test-bridge"}` attaches a
  bridge-bound secondary interface (KubeVirt multus network). The VM sees a
  second NIC; configure it with cloud-init or in-guest.

Caveats:
- A NAD that should reach the physical LAN needs `macvlan`/`ipvlan` with the
  right parent interface, or a host bridge configured in the Talos machine
  config — cluster-specific, not covered by the test NAD.
- VMs with bridge-bound secondary interfaces are not live-migratable
  (KubeVirt restriction); corral's migrate button reflects that.
- Uninstall: `kubectl delete -f deploy/multus/multus-daemonset.yaml` and
  remove `/etc/cni/net.d/00-multus.conf` via a privileged pod (or reboot the
  node — Talos regenerates the flannel config).
