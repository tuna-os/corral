// Package cmd — CLI adapter architecture
//
// The cmd package follows a thin-adapter pattern: cobra commands parse flags
// and dispatch to backend packages (pkg/kubevirt, pkg/qemu, pkg/plugin).
// There is no intermediate "operations" interface because:
//
//  1. The qemu and kubevirt backends have fundamentally different APIs.
//     Unifying them behind one interface would require either a leaky
//     abstraction (type-switching on backend) or a lossy one (dropping
//     backend-specific capabilities).
//
//  2. The cobra commands are already thin (~20-40 lines each). Adding an
//     interface layer would increase indirection without meaningful leverage
//     for callers (the only caller is the CLI) or locality for maintainers
//     (backend dispatch is already a one-line switch).
//
//  3. The existing test strategy tests backends directly (pkg/kubevirt has
//     75% coverage, pkg/qemu has 61%). Testing through a CLI operations
//     interface would test the interface, not the backend.
//
// If the CLI grows more commands or a third backend, revisit. For now the
// architecture is:
//
//	cmd (thin adapters) ──→ pkg/kubevirt (KubeVirt backend)
//	                     ──→ pkg/qemu (local QEMU backend)
//	                     ──→ pkg/plugin (krew-style dispatch)
package cmd
