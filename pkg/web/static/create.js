// Create VM and Create CT dialogs, the OS wizard and the build-log viewer.

import { api } from './api.js';
import { refresh } from './app.js';
import { icon } from './icons.js';
import { state } from './state.js';
import { $, esc, toast } from './ui/dom.js';

// ── Create dialog ─────────────────────────────────────────────────

$('#btn-create').onclick = () => {
  loadCatalog();
  // Reset to the catalog (the simple path) and update field visibility
  const srcType = document.querySelector('[name=sourceType]');
  if (srcType) srcType.value = 'catalog';
  const nsInput = document.querySelector('#create-form [name=namespace]');
  if (nsInput && !nsInput.value) nsInput.value = state.caps.defaultNamespace || '';
  updateSourceFields();
  wizardReset();
  $('#create-dialog').showModal();
};
$('#btn-cancel').onclick = () => $('#create-dialog').close();

// ── Create CT dialog (#50) — a separate button + form, not a toggle on the
// VM dialog, per PVE's own two-button Create VM / Create CT pattern. ──────

$('#btn-create-ct').onclick = () => $('#ct-create-dialog').showModal();
$('#ct-btn-cancel').onclick = () => $('#ct-create-dialog').close();

$('#ct-create-form').onsubmit = async (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const body = {
    name: f.get('name'),
    image: f.get('image'),
    namespace: f.get('namespace') || '',
    storageClass: f.get('storageClass') || '',
    cpu: parseInt(f.get('cpu'), 10) || 1,
    mem: f.get('mem') || '512Mi',
    disk: f.get('disk') || '5Gi',
    privileged: !!f.get('privileged'),
  };
  try {
    await api('/api/cts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    $('#ct-create-dialog').close();
    e.target.reset();
    toast(`Container ${body.name} created`);
    refresh(true);
  } catch (err) {
    toast(`Create failed: ${err.message}`);
  }
};

// ── Simple wizard: OS cards → name + size → create ────────────────

const WIZ_SIZES = {
  s: { cpu: 1, mem: '2G', disk: '15G' },
  m: { cpu: 2, mem: '4G', disk: '20G' },
  l: { cpu: 4, mem: '8G', disk: '40G' },
};
let wizImages = [];   // catalog entries + bootc entries (kind: 'bootc')
let wizSelected = null;
let wizFilter = 'all';

// A distro logo from simple-icons, falling back to a letter badge.
// (The badge swap is wired up post-render — see wizBindLogoFallbacks.)
function wizLogo(entry) {
  const letter = esc((entry.name || '?')[0].toUpperCase());
  if (!entry.logo) return `<span class="wiz-badge">${letter}</span>`;
  return `<img class="wiz-logo" data-letter="${letter}" src="https://cdn.simpleicons.org/${esc(entry.logo)}" alt="">`;
}

function wizBindLogoFallbacks(root) {
  root.querySelectorAll('img.wiz-logo').forEach((img) => {
    img.onerror = () => {
      const span = document.createElement('span');
      span.className = 'wiz-badge';
      span.textContent = img.dataset.letter || '?';
      img.replaceWith(span);
    };
  });
}

async function wizardLoad() {
  let imgs = [];
  try { imgs = await api('/api/images'); } catch { /* cards stay empty */ }
  wizImages = imgs.map((i) => ({ ...i, kind: i.url ? 'import' : i.iso ? 'iso' : 'server' }));
  if (state.caps.bootc) {
    $('#wiz-filters [data-filter="bootc"]').hidden = false;
    let bimgs = [];
    try { bimgs = await api('/api/images?type=bootc'); } catch { /* none */ }
    wizImages = wizImages.concat(bimgs.map((b) => ({ ...b, variant: 'bootc', kind: 'bootc' })));
  }
  wizardRenderCards();
}

function wizardRenderCards() {
  const grid = $('#wiz-cards');
  const shown = wizImages.filter((i) =>
    wizFilter === 'all' || i.variant === wizFilter);
  const cards = shown.map((i, n) => `
    <div class="wiz-card" data-wiz="${n}">
      ${i.custom ? `<button class="chip-x wiz-rmsrc" data-rmsrc="${esc(i.name)}" title="Remove custom source">×</button>` : ''}
      ${wizLogo(i)}
      <strong>${esc(i.name)}</strong>
      <span class="muted">${esc(i.description || '')}</span>
      ${i.custom ? '<span class="pill">custom</span>'
        : i.variant === 'bootc' ? '<span class="pill mid">bootc</span>'
        : i.iso ? '<span class="pill">installer</span>' : ''}
    </div>`).join('');
  // An always-present "add your own" card.
  const addCard = `<div class="wiz-card wiz-add" id="wiz-add-src">
      <span class="wiz-add-plus">${icon('plus')}</span>
      <strong>Add source</strong>
      <span class="muted">your own image or ISO URL</span>
    </div>`;
  grid.innerHTML = cards + addCard;
  wizBindLogoFallbacks(grid);
  grid.querySelectorAll('[data-wiz]').forEach((card) => {
    card.onclick = (e) => {
      if (e.target.closest('.wiz-rmsrc')) return; // handled below
      wizSelected = shown[parseInt(card.dataset.wiz, 10)];
      $('#wiz-selected').innerHTML = `${wizLogo(wizSelected)} <strong>${esc(wizSelected.name)}</strong>
        <span class="muted">${esc(wizSelected.description || '')}</span>`;
      wizBindLogoFallbacks($('#wiz-selected'));
      $('#wiz-step-1').hidden = true;
      $('#wiz-step-2').hidden = false;
      $('#wiz-back').hidden = false;
      $('#wiz-create').hidden = false;
      $('#wiz-name').focus();
    };
  });
  grid.querySelectorAll('[data-rmsrc]').forEach((b) => {
    b.onclick = async (e) => {
      e.stopPropagation();
      if (!confirm(`Remove custom source "${b.dataset.rmsrc}"?`)) return;
      try { await api(`/api/sources/${encodeURIComponent(b.dataset.rmsrc)}`, { method: 'DELETE' }); }
      catch (err) { toast(err.message); return; }
      wizardLoad();
    };
  });
  $('#wiz-add-src').onclick = addCustomSource;
}

// addCustomSource prompts for a name/kind/URI, persists it (ConfigMap), and
// reloads the wizard so the new card appears alongside the catalog.
async function addCustomSource() {
  const src = await sourceDialog();
  if (!src) return;
  try {
    await api('/api/sources', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(src),
    });
  } catch (e) { toast(e.message); return; }
  toast(`Added source "${src.name}"`);
  wizardLoad();
}

function sourceDialog() {
  return new Promise((resolve) => {
    const dlg = document.createElement('dialog');
    dlg.className = 'pick-dialog';
    dlg.innerHTML = `
      <h3>Add a custom source</h3>
      <label>Name <input id="src-name" placeholder="my-image" autofocus></label>
      <label>Kind
        <select id="src-kind">
          <option value="containerDisk">Container image (boots directly)</option>
          <option value="url">Disk image URL (qcow2/raw, imported)</option>
          <option value="iso">Installer ISO URL</option>
        </select>
      </label>
      <label id="src-uri-l">URI <input id="src-uri" placeholder="ghcr.io/me/image:tag"></label>
      <label>Description <input id="src-desc" placeholder="optional"></label>
      <div class="pick-actions">
        <button class="btn" value="cancel">Cancel</button>
        <button class="btn primary" id="src-go">${icon('plus')} Add</button>
      </div>`;
    document.body.appendChild(dlg);
    const finish = (val) => { dlg.close(); dlg.remove(); resolve(val); };
    const ph = { containerDisk: 'ghcr.io/me/image:tag', url: 'https://…/disk.qcow2', iso: 'https://…/installer.iso' };
    dlg.querySelector('#src-kind').onchange = (e) => { dlg.querySelector('#src-uri').placeholder = ph[e.target.value]; };
    dlg.querySelector('[value="cancel"]').onclick = () => finish(null);
    dlg.querySelector('#src-go').onclick = () => {
      const name = dlg.querySelector('#src-name').value.trim();
      const uri = dlg.querySelector('#src-uri').value.trim();
      if (!name || !uri) { toast('Name and URI are required'); return; }
      finish({
        name, uri,
        kind: dlg.querySelector('#src-kind').value,
        description: dlg.querySelector('#src-desc').value.trim(),
      });
    };
    dlg.addEventListener('cancel', () => finish(null));
    dlg.showModal();
  });
}

function wizardReset() {
  wizSelected = null;
  $('#wizard-simple').hidden = false;
  $('#create-form').hidden = true;
  $('#wiz-step-1').hidden = false;
  $('#wiz-step-2').hidden = true;
  $('#wiz-back').hidden = true;
  $('#wiz-create').hidden = true;
  wizardLoad();
}

$('#wiz-filters').querySelectorAll('.wiz-chip').forEach((chip) => {
  chip.onclick = () => {
    wizFilter = chip.dataset.filter;
    $('#wiz-filters').querySelectorAll('.wiz-chip').forEach((c) => c.classList.toggle('active', c === chip));
    wizardRenderCards();
  };
});
$('#wiz-sizes').querySelectorAll('.wiz-size').forEach((sz) => {
  sz.onclick = () => $('#wiz-sizes').querySelectorAll('.wiz-size')
    .forEach((c) => c.classList.toggle('selected', c === sz));
});
$('#wiz-back').onclick = () => {
  $('#wiz-step-1').hidden = false;
  $('#wiz-step-2').hidden = true;
  $('#wiz-back').hidden = true;
  $('#wiz-create').hidden = true;
};
$('#wiz-cancel').onclick = () => $('#create-dialog').close();
$('#wiz-advanced').onclick = () => {
  $('#wizard-simple').hidden = true;
  $('#create-form').hidden = false;
};
$('#btn-simple').onclick = () => {
  $('#create-form').hidden = true;
  $('#wizard-simple').hidden = false;
};

$('#wiz-create').onclick = async () => {
  const name = $('#wiz-name').value.trim();
  if (!name || !wizSelected) { toast('Pick a name'); return; }
  const size = WIZ_SIZES[$('#wiz-sizes .selected')?.dataset.size || 'm'];
  const body = {
    name,
    namespace: state.caps.defaultNamespace || '',
    cpu: size.cpu, mem: size.mem, disk: size.disk,
    sshKey: $('#wiz-sshkey').value.trim(),
  };
  if (wizSelected.kind === 'bootc') body.bootc = wizSelected.image;
  else body.image = wizSelected.name;
  $('#wiz-create').disabled = true;
  try {
    const res = await api('/api/vms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    $('#create-dialog').close();
    $('#wiz-name').value = '';
    if (res.task) watchBuild(res.task, name);
    refresh();
  } catch (err) {
    toast(`Create failed: ${err.message}`);
  }
  $('#wiz-create').disabled = false;
};
$('#btn-build-close').onclick = () => $('#build-dialog').close();

const SOURCE_HINTS = {
  containerDisk: 'quay.io/containerdisks/fedora:42',
  import: 'https://cloud-images.example/jammy.qcow2',
  iso: 'https://example.com/installer.iso',
  bootc: 'quay.io/centos-bootc/centos-bootc:stream9',
  windows: 'https://example.com/Win11_x64.iso (or pick a preset ↓)',
  pvc: 'existing-pvc-name',
};

async function loadCatalog() {
  let imgs;
  try { imgs = await api('/api/images'); } catch { imgs = []; }
  const sel = document.querySelector('[name=catalogImage]');
  // Catalog entries boot three ways; flag the slower ones so there are no surprises.
  const kindTag = (i) => (i.url ? ' [imports via CDI]' : i.iso ? ' [installer ISO]' : '');
  if (sel) sel.innerHTML = imgs.map((i) => `<option value="${esc(i.name)}">${esc(i.name)} — ${esc(i.description)}${kindTag(i)}</option>`).join('');

  // Bootc image suggestions (datalist on the source field) when the plugin is on.
  if (state.caps.bootc) {
    let bimgs;
    try { bimgs = await api('/api/images?type=bootc'); } catch { bimgs = []; }
    const dl = $('#bootc-catalog');
    if (dl) dl.innerHTML = bimgs.map((b) => `<option value="${esc(b.image)}">${esc(b.name)} — ${esc(b.description)}</option>`).join('');
  }
}

const CREATE_HINTS = {
  iso: 'The installer ISO boots with a blank disk — finish the install in the Console tab.',
  bootc: 'The SSH key is baked into the built disk. Bootc builds take a few minutes — a live build log opens after submit.',
  windows: 'UEFI + TPM + Hyper-V tuned, with the virtio-win driver CD-ROM attached. After boot, in Setup click “Load driver” → the virtio CD-ROM. Provide a Windows installer ISO URL.',
  pvc: 'Boots an existing disk as-is; cloud-init does not run again.',
};
const DEFAULT_HINT = 'Cloud-init VMs get your SSH key and Tailscale auth key (if configured) automatically.';

// isLocalTarget: the create form is aimed at this host's QEMU, not the
// cluster (#91 Phase 3). Local supports installer ISO / qcow2 sources only
// (no cloud-init, no cluster fields).
function selectedCreateBackend() {
  const select = document.querySelector('#create-form [name=target]');
  return select?.selectedOptions[0]?.dataset.backend || (select?.value === 'local' ? 'qemu' : 'kubevirt');
}

function isLocalTarget() {
  return selectedCreateBackend() === 'qemu';
}

export function updateSourceFields() {
  const backend = selectedCreateBackend();
  const local = backend === 'qemu';
  const fileBacked = backend === 'qemu' || backend === 'libvirt';
  const kube = backend === 'kubevirt';
  const srcSel = document.querySelector('[name=sourceType]');
  // File-backed hypervisors accept ISO/qcow2; Incus accepts image aliases;
  // the full source catalog is available only to KubeVirt.
  for (const opt of srcSel.options) {
    opt.hidden = fileBacked
      ? opt.value !== 'iso' && opt.value !== 'import'
      : backend === 'incus' && opt.value !== 'containerDisk' && opt.value !== 'import';
  }
  if (fileBacked && srcSel.value !== 'iso' && srcSel.value !== 'import') srcSel.value = 'iso';
  if (backend === 'incus' && srcSel.value !== 'containerDisk' && srcSel.value !== 'import') srcSel.value = 'containerDisk';
  // Cluster-only fields disappear for a local VM.
  for (const sel of ['#create-form [name=namespace]', '#create-form [name=node]',
    '#create-form [name=instancetype]', '#create-form [name=preference]',
    '#create-form [name=cloudInit]', '#sshkey-field input']) {
    const el = document.querySelector(sel);
    if (el) el.closest('label').hidden = !kube;
  }

  const type = srcSel.value;
  $('#catalog-field').hidden = type !== 'catalog';
  $('#source-field').hidden = type === 'catalog' || type === 'pvc';
  $('#pvc-source-field').hidden = type !== 'pvc';
  $('#create-hint').textContent = fileBacked
    ? `${backend} VM. Source can be a path on the Corral web host or a URL (downloaded once, then cached). Cloud-init is not yet unified for this backend.`
    : backend === 'incus'
      ? 'Enter an Incus image alias such as images:ubuntu/24.04. Corral creates a virtual-machine instance on this remote.'
      : (CREATE_HINTS[type] || DEFAULT_HINT);
  if (fileBacked) {
    const srcInput = document.querySelector('[name=source]');
    if (srcInput) {
      srcInput.placeholder = type === 'iso'
        ? '/path/on/this/host.iso or https://…/installer.iso'
        : '/path/on/this/host.qcow2 or https://…/image.qcow2';
      srcInput.removeAttribute('list');
    }
    return;
  }
  const src = document.querySelector('[name=source]');
  if (src) {
    src.placeholder = SOURCE_HINTS[type] || '';
    // Bootc and Windows get catalog suggestions; other types are free-form.
    if (type === 'bootc') src.setAttribute('list', 'bootc-catalog');
    else if (type === 'windows') src.setAttribute('list', 'windows-iso-catalog');
    else src.removeAttribute('list');
  }
  if (type === 'pvc') loadPVCSources();
}

// Populates the "Existing PVC" dropdown from the DataVolume/ISO library
// (#49) — includes uploaded ISOs and imported images, not just a free-text
// PVC name. Ready-only (Succeeded) entries, since anything still importing
// isn't bootable yet.
async function loadPVCSources() {
  const sel = document.querySelector('[name=pvcSource]');
  if (!sel) return;
  let dvs;
  try { dvs = await api('/api/datavolumes'); } catch { dvs = []; }
  const ready = dvs.filter((d) => d.phase === 'Succeeded');
  sel.innerHTML = ready.length
    ? ready.map((d) => `<option value="${esc(d.name)}">${esc(d.name)} (${esc(d.size || '?')}, ${esc(d.namespace)})</option>`).join('')
    : `<option value="">— no ready images in the library, use Import/Upload first —</option>`;
}

document.querySelector('[name=sourceType]').onchange = updateSourceFields;
document.querySelector('#create-form [name=target]').onchange = updateSourceFields;

$('#create-form').onsubmit = async (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const body = {
    name: f.get('name'),
    target: f.get('target'),
    namespace: f.get('namespace'),
    node: f.get('node'),
    cpu: parseInt(f.get('cpu'), 10) || 2,
    mem: f.get('mem'),
    disk: f.get('disk'),
    cloudInit: f.get('cloudInit') || '',
    instancetype: f.get('instancetype') || '',
    preference: f.get('preference') || '',
  };
  const src = f.get('source');
  const type = f.get('sourceType');
  if (selectedCreateBackend() === 'qemu') {
    // Local QEMU VM (#91 Phase 3): lean payload, ISO or qcow2 only.
    const local = { name: body.name, target: body.target, cpu: body.cpu, mem: body.mem, disk: body.disk };
    if (type === 'import') local.import = src; else local.iso = src;
    try {
      const res = await api('/api/vms', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(local),
      });
      $('#create-dialog').close();
      e.target.reset();
      if (res.status === 'downloading') toast(`Downloading image for ${local.name} — progress in the Tasks panel`);
      refresh();
    } catch (err) {
      toast(`Create failed: ${err.message}`);
    }
    return;
  }
  body.sshKey = f.get('sshKey') || ''; // key or GitHub username, any source type
  if (type === 'catalog') body.image = f.get('catalogImage');
  else if (type === 'containerDisk') body.containerDisk = src;
  else if (type === 'import') body.import = src;
  else if (type === 'iso') body.iso = src;
  else if (type === 'bootc') body.bootc = src;
  else if (type === 'windows') { body.windows = true; body.iso = src; }
  else body.pvc = f.get('pvcSource') || src; // pvc: prefer the library dropdown, fall back to free text

  try {
    const res = await api('/api/vms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    $('#create-dialog').close();
    e.target.reset();
    if (res.task) watchBuild(res.task, body.name);
    refresh();
  } catch (err) {
    toast(`Create failed: ${err.message}`);
  }
};

// Poll a bootc build task, streaming its log into the build dialog.
export function watchBuild(taskID, vmName, opts = {}) {
  const dlg = $('#build-dialog');
  const log = $('#build-log');
  const title = $('#build-title');
  const titleRun = opts.titleRun || `Building bootc disk for ${vmName}…`;
  const titleDone = opts.titleDone || `✅ ${vmName} ready`;
  const titleFail = opts.titleFail || `❌ Build failed`;
  title.textContent = titleRun;
  log.textContent = '';
  dlg.showModal();

  const timer = setInterval(async () => {
    let t;
    try { t = await api(`/api/tasks/${taskID}`); } catch { return; }
    log.textContent = t.log;
    log.scrollTop = log.scrollHeight;
    if (t.status === 'done') {
      clearInterval(timer);
      title.textContent = titleDone;
      refresh();
      if (opts.onDone) opts.onDone();
    } else if (t.status === 'error') {
      clearInterval(timer);
      title.textContent = titleFail;
      log.textContent += `\n${t.error}`;
    }
  }, 2000);
}