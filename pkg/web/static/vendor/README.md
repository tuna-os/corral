# Vendored browser libraries

Pinned copies so the consoles and the dashboard work offline / air-gapped
(same rationale as the vendored `../alpine.min.js` — see
docs/adr/0004-web-ui-alpinejs-no-build.md).

`MANIFEST.json` is the machine-readable record of the same set: package,
version, upstream URL, SHA-256 and byte count for every third-party browser
asset corral embeds, including `../alpine.min.js`. The table below is the
human-readable view of it; the two must not disagree.

| File | Source |
|---|---|
| `xterm.min.js`, `xterm.min.css` | `@xterm/xterm@5.5.0` |
| `addon-fit.min.js` | `@xterm/addon-fit@0.10.0` |
| `novnc-rfb.esm.js` | `@novnc/novnc@1.4.0` `core/rfb.js`, bundled to a single ES module via jsDelivr `/+esm` |
| `iron-remote-desktop.js` | `@devolutions/iron-remote-desktop@0.11.0` |
| `gridstack-all.js`, `gridstack.min.css` | `gridstack@14.0.0` (MIT) |
| `uPlot.iife.min.js`, `uPlot.min.css` | `uplot@1.6.32` (MIT) |
| `../alpine.min.js` | `alpinejs@3.15.12` (per ADR-0004) |

To update, re-download from jsdelivr with the new version pinned:

```sh
curl -fsSLo xterm.min.js      https://cdn.jsdelivr.net/npm/@xterm/xterm@<v>/lib/xterm.min.js
curl -fsSLo xterm.min.css     https://cdn.jsdelivr.net/npm/@xterm/xterm@<v>/css/xterm.min.css
curl -fsSLo addon-fit.min.js  https://cdn.jsdelivr.net/npm/@xterm/addon-fit@<v>/lib/addon-fit.min.js
curl -fsSLo novnc-rfb.esm.js "https://cdn.jsdelivr.net/npm/@novnc/novnc@<v>/core/rfb.js/+esm"
curl -fsSLo iron-remote-desktop.js \
  "https://cdn.jsdelivr.net/npm/@devolutions/iron-remote-desktop@<v>/iron-remote-desktop.js"
curl -fsSLo gridstack-all.js  https://cdn.jsdelivr.net/npm/gridstack@<v>/dist/gridstack-all.js
curl -fsSLo gridstack.min.css https://cdn.jsdelivr.net/npm/gridstack@<v>/dist/gridstack.min.css
curl -fsSLo uPlot.iife.min.js https://cdn.jsdelivr.net/npm/uplot@<v>/dist/uPlot.iife.min.js
curl -fsSLo uPlot.min.css     https://cdn.jsdelivr.net/npm/uplot@<v>/dist/uPlot.min.css
```

Then update `MANIFEST.json` in the same commit — new `version`, `url`, `sha256`
(`sha256sum <file>`) and `bytes` (`wc -c <file>`).

The `iron-remote-desktop.js` bundle carries no version string of its own. To
identify the vendored copy, we digested each published version of the package
until one matched: `0.11.0`.

The two package names from IronRDP are easy to confuse.
`@devolutions/iron-remote-desktop` is the host web component in this directory.
`@devolutions/iron-remote-desktop-rdp` is the RDP backend module that plugs into
that host. ADR-0002 names the backend module, but this directory does not hold
it yet. Keep both packages on compatible versions when phase 2 lands.
