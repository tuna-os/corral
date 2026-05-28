# tailvm-go

Go rewrite of [tailvm](../roles/bluefin_common/files/tailvm) using the Charm.sh ecosystem:
[Cobra](https://github.com/spf13/cobra), [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

## Build

```bash
cd tailvm-go
go build -o tailvm .
```

## Test

```bash
go test ./...
```

## Status

| Feature | Go | Python |
|---------|----|--------|
| list (both backends) | ✓ | ✓ |
| create (kubevirt) | ✓ | ✓ |
| create (qemu) | — | ✓ |
| start/stop/delete | ✓ stub | ✓ |
| ISO import (CDI DataVolume) | ✓ | ✓ |
| container disk | ✓ | ✓ |
| proxy (tailnet exposure) | ✓ manifest | ✓ |
| TUI (interactive) | scaffold | ✓ gum |
| completions | cobra built-in | manual |
| bootOrder | ✓ | ✓ |
| unique names | ✓ | ✓ |

## Architecture

```
tailvm-go/
├── main.go                    # entry point
├── cmd/
│   ├── root.go                # Cobra root, registry init
│   ├── create.go              # create (kubevirt backed)
│   ├── list.go                # list (Lip Gloss styled)
│   ├── commands.go            # start/stop/delete/info/viewer/logs
│   ├── helpers.go             # utility functions
│   ├── tui.go                 # Bubble Tea scaffold
│   └── cmd_test.go            # cmd-level tests
└── pkg/
    ├── types/types.go         # shared types + tests
    ├── registry/registry.go   # VM registry persistence + tests
    └── kubevirt/client.go     # KubeVirt client + manifest gen + tests
```

## Port proxy

The `pkg/kubevirt` package generates manifests for a unified proxy pod
that forwards all enabled ports to the VM's pod IP:

- Port 5900: `virtctl vnc --proxy-only`
- All other ports: `socat` to resolved VMI IP

The proxy Service is annotated with `tailscale.com/expose` for tailnet access.
