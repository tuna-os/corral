# Build your own KubeVirt "Proxmox" with Corral

> Full project docs: [README.md](../README.md), [SPEC.md](../SPEC.md),
> [API reference](api.md), [architecture](architecture.md)

This guide turns a plain Kubernetes cluster into a Proxmox-style virtualization
host. You get a web UI with in-browser consoles, live CPU/RAM changes,
snapshots, backups, templates, and more. **KubeVirt** and the **Corral**
front-end drive all of it.

Corral is a single static Go binary (CLI + TUI + web UI) that shells out to
`kubectl`/`virtctl`. There is no operator or controller to install for Corral
itself — you install the KubeVirt stack, then run Corral against it.

## What you get

| Capability | Needs |
|---|---|
| Create/start/stop/delete VMs, consoles (VNC + serial), SSH | KubeVirt + CDI |
| Live **or** offline CPU/RAM change | KubeVirt `LiveUpdate` |
| Disk hotplug (add/remove) | `HotplugVolumes` feature gate |
| Online disk expansion | StorageClass with `allowVolumeExpansion: true` |
| Snapshots / restore / clone | `Snapshot` gate + a `VolumeSnapshotClass` |
| Backup / export (download a disk image) | `VMExport` gate |
| Live migration | RWX/migratable storage **and same-CPU-vendor nodes** |
| Templates, instancetypes, image library, events/metrics | (built into Corral) |
| Secondary NIC on the LAN | Multus + a NetworkAttachmentDefinition |
| LAN access without a new NIC | a LoadBalancer-Service controller — Cilium (L2/BGP) or MetalLB |
| Boot a container image as a VM (`corral bootc`) | the `bootc` extension |

Corral detects what the cluster supports (`GET /api/capabilities`) and
greys out controls it can't do — so you can start minimal and add pieces.

---

## 1. Prerequisites

- A Kubernetes cluster (v1.26+). Nodes must support hardware virtualization
  (`/dev/kvm`) or KubeVirt software emulation.
- `kubectl` and [`virtctl`](https://kubevirt.io/user-guide/user_workloads/virtctl_client_tool/)
  on your workstation (and in the web-UI image — Corral's image bundles both).

## 2. KubeVirt + CDI

```bash
# KubeVirt operator + CR
VER=$(curl -fsSL https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt)
kubectl apply -f https://github.com/kubevirt/kubevirt/releases/download/$VER/kubevirt-operator.yaml
kubectl apply -f https://github.com/kubevirt/kubevirt/releases/download/$VER/kubevirt-cr.yaml
kubectl -n kubevirt wait kv kubevirt --for=condition=Available --timeout=10m

# CDI (containerized data importer — imports ISOs/images into PVCs)
CDI=$(basename $(curl -fsSL -o /dev/null -w '%{url_effective}' https://github.com/kubevirt/containerized-data-importer/releases/latest))
kubectl apply -f https://github.com/kubevirt/containerized-data-importer/releases/download/$CDI/cdi-operator.yaml
kubectl apply -f https://github.com/kubevirt/containerized-data-importer/releases/download/$CDI/cdi-cr.yaml
```

## 3. Enable the feature gates + LiveUpdate

This is what lights up CPU/RAM changes, hotplug, snapshots, and export:

```bash
kubectl patch kubevirt kubevirt -n kubevirt --type merge -p '{
  "spec": {
    "configuration": {
      "vmRolloutStrategy": "LiveUpdate",
      "developerConfiguration": { "featureGates": ["Snapshot","HotplugVolumes","VMExport"] }
    },
    "workloadUpdateStrategy": { "workloadUpdateMethods": ["LiveMigrate"] }
  }
}'
```

- `vmRolloutStrategy: LiveUpdate` + `workloadUpdateMethods: [LiveMigrate]` →
  CPU/memory hotplug (KubeVirt applies it with a live migration of the VM).
- `HotplugVolumes` → add/remove disks on a live VM.
- `Snapshot` → `VirtualMachineSnapshot`/`Restore`.
- `VMExport` → deploys `virt-exportproxy`; powers Corral's backup/export.

Corral creates VMs with `cpu.maxSockets` + `memory.maxGuest` headroom and
masquerade networking so they *can* hotplug/migrate.

## 4. Storage (snapshots + expansion)

Snapshots and online expansion need a **CSI** StorageClass with a
`VolumeSnapshotClass`. A simple hostPath provisioner (e.g. `local-path`)
**cannot** do them. Longhorn is an easy, replicated option.

### 4a. Install Longhorn

```bash
LH=$(curl -fsSL https://api.github.com/repos/longhorn/longhorn/releases/latest | grep -oP '"tag_name":\s*"\K[^"]+')
kubectl apply -f https://raw.githubusercontent.com/longhorn/longhorn/$LH/deploy/longhorn.yaml
kubectl label ns longhorn-system pod-security.kubernetes.io/enforce=privileged --overwrite
```

Longhorn needs `open-iscsi`/`iscsid` on every node. On normal Linux:
`apt install open-iscsi && systemctl enable --now iscsid`. **On Talos Linux**
it's a system extension — see the [siderolabs `iscsi-tools` extension](https://github.com/siderolabs/extensions/tree/main/storage/iscsi-tools)
(image-factory schematic with `siderolabs/iscsi-tools` + a `/var/lib/longhorn`
kubelet mount).

### 4b. Snapshot controller + VolumeSnapshotClass

Longhorn doesn't bundle the external-snapshotter — install it once:

```bash
SNAP=v8.2.0
for c in volumesnapshotclasses volumesnapshotcontents volumesnapshots; do
  kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/$SNAP/client/config/crd/snapshot.storage.k8s.io_$c.yaml
done
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/$SNAP/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/$SNAP/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml

kubectl apply -f - <<'EOF'
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: longhorn-snapshot
  annotations: { snapshot.storage.kubernetes.io/is-default-class: "true" }
driver: driver.longhorn.io
deletionPolicy: Delete
parameters: { type: snap }
EOF
```

Corral prefers a StorageClass named `local-path` for new VM disks (local NVMe
speed, no network IO) when present. If not, it falls back to the cluster
default. Use `--storage-class` at create time to override per-VM.

### 4c. TopoLVM on Talos: `dm_thin_pool` kernel module

TopoLVM needs the `dm-thin-pool` kernel module to thin-provision LVM volumes. On
Talos, the kernel build makes that module loadable (`CONFIG_DM_THIN_PROVISIONING=m`).
But Talos blocks `modprobe`/`insmod` via seccomp even from privileged
containers. So TopoLVM's `lvmd` DaemonSet pod can't load it itself:

```
# From any privileged container on the host
insmod /lib/modules/dm-thin-pool.ko
# → Operation not permitted (seccomp blocked)
```

Fix: add the modules to the Talos machine config so they load at boot, then
reboot the affected nodes.

```yaml
machine:
  kernel:
    modules:
      - name: dm_thin_pool
      - name: dm_bio_prison
      - name: dm_persistent_data
      - name: dm_bufio
```

This is a Talos machine-config change, not something Corral or `just`
recipes can apply for you. `corral doctor` doesn't detect it now either. We
track it as a possible future check, because the failure mode is an lvmd pod
that is stuck in silence, not a clear error.

## 4d. GPU / PCI device passthrough (optional)

The `gpu` extension (`corral gpu enable`) registers a PCI vendor:device
selector in the KubeVirt CR so VMs can request it as a resource. That's the
*Kubernetes* half. It assumes that passthrough already works on the **host**:

1. **IOMMU enabled** — in the BIOS/UEFI (`Intel VT-d` / `AMD-Vi`, sometimes
   labeled "IOMMU"). Also on the kernel command line: `intel_iommu=on` (Intel)
   or `amd_iommu=on` (AMD). We usually recommend `iommu=pt` too. On Talos,
   this is a kernel argument in the machine config:
   ```yaml
   machine:
     install:
       extraKernelArgs:
         - intel_iommu=on   # or amd_iommu=on
         - iommu=pt
   ```
2. **The device bound to `vfio-pci`**, not its normal driver (e.g. `nvidia`,
   `amdgpu`). If not, the host kernel keeps it. Then KubeVirt's device
   plugin never sees it as available. How you bind this depends on your
   distro. Talos doesn't let you rebind a device to an arbitrary driver the
   way a general-purpose Linux host does. So GPU passthrough on Talos
   specifically needs the device to come up as `vfio-pci` from boot. Do this
   via `extraKernelArgs`, e.g. `vfio-pci.ids=10de:1234`. Match the value to
   the real PCI vendor:device ID from `lspci -nn`.

`corral doctor` has a check for this — **GPU/PCI passthrough**. But it's an
indirect signal, not a direct probe of IOMMU/vfio-pci. A direct probe would
need a privileged pod/DaemonSet on the node, and none of Corral's other
checks do that.

The check runs only when you allow a device (you used
`corral gpu enable`). It fails if no node ever reports that device as
**Allocatable**. KubeVirt's device plugin only advertises devices that it can
bind. So "permitted but never allocatable anywhere" is the strongest symptom
of a missing IOMMU/vfio-pci prerequisite that this check can see. It won't
catch a setup that *partially* works (e.g. allocatable on the wrong node). If
passthrough doesn't work the way you expect, check
`kubectl describe node <name>` for the resource under `Allocatable`.

## 5. Deploy Corral

### Web UI (in-cluster)

```bash
kubectl apply -f https://raw.githubusercontent.com/tuna-os/corral/main/deploy/corral-web.yaml
```

`deploy/corral-web.yaml` creates a namespace (`tailvm`), a scoped
ServiceAccount/ClusterRole (VM lifecycle + subresources, snapshots, exports,
PVCs, storage/snapshot classes, events, metrics, instancetypes), a Deployment,
and a Service. **There is no built-in auth** — expose it only on a trusted
network. The reference manifest annotates the Service for the
[Tailscale operator](https://tailscale.com/kb/1236/kubernetes-operator); adapt
to your own Ingress/VPN.

### CLI / TUI

```bash
go install github.com/tuna-os/corral@latest      # or grab a release binary
corral            # TUI
corral web        # local web UI on 127.0.0.1:8006
corral list
```

## 6. (Optional) LAN access for VMs

By default a KubeVirt VM only gets a NATed pod-network interface. Outbound
internet works, but nothing on your LAN can reach the VM. The VM also can't
reach devices that are only on the LAN (a smartwatch, a NAS, a router admin
panel). Corral has two ways to fix that. The right one depends on what your
cluster already runs.

### 6a. A real secondary NIC — Multus

Install [Multus](https://github.com/k8snetworkplumbingwg/multus-cni) and
create a `NetworkAttachmentDefinition`. Multus changes the CNI chain — do it
in a maintenance window. Example macvlan NAD (set `master` to each node's
uplink; note nodes may name interfaces differently):

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata: { name: lan, namespace: tailvm }
spec:
  config: |
    { "cniVersion":"0.3.1","type":"macvlan","master":"eth0","mode":"bridge",
      "ipam":{"type":"dhcp"} }
```

Once a NAD exists, bridge a VM onto it:

```bash
corral networks                        # list NADs Corral can see
corral create myvm --kubevirt --image fedora --lan   # new VM, bridges automatically if there's exactly one NAD
corral addnic myvm                     # existing VM — same auto-detect
corral addnic myvm --network-nad tailvm/lan --iface net1   # explicit, if there's more than one NAD
```

Or use Corral's **Add NIC** (Hardware → Network) in the web UI. `--lan`/
`corral addnic` only auto-picks a NAD when exactly one exists on the
cluster. With several, pass `--network-nad` explicitly. That way a guess
never bridges a VM onto the wrong network. The NAD's `config` can wrap
any CNI plugin — macvlan/ipvlan/bridge/sriov. Multus is only the KubeVirt
attachment mechanism, not a networking technology in itself.

### 6b. No new interface — a LoadBalancer Service (Cilium or MetalLB)

If you prefer not to touch the CNI chain at all, `--lan-service` exposes a
VM's ports through a plain `type: LoadBalancer` Service instead. It uses the
same proxy Deployment again that the tailnet exposure of `corral create`
already runs. The only difference is a second Service in front of it, with
no vendor-specific annotations. Any controller that fulfills LoadBalancer
Services gives it an external IP the same way:

- **Cilium** with [L2 Announcement](https://docs.cilium.io/en/stable/network/l2-announcements/)
  (flat network, ARP-based) or the [BGP Control Plane](https://docs.cilium.io/en/stable/network/bgp-control-plane/).
  BGP peers with your router/switch, and you need it if the LAN spans an L3
  boundary. Also add a `CiliumLoadBalancerIPPool` to hand out addresses from.
- **[MetalLB](https://metallb.io/)** on any other CNI (Calico, Flannel, …) —
  same Service-based contract, its own L2/BGP modes.

```bash
corral create myvm --kubevirt --image fedora --lan-service   # new VM
corral lanservice myvm                                        # existing VM
kubectl get svc myvm-lan -n tailvm                             # external IP once a controller assigns one
```

Without an LB-IPAM controller installed, `myvm-lan` sits `<pending>`
forever — same as any other LoadBalancer Service on a cluster without one.

## 7. Extensions (the marketplace)

Niche features ship as plugins — krew-style `corral-<name>` binaries installed
from a curated marketplace (browse in the web UI's **Extensions** tab, or):

```bash
corral plugin search
corral plugin install bootc        # flagship: boot a container image as a VM
corral bootc create dev --image quay.io/centos-bootc/centos-bootc:stream9
```

Plugins live in `~/.local/share/corral/plugins` and run as `corral <name> …`.

---

## Caveats worth knowing up front

- **Live migration needs same-vendor CPUs.** KubeVirt pins a migration target
  to the source node's CPU vendor. So you cannot live-migrate (or live-hotplug
  CPU/RAM) between an Intel and an AMD node, even with a common `cpu.model`.
  Corral detects this and instead uses a single offline reboot.
- **Snapshots of persistent VMs need CSI + a VolumeSnapshotClass.** Ephemeral
  container-disk VMs can snapshot their definition without one.
- **Export needs the VM stopped** (its RWO disk is busy while the VM runs);
  Corral downloads via `virtctl vmexport --port-forward`.
- **No auth** in the web UI — gate it behind a VPN/tailnet, never the public net.

That's the whole stack. Corral's `Datacenter → node → VM` tree, editable
Hardware tab, Snapshots/Events tabs, image library, and in-browser consoles
give you a Proxmox-like experience on top of standard Kubernetes.
