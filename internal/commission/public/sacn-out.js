'use strict';
// sACN control: this tool as a small sACN source (internal/sacn, /api/sacn/out).
// The server holds the patch, the profiles and every level, and does the sending;
// this page asks for its state twice a second and posts changes. A fader being
// dragged posts its level at most 25 times a second and ignores the server's
// value for that channel until it is let go, so it does not jump back.
// Fixture and profile names can come from RDM or an imported file: esc() them.

const soEls = {
  status: $('soStatus'), bar: $('soBar'), state: $('soState'), iface: $('soIface'), priority: $('soPriority'), name: $('soName'),
  start: $('btnSoStart'), stop: $('btnSoStop'), clear: $('btnSoClear'), banner: $('soBanner'),
  fixtures: $('soFixtures'), channels: $('soChannels'), profiles: $('soProfiles'),
  addName: $('soAddName'), addProfile: $('soAddProfile'), addUniverse: $('soAddUniverse'), addAddress: $('soAddAddress'), addCount: $('soAddCount'),
  add: $('btnSoAdd'), addNote: $('soAddNote'), tiles: $('soTiles'),
  rawUniverse: $('soRawUniverse'), rawInfo: $('soRawInfo'), release: $('btnSoRelease'), rawSel: $('soRawSel'), rawLevel: $('soRawLevel'), rawValue: $('soRawValue'),
  rawZero: $('btnSoRawZero'), rawHalf: $('btnSoRawHalf'), rawFull: $('btnSoRawFull'), rawNone: $('btnSoRawNone'), rawNote: $('soRawNote'), grid: $('soGrid'),
  newProfile: $('btnSoNewProfile'), importBtn: $('btnSoImport'), importFile: $('soImportFile'), profileNote: $('soProfileNote'), editor: $('soEditor'), profileList: $('soProfileList'),
};

let soOn = false, soTimer = null, soBusy = false;
let so = null;                 // the last /api/sacn/out answer
let soError = '';              // why the last Start, Stop or Clear failed
let soTab = load('commission.soTab', 'fixtures');
let soFilled = false;          // the bar's inputs were filled from the server once
const soLevels = {};           // universe → Uint8Array(512), this page's copy
const soHold = new Map();      // "universe:address" → time until which the server's level is ignored
const soQueue = new Map();     // universe → Map(address → level) waiting to be posted
let soFlushTimer = null;
const soOpen = new Set();      // fixture ids whose other channels are shown
const soPick = new Set();      // selected fixture ids: the one at the lowest address is their master
let soTilesSig = '', soProfilesSig = '', soIfaceSig = '', soProfileOptsSig = '', soGridSig = '';
const soSel = new Set();       // selected addresses on the Channels tab
let soSelLast = null;
let soDragging = null;          // the fader being dragged
let soDraft = null;            // the profile being edited: { was, name, channels: [{name,type,default}] }

function sacnOutShown(on) {
  if (on === soOn) return;
  soOn = on;
  clearInterval(soTimer);
  if (!on) return;
  soPoll();
  soTimer = setInterval(soPoll, 500);
}

async function soPost(path, body) {
  const r = await fetch('/api/sacn/out/' + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
  return d;
}

async function soPoll() {
  if (soBusy || document.hidden) return;
  soBusy = true;
  try { soApply(await fxCall('/api/sacn/out')); } catch (e) { soError = 'This page cannot reach the tool: ' + e.message; soRender(); }
  soBusy = false;
}

function soApply(state) {
  if (!state || !state.universes) return;
  so = state;
  const now = Date.now(), seen = new Set();
  for (const u of so.universes) {
    seen.add(String(u.universe));
    const mine = soLevels[u.universe] || (soLevels[u.universe] = new Uint8Array(512));
    const s = atob(u.levels);
    for (let i = 0; i < 512; i++) {
      const key = u.universe + ':' + (i + 1);
      if (soHold.has(key)) { if (soHold.get(key) > now) continue; soHold.delete(key); }
      mine[i] = s.charCodeAt(i);
    }
  }
  for (const u of Object.keys(soLevels)) if (!seen.has(u)) delete soLevels[u];
  soRender();
}

function soLevel(u, a) { const l = soLevels[u]; return l ? l[a - 1] : 0; }

// Set levels here and queue them for the server.
function soSet(u, pairs) {
  const mine = soLevels[u] || (soLevels[u] = new Uint8Array(512));
  let q = soQueue.get(u);
  if (!q) soQueue.set(u, q = new Map());
  const until = Date.now() + 900;
  for (const [a, v] of pairs) { mine[a - 1] = v; q.set(a, v); soHold.set(u + ':' + a, until); }
  if (!soFlushTimer) soFlushTimer = setTimeout(soFlush, 40);
  soDrawLevels();
}

async function soFlush() {
  soFlushTimer = null;
  const batches = [...soQueue.entries()];
  soQueue.clear();
  for (const [u, q] of batches) {
    try { await soPost('levels', { universe: u, set: [...q.entries()] }); soError = ''; }
    catch (e) { soError = 'Could not set levels: ' + e.message; soRender(); }
  }
}

// ─── The output bar ─────────────────────────────────────────────────────────
function soStartWhyNot() {
  if (!so) return 'Not connected to the tool yet';
  if (so.running) return 'Already sending';
  if (!so.interfaces.length) return 'This computer has no network connection to send from';
  if (!soEls.iface.value) return 'Choose the network connection to send from first';
  if (!so.universes.length) return 'Nothing to send yet: patch a fixture or set a channel first';
  const p = Number(soEls.priority.value);
  if (!/^\d+$/.test(soEls.priority.value.trim()) || p > 200) return 'Priority runs from 0 to 200';
  return '';
}

function soRender() {
  if (!so) return;
  const running = so.running;
  if (!soFilled || running) {
    if (document.activeElement !== soEls.priority) soEls.priority.value = so.priority;
    if (document.activeElement !== soEls.name) soEls.name.value = so.name;
    soFilled = true;
  }
  const sig = JSON.stringify(so.interfaces) + so.interface + running;
  if (sig !== soIfaceSig && document.activeElement !== soEls.iface) {
    const keep = running ? so.interface : (soEls.iface.value || so.interface);
    // No connection is picked for the operator: a laptop's first one is often its Wi-Fi.
    soEls.iface.innerHTML = `<option value="">${so.interfaces.length ? 'choose…' : 'no network'}</option>` +
      so.interfaces.map(i => `<option value="${esc(i.name)}">${esc(i.name)} · ${esc(i.address)}</option>`).join('');
    soEls.iface.value = so.interfaces.some(i => i.name === keep) ? keep : '';
    soIfaceSig = sig;
  }
  for (const el of [soEls.iface, soEls.priority, soEls.name]) { el.disabled = running; el.title = running ? 'Stop the output to change this' : ''; }

  soEls.bar.classList.toggle('so-live', running);
  soEls.state.textContent = running ? (so.dry ? 'DRY RUN' : 'SENDING') : 'OUTPUT OFF';
  const us = so.universes.map(u => u.universe);
  soEls.status.textContent = running ? `— sending universe${us.length === 1 ? '' : 's'} ${us.join(', ')} at priority ${so.priority}` : '— off';

  const why = soStartWhyNot();
  soEls.start.disabled = !!why;
  soEls.start.title = why || `Start sending ${us.length} universe${us.length === 1 ? '' : 's'} (${us.join(', ')}) onto the network`;
  soEls.stop.disabled = !running;
  soEls.stop.title = running ? 'Stop sending. Levels are kept; nodes go back to their other sources' : 'The output is off';
  soEls.clear.disabled = !so.universes.length;
  soEls.clear.title = so.universes.length ? 'Every fixture to its defaults, every other channel to zero' : 'No levels set';

  const lines = [];
  let cls = 'banner';
  if (soError) { lines.push(esc(soError)); cls = 'banner err'; }
  if (so.error) { lines.push(esc(so.error)); cls = 'banner err'; }
  if (running && so.dry) lines.push('DRY RUN: the output is running but nothing is sent to the network.');
  for (const u of so.universes) {
    for (const o of u.others) {
      lines.push(`Universe ${u.universe}: <strong>${esc(o.name || 'another source')}</strong> (${esc(o.ip)}, priority ${o.priority}) is sending too. ` +
        (o.priority > so.priority ? 'It has the higher priority, so nodes follow it and ignore this tool.' :
          o.priority < so.priority ? `This tool has the higher priority, so nodes follow this tool${running ? '' : ' once it starts'}.` :
            'At the same priority a node takes the higher of the two levels on each channel.'));
    }
  }
  soEls.banner.className = lines.length ? cls : 'banner hidden';
  soEls.banner.innerHTML = lines.join('<br>');

  for (const t of document.querySelectorAll('.so-tab')) t.classList.toggle('active', t.dataset.so === soTab);
  soEls.fixtures.classList.toggle('hidden', soTab !== 'fixtures');
  soEls.channels.classList.toggle('hidden', soTab !== 'channels');
  soEls.profiles.classList.toggle('hidden', soTab !== 'profiles');

  const opts = JSON.stringify(so.profiles.map(p => [p.name, p.channels.length]));
  if (opts !== soProfileOptsSig && document.activeElement !== soEls.addProfile) {
    const keep = soEls.addProfile.value;
    soEls.addProfile.innerHTML = so.profiles.map(p => `<option value="${esc(p.name)}">${esc(p.name)} · ${p.channels.length} ch</option>`).join('');
    if (so.profiles.some(p => p.name === keep)) soEls.addProfile.value = keep;
    soProfileOptsSig = opts;
  }
  soRenderTiles();
  soRenderGrid();
  soRenderProfiles();
  soDrawLevels();
}

// ─── Fixtures ───────────────────────────────────────────────────────────────
const SO_INTENSITY = ['red', 'green', 'blue', 'white', 'amber', 'lime', 'warm', 'cool', 'uv'];
const SO_TINT = { red: [255, 0, 0], green: [0, 255, 0], blue: [0, 0, 255], white: [255, 255, 255], amber: [255, 170, 0], lime: [170, 255, 0], warm: [255, 190, 120], cool: [190, 215, 255], uv: [110, 0, 255] };

function soMain(f) { // the faders a tile always shows: its dimmers, or everything when it has none
  const d = f.channels.map((c, i) => i).filter(i => f.channels[i].type === 'dimmer');
  return d.length ? d : f.channels.map((c, i) => i);
}

// The selected fixtures in patch order (universe, then address), and their master:
// the first of them, once two or more are selected.
function soPicked() { return so.fixtures.filter(f => soPick.has(f.id)).sort((a, b) => a.universe - b.universe || a.address - b.address); }
function soMaster() { const p = soPicked(); return p.length > 1 ? p[0] : null; }

// "Starting from M select every N": the Mth fixture along the patch (universe, then address) and
// every Nth after it. why says what stops it, for the button's tooltip.
function soNth(n) { const t = n % 100; return n + (t > 10 && t < 14 ? 'th' : ['th', 'st', 'nd', 'rd'][n % 10] || 'th'); }
function soEvery() {
  const num = id => { const s = $(id).value.trim(); return /^\d+$/.test(s) ? parseInt(s, 10) : 0; };
  const n = num('soEveryN'), from = num('soEveryFrom'), out = { n, from, ids: [], why: '' };
  if (!so.fixtures.length) out.why = 'No fixtures patched';
  else if (n < 1) out.why = 'Select every needs a whole number, 1 or more';
  else if (from < 1) out.why = 'Starting from needs a whole number, 1 or more';
  else if (from > so.fixtures.length) out.why = `There are only ${so.fixtures.length} fixtures, so none is the ${soNth(from)}`;
  else out.ids = [...so.fixtures].sort((a, b) => a.universe - b.universe || a.address - b.address).filter((f, i) => i + 1 >= from && (i + 1 - from) % n === 0).map(f => f.id);
  return out;
}

// The channel of fixture g that goes with channel i of fixture f: the same type,
// counted the same way (the second "white" with the second "white"), so fixtures
// of different profiles follow each other where they have the same kind of channel.
function soMatch(f, i, g) {
  const type = f.channels[i].type;
  const nth = f.channels.slice(0, i).filter(c => c.type === type).length;
  let seen = 0;
  for (let j = 0; j < g.channels.length; j++) if (g.channels[j].type === type && seen++ === nth) return j;
  return -1;
}

// Levels for a fixture, and for the others selected when it is their master.
function soSetFixture(f, pairs) { // pairs: [channel index, level]
  const master = soMaster(), byU = new Map();
  const put = (g, i, v) => { if (!byU.has(g.universe)) byU.set(g.universe, []); byU.get(g.universe).push([g.address + i, v]); };
  for (const [i, v] of pairs) {
    put(f, i, v);
    if (master && master.id === f.id) for (const g of soPicked()) { if (g.id === f.id) continue; const j = soMatch(f, i, g); if (j >= 0) put(g, j, v); }
  }
  for (const [u, list] of byU) soSet(u, list);
}

function soFader(f, i) {
  const c = f.channels[i], a = f.address + i;
  return `<label class="so-ch so-t-${esc(c.type)}"><span title="Address ${a}">${esc(c.name)}</span>
    <input type="range" min="0" max="255" value="0" data-u="${f.universe}" data-a="${a}" data-i="${i}"><output>0</output></label>`;
}

function soRenderTiles() {
  for (const id of [...soPick]) if (!so.fixtures.some(f => f.id === id)) soPick.delete(id);
  const master = soMaster(), picked = soPicked();
  $('soPickInfo').textContent = !so.fixtures.length ? '' : master ? `${picked.length} selected. ${master.name} has the lowest address, so its faders and buttons move them all.` :
    picked.length ? '1 selected. Select another to move them together.' : 'Click a fixture\'s name to select it. With several selected, the one at the lowest address moves them all.';
  for (const [id, off, why] of [['btnSoPickAll', !so.fixtures.length || picked.length === so.fixtures.length, so.fixtures.length ? 'Every fixture is selected' : 'No fixtures patched'], ['btnSoPickNone', !picked.length, 'Nothing is selected']]) {
    $(id).disabled = off; $(id).title = off ? why : '';
  }
  const ev = soEvery(), evBtn = $('btnSoPickEvery');
  evBtn.disabled = !ev.ids.length;
  evBtn.title = ev.why || `Select ${ev.ids.length} of the ${so.fixtures.length} fixtures: starting from the ${soNth(ev.from)}, every ${soNth(ev.n)} one, in address order. This replaces the selection`;
  const rm = $('btnSoPickRemove');
  rm.disabled = !picked.length;
  rm.textContent = picked.length ? `Remove ${picked.length} selected` : 'Remove selected';
  rm.title = picked.length ? `Take the ${picked.length} selected fixture${picked.length === 1 ? '' : 's'} off the patch at once; their channels go to zero` : 'Select the fixtures to remove first (click their names, or Select all)';
  const sig = JSON.stringify([so.fixtures, [...soOpen], [...soPick]]);
  if (sig === soTilesSig) return;
  soTilesSig = sig;
  if (!so.fixtures.length) {
    soEls.tiles.innerHTML = `<div class="empty-state small">No fixtures patched. Pick a profile, a universe and an address above, or use <strong>Patch</strong> on a fixture in the Fixtures view.</div>`;
    return;
  }
  soEls.tiles.innerHTML = `<div class="so-tiles">` + so.fixtures.map(f => {
    const main = soMain(f), rest = f.channels.map((c, i) => i).filter(i => !main.includes(i));
    const open = soOpen.has(f.id);
    const end = f.address + Math.max(1, f.channels.length) - 1;
    const sel = soPick.has(f.id), isMaster = master && master.id === f.id;
    return `<div class="so-fx${f.problem ? ' so-fx-bad' : ''}${sel ? ' so-picked' : ''}${isMaster ? ' so-master' : ''}" data-id="${f.id}">
      <div class="so-fx-head" data-act="pick" title="${sel ? 'Selected. Click to take it out of the selection' : 'Click to select'}"><span class="so-swatch"></span><b>${esc(f.name)}</b>
        ${isMaster ? '<span class="so-badge">MASTER</span>' : sel && master ? `<span class="so-badge so-follow">follows ${esc(master.name)}</span>` : ''}
        <span class="muted">U${f.universe} · ${f.address}${end > f.address ? '–' + end : ''} · ${esc(f.profile)}</span></div>
      ${f.problem ? `<div class="so-fx-why">${esc(f.problem)}</div>` : ''}
      <div class="so-faders">${main.map(i => soFader(f, i)).join('')}${open ? rest.map(i => soFader(f, i)).join('') : ''}</div>
      <div class="so-fx-foot">
        <button class="btn btn-small" data-act="off" ${f.channels.length ? '' : 'disabled title="No profile"'}>Off</button>
        <button class="btn btn-small" data-act="full" ${f.channels.length ? '' : 'disabled title="No profile"'}>Full</button>
        <button class="btn btn-small so-hl${f.highlight ? ' active' : ''}" data-act="highlight" ${f.canHighlight ? `title="${f.highlight ? 'Fade back to the levels it had before' : 'Fade to its highlight levels over half a second, to find it on the rig. Click again to put it back'}"` : 'disabled title="Its profile has no highlight levels: set them on Profiles"'}>Highlight</button>
        <span class="spacer"></span>
        <button class="btn btn-small" data-act="more" ${rest.length ? `title="${open ? 'Hide' : 'Show'} its other channels"` : 'disabled title="All its channels are shown"'}>${open ? 'Less ▴' : (rest.length ? `${rest.length} more ▾` : 'More ▾')}</button>
        <button class="btn btn-small btn-danger" data-act="remove" title="Take this fixture off the patch at once; its channels go to zero">Remove</button>
      </div></div>`;
  }).join('') + `</div>`;
}

function soSwatch(f) {
  let dim = 1, any = false, rgb = [0, 0, 0], hasDim = false;
  f.channels.forEach((c, i) => {
    const v = soLevel(f.universe, f.address + i) / 255;
    if (c.type === 'dimmer' && !hasDim) { dim = v; hasDim = true; }
    const t = SO_TINT[c.type];
    if (t) { any = true; for (let k = 0; k < 3; k++) rgb[k] += t[k] * v; }
  });
  if (!any) rgb = [255, 235, 200];
  return `rgb(${rgb.map(x => Math.round(Math.min(255, x) * dim)).join(',')})`;
}

// Levels into whatever is on screen, without rebuilding it.
function soDrawLevels() {
  if (!so) return;
  if (soTab === 'fixtures') {
    for (const el of soEls.tiles.querySelectorAll('input[type=range]')) {
      const v = soLevel(Number(el.dataset.u), Number(el.dataset.a));
      // Only the fader under the finger is left alone, so the ones following a master move with it.
      if (Number(el.value) !== v && el !== soDragging) el.value = v;
      el.nextElementSibling.textContent = el.value;
    }
    for (const tile of soEls.tiles.querySelectorAll('.so-fx')) {
      const f = so.fixtures.find(x => x.id === Number(tile.dataset.id));
      if (f) tile.querySelector('.so-swatch').style.background = soSwatch(f);
    }
  } else if (soTab === 'channels') {
    const u = soRawU(), cells = soEls.grid.children;
    for (let i = 0; i < cells.length; i++) {
      const v = u ? soLevel(u, i + 1) : 0, c = cells[i];
      if (c._v !== v) { c._v = v; c.lastChild.textContent = v; c.style.setProperty('--l', (v / 2.55).toFixed(1) + '%'); c.classList.toggle('on', v > 0); }
      c.classList.toggle('sel', soSel.has(i + 1));
    }
  }
}

soEls.tiles.addEventListener('input', ev => {
  const el = ev.target;
  if (el.type !== 'range') return;
  soDragging = el;
  const f = so.fixtures.find(x => x.id === Number(el.closest('.so-fx').dataset.id));
  if (f) soSetFixture(f, [[Number(el.dataset.i), Number(el.value)]]);
});
soEls.tiles.addEventListener('change', () => { soDragging = null; });
soEls.tiles.addEventListener('click', async ev => {
  const b = ev.target.closest('[data-act]');
  if (!b || b.disabled) return;
  const id = Number(b.closest('.so-fx').dataset.id), f = so.fixtures.find(x => x.id === id);
  if (!f) return;
  const act = b.dataset.act;
  if (act === 'pick') { soPick.has(id) ? soPick.delete(id) : soPick.add(id); soRender(); return; }
  if (act === 'more') { soOpen.has(id) ? soOpen.delete(id) : soOpen.add(id); soRender(); return; }
  if (act === 'remove') {
    try { soOpen.delete(id); soApply(await soPost('fixtures/remove', { ids: [id] })); soEls.addNote.textContent = `› Removed ${f.name}.`; }
    catch (e) { soEls.addNote.textContent = '› ' + e.message; }
    return;
  }
  if (act === 'highlight') {
    // The server does the fade; look more often while it runs so the faders follow.
    try {
      const m = soMaster(), on = !f.highlight;
      for (const g of m && m.id === id ? soPicked() : [f]) if (g.canHighlight) soApply(await soPost('fixtures/highlight', { id: g.id, on }));
      for (const ms of [150, 300, 450, 650]) setTimeout(soPoll, ms);
    }
    catch (e) { soEls.addNote.textContent = '› ' + e.message; }
    return;
  }
  // Full and Off move the dimmer; a fixture without one moves its colours.
  // Each fixture by its own channels, so a master with a dimmer still turns off one without.
  const m = soMaster();
  for (const g of m && m.id === id ? soPicked() : [f]) {
    let idx = g.channels.map((c, i) => i).filter(i => g.channels[i].type === 'dimmer');
    if (!idx.length) idx = g.channels.map((c, i) => i).filter(i => SO_INTENSITY.includes(g.channels[i].type));
    if (!idx.length) idx = g.channels.map((c, i) => i);
    soSet(g.universe, idx.map(i => [g.address + i, act === 'full' ? 255 : 0]));
  }
});
$('btnSoPickAll').addEventListener('click', () => { for (const f of so.fixtures) soPick.add(f.id); soRender(); });
$('btnSoPickNone').addEventListener('click', () => { soPick.clear(); soRender(); });
$('btnSoPickEvery').addEventListener('click', () => { const ev = soEvery(); if (!ev.ids.length) return; soPick.clear(); for (const id of ev.ids) soPick.add(id); soRender(); });
for (const id of ['soEveryN', 'soEveryFrom']) $(id).addEventListener('input', () => soRender());
$('btnSoPickRemove').addEventListener('click', async () => {
  const picked = soPicked();
  if (!picked.length) return;
  try {
    for (const f of picked) soOpen.delete(f.id);
    soApply(await soPost('fixtures/remove', { ids: picked.map(f => f.id) }));
    soEls.addNote.textContent = `› Removed ${picked.length} fixture${picked.length === 1 ? '' : 's'}: ${picked.map(f => f.name).join(', ')}.`;
  } catch (e) { soEls.addNote.textContent = '› ' + e.message; }
});

function soNum(el) { const s = el.value.trim(); return /^\d+$/.test(s) ? Number(s) : NaN; }

async function soAdd() {
  const profile = soEls.addProfile.value, universe = soNum(soEls.addUniverse), address = soNum(soEls.addAddress), count = soNum(soEls.addCount);
  if ([universe, address, count].some(Number.isNaN)) { soEls.addNote.textContent = '› Universe, address and how many must be numbers.'; return; }
  try {
    soApply(await soPost('fixtures/add', { name: soEls.addName.value, profile, universe, address, count }));
    const p = so.profiles.find(x => x.name === profile), next = address + count * (p ? p.channels.length : 1);
    soEls.addNote.textContent = `› Added ${count} × ${profile} at universe ${universe}, address ${address}.` + (so.running ? ' It is being sent now.' : '');
    soEls.addAddress.value = next <= 512 ? next : address;
    const m = soEls.addName.value.trim().match(/^(.*?)(\d+)$/);
    if (m) soEls.addName.value = m[1] + (Number(m[2]) + count);
  } catch (e) { soEls.addNote.textContent = '› Not added: ' + e.message; }
}
soEls.add.addEventListener('click', soAdd);

// Called by the Fixtures view's Patch button: patch a fixture that RDM has read,
// making a plain profile of the right size if none has its name yet.
// slotsUrl is the fixture's /slots address on its node: where it says, over RDM, what
// each channel is. Channels it describes get their type's usual highlight level from
// the server (dimmer 50%, red, green and blue full, cyan, magenta and yellow zero).
async function sacnPatchFromRdm(f, universe, slotsUrl) {
  if (!so) soApply(await fxCall('/api/sacn/out'));
  const profile = ([f.model, f.personalityName].filter(Boolean).join(' ') || `RDM fixture ${f.footprint} ch`).slice(0, 80);
  let how = `the profile "${profile}", which was already saved`;
  if (!so.profiles.some(p => p.name.toLowerCase() === profile.toLowerCase())) {
    let channels = null;
    try {
      const d = slotsUrl ? await fxCall(slotsUrl) : null;
      if (d && d.slots && d.slots.length === f.footprint) channels = d.slots.map(s => ({ name: s.name, type: so.types.includes(s.type) ? s.type : 'other', default: 0 }));
    } catch { /* the fixture does not describe its channels */ }
    how = channels ? `a new profile "${profile}", with its channels as the fixture describes them`
      : `a new profile "${profile}". The fixture does not say what its channels are, so they are numbered and Highlight is off until you set them on sACN control, Profiles`;
    channels = channels || Array.from({ length: f.footprint }, (x, i) => ({ name: `Ch ${i + 1}`, type: 'other', default: 0, highlight: -1 }));
    soApply(await soPost('profiles/save', { name: profile, channels }));
  }
  const name = (f.label || f.model || f.uid).slice(0, 60);
  soApply(await soPost('fixtures/add', { name, profile, universe, address: f.start, count: 1 }));
  return `Patched ${name} at universe ${universe}, address ${f.start} with ${how}.`;
}

// ─── Channels ───────────────────────────────────────────────────────────────
function soRawU() { const u = soNum(soEls.rawUniverse); return u >= 1 && u <= 63999 ? u : 0; }

function soRenderGrid() {
  if (!soEls.grid.children.length) {
    let cells = '';
    for (let i = 1; i <= 512; i++) cells += `<div class="sacn-cell" data-a="${i}"><i>${i}</i><b>0</b></div>`;
    soEls.grid.innerHTML = cells;
  }
  const u = soRawU(), row = so.universes.find(x => x.universe === u);
  const here = so.fixtures.filter(f => f.universe === u);
  soEls.rawInfo.textContent = !u ? 'Universes run from 1 to 63999.' : !row ? 'Not being sent. Setting a level adds this universe to the output.' :
    `${here.length} fixture${here.length === 1 ? '' : 's'} patched here.` + (so.running ? ' Being sent now.' : '');
  const why = !row ? 'This universe is not in the output' : here.length ? 'Fixtures are patched here: remove them first' : '';
  soEls.release.disabled = !!why;
  soEls.release.title = why || 'Take this universe out of the output (receivers are told it stopped)';
  const n = soSel.size, off = !u || !n;
  soEls.rawSel.textContent = n ? `${n} channel${n === 1 ? '' : 's'} selected.` : 'Click channels to select them (Shift-click for a range).';
  for (const el of [soEls.rawLevel, soEls.rawZero, soEls.rawHalf, soEls.rawFull, soEls.rawNone]) { el.disabled = off; el.title = off ? 'Select channels first' : ''; }
  if (n && document.activeElement !== soEls.rawLevel) soEls.rawLevel.value = soLevel(u, Math.min(...soSel));
  soEls.rawValue.textContent = soEls.rawLevel.value;

  const sig = JSON.stringify([u, here]);
  if (sig === soGridSig) return;
  soGridSig = sig;
  const owner = {};
  for (const f of here) f.channels.forEach((c, i) => { owner[f.address + i] = `${f.name}: ${c.name}`; });
  for (const c of soEls.grid.children) {
    const o = owner[c.dataset.a];
    c.classList.toggle('patched', !!o);
    c.title = o || '';
  }
}

function soRawSet(v) {
  const u = soRawU();
  if (!u || !soSel.size) return;
  soEls.rawLevel.value = v;
  soEls.rawValue.textContent = v;
  soSet(u, [...soSel].map(a => [a, v]));
}
soEls.grid.addEventListener('click', ev => {
  const c = ev.target.closest('.sacn-cell');
  if (!c) return;
  const a = Number(c.dataset.a);
  if (ev.shiftKey && soSelLast) for (let i = Math.min(a, soSelLast); i <= Math.max(a, soSelLast); i++) soSel.add(i);
  else soSel.has(a) ? soSel.delete(a) : soSel.add(a);
  soSelLast = a;
  soRender();
});
soEls.rawLevel.addEventListener('input', () => soRawSet(Number(soEls.rawLevel.value)));
soEls.rawZero.addEventListener('click', () => soRawSet(0));
soEls.rawHalf.addEventListener('click', () => soRawSet(128));
soEls.rawFull.addEventListener('click', () => soRawSet(255));
soEls.rawNone.addEventListener('click', () => { soSel.clear(); soSelLast = null; soRender(); });
soEls.rawUniverse.addEventListener('input', () => { soSel.clear(); soSelLast = null; soEls.rawNote.textContent = ''; soRender(); });
soEls.release.addEventListener('click', async () => {
  if (soEls.release.disabled) return;
  try { soApply(await soPost('release', { universe: soRawU() })); soEls.rawNote.textContent = `› Universe ${soRawU()} is out of the output.`; }
  catch (e) { soEls.rawNote.textContent = '› ' + e.message; }
});

// ─── Profiles ───────────────────────────────────────────────────────────────
function soRenderProfiles() {
  const sig = JSON.stringify([so.profiles, so.fixtures.map(f => f.profile), so.folder]);
  if (sig === soProfilesSig) return;
  soProfilesSig = sig;
  const used = new Set(so.fixtures.map(f => f.profile.toLowerCase()));
  soEls.profileList.innerHTML = `<table class="fx-table so-profiles"><thead><tr><th>Profile</th><th>Ch</th><th>Channels</th><th></th></tr></thead><tbody>` +
    so.profiles.map((p, i) => {
      const inUse = used.has(p.name.toLowerCase());
      const del = p.builtIn ? 'Built-in profiles cannot be deleted' : inUse ? 'Patched fixtures use it: remove them first' : '';
      return `<tr><td><b>${esc(p.name)}</b>${p.builtIn ? ' <span class="muted">built in</span>' : ''}</td><td class="fx-num">${p.channels.length}</td>
        <td class="so-chlist">${p.channels.slice(0, 24).map(c => `<span class="so-chip so-t-${esc(c.type)}">${esc(c.name)}</span>`).join('')}${p.channels.length > 24 ? ` <span class="muted">+${p.channels.length - 24}</span>` : ''}</td>
        <td class="so-actions">
          <button class="btn btn-small" data-p="${i}" data-act="${p.builtIn ? 'copy' : 'edit'}" title="${p.builtIn ? 'Start a new profile from this one' : 'Change this profile'}">${p.builtIn ? 'Copy' : 'Edit'}</button>
          <button class="btn btn-small" data-p="${i}" data-act="export" title="Save this profile as a file, to load on another computer">Export</button>
          <button class="btn btn-small btn-danger" data-p="${i}" data-act="delete" ${del ? `disabled title="${del}"` : 'title="Delete this profile at once"'}>Delete</button>
        </td></tr>`;
    }).join('') + `</tbody></table><div class="muted so-folder">Your own profiles and the patch are kept in ${esc(so.folder)}.</div>`;
}

function soDrawEditor() {
  const d = soDraft;
  if (!d) { soEls.editor.innerHTML = ''; return; }
  soEls.editor.innerHTML = `<div class="so-editor">
    <div class="so-form"><label>Profile name <input type="text" id="soDraftName" value="${esc(d.name)}" maxlength="80" autocomplete="off"></label>
      <span class="muted">${d.was ? `Editing “${esc(d.was)}”. A new name saves a copy.` : 'New profile.'} Channels are in DMX order.</span></div>
    <table class="fx-table so-edit-table"><thead><tr><th>#</th><th>Name</th><th>Type</th><th>Default</th><th title="The level Highlight fades this channel to. Empty leaves the channel where it is.">Highlight</th><th></th></tr></thead><tbody>` +
    d.channels.map((c, i) => `<tr data-i="${i}"><td class="fx-num">${i + 1}</td>
      <td><input type="text" data-f="name" value="${esc(c.name)}" maxlength="40" autocomplete="off"></td>
      <td><select data-f="type">${so.types.map(t => `<option${t === c.type ? ' selected' : ''}>${esc(t)}</option>`).join('')}</select></td>
      <td><input type="text" inputmode="numeric" data-f="default" value="${c.default}" class="so-num" autocomplete="off"></td>
      <td><input type="text" inputmode="numeric" data-f="highlight" value="${c.highlight >= 0 ? c.highlight : ''}" placeholder="—" class="so-num" autocomplete="off" title="0-255, or empty to leave this channel alone"></td>
      <td class="so-actions"><button class="btn btn-small" data-act="up" ${i ? '' : 'disabled title="Already first"'}>↑</button>
        <button class="btn btn-small" data-act="down" ${i < d.channels.length - 1 ? '' : 'disabled title="Already last"'}>↓</button>
        <button class="btn btn-small" data-act="drop" ${d.channels.length > 1 ? '' : 'disabled title="A profile needs a channel"'}>Remove</button></td></tr>`).join('') +
    `</tbody></table>
    <div class="btn-row"><button class="btn" data-act="addch">Add a channel</button><span class="spacer"></span>
      <button class="btn" data-act="cancel">Cancel</button><button class="btn btn-primary" data-act="save">Save profile</button></div></div>`;
}

function soTypeHighlight(t) { return t === 'dimmer' ? 128 : ['red', 'green', 'blue'].includes(t) ? 255 : -1; }

function soEdit(p, copy) {
  soDraft = p ? { was: copy ? '' : p.name, name: copy ? p.name + ' copy' : p.name, channels: p.channels.map(c => ({ ...c })) }
    : { was: '', name: '', channels: [{ name: 'Dimmer', type: 'dimmer', default: 0, highlight: 128 }] };
  soEls.profileNote.textContent = '';
  soDrawEditor();
}

soEls.editor.addEventListener('input', ev => {
  if (!soDraft) return;
  if (ev.target.id === 'soDraftName') { soDraft.name = ev.target.value; return; }
  const tr = ev.target.closest('tr[data-i]'), f = ev.target.dataset.f;
  if (!tr || !f) return;
  const c = soDraft.channels[Number(tr.dataset.i)];
  if (f === 'default') c.default = /^\d+$/.test(ev.target.value.trim()) ? Number(ev.target.value) : -1;
  else if (f === 'highlight') { const v = ev.target.value.trim(); c.highlight = v === '' ? -1 : /^\d+$/.test(v) ? Number(v) : 999; }
  else {
    // Picking a type names a channel still called by its old type or its number.
    if (f === 'type' && (!c.name || /^Ch \d+$/.test(c.name) || c.name.toLowerCase() === c.type)) {
      c.name = ev.target.value[0].toUpperCase() + ev.target.value.slice(1);
      tr.querySelector('[data-f=name]').value = c.name;
    }
    if (f === 'type') {
      // The usual highlight level for the new type: dimmer at 50%, red, green and blue at full.
      c.highlight = soTypeHighlight(ev.target.value);
      tr.querySelector('[data-f=highlight]').value = c.highlight >= 0 ? c.highlight : '';
    }
    c[f] = ev.target.value;
  }
});
soEls.editor.addEventListener('click', async ev => {
  const b = ev.target.closest('[data-act]');
  if (!b || b.disabled || !soDraft) return;
  const tr = b.closest('tr[data-i]'), i = tr ? Number(tr.dataset.i) : -1, ch = soDraft.channels, act = b.dataset.act;
  if (act === 'addch') ch.push({ name: `Ch ${ch.length + 1}`, type: 'other', default: 0, highlight: -1 });
  else if (act === 'drop') ch.splice(i, 1);
  else if (act === 'up') [ch[i - 1], ch[i]] = [ch[i], ch[i - 1]];
  else if (act === 'down') [ch[i + 1], ch[i]] = [ch[i], ch[i + 1]];
  else if (act === 'cancel') soDraft = null;
  else if (act === 'save') {
    try {
      const name = soDraft.name.trim();
      soApply(await soPost('profiles/save', { name, channels: ch }));
      soDraft = null;
      soEls.profileNote.textContent = `› Saved “${name}”.`;
    } catch (e) { soEls.profileNote.textContent = '› Not saved: ' + e.message; return; }
  }
  soDrawEditor();
});

soEls.profileList.addEventListener('click', async ev => {
  const b = ev.target.closest('[data-act]');
  if (!b || b.disabled) return;
  const p = so.profiles[Number(b.dataset.p)];
  if (!p) return;
  if (b.dataset.act === 'edit') soEdit(p, false);
  else if (b.dataset.act === 'copy') soEdit(p, true);
  else if (b.dataset.act === 'export') {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([JSON.stringify({ name: p.name, channels: p.channels }, null, 2) + '\n'], { type: 'application/json' }));
    a.download = p.name.replace(/[^\w .+-]+/g, '_') + '.sacn-profile.json';
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  } else if (b.dataset.act === 'delete') {
    try { soApply(await soPost('profiles/delete', { name: p.name })); soEls.profileNote.textContent = `› Deleted “${p.name}”.`; }
    catch (e) { soEls.profileNote.textContent = '› Not deleted: ' + e.message; }
  }
});
soEls.newProfile.addEventListener('click', () => soEdit(null));
soEls.importBtn.addEventListener('click', () => soEls.importFile.click());
soEls.importFile.addEventListener('change', async () => {
  const file = soEls.importFile.files[0];
  soEls.importFile.value = '';
  if (!file) return;
  try {
    const p = JSON.parse(await file.text());
    soApply(await soPost('profiles/save', { name: p.name, channels: p.channels }));
    soEls.profileNote.textContent = `› Imported “${String(p.name).trim()}” (${p.channels.length} channels).`;
  } catch (e) { soEls.profileNote.textContent = `› ${file.name} was not imported: ${e.message}`; }
});

// ─── Bar buttons and tabs ───────────────────────────────────────────────────
async function soCommand(path, body) {
  soError = '';
  try { soApply(await soPost(path, body)); } catch (e) { soError = e.message; soRender(); }
}
soEls.start.addEventListener('click', () => { if (!soEls.start.disabled) soCommand('start', { interface: soEls.iface.value, priority: Number(soEls.priority.value), name: soEls.name.value }); });
soEls.stop.addEventListener('click', () => { if (!soEls.stop.disabled) soCommand('stop'); });
soEls.clear.addEventListener('click', () => { if (!soEls.clear.disabled) { soHold.clear(); soCommand('clear'); } });
soEls.priority.addEventListener('input', soRender);
soEls.iface.addEventListener('change', soRender);
document.querySelector('.so-tabs').addEventListener('click', ev => {
  const t = ev.target.closest('.so-tab');
  if (!t) return;
  soTab = t.dataset.so;
  save('commission.soTab', soTab);
  soRender();
});
