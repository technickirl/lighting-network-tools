'use strict';
// Luminex IP Setup — browser side. All state comes from the server over one
// EventSource; the page renders it. History and the numbering setup live in localStorage.

const $ = id => { const el = document.getElementById(id); if (!el) throw new Error(`Missing element #${id}`); return el; };
const els = {
  version: $('version'), wordmark: $('wordmark'), toolTitle: $('toolTitle'), dryBadge: $('dryBadge'), connPill: $('connPill'), banner: $('banner'),
  schemeRows: $('schemeRows'), scanStatus: $('scanStatus'), deviceArea: $('deviceArea'),
  ignoreClash: $('ignoreClash'), optIp: $('optIp'), renumberAll: $('renumberAll'), btnFixDup: $('btnFixDup'), numberingMethod: $('numberingMethod'), allOctetInput: $('allOctetInput'), methodNote: $('methodNote'), btnSaveNumbering: $('btnSaveNumbering'), btnLoadNumbering: $('btnLoadNumbering'), numberingFile: $('numberingFile'), btnScan: $('btnScan'), btnApply: $('btnApply'), btnReset: $('btnReset'), keepProfiles: $('keepProfiles'), keepIp: $('keepIp'),
  summary: $('summary'), logLine: $('logLine'), netInfo: $('netInfo'), historyList: $('historyList'), btnClearHistory: $('btnClearHistory'),
  confirmModal: $('confirmModal'), confirmCount: $('confirmCount'), confirmProfiles: $('confirmProfiles'), confirmIp: $('confirmIp'), confirmList: $('confirmList'),
  confirmGen2Note: $('confirmGen2Note'), btnConfirmCancel: $('btnConfirmCancel'), btnConfirmGo: $('btnConfirmGo'),
  passwordModal: $('passwordModal'), passwordList: $('passwordList'), pwUser: $('pwUser'), pwPass: $('pwPass'), pwAll: $('pwAll'),
  btnPwSkip: $('btnPwSkip'), btnPwGo: $('btnPwGo'),
  btnTopo: $('btnTopo'), comboNote: $('comboNote'), maskSelect: $('maskSelect'), maskNote: $('maskNote'),
  prefixA: $('prefixA'), prefixB: $('prefixB'), prefixPreset: $('prefixPreset'), prefixNote: $('prefixNote'), addrForm: $('addrForm'),
};

const viewEls = {
  tabs: $('viewTabs'), subTabs: $('subTabs'), configure: $('configureView'), node: $('nodeView'), address: $('addressView'), groups: $('groupsView'), fixtures: $('fixturesView'), sacn: $('sacnView'), sacnout: $('sacnOutView'),
  list: $('configureList'), status: $('configScanStatus'), scan: $('btnScanConfigure'), log: $('configLog'),
};


// One row per device type. Each type is either Paired (switch and node share a last octet, numbered
// along the cabling) or Sequence (counts up 1, 2, 3 on its own third octet); see combo.js.
const TYPES = [
  { key: 'gigacore10',  label: 'GigaCore 10',    dot: 'dot-switch', role: 'switch', mode: 'paired',   third: 11, prefix: 'Switch' },
  { key: 'gigacore12',  label: 'GigaCore 12',    dot: 'dot-switch', role: 'switch', mode: 'sequence', third: 11, prefix: '12p Switch' },
  { key: 'gigacore',    label: 'Other GigaCore', dot: 'dot-switch', role: 'switch', mode: 'sequence', third: 11, prefix: 'Switch' },
  { key: 'luminode4',   label: 'LumiNode 4',     dot: 'dot-node4',  role: 'node',   mode: 'paired',   third: 4,  prefix: 'Node' },
  { key: 'luminode12',  label: 'LumiNode 12',    dot: 'dot-node12', role: 'node',   mode: 'sequence', third: 12, prefix: '12p Node' },
  { key: 'lumicore',    label: 'LumiCore',       dot: 'dot-node4',  role: 'node',   mode: 'sequence', third: 10, prefix: 'Node' },
];
const TYPE = Object.fromEntries(TYPES.map(t => [t.key, t]));
const MASKS = ['255.0.0.0', '255.255.0.0', '255.255.255.0'], GATEWAY = '0.0.0.0';

let state = null;
let netmask = MASKS.includes(load('iptop.netmask', '')) ? load('iptop.netmask', '') : '255.255.0.0';   // one mask for every device addressed
// The first two octets of every address: 192.168 (the usual), 172.16, 10.0, 2.0… The third octet is per type and the last is numbered.
let netPrefix = Combo.validPrefix(load('iptop.prefix', '')) ? load('iptop.prefix', '') : '192.168';
let history = load('iptop.history', []);
const legacyThirds = load('iptop.scheme', {});      // third octets saved before the numbering table; still used until one is set there
const typeCfg = load('iptop.types', {});           // type key -> {mode, third, start}: only what has been changed
const namePrefixes = load('iptop.namePrefixes', {});   // type key -> name prefix ("12p Switch")
let comboOrder = load('iptop.comboOrder', []);     // serials the user has moved, in order
let excluded = new Set();                          // device ids unticked by the user; the server keeps them for the session
const tickPending = new Map();                     // id -> ticked, for clicks the server has not confirmed yet
let handledJob = load('iptop.handledJob', null);   // last job id folded into history
let pwPromptedKey = null, pwDismissed = new Set();
let plan = null;                                   // current numbering result (combo.js)
let comboRun = null;                               // while applying: the address phases still to send
let applyRun = null;                               // one Apply press: the settings still to send once addresses are done
let method = load('iptop.method', 'cabling') === 'simple' ? 'simple' : 'cabling';   // follow the cabling, or one 4th octet per batch
let batchOctet = Number.isInteger(load('iptop.octet', 1)) && load('iptop.octet', 1) >= 1 && load('iptop.octet', 1) <= 254 ? load('iptop.octet', 1) : 1;
let earlierPhases = [];                            // what the nodes' job reported: the switches' job replaces it on the server
let renumberAll = false;                           // ignore the addresses devices already have and number the whole chain in order
let schemeDirty = false;                           // the types table must be redrawn even though a field in it has focus

function load(k, def) { try { const v = localStorage.getItem(k); return v ? JSON.parse(v) : def; } catch { return def; } }
function save(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch {} }
function esc(s) { return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }

// ─── Server connection ──────────────────────────────────────────────────────
let pageVersion = null; // the app's version when this page loaded
function connect() {
  const es = new EventSource('/api/events');
  es.addEventListener('state', ev => {
    state = JSON.parse(ev.data);
    // The app restarted as another release: reload, so the page runs its code.
    if (pageVersion && state.version && state.version !== pageVersion) { location.reload(); return; }
    pageVersion = pageVersion || state.version;
    render();
  });
  es.onopen = () => setConn('AGENT OK', 'ok');
  es.onerror = () => setConn('AGENT OFFLINE — is the terminal window still open?', 'err');
}
function setConn(t, c) { els.connPill.textContent = t; els.connPill.className = 'pill ' + c; }
async function post(url, body) {
  const r = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
  return j;
}
function log(msg) { els.logLine.textContent = viewEls.log.textContent = '› ' + msg; }

// ─── Numbering setup ────────────────────────────────────────────────────────
function typeOf(d) {
  if (d.family === 'gigacore') return /^GigaCore\s*10(?!\d)/i.test(d.model) ? TYPE.gigacore10 : /^GigaCore\s*12(?!\d)/i.test(d.model) ? TYPE.gigacore12 : TYPE.gigacore;
  if (d.family === 'lumicore') return TYPE.lumicore;
  if (d.family === 'luminode') return /12/.test(d.model) ? TYPE.luminode12 : TYPE.luminode4;
  return null;
}
const inRange = (v, lo, hi) => Number.isInteger(v) && v >= lo && v <= hi;
function cfgOf(t) {
  const c = typeCfg[t.key] || {}, old = parseInt(legacyThirds[t.role === 'switch' ? 'gigacore' : t.key]);
  return { mode: c.mode === 'paired' || c.mode === 'sequence' ? c.mode : t.mode, third: inRange(c.third, 0, 255) ? c.third : inRange(old, 0, 255) ? old : t.third, start: inRange(c.start, 1, 254) ? c.start : 1 };
}
function setCfg(key, patch) { typeCfg[key] = Object.assign({}, typeCfg[key], patch); save('iptop.types', typeCfg); schemeDirty = true; render(); }
function third(t) { return cfgOf(t).third; }
function prefixOf(t) { return t.key in namePrefixes ? namePrefixes[t.key] : t.prefix; }
function isSwitch(d) { return d.family === 'gigacore'; }
const canIdentify = d => d.family === 'gigacore' || d.apiKind === 'luminode';   // GigaCore (Gen1 / Gen2) and LumiNode / LumiCore
function newIpFor(d) { return (plan && plan.ips[d.serial]) || ''; }
const presentTypes = () => new Set((state?.devices || []).map(d => typeOf(d)?.key));
// Paired types share one first number: the first paired type in the table (preferring ones that are present).
function leadPaired() {
  const present = presentTypes(), paired = TYPES.filter(t => cfgOf(t).mode === 'paired');
  return paired.find(t => present.has(t.key)) || paired[0] || null;
}
const pairedStart = () => { const t = leadPaired(); return t ? cfgOf(t).start : 1; };

// Recomputed from the latest scan + topology, but frozen while addresses are being sent
// (the devices change address under us, the numbering must not).
function updatePlan() {
  if (comboRun || applyRun || jobActive()) return;
  const devs = (state.devices || []).filter(d => d.serial && typeOf(d));
  if (method === 'simple') {
    const given = new Map(history.filter(b => !b.dry).flatMap(b => b.devices.map(d => [d.serial, d.ip])));   // the last batch stays put if it is still plugged in
    plan = Combo.simplePlan({ devices: devs.map(d => ({ serial: d.serial, type: typeOf(d).key, ip: d.ip })), cfg: Object.fromEntries(TYPES.map(t => [t.key, cfgOf(t)])), octet: batchOctet, prefix: netPrefix, ticked: new Set(selectedDevices().map(d => d.serial)), keep: given });
    return;
  }
  plan = Combo.buildPlan({
    devices: devs.map(d => ({ serial: d.serial, role: typeOf(d).role, type: typeOf(d).key, ip: d.ip })),
    links: ((state.topology || {}).links || []).filter(l => l.to),
    cfg: Object.fromEntries(TYPES.map(t => [t.key, cfgOf(t)])), prefix: netPrefix,
    pairedStart: pairedStart(), order: comboOrder, renumber: renumberAll, ticked: new Set(selectedDevices().map(d => d.serial)),
    reserved: history.filter(b => !b.dry).flatMap(b => b.devices.map(d => d.ip)),   // batches that have since been unplugged
  });
}

function renderScheme() {
  if (!schemeDirty && els.schemeRows.contains(document.activeElement)) return;   // do not pull the field out from under the user
  schemeDirty = false;
  const present = presentTypes(), lead = leadPaired(), ps = pairedStart();
  els.schemeRows.innerHTML = TYPES.map(t => {
    const c = cfgOf(t), paired = c.mode === 'paired', follows = paired && lead && lead !== t, start = paired ? ps : c.start;
    const unused = method === 'simple' ? 'disabled title="Not used when each batch gets one 4th octet"' : '';
    const offCell = `class="col-off"${method === 'simple' ? ' title="Not used when each batch gets one 4th octet"' : ''}`;
    return `<tr class="${present.has(t.key) ? '' : 'dim'}">
    <td><span class="device-dot ${t.dot}" style="display:inline-block;margin-right:6px"></span>${t.label}</td>
    <td ${offCell}><select data-mode="${t.key}" ${unused || 'title="Paired: shares a last octet with its cabled device. Sequence: counts up alone."'}><option value="paired" ${paired ? 'selected' : ''}>Paired</option><option value="sequence" ${paired ? '' : 'selected'}>Sequence</option></select></td>
    <td><input type="number" min="0" max="255" data-key="${t.key}" value="${c.third}"></td>
    <td ${offCell}><input type="number" min="1" max="254" data-start="${t.key}" value="${start}" ${unused || (follows ? 'disabled title="Paired types share the first paired type\'s start"' : '')}></td>
    <td><input type="text" maxlength="12" data-prefix="${t.key}" value="${esc(prefixOf(t))}"></td>
    <td class="new-ip" title="Next new device of this type. Counts the Addressed list; clear it to restart.">${netPrefix}.${c.third}.${plan && plan.next ? plan.next[t.key] : start}</td></tr>`;
  }).join('');
  for (const i of els.schemeRows.querySelectorAll('input[data-key]')) i.addEventListener('change', () => setCfg(i.dataset.key, { third: parseInt(i.value) }));
  for (const i of els.schemeRows.querySelectorAll('input[data-start]')) i.addEventListener('change', () => setCfg(i.dataset.start, { start: parseInt(i.value) }));
  for (const i of els.schemeRows.querySelectorAll('select[data-mode]')) i.addEventListener('change', () => setCfg(i.dataset.mode, { mode: i.value }));
  for (const i of els.schemeRows.querySelectorAll('input[data-prefix]')) i.addEventListener('change', () => { namePrefixes[i.dataset.prefix] = i.value; save('iptop.namePrefixes', namePrefixes); schemeDirty = true; render(); });
}

els.renumberAll.addEventListener('change', () => { renumberAll = els.renumberAll.checked; schemeDirty = true; render(); });
// How the numbers are chosen: from the cabling and the table, or one 4th octet per batch with no cabling at all.
els.numberingMethod.value = method;
els.allOctetInput.value = batchOctet;
els.numberingMethod.addEventListener('change', () => { method = els.numberingMethod.value; save('iptop.method', method); schemeDirty = true; render(); });
els.allOctetInput.addEventListener('input', () => {
  const n = parseInt(els.allOctetInput.value);
  if (n >= 1 && n <= 254) { batchOctet = n; save('iptop.octet', n); schemeDirty = true; }
  render();
});

// Save the numbering table to a file, and load one back, to move it between computers or keep one per rig.
els.btnSaveNumbering.addEventListener('click', () => {
  const body = { kind: 'luminex-numbering', version: 1, prefix: netPrefix, netmask, method, nameStyle: window.Settings.style(),
    types: Object.fromEntries(TYPES.map(t => [t.key, cfgOf(t)])), namePrefixes: Object.fromEntries(TYPES.map(t => [t.key, prefixOf(t)])) };
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([JSON.stringify(body, null, 2) + '\n'], { type: 'application/json' }));
  a.download = 'luminex-numbering.json';
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  log('Saved the numbering table as luminex-numbering.json.');
});
els.btnLoadNumbering.addEventListener('click', () => els.numberingFile.click());
els.numberingFile.addEventListener('change', async () => {
  const file = els.numberingFile.files[0];
  els.numberingFile.value = '';
  if (!file) return;
  try {
    const f = Combo.readNumberingFile(await file.text(), TYPES.map(t => t.key), MASKS);
    for (const k of Object.keys(typeCfg)) delete typeCfg[k];
    Object.assign(typeCfg, f.types); save('iptop.types', typeCfg);
    Object.assign(namePrefixes, f.namePrefixes); save('iptop.namePrefixes', namePrefixes);
    if (f.prefix) { netPrefix = f.prefix; save('iptop.prefix', netPrefix); }
    if (f.netmask) { netmask = f.netmask; save('iptop.netmask', netmask); }
    if (f.nameStyle) window.Settings.setStyle(f.nameStyle);
    if (f.method) { method = f.method; els.numberingMethod.value = method; save('iptop.method', method); }
    schemeDirty = true; render();
    log(`Loaded the numbering table from ${file.name}.`);
  } catch (e) { log(`Could not load ${file.name}: ${e.message}.`); }
});
els.btnTopo.addEventListener('click', () => { log('Reading topology…'); post('/api/topology').catch(e => log('Could not read topology: ' + e.message)); });

// Move one device (or combo, by its switch) up or down within its own group; the order is remembered.
function moveTo(serials, from, to) {
  if (from === to || from < 0 || to < 0 || from >= serials.length || to >= serials.length) return;
  const g = [...serials];
  g.splice(to, 0, g.splice(from, 1)[0]);
  comboOrder = [...comboOrder.filter(s => !g.includes(s)), ...g];
  save('iptop.comboOrder', comboOrder); render();
}
function moveInGroup(serials, i, dir) { moveTo(serials, i, i + dir); }

// How the device list is drawn: detailed (the original rows), compact (one line each) or tiles (small cards).
let listView = ['detailed', 'compact', 'tiles'].includes(load('iptop.listView', 'detailed')) ? load('iptop.listView', 'detailed') : 'detailed';
// Below this width the one-line-per-device grid no longer fits, so Detailed and Compact rows wrap like tile rows.
let narrow = false;
function applyView() {
  els.deviceArea.className = 'view-' + listView + (narrow ? ' narrow' : '');
  for (const b of document.querySelectorAll('#viewToggle button')) b.classList.toggle('on', b.dataset.view === listView);
}
for (const b of document.querySelectorAll('#viewToggle button')) b.addEventListener('click', () => { listView = b.dataset.view; save('iptop.listView', listView); applyView(); });
applyView();
if (window.ResizeObserver) new ResizeObserver(([e]) => {
  const n = e.contentRect.width < 940;
  if (n !== narrow) { narrow = n; applyView(); }
}).observe(els.deviceArea);

// Drag and drop reordering: the same ordering as the arrows, which stay. Only a combo header or a device that is
// still to be numbered has a handle, and a drop only counts within the same group.
let drag = null;
const freshComboSerials = () => (plan?.combos || []).filter(c => !c.kept).map(c => c.sw);
const freshSeqSerials = type => { const g = (plan?.sequences || []).find(x => x.type === type); return g ? g.devices.filter(x => !x.kept).map(x => x.serial) : []; };
function setupDrag() {
  const area = els.deviceArea;
  const clear = () => area.querySelectorAll('.drop-before, .drop-after, .dragging').forEach(e => e.classList.remove('drop-before', 'drop-after', 'dragging'));
  for (const h of area.querySelectorAll('.drag-handle')) {
    h.addEventListener('dragstart', e => {
      drag = { kind: h.dataset.drag, key: h.dataset.gkey || '', serial: h.dataset.serial };
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', 'luminex-reorder');
      const box = h.closest(drag.kind === 'combo' ? '.device-group' : '.device-row');
      if (box) { if (e.dataTransfer.setDragImage) e.dataTransfer.setDragImage(box, 12, 12); box.classList.add('dragging'); }
    });
    h.addEventListener('dragend', () => { drag = null; render(); });
  }
  for (const t of area.querySelectorAll('[data-drop]')) {
    const mine = () => drag && drag.kind === t.dataset.drop && drag.key === (t.dataset.gkey || '');
    const after = e => { const r = t.getBoundingClientRect(); return listView === 'tiles' ? e.clientX > r.left + r.width / 2 : e.clientY > r.top + r.height / 2; };
    t.addEventListener('dragover', e => {
      if (!mine()) return;
      e.preventDefault(); e.dataTransfer.dropEffect = 'move';
      area.querySelectorAll('.drop-before, .drop-after').forEach(x => x.classList.remove('drop-before', 'drop-after'));
      t.classList.add(after(e) ? 'drop-after' : 'drop-before');
    });
    t.addEventListener('dragleave', e => { if (!t.contains(e.relatedTarget)) t.classList.remove('drop-before', 'drop-after'); });
    t.addEventListener('drop', e => {
      if (!mine()) return;
      e.preventDefault();
      // Read the order now and go by serial: the rows on screen are frozen during a drag, but a scan can still change the plan.
      const list = drag.kind === 'combo' ? freshComboSerials() : freshSeqSerials(drag.key);
      const from = list.indexOf(drag.serial), on = list.indexOf(t.dataset.serial);
      clear();
      if (jobActive() || comboRun || applyRun || from < 0 || on < 0) return;
      const to = on + (after(e) ? 1 : 0);
      moveTo(list, from, from < to ? to - 1 : to);
    });
  }
}
// Devices already on a planned address keep their number, so the arrows only reorder the ones still to be numbered
// (everything, once "Renumber all" is ticked).
function moveCombo(i, dir) { moveInGroup(plan.combos.filter(c => !c.kept).map(c => c.sw), i, dir); }
function moveSeq(type, i, dir) { const g = plan.sequences.find(x => x.type === type); if (g) moveInGroup(g.devices.filter(x => !x.kept).map(x => x.serial), i, dir); }

// The first two octets: two number boxes and a list of the usual choices.
const PRESETS = ['192.168', '172.16', '10.0', '2.0'];
function setPrefix(p) { if (!Combo.validPrefix(p) || p === netPrefix) return; netPrefix = p; save('iptop.prefix', p); schemeDirty = true; render(); }
for (const el of [els.prefixA, els.prefixB]) el.addEventListener('input', () => setPrefix(`${parseInt(els.prefixA.value, 10)}.${parseInt(els.prefixB.value, 10)}`));
els.prefixPreset.addEventListener('change', () => { if (els.prefixPreset.value) setPrefix(els.prefixPreset.value); });
function renderPrefix() {
  const busy = jobActive() || !!comboRun || !!applyRun;
  for (const el of [els.prefixA, els.prefixB, els.prefixPreset]) el.disabled = busy;
  if (document.activeElement !== els.prefixA) els.prefixA.value = netPrefix.split('.')[0];
  if (document.activeElement !== els.prefixB) els.prefixB.value = netPrefix.split('.')[1];
  els.prefixPreset.value = PRESETS.includes(netPrefix) ? netPrefix : '';
  const ok = Combo.validPrefix(`${parseInt(els.prefixA.value, 10)}.${parseInt(els.prefixB.value, 10)}`);
  els.prefixA.classList.toggle('bad', !ok); els.prefixB.classList.toggle('bad', !ok);
  els.addrForm.textContent = `${netPrefix}.<third>.<octet>`;
  // This computer has to be on the new network to reach the devices once they move (the mask decides how much of it has to match).
  const notes = [];
  if (!ok) notes.push('Use a first octet from 1 to 223 (not 127) and a second from 0 to 255.');
  else if (netmask !== '255.255.255.0') {
    const num = s => s.split('.').reduce((n, o) => n * 256 + (+o), 0), m = num(netmask);
    const mine = (state?.interfaces || []).map(i => i.address).filter(Boolean);
    if (mine.length && !mine.some(ad => ((num(ad) & m) >>> 0) === ((num(netPrefix + '.0.0') & m) >>> 0))) {
      notes.push(`This computer has no address on ${netPrefix}.x.x yet (it has ${mine.join(', ')}). Give it one such as ${netPrefix}.0.10, or the devices will not be reachable once they move.`);
    }
  }
  els.prefixNote.innerHTML = notes.map(esc).join('<br>');
  els.prefixNote.classList.toggle('hidden', !notes.length);
}
els.maskSelect.addEventListener('change', () => { netmask = els.maskSelect.value; save('iptop.netmask', netmask); render(); });
// Different third octets are different subnets under /24, and this computer must share one to reach them.
function renderMask() {
  els.maskSelect.value = netmask; els.maskSelect.disabled = jobActive() || !!comboRun || !!applyRun;
  const thirds = [...new Set([...presentTypes()].filter(Boolean).map(k => third(TYPE[k])))];
  const notes = [];
  if (netmask === '255.255.255.0' && thirds.length > 1) notes.push(`/24: third octets ${thirds.join(' and ')} are separate subnets and need a router.`);
  if (netmask !== '255.255.0.0') notes.push(`This computer needs an address in the new subnet (has ${(state.interfaces || []).map(i => i.address + '/' + i.prefix).join(', ') || 'none'}).`);
  els.maskNote.innerHTML = notes.map(esc).join('<br>');
  els.maskNote.classList.toggle('hidden', !notes.length);
}

// Topology status and any problems with the plan, shown under the types table.
function renderTopology() {
  const simple = method === 'simple';
  els.btnTopo.disabled = simple || jobActive() || !!comboRun || !!applyRun || state.readingTopology;
  els.btnTopo.title = simple ? 'Not used with one 4th octet per batch' : '';
  const topo = simple ? null : state.topology, notes = [];
  if (!simple) {
    if (state.readingTopology) notes.push('Reading topology…');
    else if (!topo) notes.push('Topology not read. Scan reads it.');
    else if (!topo.links.length) notes.push('No neighbours found: numbering by serial.');
    if (topo && topo.unread.length) notes.push(`Could not read: ${topo.unread.map(esc).join('; ')}`);
  }
  if (plan) {
    notes.push(...plan.warnings.map(esc));
    const name = sn => (state.devices.find(d => d.serial === sn) || {}).name || sn;
    notes.push(...plan.clashes.map(c => `${esc(c.ip)} goes to ${c.serials.map(s => esc(name(s))).join(' and ')}: ${simple ? 'untick one or change a third octet' : 'change a third octet'}.`));
  }
  els.comboNote.innerHTML = notes.join('<br>');
  els.comboNote.classList.toggle('warn', !!(topo && topo.unread.length) || !!(plan && (plan.warnings.length || plan.clashes.length)));
}

// ─── Rendering ──────────────────────────────────────────────────────────────
function jobItem(id) { return state?.job?.items.find(i => i.id === id); }
// A row's result from this Apply, whichever of its phases the device was in.
function rowJobItem(id) { return jobItem(id) || earlierPhases.find(i => i.id === id); }
function jobActive() { return !!(state?.job && !state.job.done); }
function usable(d) { return d.reachable && !!d.apiKind && !!typeOf(d); }
function rows() {
  const list = [...(state.devices || [])];
  if (state.job) for (const it of state.job.items) if (!list.find(d => d.id === it.id)) list.push({ ...it.device, _ghost: true });
  return list;
}
// ─── Configure (only in a tool that mounts the node configure page) ─────────
function ipNumber(ip) { return ip.split('.').reduce((n, o) => n * 256 + Number(o), 0); }
function inSubnet(ip, address, prefix) {
  const size = 2 ** (32 - prefix);
  return Math.floor(ipNumber(ip) / size) === Math.floor(ipNumber(address) / size);
}
function canReach(ip) { return (state.interfaces || []).some(i => inSubnet(ip, i.address, i.prefix)); }
function inRunningJob(id) { const it = jobItem(id); return !!it && ['queued', 'checking', 'sending', 'rebooting'].includes(it.status); }
function configureButton(id, ip, why, cls = '') {
  if (!state.configureUrl) return '';
  const title = why || `Open ${ip} on the Node tab`;
  return `<button class="btn btn-configure ${cls}" data-configure-id="${esc(id)}" data-configure-ip="${esc(ip)}" title="${esc(title)}" ${why ? 'disabled' : ''}>Configure</button>`;
}
function configureWhyNot(apiKind, id, ip) {
  if (apiKind !== 'luminode') return 'Configure is for LumiNode and LumiCore.';
  if ((state.devices || []).some(d => d.id === id && d.sharedIp)) return 'Address shared. Fix that first.';
  if (inRunningJob(id)) return 'In a running job.';
  if (!ip) return 'No address reported.';
  if (!canReach(ip)) return `No adapter on ${ip}'s subnet.`;
  return '';
}
function rowConfigure(d, it, cls = '') {
  const ip = (it && it.status === 'done' && it.newIp) || d.ip;
  return configureButton(d.id, ip, configureWhyNot(d.apiKind, d.id, ip), cls);
}
// The Addressed list outlives the scan: a batch that has been swapped out keeps
// its button, at the last address known for it.
function historyConfigure(b, h) {
  const live = (state.devices || []).find(d => d.id === h.id);
  const ip = live ? live.ip : h.ip;
  const why = b.dry ? 'Dry run: this address was not applied.' : configureWhyNot(h.apiKind, h.id, ip);
  return configureButton(h.id, ip, why, 'btn-configure-small');
}
function onConfigureClick(ev) {
  const b = ev.target.closest('[data-configure-ip]');
  if (!b || b.disabled) return;
  openNode(b.dataset.configureIp, b.dataset.configureId); // node.js
}

// ─── Views ──────────────────────────────────────────────────────────────────
// A tool with a configure page opens on Devices, All devices, which has no ticks,
// no Apply and no Factory reset, so a running system cannot be re-addressed from
// it by a stray click. A reload stays on the same view; a new tab starts on it
// (Devices, All devices).
// Four views. Devices and sACN have parts, each one of the older views; the page
// remembers the part each was on.
const VIEW_GROUPS = { devices: ['configure', 'address', 'groups'], node: ['node'], fixtures: ['fixtures'], sacn: ['sacn', 'sacnout'] };
const groupOf = v => Object.keys(VIEW_GROUPS).find(g => VIEW_GROUPS[g].includes(v)) || 'devices';
let view = 'configure';
let groupPart = {}; // group → the part it was last on
try {
  const v = sessionStorage.getItem('commission.view');
  if (Object.values(VIEW_GROUPS).flat().includes(v)) view = v;
  groupPart = JSON.parse(sessionStorage.getItem('commission.parts') || '{}') || {};
} catch {}
function setView(v) {
  view = v;
  groupPart[groupOf(v)] = v;
  try { sessionStorage.setItem('commission.view', v); sessionStorage.setItem('commission.parts', JSON.stringify(groupPart)); } catch {}
}
function currentView() { return state.configureUrl ? view : 'address'; }
function renderView() {
  const v = currentView(), g = groupOf(v);
  viewEls.tabs.classList.toggle('hidden', !state.configureUrl);
  viewEls.configure.classList.toggle('hidden', v !== 'configure');
  viewEls.node.classList.toggle('hidden', v !== 'node');
  if (typeof renderNode === 'function') renderNode();
  viewEls.address.classList.toggle('hidden', v !== 'address');
  viewEls.groups.classList.toggle('hidden', v !== 'groups');
  viewEls.fixtures.classList.toggle('hidden', v !== 'fixtures');
  if (v === 'fixtures' && typeof renderFixtures === 'function') renderFixtures();
  viewEls.sacn.classList.toggle('hidden', v !== 'sacn');
  if (typeof sacnShown === 'function') sacnShown(v === 'sacn');
  viewEls.sacnout.classList.toggle('hidden', v !== 'sacnout');
  if (typeof sacnOutShown === 'function') sacnOutShown(v === 'sacnout');
  for (const t of viewEls.tabs.querySelectorAll('.view-tab')) t.classList.toggle('active', t.dataset.group === g);
  for (const t of viewEls.tabs.querySelectorAll('.sub-tab')) {
    t.classList.toggle('hidden', t.dataset.in !== g);
    t.classList.toggle('active', t.dataset.view === v);
  }
  viewEls.subTabs.classList.toggle('hidden', VIEW_GROUPS[g].length < 2);
}
viewEls.tabs.addEventListener('click', ev => {
  const t = ev.target.closest('.view-tab, .sub-tab');
  if (!t) return;
  const parts = VIEW_GROUPS[t.dataset.group] || [];
  setView(t.dataset.view || (parts.includes(groupPart[t.dataset.group]) ? groupPart[t.dataset.group] : parts[0]));
  render();
});

function renderConfigureList() {
  viewEls.status.textContent = els.scanStatus.textContent;
  const list = rows().sort((a, b) => (b.apiKind === 'luminode') - (a.apiKind === 'luminode')); // nodes first
  if (!list.length) {
    viewEls.list.innerHTML = `<div class="empty-state">${state.scanning ? 'Scanning the network…' : 'No Luminex devices found yet. Click <strong>Scan</strong>.'}</div>`;
    return;
  }
  viewEls.list.innerHTML = `<div class="device-list">` + list.map(d => {
    const t = typeOf(d) || { label: d.typeLabel || d.model, dot: 'dot-unknown' };
    const it = jobItem(d.id);
    let note = '';
    if (inRunningJob(d.id)) note = `${esc(it.label)} — address or reset job running`;
    else if (d.sharedIp) note = `Shares ${esc(d.ip)}. Fix on Address &amp; reset.`;
    else if (!d.reachable) note = `Unreachable. ${esc(d.reachFix)}`;
    else if (d.reachWarning) note = esc(d.reachWarning);
    const gen = d.family === 'gigacore' ? d.typeLabel.replace('GigaCore ', '') : d.model;
    return `<div class="node-row">
      <div class="device-dot ${t.dot}"></div>
      <div class="device-type">${esc(t.label)}<small>${esc(gen)}</small></div>
      <div class="device-name"><b>${esc(d.name) || '(no name)'}</b> · <span class="mono">${esc(d.serial)}</span> · fw ${esc(d.firmware) || '?'}</div>
      <div class="device-ip">${esc(d.ip) || '?'}${d.prefix ? '/' + d.prefix : ''}</div>
      ${rowConfigure(d, it, 'btn-primary')}
      <div class="node-status">${note}</div>
    </div>`;
  }).join('') + `</div>`;
}

function syncTicks() {
  excluded = new Set(state.unticked || []);
  for (const [id, ticked] of tickPending) ticked ? excluded.delete(id) : excluded.add(id);
}
async function setTicked(id, ticked) {
  tickPending.set(id, ticked);
  render();
  try { await post(`/api/device/${encodeURIComponent(id)}/ticked`, { ticked }); }
  catch (e) { log('Could not save that tick: ' + e.message); }
  tickPending.delete(id);
  render();
}
function selectedDevices() { return rows().filter(d => usable(d) && !d._ghost && !excluded.has(d.id)); }

function render() {
  if (!state) return;
  syncTicks();
  els.version.textContent = 'v' + state.version;
  if (state.title) { document.title = state.title; els.wordmark.firstChild.textContent = state.title + ' '; els.toolTitle.textContent = ''; }
  els.dryBadge.classList.toggle('hidden', !state.dryRun);
  els.scanStatus.textContent = state.scanning ? '— scanning…' : state.lastScanAt ? `— ${state.devices.length} device(s), ${new Date(state.lastScanAt).toLocaleTimeString()}` : '';
  renderView();
  updatePlan();
  renderMask();
  renderPrefix();
  renderTopology();
  renderScheme();
  renderBanner();
  renderDevices();
  renderConfigureList();
  renderButtons();
  renderHistory();
  renderAddDevice();
  foldFinishedJob();
  maybePromptPassword();
  els.netInfo.innerHTML = `This computer: ${(state.interfaces || []).map(i => `<code>${esc(i.name)} ${esc(i.address)}/${i.prefix}</code>`).join(' · ') || '<em>no addresses</em>'} · discovery: <code>${esc(state.discovery.backend)}</code>`;
}


// 2.x.x.x/8 is the factory-default network of many Luminex devices, not all, so a reset can
// land a device there and "unreachable" is expected, not a fault.
function factoryDefault(d) { return !!d.ip && d.ip.startsWith('2.') && (!d.prefix || d.prefix === 8); }
// state.linkLost = addresses this computer had the last time a device was reachable
// and does not have now. macOS drops a link-down port's addresses, so this is
// what a rebooting device or switch on the other end of the cable looks like.
function linkDownNote(n, lost) {
  const was = lost.map(i => `<code>${esc(i.address)}/${i.prefix}</code> on ${esc(i.name)}`).join(', ');
  return `<strong>Network link down.</strong> Lost ${was}; ${n} device(s) unreachable. If its switch is rebooting, wait, then Scan. Now: ${hostAddresses()}.`;
}
function hostAddresses() { return state.interfaces.map(i => `<code>${esc(i.address)}/${i.prefix}</code>`).join(', ') || '<em>none</em>'; }
function factoryNote(n, verb) {
  return `<strong>${n} device(s) ${verb} on 2.x.x.x/8.</strong> Add <code>2.0.0.250 / 255.0.0.0</code> to this computer's adapter, then Scan. Now: ${hostAddresses()}.`;
}

function renderBanner() {
  const b = els.banner;
  const unreachable = (state.devices || []).filter(d => !d.reachable && !factoryDefault(d));
  const factory = (state.devices || []).filter(d => !d.reachable && factoryDefault(d));
  const unknown = (state.devices || []).filter(d => !d.apiKind || !typeOf(d));
  const lost = state.linkLost || [];
  if (state.scanError) { b.className = 'banner err'; b.innerHTML = `<strong>Scan failed:</strong> ${esc(state.scanError)}`; }
  else if ((state.ownClashes || []).length) {
    b.className = 'banner err';
    b.innerHTML = state.ownClashes.map(c => `<strong>${esc(c.name || 'A device')} is on ${esc(c.ip)}, the same address as this computer (${esc(c.interface)}).</strong>`).join('<br>') +
      ` It can't be reached and may not be listed. Change this computer's address, then Scan.`;
  }
  else if (state.devices.some(d => d.sharedIp)) {
    const shared = state.devices.filter(d => d.sharedIp), stuck = shared.filter(d => !d.linkRoute);
    b.className = 'banner err';
    b.innerHTML = `<strong>${shared.length} devices share ${[...new Set(shared.map(d => esc(d.ip)))].join(', ')}.</strong> ` +
      (shared.length > stuck.length ? `Apply settings or Fix duplicates moves them over IPv6. A node's active profile name may clear. ` : '') +
      (stuck.length ? `${stuck.map(d => esc(d.name || d.serial)).join(', ')}: no IPv6 answer. Connect on its own, then Scan.` : '');
  }
  else if (!state.scanning && state.lastScanAt && !state.devices.length) {
    b.className = 'banner';
    b.innerHTML = `<strong>No Luminex devices found.</strong> Check power and cabling, then Scan.` +
      (state.discovery.platform === 'win32' ? ' Windows: set the network to <em>Private</em> and allow UDP 5353.' : '');
  } else if (unreachable.length && lost.length) {
    b.className = 'banner';
    b.innerHTML = linkDownNote(unreachable.length, lost);
  } else if (unreachable.length) {
    const u = unreachable[0];
    b.className = 'banner';
    b.innerHTML = `<strong>${unreachable.length} device(s) unreachable.</strong> ${esc(u.reachReason)} ${esc(u.reachFix)} This computer: ${hostAddresses()}.` +
      (factory.length ? ` ${factory.length} more on 2.x.x.x/8.` : '');
  } else if (factory.length) {
    b.className = 'banner info';
    b.innerHTML = factoryNote(factory.length, 'are');
  } else if (unknown.length) {
    b.className = 'banner';
    b.innerHTML = `<strong>${unknown.length} unknown device(s)</strong> will be skipped. Report the product_type from the row's mDNS details.`;
  } else { b.className = 'banner hidden'; return; }
  b.classList.remove('hidden');
}

// The list follows the plan: one group per combo (switch + its node) with reorder buttons in the header, then
// anything numbered after the combos, then one group per Sequence type with a reorder pair on each device.
function groupedRows(list) {
  if (!plan) return [{ rows: list }];
  if (plan.simple) return [{ title: 'This batch', sub: `ticked devices get .${batchOctet}, unless in an earlier batch`, rows: list }];
  const bySerial = new Map(list.filter(d => d.serial).map(d => [d.serial, d]));
  const used = new Set(), groups = [];
  const take = serials => serials.map(s => bySerial.get(s)).filter(Boolean).map(d => (used.add(d), d));
  const freshCombos = plan.combos.filter(c => !c.kept);
  plan.combos.forEach(c => groups.push({ sw: c.sw, idx: freshCombos.indexOf(c), last: freshCombos.length - 1, title: `Combo ${c.n}`, sub: [c.node ? '' : 'no node', c.kept ? 'keeps its number' : ''].filter(Boolean).join(' · '), rows: take([c.sw, c.node].filter(Boolean)) }));
  const loose = take(plan.extras.map(e => e.node));
  if (loose.length) groups.push({ title: 'Numbered after the combos', sub: [...new Set(plan.extras.map(e => e.reason))].join(', '), rows: loose });
  for (const g of plan.sequences) {
    const t = TYPE[g.type];
    groups.push({ title: `${t.label} — own numbering`, sub: `${netPrefix}.${third(t)}.n`, seq: g.type, fresh: g.devices.filter(x => !x.kept).map(x => x.serial), rows: take(g.devices.map(x => x.serial)) });
  }
  const rest = list.filter(d => !used.has(d));
  if (rest.length) groups.push({ title: 'Other devices', rows: rest });
  return groups;
}

function deviceRow(d, locked, seq) {
  const t = typeOf(d) || { label: d.typeLabel, dot: 'dot-unknown' };
  const it = rowJobItem(d.id);
  const ok = usable(d) && !d._ghost;
  const ticked = ok && !excluded.has(d.id);
  const S = window.Settings;
  const statusHtml = x => {
    const busy = ['queued', 'checking', 'sending', 'rebooting'].includes(x.status);
    return `<span class="status ${x.status} ${busy ? 'spin' : ''}">${x.prefix ? esc(x.prefix) + ': ' : ''}${esc(x.label)}</span>` + (x.detail ? `<div class="detail">${esc(x.detail)}</div>` : '');
  };
  let status = '';
  if (it) status = statusHtml(it);
  else if (d.sharedIp) status = `<span class="status ${d.linkRoute ? '' : 'failed'}">Shares its address</span><div class="detail">${d.linkRoute ? 'Via IPv6.' : 'No IPv6 answer. Connect on its own, then Scan.'}</div>`;
  else if (!d.reachable && factoryDefault(d)) status = `<span class="status">On 2.x.x.x/8</span><div class="detail">Add a 2.x address to this computer.</div>`;
  else if (!d.reachable && (state.linkLost || []).length) status = `<span class="status failed">Link down</span><div class="detail">See note above.</div>`;
  else if (!d.reachable) status = `<span class="status failed">Unreachable</span><div class="detail">${(state.ownClashes || []).some(c => c.ip === d.ip) ? "On this computer's address" : "Not on this subnet"} — see note above.</div>`;
  else if (!ok) status = `<span class="status failed">Unknown type</span><div class="detail">Will be skipped.</div>`;
  else if (d.reachWarning) status = `<div class="detail">${esc(d.reachWarning)}</div>`;
  if (S) for (const sr of S.items(d.id)) status += `<div class="settings-status">${statusHtml(sr)}</div>`;
  const txt = Object.entries(d.txt || {}).map(([k, v]) => `${k}=${v}`).join('\n');
  const gen = d.family === 'gigacore' ? `<small>${esc(d.typeLabel.replace('GigaCore ', ''))}${d.apiKind === 'gen2' && !state.gen2Verified ? ' · unverified' : ''}</small>` : `<small>${esc(d.model)}</small>`;
  const newName = S && ticked && S.naming() ? S.nameFor(d) : '';
  const nameCell = `<b>${esc(d.name) || '(no name)'}</b>` + (newName ? ` <span class="device-arrow">→</span> <span class="device-newip ${S.nameTooLong(d) ? 'bad' : ''}" title="${S.nameTooLong(d) ? 'Too long for this device' : 'Name it will get when you press Apply settings'}">${esc(newName)}</span>` : '');
  const unreachableShared = d.sharedIp && !d.linkRoute;   // the server refuses it too: the request could flash the wrong device
  const identify = ok && canIdentify(d) ? `<button class="btn btn-small" data-identify="${esc(d.id)}" ${locked || unreachableShared || (S && S.running()) ? 'disabled' : ''} title="${unreachableShared ? 'Address shared, no IPv6 answer: another device could flash. Connect on its own, then Scan.' : 'Flash this device'}">Identify</button>` : '';
  const actions = `<span class="row-actions">${rowConfigure(d, it) || '<span></span>'}${identify || '<span></span>'}</span>`;   // Configure then Identify, each in its own slot, always at the right of the row
  const arrows = seq && seq.i > -1 ? `<span class="seq-move move-end"><button data-seq="${seq.type}" data-i="${seq.i}" data-dir="-1" ${locked || seq.i === 0 ? 'disabled' : ''} title="Number this one earlier">↑</button><button data-seq="${seq.type}" data-i="${seq.i}" data-dir="1" ${locked || seq.i === seq.last ? 'disabled' : ''} title="Number this one later">↓</button></span>` : '';
  const canDrag = !locked && seq && seq.i > -1;
  const handle = canDrag ? `<span class="drag-handle" draggable="true" data-drag="seq" data-gkey="${esc(seq.type)}" data-serial="${esc(d.serial)}" title="Drag to change the order">⠿</span>` : '';
  const dropAttrs = seq && seq.i > -1 ? ` data-drop="seq" data-gkey="${esc(seq.type)}" data-serial="${esc(d.serial)}"` : '';
  const lead = handle || '<span class="drag-handle off"></span>', trail = arrows || '<span class="seq-move move-end"></span>';   // every row has the same cells, so the handle, Configure/Identify and arrows line up
  return `<div class="device-row ${ok ? '' : 'disabled'}"${dropAttrs}>
    ${lead}<input type="checkbox" data-id="${esc(d.id)}" ${ticked ? 'checked' : ''} ${ok && !locked ? '' : 'disabled'} title="${ok ? 'Include in this batch' : 'Cannot be addressed'}">
    <div class="device-dot ${t.dot}"></div>
    <div class="device-type">${esc(t.label)}${gen}</div>
    <div class="device-name" title="${esc(d.serial)}">${nameCell}<div class="muted"><span class="mono serial">${esc(d.serial)}</span><span class="fw"> · fw ${esc(d.firmware) || '?'}</span></div><details><summary>mDNS details</summary><pre class="txt">${esc(txt)}</pre></details></div>
    <div class="device-ip">${esc(d.ip) || '?'}${d.prefix ? '/' + d.prefix : ''}</div>
    <div class="device-arrow">→</div>
    <div class="device-newip">${it && !state.job.done && it.targetIp ? esc(it.targetIp) : (ticked ? esc(newIpFor(d)) || '—' : '<span class="muted">skipped</span>')}</div>
    ${actions}${trail}
    <div class="device-status">${status}</div>
  </div>`;
}

function renderDevices() {
  if (drag) return;   // a redraw would cancel the drag in progress; dragend redraws
  const list = rows();
  if (!list.length) {
    els.deviceArea.innerHTML = `<div class="empty-state">${state.scanning ? 'Scanning the network…' : 'Connect one switch (+ any attached nodes)<br>then click <strong>Scan</strong>'}</div>`;
    return;
  }
  const locked = jobActive() || !!comboRun || !!applyRun;
  const groups = groupedRows(list);
  els.deviceArea.innerHTML = groups.map(g => {
    const head = g.title ? `<div class="combo-head">${g.idx > -1 && !locked ? `<span class="drag-handle" draggable="true" data-drag="combo" data-serial="${esc(g.sw)}" title="Drag to change the order">⠿</span>` : ''}<span class="combo-num">${esc(g.title)}</span>${g.sub ? `<span class="muted">${esc(g.sub)}</span>` : ''}` +
      (g.idx > -1 ? `<span class="spacer"></span><button data-i="${g.idx}" data-dir="-1" ${locked || g.idx === 0 ? 'disabled' : ''} title="Move this combo up">↑</button><button data-i="${g.idx}" data-dir="1" ${locked || g.idx === g.last ? 'disabled' : ''} title="Move this combo down">↓</button>` : '') + `</div>` : '';
    return `<div class="device-group ${g.idx !== undefined ? 'combo-group' : ''}"${g.idx > -1 ? ` data-drop="combo" data-serial="${esc(g.sw)}"` : ''}>${head}<div class="device-list">${g.rows.map((d, i) => deviceRow(d, locked, g.seq ? { type: g.seq, i: g.fresh.indexOf(d.serial), last: g.fresh.length - 1 } : null)).join('')}</div></div>`;
  }).join('');
  for (const cb of els.deviceArea.querySelectorAll('input[type=checkbox]')) {
    cb.addEventListener('change', () => setTicked(cb.dataset.id, cb.checked));
  }
  for (const b of els.deviceArea.querySelectorAll('.combo-head button')) b.addEventListener('click', () => moveCombo(+b.dataset.i, +b.dataset.dir));
  for (const b of els.deviceArea.querySelectorAll('[data-seq]')) b.addEventListener('click', () => moveSeq(b.dataset.seq, +b.dataset.i, +b.dataset.dir));
  for (const b of els.deviceArea.querySelectorAll('[data-identify]')) b.addEventListener('click', () => window.Settings.identify(b.dataset.identify));
  setupDrag();
}

function renderButtons() {
  const sel = selectedDevices();
  const busy = jobActive() || state.scanning;
  els.btnScan.disabled = busy;
  els.btnScan.title = busy ? 'Busy' : '';
  const S = window.Settings, wantIp = els.optIp.checked, picked = S ? S.chosen().length : 0;
  const numbered = !wantIp || (!!plan && !plan.warnings.length && sel.every(d => plan.ips[d.serial]));
  // Paired numbering follows the cabling, so wait for it, and refuse a number that rests on cabling we could not establish.
  const topo = state.topology;
  const unread = new Set(sel.filter(d => ((topo && topo.unread) || []).some(u => u.startsWith(d.serial + ' '))).map(d => d.serial));
  const unsure = plan && topo ? Combo.unsure(plan, new Set(sel.map(d => d.serial)), unread) : [];
  const unsureDev = sel.find(d => d.serial === unsure[0]);
  const cablingWhy = method === 'simple' || !(wantIp && sel.some(d => typeOf(d) && cfgOf(typeOf(d)).mode === 'paired')) ? ''
    : state.readingTopology ? 'Still reading the cabling…'
    : !topo ? 'The cabling has not been read yet — Scan first'
    : unsureDev ? `${unsureDev.name || unsureDev.serial}: cabling unknown. Read topology, untick it or set ${typeOf(unsureDev).label} to Sequence` : '';
  const problem = S && picked ? S.problem() : '';
  const octetOk = inRange(parseInt(els.allOctetInput.value), 1, 254);
  const why = busy || comboRun || applyRun ? 'Busy' : !sel.length ? 'No devices ticked' : !wantIp && !picked ? 'Tick IP addresses or a setting to apply'
    : wantIp && method === 'simple' && !octetOk ? 'Enter a 4th octet from 1 to 254'
    : !numbered ? 'Fix the numbering note under the table first' : cablingWhy || problem;
  els.btnApply.disabled = !!why;
  els.btnApply.title = why;
  viewEls.scan.disabled = busy;
  viewEls.scan.title = els.btnScan.title;
  const writing = busy || !!comboRun || !!applyRun || !!(S && S.running());   // an Apply, or its settings stage, is still sending
  els.btnReset.disabled = writing || !sel.length;
  els.btnReset.title = sel.length ? (writing ? 'Busy' : '') : 'No devices ticked';
  els.keepProfiles.disabled = busy;
  els.keepIp.disabled = busy;
  els.keepIp.parentElement.title = busy ? 'Busy' : 'Devices keep their IP address, mask and gateway through the factory reset.';
  const sending = !!(comboRun || applyRun || jobActive());
  const moves = sharedMoves(), anyShared = (state.devices || []).some(d => d.sharedIp);
  const fixWhy = writing ? 'Busy' : !anyShared ? 'No devices share an address' : !moves.length ? 'Tick devices that share an address and answer over IPv6'
    : plan && plan.clashes.some(c => c.serials.some(s => moves.some(m => m.serial === s))) ? 'Two devices would get one address: see note' : '';
  els.btnFixDup.disabled = !!fixWhy;
  els.btnFixDup.title = fixWhy || 'Move ticked shared devices to their planned addresses over IPv6';
  els.btnLoadNumbering.disabled = sending;
  els.btnLoadNumbering.title = sending ? 'Busy' : 'Load a saved numbering table';
  const simple = method === 'simple';
  els.numberingMethod.disabled = sending;
  els.numberingMethod.title = sending ? 'Busy' : '';
  els.allOctetInput.disabled = sending || !simple;
  els.allOctetInput.title = !simple ? 'Used when each batch gets one 4th octet' : sending ? 'Busy' : 'Every ticked device gets this 4th octet';
  els.renumberAll.disabled = simple;
  els.renumberAll.title = simple ? 'Not used when each batch gets one 4th octet' : '';
  // Show what the chosen method does not use, not only disable it: dimmed, greyscale, not-allowed cursor.
  els.allOctetInput.closest('label').classList.toggle('is-off', !simple);
  els.allOctetInput.closest('label').title = simple ? '' : 'Used when each batch gets one 4th octet';
  els.renumberAll.closest('label').classList.toggle('is-off', simple);
  els.renumberAll.closest('label').title = simple ? 'Not used when each batch gets one 4th octet' : '';
  els.btnTopo.classList.toggle('is-off', simple);
  els.schemeRows.closest('table').classList.toggle('simple-mode', simple);
  els.methodNote.textContent = simple ? `Ticked devices get .${batchOctet} on their type's third octet, unless in an earlier batch. Moves on after each batch. Cabling not used.` : 'Follows the cabling and table below.';
  const job = state.job;
  if (!job) { els.summary.classList.add('hidden'); return; }
  // A later phase only starts if the earlier ones were clean, so what they add is either done or dry-run.
  const earlier = st => earlierPhases.filter(i => i.status === st).length;
  const c = { ...job.counts, ok: job.counts.ok + earlier('done'), dry: job.counts.dry + earlier('dry_run'), total: job.counts.total + earlierPhases.length }, parts = [];
  if (job.dryRun) parts.push(`<span class="pend">${c.dry} dry-run</span>`);
  parts.push(`<span class="ok">${c.ok} ${job.kind === 'reset' ? 'reset' : 'addressed'} ✓</span>`, `<span class="fail">${c.failed} failed</span>`);
  if (c.needsPassword) parts.push(`<span class="pend">${c.needsPassword} need password</span>`);
  if (c.pending) parts.push(`<span class="pend">${c.pending} in progress</span>`);
  els.summary.innerHTML = (job.done ? 'Finished: ' : 'Running: ') + parts.join(' · ') + ` of ${c.total}`;
  els.summary.classList.remove('hidden');
}

// When an apply phase finishes, record it and move on to the next phase (or the settings).
function foldFinishedJob() {
  const job = state.job;
  if (!job || !job.done || job.id === handledJob) return;
  handledJob = job.id; save('iptop.handledJob', handledJob);
  if (job.kind !== 'apply') { log(job.counts.failed ? 'Reset: failures, check rows.' : 'Reset done. Scan when devices are back.'); return; }
  const good = job.items.filter(i => i.status === 'done' || i.status === 'dry_run');
  if (good.length) {
    history.push({ octet: 'plan', at: Date.now(), dry: job.dryRun, devices: good.map(i => ({ type: typeOf(i.device)?.label || i.device.typeLabel, name: i.device.name || i.device.serial, serial: i.device.serial, ip: i.targetIp, id: i.id, apiKind: i.device.apiKind })) });
    save('iptop.history', history);
  }
  if (comboRun) {
    const clean = job.counts.failed === 0 && job.counts.needsPassword === 0;
    if (!clean) { comboRun = null; applyRun = null; log('Stopped: failures. Rest not changed. Check rows, then Apply.'); }
    else if (comboRun.phases.length) { earlierPhases.push(...job.items); sendPhase(comboRun.phases.shift()); return; }
    else {
      comboRun = null;
      if (method === 'simple') {
        const done = batchOctet;
        if (done < 254) { batchOctet = done + 1; save('iptop.octet', batchOctet); els.allOctetInput.value = batchOctet; schemeDirty = true; }
        log(`✓ Batch .${done} done. Connect the next and Scan: it gets .${batchOctet}.`);
      } else log('✓ Addressed. Connect the next batch and Scan: numbers carry on.');
      renumberAll = false; els.renumberAll.checked = false; startSettingsStage(); return;
    }
    render();
    return;
  }
  render();
}

function renderHistory() {
  const sending = !!(comboRun || applyRun || jobActive());
  els.btnClearHistory.disabled = sending || !history.length;
  els.btnClearHistory.title = sending ? 'Busy' : !history.length ? 'Nothing addressed yet' : 'Forget batches; numbering restarts from Start. No device changed.';
  if (!history.length) { els.historyList.innerHTML = '<div class="history-empty">Nothing addressed yet.</div>'; return; }
  els.historyList.innerHTML = [...history].reverse().map(b => `<div class="history-batch"><div class="history-batch-label">${typeof b.octet === 'number' ? 'octet .' + b.octet : esc(b.octet)}${b.dry ? ' (dry run)' : ''} · ${new Date(b.at).toLocaleTimeString()}</div>` +
    b.devices.map(d => `<div class="history-item ${state.configureUrl ? 'has-configure' : ''}"><span>${esc(d.type)}</span><span class="nm" title="${esc(d.serial)}">${esc(d.name)}</span><span class="ip">${esc(d.ip)}</span>${historyConfigure(b, d)}</div>`).join('') + '</div>').join('');
}
els.deviceArea.addEventListener('click', onConfigureClick);
viewEls.list.addEventListener('click', onConfigureClick);
const addIp = document.getElementById('addIp'), btnAdd = document.getElementById('btnAdd');
function renderAddDevice() {
  document.getElementById('addDevice').classList.toggle('hidden', !state.configureUrl);
  const empty = !addIp.value.trim(), busy = jobActive();
  btnAdd.disabled = empty || busy;
  btnAdd.title = busy ? 'Busy' : empty ? 'Enter an address'
    : 'List this device. Without mDNS, address changes end as "Not seen afterwards".';
}
async function addDevice() {
  if (btnAdd.disabled) return;
  try {
    const r = await post('/api/device/add', { ip: addIp.value.trim() });
    log(`Added ${r.model} at ${addIp.value.trim()}.`);
    addIp.value = '';
  } catch (e) { log('Could not add it: ' + e.message); }
  renderAddDevice();
}
addIp.addEventListener('input', renderAddDevice);
addIp.addEventListener('keydown', e => { if (e.key === 'Enter') addDevice(); });
btnAdd.addEventListener('click', addDevice);
els.historyList.addEventListener('click', onConfigureClick);
els.btnClearHistory.addEventListener('click', () => { history = []; save('iptop.history', history); schemeDirty = true; render(); });

// ─── Actions ────────────────────────────────────────────────────────────────
for (const b of [els.btnScan, viewEls.scan]) b.addEventListener('click', () => { log('Scanning…'); post('/api/scan').catch(e => log('Scan failed: ' + e.message)); });

async function sendPhase(items) {
  pwDismissed = new Set();
  try { await post('/api/apply', { items: items.map(({ id, newIp }) => ({ id, newIp })), netmask, gateway: GATEWAY, ignoreClash: els.ignoreClash.checked }); }
  catch (e) { comboRun = null; applyRun = null; log('Could not start: ' + e.message); render(); }
}

// Addresses go out in two phases, nodes then switches. The switches take <prefix>.<third>.n, which a
// factory-fresh node may be using right now, so the nodes have to move off it first. Anything that would
// still land on an address in use is refused up front rather than discovered halfway through.
function comboClash(phases, ignoreDevices) {
  const target = new Map(phases.flat().map(i => [i.id, i.newIp]));
  const mine = new Set((state.interfaces || []).map(i => i.address));
  const moved = new Set();
  for (const phase of phases) {
    for (const it of phase) {
      if (mine.has(it.newIp)) return `${it.newIp} is this computer's own address.`;
      if (ignoreDevices) continue;
      const holder = (state.devices || []).find(d => d.ip === it.newIp && d.id !== it.id);
      if (holder && !(moved.has(holder.id) && target.get(holder.id) !== holder.ip)) {
        return `${it.newIp} is in use by ${holder.name || holder.serial} (${holder.serial}). Tick it, change a third octet or tick "Ignore address clashes".`;
      }
    }
    phase.forEach(i => moved.add(i.id));
  }
  return '';
}

// Apply = addresses (if ticked), then a rescan, then each ticked setting in turn (settings.js runs that part).
els.btnApply.addEventListener('click', async () => {
  const S = window.Settings;
  // Devices already on their planned address and mask are left alone: re-sending would only make a node reboot.
  const bits = netmask.split('.').reduce((n, o) => n + (+o).toString(2).replace(/0/g, '').length, 0);
  const wanted = els.optIp.checked ? selectedDevices().map(d => ({ id: d.id, newIp: newIpFor(d), sw: isSwitch(d), cur: d.ip, curBits: d.prefix })).filter(i => i.newIp) : [];
  const items = wanted.filter(i => i.newIp !== i.cur || i.curBits !== bits);
  if (wanted.length > items.length) log(`${wanted.length - items.length} already set — not re-sent.`);
  const dupes = items.map(i => i.newIp).filter((ip, i, a) => a.indexOf(ip) !== i);
  if (dupes.length && !els.ignoreClash.checked) { log(`Two devices would get ${dupes[0]}: untick one or tick "Ignore address clashes".`); return; }
  if (dupes.length) log(`Warning: two devices will share ${dupes[0]}. Move one before configuring.`);
  earlierPhases = [];
  S.clearResults();
  applyRun = { actions: S.chosen(), names: S.nameValues(), addressed: items.length > 0 };
  if (!items.length) { startSettingsStage(); return; }
  sendAddresses(items);
});

function sendAddresses(items) {
  const phases = [items.filter(i => !i.sw), items.filter(i => i.sw)].filter(p => p.length);
  const clash = comboClash(phases, els.ignoreClash.checked);
  if (clash) { applyRun = null; log(clash); return; }
  if (els.ignoreClash.checked) log('Ignoring address clashes, as ticked.');
  comboRun = { phases: phases.slice(1) };
  log(`Addressing ${items.length} device(s)${phases.length > 1 ? ' — nodes first, then switches' : ''}…`);
  render();
  sendPhase(phases[0]);
}

// Ticked devices sharing an address that can be told apart, and are not simply keeping it.
function sharedMoves() {
  return selectedDevices().filter(d => d.sharedIp && d.linkRoute && newIpFor(d) && newIpFor(d) !== d.ip)
    .map(d => ({ id: d.id, serial: d.serial, newIp: newIpFor(d), sw: isSwitch(d), cur: d.ip }));
}

els.btnFixDup.addEventListener('click', () => {
  earlierPhases = [];
  window.Settings.clearResults();
  applyRun = { actions: [], names: {}, addressed: true };
  sendAddresses(sharedMoves());
});

// After the addresses are done (or if none were sent) hand over to the settings stage, once.
function startSettingsStage() {
  const run = applyRun;
  if (!run || !run.actions.length) { applyRun = null; render(); return; }
  render();
  window.Settings.runStage(run).catch(e => log('Settings stopped: ' + e.message)).finally(() => { applyRun = null; render(); });
}

els.btnReset.addEventListener('click', () => {
  const list = selectedDevices();
  els.confirmCount.textContent = list.length;
  els.confirmProfiles.textContent = els.keepProfiles.checked ? 'LumiNode profiles kept.' : 'LumiNode profiles wiped too.';
  els.confirmIp.textContent = els.keepIp.checked ? 'Wipes settings but keeps each device\'s IP address. Devices reboot on the same address.'
    : 'Wipes IP settings. Devices reboot on their factory default address.';
  els.confirmList.innerHTML = list.map(d => `<li><span>${esc(d.typeLabel)}</span><span>${esc(d.model)}</span><span class="mono">${esc(d.serial)}</span><span class="mono">${esc(d.ip)}</span><span class="muted">${esc(d.name)}</span></li>`).join('');
  const gen2 = list.some(d => d.apiKind === 'gen2');
  els.confirmGen2Note.classList.toggle('hidden', !(gen2 && !state.gen2Verified));
  els.confirmGen2Note.textContent = 'Includes Gen2: reset call not confirmed on a real switch.';
  els.btnConfirmGo.textContent = state.dryRun ? 'Run dry run (nothing is sent)' : 'Yes, reset them';
  els.confirmModal.classList.remove('hidden');
});
els.btnConfirmCancel.addEventListener('click', () => els.confirmModal.classList.add('hidden'));
els.btnConfirmGo.addEventListener('click', async () => {
  els.confirmModal.classList.add('hidden');
  pwDismissed = new Set();
  earlierPhases = [];
  window.Settings.clearResults();
  log('Resetting…');
  try { await post('/api/reset', { ids: selectedDevices().map(d => d.id), keepProfiles: els.keepProfiles.checked, keepIp: els.keepIp.checked }); }
  catch (e) { log('Could not start: ' + e.message); }
});

// ─── Password prompt ────────────────────────────────────────────────────────
function pendingPasswordIds() { return state.job.items.filter(i => i.status === 'needs_password' && !pwDismissed.has(i.id)).map(i => i.id); }
function maybePromptPassword() {
  if (!state.job || !els.passwordModal.classList.contains('hidden')) return;
  const need = state.job.items.filter(i => i.status === 'needs_password' && !pwDismissed.has(i.id));
  if (!need.length) return;
  const key = state.job.id + ':' + need.map(i => i.id).sort().join(',');
  if (pwPromptedKey === key) return;
  pwPromptedKey = key;
  els.passwordList.innerHTML = need.map(i => `<li><span>${esc(i.device.typeLabel)}</span><span class="mono">${esc(i.device.serial)}</span><span class="mono">${esc(i.device.ip)}</span><span class="muted">${esc(i.detail)}</span></li>`).join('');
  els.pwPass.value = '';
  els.pwAll.parentElement.classList.toggle('hidden', need.length < 2);
  els.passwordModal.classList.remove('hidden');
  els.pwPass.focus();
}
els.btnPwGo.addEventListener('click', async () => {
  const all = pendingPasswordIds();
  const ids = els.pwAll.checked || all.length === 1 ? all : all.slice(0, 1);
  els.passwordModal.classList.add('hidden'); pwPromptedKey = null;
  try { await post('/api/job/password', { ids, username: els.pwUser.value || 'admin', password: els.pwPass.value }); }
  catch (e) { log('Could not send the password: ' + e.message); }
});
els.btnPwSkip.addEventListener('click', () => {
  for (const id of pendingPasswordIds()) pwDismissed.add(id);
  els.passwordModal.classList.add('hidden'); pwPromptedKey = null;
  post('/api/job/cancel').catch(() => {});
});
els.pwPass.addEventListener('keydown', e => { if (e.key === 'Enter') els.btnPwGo.click(); });

connect();
