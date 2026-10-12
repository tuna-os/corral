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
| Proxmox VE | **Source.** We cloned pve-manager and read `www/manager6/`, and the GUI page of its documentation. See the licence note below. |
| VS Code | **Primary.** The keybindings and tips pages. |
| Grafana | **Primary.** The panel and dashboard documentation. |
| vSphere | **Primary** for the failure: a Broadcom support article about a lost pane. |
| Nutanix Prism | **Secondary**, and it cannot be better. See the note below. |
| Linear | **Secondary.** Write-ups about its palette and shortcuts. |
| k9s | **Secondary.** Comparison articles that quote its `:po` prefixes. |
| Harvester | **Secondary.** The Harvester and Rancher documentation, and its UI extension's architecture page. |
| Cockpit | **Primary** for the list behaviour: the Fedora and Rocky guides to the Machines page. |
| Portainer | **Secondary.** Tutorials and reviews, because the project publishes no UI guide. |
| Rancher | **Secondary.** Its own documentation for the Virtualization Management area. |
| Data-table conventions | **Secondary.** A 2026 design guide, not a standard. |

A secondary row is still useful, because the pattern repeats across products.
Two sources had to agree before we took a pattern from them.

### Reading pve-manager, and not copying it

Proxmox VE is the closest peer in this list, and it publishes its code. So we
cloned `pve-manager` and read the ExtJS UI under `www/manager6/`. Before that,
the documentation was all we had used, which is a thin way to study the one
product we can read.

pve-manager is AGPL-3.0 and corral is Apache-2.0. AGPL code cannot move into
an Apache-2.0 project, so we take decisions from it and no code. Each row that
rests on the source says which decision, and the shape corral used instead.

### Why Prism stays a secondary source

We searched for better evidence. There is none to find.

- **Prism has no public source.** Nutanix publishes infrastructure tooling, a
  Terraform provider and an Ansible collection. It publishes no UI library and
  no design system. Every repository named "Prism UI" on GitHub belongs to an
  unrelated project.
- **The vendor's documentation needs a login.** `portal.nutanix.com` keeps the
  Prism Central guide behind authentication. We therefore have its text only
  where a search result quoted it. Any row that rests on that says so.
- **We have no screenshots.** The portal gates its images, and the Nutanix
  Bible's figures did not come through. No row below rests on a picture of the
  product.

What we did read in full is the Nutanix Bible's navigation chapter, which
documents the keyboard model. That chapter is where the Prism rows come from.

## The interfaces

| Interface | Why it is here |
|---|---|
| [Proxmox VE](https://pve.proxmox.com/wiki/Graphical_User_Interface) | The closest peer, and the one whose code we can read. |
| [Harvester](https://docs.harvesterhci.io/v1.8/) | The closest peer of all: a web console over KubeVirt. |
| [Cockpit](https://docs.fedoraproject.org/en-US/fedora-server/virtualization/vm-management-cockpit/) | The same guests, managed from one host. |
| [Portainer](https://earthly.dev/blog/portainer-for-docker-container-management/) | The container console that corral's CT views answer to. |
| [Rancher](https://ranchermanager.docs.rancher.com/integrations-in-rancher/harvester/overview) | One console over both guests and workloads. |
| vSphere Web Client | The same shape, and a documented failure corral can avoid. |
| [Nutanix Prism](https://www.nutanixbible.com/3b-book-of-prism-navigation.html) | Search-first navigation, and a keyboard model written down. |
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
| The URL addresses what is on screen | Proxmox (`StateProvider`, read in the source) | **Added.** The page had one address, so nobody could link to a guest and the back button did nothing. `deep-link` |
| Navigation goes in the URL, layout stays local | Proxmox (its `hslist` is view, resource and tab, and nothing else) | **Added.** A link says where you are. Pane widths are how you like to work, and sending those to somebody else would be rude. `deep-link` |
| Back and forward walk the UI | Proxmox | **Added.** `deep-link` |
| A URL short enough to paste | Proxmox (a positional list against a dictionary of every tab name) | **Declined.** That needs a central table naming each tab, which is the shape the capability registry just removed. corral uses readable keys, and accepts a longer address. |
| Typing the name to confirm a removal | Proxmox (`ConfirmRemoveDialog`, read in the source) | **Added.** Deleting a guest takes its disks, and it was one `confirm()`: a box with its default button already focused, so a stray Enter after a bulk selection destroyed guests and their storage. `confirm-destroy` |
| A plain confirm for everything else | Proxmox (only `dangerous` removals get the field) | **Kept.** A dialog that always demands typing teaches people to type without reading. |
| Name each failure, not a count | Proxmox (its bulk actions return a task, and the task log names each guest) | **Added.** "2 failed" told the operator nothing to act on, in a toast that was gone before they could ask. `bulk-failures` |
| Reset the layout | Dashboard widgets, and vSphere by its absence | **Added.** vSphere let admins lose a pane for good. `reset-layout` |

## Navigation

| Pattern | Seen in | corral |
|---|---|---|
| Several groupings of one fleet | Proxmox (Server, Storage, Pool, Folder) | **Added** Storage View. Server, Namespace and Pool existed. `storage-view` |
| Breadcrumb to the parent | VS Code, vSphere | **Added.** `breadcrumb` |
| Search before menus | Prism, Grafana | Palette existed. **Added** a visible way in. `palette-reach` |
| Single-key jumps | Prism, Linear | Present (`g d`, `c`, `s`). **Added** hints, because nothing told anyone. `palette-keys` |
| The view switch has a key | Prism (`O`/`D`/`T`, page-aware) | **Added.** Four views of the same fleet, reachable by mouse alone until now. `v` walks them. `palette-filters` |
| A list, then a detail screen with its tools along the top | Portainer, Cockpit | Present. |
| A wizard behind the list's Create action | Harvester | Present. |
| Status bar | VS Code | **Declined.** The dock already holds the state a status bar would carry. |

## The command palette

| Pattern | Seen in | corral |
|---|---|---|
| `Ctrl/Cmd+K` | VS Code, Grafana, Linear | Present. |
| A visible trigger | Every touch UI | **Added.** On a phone the palette could not open at all. `palette-reach` |
| Typed prefix narrows the list | k9s (`:po`), VS Code Quick Open | **Added.** `palette-scope` |
| `field=value` in the query | Prism (`vm alerts severity=critical`) | **Added.** node, namespace, status, backend and tag. A name match cannot tell a node from a guest called after one, and an unknown field matches nothing rather than look answered. `palette-filters` |
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
| Say whether the server refused or never answered | Proxmox (`getResponseErrorMessage` returns "Connection error" when there is no status) | **Added.** `fetch` rejects with "Failed to fetch", which says nothing about corral being down. `api-errors` |
| Never show an empty error | Proxmox (its message always carries the status) | **Added.** `statusText` is the HTTP reason phrase and HTTP/2 has none, so behind any proxy speaking h2 a non-JSON failure threw a blank message. `api-errors` |
| Focus before the scroll, and without moving the view | Proxmox (an override on its grid view, for this exact order) | **Added.** corral restored the scroll and focused after, and focusing scrolls the element into view, so a render that moved the focused row threw the position away. |
| One poll at a time | Proxmox (`UpdateStore` schedules the next load from the last load's callback) | **Added.** A fixed 5-second timer over an asynchronous refresh sent a second request before the first came back. Measured: two in flight against a slow endpoint, now one. `poll-backoff` |
| A slow server is polled less | Proxmox (its interval is the base plus twice the last load's runtime) | **Added**, with the same arithmetic. Nobody tunes it, and a struggling server gets room. `poll-backoff` |
| A refresh asked for during a poll is not lost | — | **Added** with the guard. The callers that force a refresh have just changed something and have to see it. |
| Land on the nearest surviving parent | Proxmox (`ResourceTree` walks the parent chain) | **Added.** A deleted or migrated guest dropped you at the datacenter, the furthest place from where you were. `vanished-selection` |
| Compare only the fields that are drawn | Proxmox (`changedFields`, and it mutates the record in place) | **Declined**, and measured first. corral rebuilds a row when any field of the guest changes, because its handlers close over the guest object and a reused node must not hold an old one. That is only safe while the fleet payload carries nothing volatile, which it does not: two polls of `/api/vms` are byte-identical. `tree-reconcile` is what guards it, because a volatile field would churn every signature and the kept-row count would drop. |

## Customisation

| Pattern | Seen in | corral |
|---|---|---|
| Draggable, resizable widgets | Grafana | Present. |
| Layout kept per browser | Grafana, Proxmox | Present, and **extended** to the workspace. |
| Light and dark | Most modern UIs | **Added.** Follows the desktop. `colour-scheme` |
| Accent colour | Proxmox theming | Present, through `/api/theme`. |
| Several dashboards for one scope | Prism (Manage Dashboard) | **Added.** A scope had exactly one, so watching capacity and chasing a failure meant rebuilding the same screen twice. The arrangement and the density belong to the dashboard. `named-dashboards` |
| Density for the dashboard, not only for a table | Prism (its Data Density setting) | **Added.** Three modes under the data grid's own names, kept per dashboard, and Reset layout restores them. `dash-density` |

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
| A single key opens a panel | Prism (`P` tasks, `A` alerts) | **Added.** The dock panels answered to the mouse and to Tab, and to nothing else. The letters are corral's own, after its panel names. `dock-keys` |
| The same key dismisses it | General | **Added** with the key, so one key does both. `dock-keys` |
| Arrow keys move through a menu | Prism | Present. |
| A single key is a letter inside a field | Prism, and every app with single-key shortcuts | Present, and the new keys obey it. `dock-keys` |

## Extensibility

| Pattern | Seen in | corral |
|---|---|---|
| A marketplace of plugins | KubeStellar | Present. |
| Permissions a plugin declares | KubeStellar | Present. |
| The host places a contribution, rather than naming it | KubeStellar | **Added.** A capability registers what it adds; core draws it. `host-power` |
| A plugin declares its place in the UI | KubeStellar | **Proposed** for the plugin contract. Stage 0 is built. See [RFC-0002](rfc/0002-plugin-ui-contributions.md). |
| A plugin ships code the page runs | KubeStellar (WASM, `plugin-component.js`) | **Declined.** corral embeds its UI and has no build step. Script from a marketplace could read the session and call any API. |
| Documented extension points | — | **Added.** See [the extension points](web-ui-extension-points.md). |

## Taken from Prism, and not taken

Prism's navigation chapter documents a keyboard model, and one of its ideas
needs a screen corral does not have. This page keeps it, so the next person
does not have to find it again.

| Pattern | corral |
|---|---|
| A diagram of the fleet, as a third view | **Open.** Prism switches between overview, diagram and table. corral has no diagram, so that view has to exist before a switch can reach it. The key below walks the four views it does have. |

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
