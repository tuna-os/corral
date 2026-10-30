// Entry point of the standalone console page (/console/{ns}/{name}). The
// logic and its automation contract live in console/page.js.

import { runConsolePage } from './console/page.js';
import { openVNC } from './console/vnc.js';

runConsolePage({
  body: document.body,
  screen: document.getElementById('vnc-screen'),
  status: document.getElementById('console-status'),
  open: openVNC,
});
