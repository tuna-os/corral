# VDI plugin: CT-backed ephemeral desktop pools

A CT-backed pool is a set of ephemeral Linux desktops that Corral builds from
one container image. Each member is a normal Corral Container (CT). When a user
releases a member, Corral destroys it and makes a new one from the image. The
next user always gets a clean desktop. Issue
[#230](https://github.com/tuna-os/corral/issues/230) tracks this slice of
[RFC-0001](rfc/0001-vdi-plugin.md). For VM-backed pools, see
[docs/vdi.md](vdi.md).

Kubernetes has no `VirtualMachinePool` equivalent for pods that keep a stable
name and volume. Corral does not act as if one exists. A small reconciler in
`pkg/vdi` keeps the members in line with a declared pool.

## Model

| Part | Where it lives | What it holds |
|---|---|---|
| Pool definition | ConfigMap `corral-vdi-ctpool-<pool>`, key `spec` | Source image, CPU, memory, disk, StorageClass, privileged and init flags, mode, and size |
| Pool status | Same ConfigMap, key `status` | Desired, observed, ready, claimed, free, and unschedulable counts, one row for each member, and the errors of the last reconcile |
| Member | CT `<pool>-<n>`, `n` from 1 to the size | A pod and a data PVC with the labels `corral.dev/vdi-ct-pool`, `corral.dev/vdi-ct-member`, and `corral.dev/vdi-ct-source` |
| Claim | Lease `corral-vdi-ct-<member>` | The holder identity, the acquire time, and the expiry, as in the VM claim contract of #226 |
| Assignment display | Annotation `corral.dev/vdi-assigned-to` on the member PVC | A copy of the claim holder for display. The Lease is the authority. |

The `corral.dev/vdi-ct-source` label is a hash of the source fields. A member
with a different hash has drifted from the pool definition.

A member is in one of these states: `Missing`, `Stopped`, `Starting`, `Ready`,
`Unschedulable`, `Failed`, or `Terminating`. The status row also shows the
claim holder, an expired claim, maintenance, drift, and a member above the
pool size.

## Commands

```bash
# Define a pool of 3 desktops and reconcile it once.
corral vdi ct-pool apply desk --image ghcr.io/example/desktop:1 --size 3 --mem 2Gi

# Keep the pool in line with its definition. Failed steps are retried.
corral vdi ct-pool reconcile desk --watch --interval 30s

# Show the live state of each member. This command changes nothing.
corral vdi ct-pool status desk

# Give a free desktop to a user. A second claim by the same user gets the same desktop.
corral vdi ct-pool claim desk alice@example.com

# The user is done: destroy the desktop and make a new one from the image.
corral vdi ct-pool release desk desk-1 alice@example.com

# Change the image or the size, then reconcile again.
corral vdi ct-pool apply desk --image ghcr.io/example/desktop:2 --size 5

# Remove the pool and all of its members.
corral vdi ct-pool delete desk
```

All commands use the `-n` namespace flag. The default is the Corral namespace.
The `--context` flag selects a Corral context of the `kubevirt` backend. Without
it, kubectl uses its current context.

## Reconciliation

Each reconcile reads the definition, the members, and the claims. Then it does
these steps:

1. It creates each missing member from 1 to the pool size.
2. It starts each stopped member.
3. It replaces each unclaimed member that has drifted or failed. It deletes the
   member and then creates it again from the current source.
4. It deletes each unclaimed member above the pool size.
5. It records the new status in the pool ConfigMap.

A reconcile never destroys a member that a user has claimed. This rule applies
to drift, failure, scale-down, and an expired claim. A claimed member stays
until the user or an administrator releases it.

Before a create, a replace, or a delete, the reconciler takes the member Lease
as the holder `corral-vdi/reconciler`. A user cannot claim a member while the
reconciler holds its Lease. If a user claims the member first, the reconciler
does not get the Lease and leaves the member alone. The reconciler removes its
Lease when the step ends, also when the step fails. A Lease from a reconciler
that stopped mid-step expires after 10 minutes. Then the next reconcile takes
it over.

Because of these rules, you can run reconcile again at any time. Two
reconcilers can also run at the same time. A second run with no change in the
cluster does nothing.

## Claim and release

A claim follows the Lease contract of #226. The first writer to create the
Lease wins. Corral gives a free member to the identity. If the member has no
pod, Corral starts it.

A free member has no Lease, an index in the pool size, and a current source.
Its state is `Ready`, `Starting`, or `Stopped`. If no member is free, the
claim fails with "pool has no free members". The error shows the desired,
claimed, and unschedulable counts.

A release by the claim holder first changes the Lease holder to the
reconciler. This change is a compare-and-swap on the Lease
`resourceVersion`. Then Corral deletes the pod, the console Service, and the
data PVC, and waits until they are gone. Then it creates the member again from
the current source, but only if the member index is in the pool size. At the
end, Corral deletes the Lease.

If Corral cannot create the new member, the release reports the error. The
next reconcile then creates the member.

A release of a member with no claim does nothing. An administrator can use
`--force` to release a claim that another identity holds.

A claim expires after one hour. The session work of
[#228](https://github.com/tuna-os/corral/issues/228) will renew claims. Until
then, a claim by the same identity renews an expired claim. An expired claim
still protects the desktop. Corral does not give the member to a second user.
Use `release --force` to recover it.

## Persistence

Only the `ephemeral` mode is supported. A release destroys the member data
PVC. A replacement after drift or failure also destroys the data PVC of an
unclaimed member. A pool definition with `mode: persistent` fails with an
explicit error at `apply`, `reconcile`, `claim`, and `release`. Use a VM pool
for persistent desktops.

The data PVC keeps data while a member stops and starts. An unprivileged member
keeps `/data` only. A privileged member keeps its root file system on the PVC
(see the CT model in `pkg/ct`).

## Capacity and partial failure

- A member pod that the scheduler cannot place is `Unschedulable`. Its status
  row shows the scheduler message. It does not count as free. When the
  cluster gets capacity again, the scheduler starts the pod and the member
  becomes free without help.
- `exhausted: true` in the status shows that the pool has members but none is
  free.
- A failed create, start, or delete adds a line to `errors` in the status and
  makes `reconcile` exit with an error. The step does not leave a Lease, so the
  next reconcile tries again. `reconcile --watch` retries on each interval.
- A deleted pool is first set to size 0. If a member delete fails, the
  definition stays, and you can run `delete` again.

## Prerequisites

### Kubernetes access

The identity that runs `corral vdi ct-pool` needs these permissions in the
pool namespace:

- ConfigMaps: get, list, create, patch, delete.
- Pods and PersistentVolumeClaims: get, list, create, patch, delete.
- Services: delete (a release removes a console Service if one exists).
- Pods `exec`, for `corral ct console`.
- `coordination.k8s.io` Leases: get, list, create, update, delete.

### Image

- The image must be pullable from each node that can run a member.
- Members run `sleep infinity` unless you set `--init`. Use `--init` for an
  image with its own entrypoint that does not stop. A desktop session or an
  SSH server is an example.
- The image is the only source of a member. Put the desktop software in the
  image, not in a member. Corral destroys all changes at release.

### Storage

- Each member gets one `ReadWriteOnce` PVC of `--disk` size (default `5Gi`).
- The StorageClass must make volumes on request (a dynamic provisioner). A
  release deletes and creates a PVC each time, so a slow provisioner makes a
  release slow.
- A PVC that stays in the `Terminating` state blocks the new member. Corral
  waits up to 2 minutes for each delete.

### Networking

- A member has the pod network of the cluster. Corral does not add a
  NetworkPolicy. Add one if members must not reach each other.
- `corral ct console <member>` opens a terminal through `kubectl exec`. A CT
  member has no VNC framebuffer. Browser desktop streams (WebRTC/Selkies) are
  a separate RFC-0001 phase.

### Security

- By default, Corral makes unprivileged members. `--privileged` gives the
  member container `privileged: true`. Use it only for trusted users.
- Do not put tenant secrets, cloud credentials, or tokens in the image or in
  the pool ConfigMap. The ConfigMap is readable by all users with ConfigMap
  read access in the namespace. Use Kubernetes Secrets for secrets.
- The claim identity is the string that the caller gives. The CLI does not
  authenticate it. A broker in front of the pool
  ([#227](https://github.com/tuna-os/corral/issues/227)) must give a trusted
  identity. Tailscale or the OIDC gateway can supply this identity.
- Corral keeps the identity `corral-vdi/reconciler` for the reconciler. A
  claim with this identity fails.
