# Manual pass for the Tier 3 backends

CI cannot run vSphere, Prism Central, or a XenServer/XCP-ng pool. CI also
runs no VirtualBox host. For these four backends, fixture tests prove that
Corral sends the correct calls. This runbook proves that the platform does
the correct thing. Do this pass before you change a Tier 3 row in
[backend-parity.md](../backend-parity.md) or the tier in
[enterprise-readiness.md](../enterprise-readiness.md).

[RFC-0003](../rfc/0003-enterprise-backends.md) gives the design. Issue #507
tracks the work.

## 1. Before you start

- Use a test VM that you can delete. Some steps stop it, clone it, or
  change its CPU and memory.
- Record the platform version and the Corral commit in your report.
- Put each password in the environment. Corral never reads a password from
  the config file.

| Backend | Context value | Password variable |
|---|---|---|
| VirtualBox | a label, for example `local` | none |
| vSphere | the vCenter host | `CORRAL_VSPHERE_PASSWORD` or `CORRAL_VSPHERE_PASSWORD_<CONTEXT>` |
| XenServer/XCP-ng | the pool master host | `CORRAL_XEN_PASSWORD` or `CORRAL_XEN_PASSWORD_<CONTEXT>` |
| Nutanix | the Prism Central VIP | `CORRAL_NUTANIX_PASSWORD` or `CORRAL_NUTANIX_PASSWORD_<CONTEXT>` |

`<CONTEXT>` is the context name in upper case. Corral changes each character
that is not a letter or a digit to `_`.

## 2. Add the context

1. Add the context:

   ```bash
   corral context add vc1 --backend vsphere --context vc.example.com
   ```

2. For vSphere, Xen, and Nutanix, add the user and the certificate trust to
   the context in `~/.config/corral/config.yaml`:

   ```yaml
   contexts:
     - name: vc1
       backend: vsphere
       context: vc.example.com
       vsphere:
         username: administrator@vsphere.local
         fingerprint: "AB:CD:..."   # or insecure: true on a lab estate
   ```

   Use the key `xen:` or `nutanix:` for those backends. A Nutanix context
   also accepts `apiVersion:` (the default is `v4.0`).

3. Run `corral doctor`. The check for the context must pass.

## 3. Do the pass

Do each step on the test VM. Write down the result. Compare the result with
the platform's own console, not only with the Corral output.

Some operations have a CLI command for every backend. For the others, use
the action in the TUI (`corral tui`). The CLI commands for those operations
support only KubeVirt.

| Step | Where | VirtualBox | vSphere | Xen | Nutanix |
|---|---|---|---|---|---|
| Inventory | `corral list` | yes | yes | yes | yes |
| Stop, start | `corral stop VM`, `corral start VM` | yes | yes | yes | yes |
| Restart | `corral restart VM` | yes | yes | yes | yes |
| Snapshot | `corral snapshot create VM`, then `ls`, `restore`, `rm` | yes | yes | yes | yes |
| Clone | `corral clone VM VM-copy` | yes | yes | yes | yes |
| SSH | `corral ssh VM` | yes | no | no | no |
| Pause, resume | TUI action | yes | yes | yes | no |
| Scale | TUI hardware action | stopped VM only | yes | no | no |
| Migrate | TUI action | no | yes | yes | yes |
| Export | TUI export action, or the backup plugin | stopped VM only | yes | stopped VM only | no |

Expected results for the backend-specific rules:

- **VirtualBox scale and export.** When the VM runs, Corral refuses the
  operation and tells you to stop the VM. VirtualBox makes no change.
- **VirtualBox SSH.** Corral uses a NAT port-forward to guest port 22 if one
  exists. If not, Corral uses the address from Guest Additions.
- **vSphere export.** The VM continues to run during the export. Corral removes the
  `corral-export-*` snapshot when the export ends. The result is
  `filesystem` consistent when VMware Tools runs, else `crash` consistent.
- **Xen export.** The result is an `.xva` file. Import it on a pool to
  check it.

## 4. Report

Add a comment to #507. Give the platform version, the Corral commit, and the
result of each step. If a step fails, open an issue. Include the command,
the output, and what the platform console showed.
