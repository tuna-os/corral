# Vendored console libraries

Pinned copies so the consoles work offline / air-gapped (same rationale as
the vendored `../alpine.min.js` — see docs/adr/0004-web-ui-alpinejs-no-build.md).

| File | Source |
|---|---|
| `xterm.min.js`, `xterm.min.css` | `@xterm/xterm@5.5.0` |
| `addon-fit.min.js` | `@xterm/addon-fit@0.10.0` |
| `novnc-rfb.esm.js` | `@novnc/novnc@1.4.0` `core/rfb.js`, bundled to a single ES module via jsDelivr `/+esm` |

To update, re-download from jsdelivr with the new version pinned:

```sh
curl -fsSLo xterm.min.js      https://cdn.jsdelivr.net/npm/@xterm/xterm@<v>/lib/xterm.min.js
curl -fsSLo xterm.min.css     https://cdn.jsdelivr.net/npm/@xterm/xterm@<v>/css/xterm.min.css
curl -fsSLo addon-fit.min.js  https://cdn.jsdelivr.net/npm/@xterm/addon-fit@<v>/lib/addon-fit.min.js
curl -fsSLo novnc-rfb.esm.js "https://cdn.jsdelivr.net/npm/@novnc/novnc@<v>/core/rfb.js/+esm"
```
