# Browser demo site

This directory holds a static demo site for Corral (#284). The site runs the
real Corral dashboard in the browser. It needs no server process, no cluster
and no install.

## How it works

1. `cmd/corral-wasm` compiles the web UI and its API to WebAssembly. It uses
   the in-memory demo cluster from `pkg/demo`, which `corral web --demo` also
   uses.
2. `index.html` registers the service worker `sw.js`, then reloads the page.
3. `sw.js` starts the WebAssembly module. It then sends each request from the
   page to the Go HTTP handler and returns the answer to the page.

All state stays in the browser tab. When the browser stops the service worker,
the demo cluster goes back to its start state.

## Build and run

```sh
just web-demo            # builds dist/web-demo
python3 -m http.server -d dist/web-demo 8000
```

Then open ``http://localhost:8000/`` in a browser. The site also works
from a sub-path, for example a project site on GitHub Pages.

The service worker needs a secure context. Use `localhost` or HTTPS.

## Publish to GitHub Pages

The `web-demo` workflow builds the site on each pull request that changes it.
To publish the site from `main`, a maintainer must do two steps:

1. Set **Settings > Pages > Source** to "GitHub Actions".
2. Set the repository variable `CORRAL_WEB_DEMO_PAGES` to `true`.

## Limits

- The consoles (VNC, serial and shell) use WebSockets. A service worker cannot
  answer a WebSocket, so the consoles do not connect.
- The demo does not show the local QEMU VM. That VM needs a filesystem, and the
  browser has none.
- When the site uses a sub-path, disk export opens a page outside the site.
  That page does not load.
- The first visit downloads about 5 MB of compressed WebAssembly.

A real guest console needs QEMU compiled to WebAssembly. That is future work.
