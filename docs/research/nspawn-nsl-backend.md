# nspawn / NSL local backend evaluation

**Decision: no-go for a Corral backend or lifecycle prototype now.** Revisit
with a separate, opt-in NSL experiment after the capability and identity gaps
below are addressed and tested. This is a source evaluation, not evidence of a
working integration. No runtime registration, probe, dependency, privilege
policy, or behavior on hosts without these tools is changed by this decision.

## Comparison

| | Direct systemd-nspawn / machinectl | NSL CLI |
|---|---|---|
| Boundary | Host system manager and machined; containers share the host kernel. | User-owned NSL state and CLI; containers run inside NSL's shared or isolated VM, not directly on the host. |
| Inventory | `machinectl list` lists registered running machines, including machines owned by other software. `list-images` lists available images, not necessarily running instances. A safe combined inventory must reconcile the two without starting anything. | `nsl list --json` returns VMs and machines, including stopped machines, without starting them. Its status collection skips stopped VMs and treats an unavailable agent report as absent; at the inspected revision a machine may therefore appear stopped even if a running VM's agent could not report it. Do not use this best-effort status alone to authorize mutations. |
| Start / stop | `machinectl start NAME` starts the nspawn service for an image; `poweroff NAME` requests shutdown and returns before teardown finishes. Availability, image configuration and authorization are host-dependent. | `nsl start NAME` starts the backing VM if needed and waits for the machine; `nsl stop NAME` requests stop in a running VM and may terminate it after a timeout. Both use NSL's owned machine records and agent identity checks. |
| Enter | `machinectl shell USER@NAME` opens a privileged machine session; it is not SSH. | `nsl -m NAME` opens a login shell; `nsl run -m NAME ...` executes as the configured guest user by default. Both can start the machine and VM. |
| Costs / coupling | No extra VM, but Corral must establish safe ownership and manage host authorization, image lifecycle, service settings and stopped-machine discovery. | Existing named ownership and structured list are a better fit for a future experiment, but add a pre-release external CLI, systemd-vmspawn/QEMU/KVM and its versioned host/VM protocol. NSL is not an Intune service. |

The two are different backends, not interchangeable implementations selected
implicitly by `$PATH`. NSL's current requirements include an x86-64 Linux
host, KVM, systemd-vmspawn 259+, QEMU/UEFI and virtiofsd. Direct nspawn
support must not treat NSL's container names as host machinectl names.

## Why no-go

1. Corral's [`Power`](../../pkg/backend/ops.go) bundles start, stop and
   **delete**. Implementing it for a list/enter/start/stop-only prototype
   would advertise delete in the matrix, or require a dishonest stub. Calling
   NSL remove to satisfy the interface would exceed the issue's scope and
   permanently delete the machine's data. Split the capability and audit its
   callers before implementing a non-destructive subset.
2. `InstanceRef` identifies peer/backend/context/namespace/name, not a stable
   machine generation. NSL verifies the record ID during operations, but its
   public list JSON at the inspected revision does not expose that ID; selecting
   a name and later invoking the CLI cannot detect a replacement between the
   selection and the call. Direct machinectl has no Corral ownership boundary
   at all. An explicit selection alone must not become permission to operate
   on an unrelated or replaced machine.
3. A CLI shell is not automatically a web TTY. `pkg/web/console.go` routes by
   namespace and otherwise falls back to KubeVirt/CT paths; unsupported direct
   requests must fail closed rather than reach a same-named guest elsewhere.
   The SSH options include identity, password, forwarding and vsock, which are
   not equivalent to either shell. Do not advertise SSH or web TTY until their
   contracts and callers are explicitly handled.
4. Neither path has been exercised on a representative host here. A successful
   source review cannot establish authorization, teardown, state reporting or
   session behavior. Incus already provides an integrated container option;
   there is no reason to weaken the boundary merely to register another one.

## Scope and security for a future experiment

An eventual backend must be explicitly configured, never discovered or started
implicitly. Inventory must work while machines are stopped; list must not boot
anything. Carry a distinct backend and local context through fleet, canonical
selectors, CLI, TUI, web and peer operations. Reject ambiguous names; do not
fall back to another backend. An ownership mechanism must bind the selected
instance to the operation at the authoritative boundary, including replacement
and concurrent operations; rechecking an untrusted name immediately before a
command does not eliminate the race. Reject unknown, replaced, and foreign
machines without side effects. NSL's current CLI would need a supported way to
bind a mutation to a selected ID before it can meet that requirement.

Host machinectl actions go through systemd/machined authorization, often
root or polkit. Do not install broad sudo or polkit grants; verify noninteractive
and interactive failures explicitly. `machinectl shell` confers the selected
container user's authority and must not be exposed via the web service's host
identity without a separate authorization design. Containers share a kernel;
do not claim VM isolation or use them for untrusted tenants. Explicit user
namespace and host resource policies are needed for direct nspawn. NSL's
shared tier gives the guest access to host files as the user; its isolated tier
uses a separate VM. NSL's agent config uses `PrivateUsers=no` **inside that
VM**, which is not a safe default to transplant to a host container.
Intuneme's device, host socket, X11 and session-bus integration is for its own
use case; do not carry it over as a generic default.

Until implemented and verified, Corral advertises no nspawn/NSL capabilities.
A future narrow adapter must explicitly reject firmware/UEFI, snapshots,
disk migration/export, VNC and other unsupported VM operations rather than
routing them to QEMU or KubeVirt. Do not claim create, delete, web console or
SSH merely because the external tool has a related command. Enforce this at
server/CLI dispatch as well as in displayed capability flags.

## Exit criteria and verification

Before changing the decision, a follow-up should:

- Separate start/stop from deletion in the operation family and update all
  callers, capability derivation, matrix and conformance tests without changing
  existing backends. Establish an authoritative, generation-bound selection
  mechanism; specify failure behavior if the selected machine is replaced.
- Pin and version-check the chosen NSL CLI contract; verify machine and VM
  status errors are distinguishable from stopped. Bound subprocess duration,
  pass arguments without a shell, propagate errors, and never use a default
  machine implicitly. Keep executable discovery and registration opt-in.
- Test missing executables and denied authorization, no-start inventory,
  name collisions across contexts/backends, replacement races, foreign names,
  failed start/stop, timeout, and every unsupported operation through each
  exposed surface. Run the complete `pkg/backend`, `pkg/fleet`, `pkg/types`,
  `pkg/lifecycle`, `pkg/snapshot`, `pkg/export`, `pkg/move`, `pkg/web`, and
  `cmd` test modules in both build tag sets, plus the repository's `just ci`.
- On a disposable, appropriately authorized Linux host, create two explicitly
  named disposable machines (one selected, one unrelated). Record their
  identities and stopped states; list without starting; start and enter only
  the selected one; stop and wait for teardown; verify the unrelated one is
  unchanged. Exercise denied and stale/replaced selections and verify they
  cause no side effects. Remove only the disposable test resources using the
  tool's own documented cleanup, after recording results. Repeat on a host
  without the optional tools and confirm unchanged Corral behavior. Record
  versions, configuration, commands and outcomes; do not substitute mocks for
  this integration run.

This environment has neither `machinectl`, `systemd-nspawn`, `systemctl`,
Podman nor `/dev/kvm`; the disposable lifecycle was **not run**. No lifecycle
prototype is proposed or claimed by this record. The full modules listed above
passed with both plain and `bootc` tags. `just ci` passed format, vet and build
for both tag sets, then stopped at race tests: CGO is disabled and no C
compiler is installed. The lint stage did not run; `golangci-lint` is also
absent. These checks do not replace a real-host integration run.

## Evidence reviewed

- Corral: [`pkg/backend/ops.go`](../../pkg/backend/ops.go),
  [`list.go`](../../pkg/backend/list.go), [`matrix.go`](../../pkg/backend/matrix.go),
  [`pkg/fleet/fleet.go`](../../pkg/fleet/fleet.go),
  [`pkg/types/types.go`](../../pkg/types/types.go), and
  [`pkg/web/console.go`](../../pkg/web/console.go). Searches covered lifecycle,
  inventory, console, snapshot/export and move callers; no existing nspawn or
  NSL integration was found outside vendored assets.
- NSL at [`b78a782`](https://github.com/frostyard/nsl/tree/b78a782f7f56eb1717afac16fa69d05a58cfd60e):
  [`machines.go`](https://github.com/frostyard/nsl/blob/b78a782f7f56eb1717afac16fa69d05a58cfd60e/machines.go),
  [`storage.go`](https://github.com/frostyard/nsl/blob/b78a782f7f56eb1717afac16fa69d05a58cfd60e/storage.go),
  [`cmd/nsl-agent/machines.go`](https://github.com/frostyard/nsl/blob/b78a782f7f56eb1717afac16fa69d05a58cfd60e/cmd/nsl-agent/machines.go),
  and [README](https://github.com/frostyard/nsl/blob/b78a782f7f56eb1717afac16fa69d05a58cfd60e/README.md).
- Intuneme at [`2cde560`](https://github.com/frostyard/intuneme/blob/2cde560c5559470aa5ca9ae2baa28edc875dc68f/internal/nspawn/nspawn.go):
  boot arguments, shell and stop/wait behavior.
- systemd manuals: [machinectl](https://www.freedesktop.org/software/systemd/man/latest/machinectl.html)
  and [systemd-nspawn](https://www.freedesktop.org/software/systemd/man/latest/systemd-nspawn.html).
- Both inspected roots have MIT licenses. No source was copied; future copying
  must retain the applicable copyright and license notices.
