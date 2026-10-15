// Confirming something you cannot undo.
//
// Deleting a guest takes its disks with it, and there is no way back. Every
// such action here used to be one browser `confirm()`: a dialog whose default
// button is already focused, so a stray Enter after a bulk selection destroys
// guests and their storage. The operator is not asked to look at what they
// picked, only to dismiss a box.
//
// Proxmox makes a dangerous removal different in kind, not merely in wording.
// Its ConfirmRemoveDialog keeps the confirm button disabled until the operator
// types the resource's own identifier, so the action cannot complete without
// reading what is on screen. That is what this does.
//
// Everything else stays a plain confirm. A dialog that always demands typing
// teaches people to type without reading, which is the habit this exists to
// break.

import { esc } from './dom.js';

// Ask before something irreversible. Resolves true only when the operator
// typed `identifier` and confirmed.
//
//   title       the heading, naming the action
//   identifier  what has to be typed, exactly
//   label       how to describe what to type, for the field's own label
//   items       optional list of names, shown so the selection is visible
//   note        optional line about what else goes with it
//   verb        the confirm button's text
export function confirmDestroy({ title, identifier, label, items = [], note = '', verb = 'Delete' }) {
  return new Promise((resolve) => {
    const dlg = document.createElement('dialog');
    dlg.className = 'confirm-destroy';
    dlg.innerHTML = `
      <h2>${esc(title)}</h2>
      ${items.length ? `<ul class="confirm-items">${items.map((n) => `<li>${esc(n)}</li>`).join('')}</ul>` : ''}
      ${note ? `<p class="confirm-note">${esc(note)}</p>` : ''}
      <label class="confirm-label">${esc(label)}
        <input type="text" autocomplete="off" spellcheck="false" class="confirm-input">
      </label>
      <div class="dialog-actions">
        <button type="button" class="btn confirm-cancel">Cancel</button>
        <button type="button" class="btn danger confirm-go" disabled>${esc(verb)}</button>
      </div>`;
    document.body.appendChild(dlg);

    const input = dlg.querySelector('.confirm-input');
    const go = dlg.querySelector('.confirm-go');

    // Settle once, whichever way the dialog closes, and take it off the page.
    // A dialog left behind would answer the next query for '.confirm-destroy'.
    let settled = false;
    const finish = (ok) => {
      if (settled) return;
      settled = true;
      if (dlg.open) dlg.close();
      dlg.remove();
      resolve(ok);
    };

    // The whole point: the button cannot be reached until what is typed
    // matches. Trimmed, because a trailing space from a paste is not a
    // different answer, and nothing else is forgiven.
    input.oninput = () => { go.disabled = input.value.trim() !== identifier; };
    input.onkeydown = (e) => {
      if (e.key === 'Enter' && !go.disabled) { e.preventDefault(); finish(true); }
    };
    go.onclick = () => finish(true);
    dlg.querySelector('.confirm-cancel').onclick = () => finish(false);
    // Escape closes a <dialog> natively, and a click on the backdrop should
    // too. Both must resolve, or the caller waits for ever.
    dlg.addEventListener('cancel', () => finish(false));
    dlg.addEventListener('click', (e) => { if (e.target === dlg) finish(false); });

    dlg.showModal();
    input.focus();
  });
}
