// RDP console transport: IronRDP through the RDCleanPath proxy (ADR-0002).
// The open/close contract is the one in console/vnc.js.
//
// IronRemoteDesktop is the global vendor/iron-remote-desktop.js defines.

import { rdpURL } from '../api.js';

// openRDP mounts IronRDP in `screen` and connects with the given credentials.
// `onFail` gets the error if the connection cannot be made.
export function openRDP(screen, vm, { username, password, onFail }) {
  const ironRDP = document.createElement('div');
  ironRDP.id = 'rdp-iron';
  screen.appendChild(ironRDP);

  const cfg = new window.IronRemoteDesktop.ConfigBuilder()
    .withUsername(username)
    .withPassword(password)
    .withDestination(vm.name)
    .withProxyAddress(rdpURL(vm))
    .withAuthToken('corral')
    .build();

  let session = null;
  const ui = new window.IronRemoteDesktop.UserInteraction(ironRDP);
  ui.setVisibility(true);
  ui.connect(cfg).then((info) => {
    session = ui;
    info.run(); // start the RDP session loop
  }).catch(onFail);

  return {
    // Only once connected: before that there is no session to send it to.
    ctrlAltDel() { if (session) session.ctrlAltDel(); },
    close() {
      try { ui.shutdown(); } catch { /* already gone */ }
    },
  };
}
