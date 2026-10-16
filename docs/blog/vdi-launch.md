# VDI launch announcement draft

Use this text for the announcement in TunaOS Discussions, Matrix and
Reddit. Each claim agrees with the stability table in
[docs/vdi.md](../vdi.md). Before you post, read that table again. If a
level changed, change this text.

---

## Corral v0.7.0: self-service desktop pools on KubeVirt

Corral v0.7.0 includes the VDI plugin. The plugin makes pools of desktop
VMs from clones of one golden VM, and gives the desktops to users.

**Stable on KubeVirt: the self-service broker.** In `corral web`, a user
who signed in can claim a desktop from a persistent VM pool. The user can
then connect to it and release it. No admin is necessary. The broker takes
back a desktop after a console disconnect or after a maximum duration.

A lease protects each claim, so two users cannot get the same desktop at
the same time. The `e2e-kind` CI job tests the broker end to end on each
change (`scripts/e2e-vdi.sh`). A test also checks the upgrade from the
assignments of v0.6.0.

**Preview on KubeVirt: the rest.** These parts have unit tests, but no
end-to-end test in CI yet:

- The `corral vdi` CLI: `pool create`, `assign`, `unassign`, `connect`.
- Ephemeral VM pools (`--ephemeral`).
- CT-backed pools of Linux desktops that Corral builds from a container
  image.
- Native USB redirection for assigned desktops.
- GPU and host-device capacity checks before Corral creates a pool.

**Not included.** There is no input-idle reclaim and no live resize of a
pool. VDI operates on KubeVirt only.

Try it:

```bash
corral plugin install vdi
corral vdi pool create devpool --from golden-desktop --size 3
```

Guide: https://github.com/tuna-os/corral/blob/main/docs/vdi.md
Release notes: https://github.com/tuna-os/corral/blob/main/docs/releases/v0.7.0.md

---

## Posting notes (not part of the post)

- Post in TunaOS Discussions (Announcements) and the Matrix room first.
  Then cross-post to r/kubernetes and r/selfhosted.
- Do not call VDI "GA". Only the broker on persistent VM pools on KubeVirt
  is Stable.
