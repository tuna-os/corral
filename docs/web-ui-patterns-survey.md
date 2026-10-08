# Web UI: a survey of reference interfaces

This page records a survey of other interfaces, and what corral took from each
one. It exists so the next person does not repeat the search, and so a
pattern that corral rejects stays rejected for a stated reason.

Each row names the pattern, where we saw it, and what corral does. "Added"
rows name the check in `scripts/ui-smoke.mjs` that holds the behaviour in
place.

## How we checked each one

Not every row rests on the same quality of evidence, so the table below says
which. Read a "secondary" row as weaker. The pattern comes from a write-up
about the product, not from the product or its own documentation.

| Interface | Evidence |
|---|---|
| KubeStellar | **Source.** We cloned kubestellar/ui and read `plugin.yml`, `PluginAPI.ts` and the plugin types. |
| W3C APG | **Primary.** The pattern pages themselves. |
| WCAG | **Primary.** The success criteria. |
| Proxmox VE | **Primary.** The GUI page of the Proxmox documentation. |
| VS Code | **Primary.** The keybindings and tips pages. |
| Grafana | **Primary.** The panel and dashboard documentation. |
| vSphere | **Primary** for the failure: a Broadcom support article about a lost pane. |
| Nutanix Prism | **Secondary.** The Nutanix Bible, a community reference. |
| Linear | **Secondary.** Write-ups about its palette and shortcuts. |
| k9s | **Secondary.** Comparison articles that quote its `:po` prefixes. |
| Harvester | **Secondary.** The Harvester and Rancher documentation, and its UI extension's architecture page. |
| Cockpit | **Primary** for the list behaviour: the Fedora and Rocky guides to the Machines page. |
| Portainer | **Secondary.** Tutorials and reviews, because the project publishes no UI guide. |
| Rancher | **Secondary.** Its own documentation for the Virtualization Management area. |
| Data-table conventions | **Secondary.** A 2026 design guide, not a standard. |

A secondary row is still useful, because the pattern repeats across products.
Two sources had to agree before we took a pattern from them.

## The interfaces

| Interface | Why it is here |
|---|---|
| [Proxmox VE](https://pve.proxmox.com/wiki/Graphical_User_Interface) | The closest peer. Same four regions, same job. |
| [Harvester](https://docs.harvesterhci.io/v1.8/) | The closest peer of all: a web console over KubeVirt. |
| [Cockpit](https://docs.fedoraproject.org/en-US/fedora-server/virtualization/vm-management-cockpit/) | The same guests, managed from one host. |
| [Portainer](https://earthly.dev/blog/portainer-for-docker-container-management/) | The container console that corral's CT views answer to. |
| [Rancher](https://ranchermanager.docs.rancher.com/integrations-in-rancher/harvester/overview) | One console over both guests and workloads. |
| vSphere Web Client | The same shape, and a documented failure corral can avoid. |
| [Nutanix Prism](https://www.nutanixbible.com/3b-book-of-prism-navigation.html) | Search-first navigation over a tree. |
| [VS Code](https://code.visualstudio.com/docs/editing/getting-started/tips-and-tricks) | The reference for a command palette and a breadcrumb. |
| Linear | A dense, keyboard-first list UI. |
| Grafana | Dashboards of draggable panels, and `Ctrl+K`. |
| [k9s](https://kprompt.ai/blog/k9s-alternatives) | Typed prefixes that jump between resource kinds. |
| [KubeStellar](https://github.com/kubestellar/ui) | A plugin that declares its own place in the UI. |
| [W3C APG](https://www.w3.org/WAI/ARIA/apg/) | The behaviour contract for splitter, combobox and tabs. |
| [Data-table conventions](https://www.setproduct.com/blog/data-table-ui-design) | Density, keyboard movement and focus in a grid. |
| WCAG | Skip links, status messages, reduced motion. |

## Layout

| Pattern | Seen in | corral |
|---|---|---|
| Tree, content, dock, header | Proxmox, vSphere | Present before this work. |
| Only the content pane scrolls | Proxmox, vSphere | **Added.** The window scrolled, which carried the header and tree away. `workspace-layout` |
| Resizable tree and dock | Proxmox | **Added.** One splitter primitive. `workspace-layout` |
| Hide a pane completely | Proxmox | **Added.** The separator stays, because it owns the key that restores the pane. `workspace-layout` |
| Dockable, movable panels | vSphere Web Client | **Declined.** Two resizable edges and a tab strip cover the need. Free docking is a large build with little gain here. |
| Reset the layout | Dashboard widgets, and vSphere by its absence | **Added.** vSphere let admins lose a pane for good. `reset-layout` |

## Navigation

| Pattern | Seen in | corral |
|---|---|---|
| Several groupings of one fleet | Proxmox (Server, Storage, Pool, Folder) | **Added** Storage View. Server, Namespace and Pool existed. `storage-view` |
| Breadcrumb to the parent | VS Code, vSphere | **Added.** `breadcrumb` |
| Search before menus | Prism, Grafana | Palette existed. **Added** a visible way in. `palette-reach` |
| Single-key jumps | Prism, Linear | Present (`g d`, `c`, `s`). **Added** hints, because nothing told anyone. `palette-keys` |
| A list, then a detail screen with its tools along the top | Portainer, Cockpit | Present. |
| A wizard behind the list's Create action | Harvester | Present. |
| Status bar | VS Code | **Declined.** The dock already holds the state a status bar would carry. |

## The command palette

| Pattern | Seen in | corral |
|---|---|---|
| `Ctrl/Cmd+K` | VS Code, Grafana, Linear | Present. |
| A visible trigger | Every touch UI | **Added.** On a phone the palette could not open at all. `palette-reach` |
| Typed prefix narrows the list | k9s (`:po`), VS Code Quick Open | **Added.** `palette-scope` |
| The palette teaches its shortcuts | VS Code, Linear | **Added.** `palette-keys` |
| Recent entries first | VS Code, Linear | Present. |
| Result count announced | APG combobox | **Added.** `palette-reach` |

## The data grid

| Pattern | Seen in | corral |
|---|---|---|
| Sort, reorder, hide, resize columns | Grafana, every console | Present. |
| Saved views | Grafana | Present. |
| Sticky header | Data-table conventions | Present. The survey expected a gap and found none. |
| Density as named modes | Data-table conventions | **Added.** Three modes, kept per grid. `grid-density` |
| Arrow keys move between rows | Data-table conventions | **Added.** Tab belongs to the controls in a row. `grid-keys` |
| Quiet hover, loud focus ring | Data-table conventions | **Added** with density. |
| One action on each row, following the row's state | Cockpit | **Added.** Every action needed a checkbox and the bulk bar, or opening the guest. `grid-row-actions` |
| The rest of the actions one menu away | Cockpit, Portainer | **Added** with the row action, from the set the right-click already offered. `grid-row-actions` |
| A control in a row keeps its own keys | APG practice | **Added.** Space on a row's checkbox opened the guest instead of selecting it. `grid-row-actions` |
| Act on many rows from the list | Portainer | Present. `bulk-select` |
| A colour and a word for state | Portainer, Cockpit | Present. |
| Node placement in the list | Rancher | Present. |
| Live usage bars in the rows | Cockpit | **Declined.** The CPU and Mem columns hold what a guest was given. A bar reads as a share of something in use, so it would state a number corral does not have. |

## State and redraw

| Pattern | Seen in | corral |
|---|---|---|
| Update rows in place | Every live console | **Added.** The poll rebuilt everything every 5 s. `tree-reconcile`, `grid-reconcile` |
| Keep a live console across a redraw | Proxmox, vSphere | **Added.** The page used to freeze instead. `console-stays-live`, `multiview-stays-live` |
| Hold off during a gesture | General | **Added.** `interaction-guard` |

## Customisation

| Pattern | Seen in | corral |
|---|---|---|
| Draggable, resizable widgets | Grafana | Present. |
| Layout kept per browser | Grafana, Proxmox | Present, and **extended** to the workspace. |
| Light and dark | Most modern UIs | **Added.** Follows the desktop. `colour-scheme` |
| Accent colour | Proxmox theming | Present, through `/api/theme`. |

## Small screens

| Pattern | Seen in | corral |
|---|---|---|
| Sidebar becomes a drawer | Most responsive apps | Present. |
| One column for a widget grid | Grafana mobile | **Added.** Three columns at 420px clipped every figure. `dashboard-on-a-phone` |
| Icons replace labels on a toolbar | Most mobile apps | **Added.** `toolbar-on-a-phone` |
| Rare actions move to an overflow menu | Most mobile apps | **Added.** `toolbar-on-a-phone` |

## Access without a mouse

| Pattern | Seen in | corral |
|---|---|---|
| Splitter keys, and Enter to collapse | APG window splitter | **Added.** The pattern needs Enter. `workspace-layout` |
| Combobox semantics in the palette | APG combobox | Present. |
| Tabs with a roving tabindex | APG tabs | **Added** with the dock tabs. `dock-tabs` |
| Skip link | WCAG 2.4.1 | **Added.** The tree rows made the walk longer. `reachable` |
| Status messages announced | WCAG 4.1.3 | **Added.** Toasts were silent. `reachable` |
| Reduced motion | WCAG 2.3.3 | **Added.** `reachable` |

## Extensibility

| Pattern | Seen in | corral |
|---|---|---|
| A marketplace of plugins | KubeStellar | Present. |
| Permissions a plugin declares | KubeStellar | Present. |
| The host places a contribution, rather than naming it | KubeStellar | **Added.** A capability registers what it adds; core draws it. `host-power` |
| A plugin declares its place in the UI | KubeStellar | **Proposed** for the plugin contract. Stage 0 is built. See [RFC-0002](rfc/0002-plugin-ui-contributions.md). |
| A plugin ships code the page runs | KubeStellar (WASM, `plugin-component.js`) | **Declined.** corral embeds its UI and has no build step. Script from a marketplace could read the session and call any API. |
| Documented extension points | — | **Added.** See [the extension points](web-ui-extension-points.md). |

## What the survey still leaves open

- **A Storage *content* screen.** Storage View reads the catalogue. To add,
  remove or clean up images needs backend work.
- **Dock panels beyond Tasks and Events.** #350 also asks for a cluster log.
  corral's task log already is that log, so a second tab would show one thing
  twice.
- **Plugin UI contributions, stages 1 and 2.** RFC-0002 holds the design. It
  covers the `ui` metadata section and the typed documents. It also covers a
  marketplace plugin whose only purpose is a screen. A maintainer owns that
  call, because it changes the boundary in ADR-0007.

  Stage 0 is built. `ui/capabilities.js` holds a registry that accepts entries
  at run time, and host-power registers with it. Core keeps one line about
  that capability: the import that loads its module. See
  [the extension points](web-ui-extension-points.md) for the hooks.
