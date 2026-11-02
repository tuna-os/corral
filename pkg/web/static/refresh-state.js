// Shared refresh state: navigation records a render without loading the fleet.
import { PollingState } from './poll.js';
import { state } from './state.js';
import { capabilityFingerprint } from './ui/capabilities.js';

export const pollingState = new PollingState();

// Everything the tree and content pane draw from. One definition, because the
// poll compares against it and markRendered writes it: when the two disagreed
// — the second was missing a capability's data — a render could be judged
// necessary every tick for a change that was already on screen. The image
// catalogue is in here too, or Storage View would fetch its images and then
// conclude there was nothing new to draw. A capability contributes its own
// data through the registry, so this list does not have to name it.
export function renderFingerprint() {
  return JSON.stringify([
    state.vms, state.cts, state.nodes, capabilityFingerprint(),
    state.images, state.dataVolumes, state.selected, state.tab,
  ]);
}
