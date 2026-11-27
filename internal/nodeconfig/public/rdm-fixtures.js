'use strict';
// RDM tab, part two: fixture tiles, the fixture page (a side panel), and
// selecting several fixtures on one port to act on together. Uses the state and
// helpers of the RDM section in nodeconfig.js (rdm, renderRdm, rdmSend, call…).
// No pop-ups: risky actions open an inline warning with a second button.

try { rdm.view = localStorage.getItem('rdm.view') === 'table' ? 'table' : 'tiles'; } catch (_) { rdm.view = 'tiles'; }

function rdmSetView(v) {
  rdm.view = v;
  try { localStorage.setItem('rdm.view', v); } catch (_) {}
  renderRdm();
}

// ── Tiles ───────────────────────────────────────────────────────────────────

function rdmSelected(port) { return rdm.sel && rdm.sel.port === port ? rdm.sel.uids : new Set(); }

function rdmTiles(port, fixtures) {
  const sel = rdmSelected(port);
  return `<div class="rdm-tiles">${fixtures.map(f => {
    const problems = rdmProblems(f, fixtures);
    const st = rdm.fxStatus[f.uid];
    const end = f.start != null && f.footprint ? f.start + f.footprint - 1 : null;
    const open = rdm.panel && rdm.panel.uid === f.uid;
    const picked = rdm.pick && rdm.pick.port === port && rdm.pick.uid === f.uid;
    const cls = ['rdm-tile', f.error ? 'err' : problems.length ? 'clash' : '', sel.has(f.uid) ? 'sel' : '', open ? 'open' : '',
      picked ? 'picked' : ''].join(' ');
    const pers = f.personalityName || (f.personality ? `Personality ${f.personality}` : '');
    return `<div class="${cls}" onclick="rdmTileTap(${port}, '${esc(f.uid)}')" title="UID ${esc(f.uid)}${f.category ? ' · ' + esc(f.category) : ''} · tap to identify, double-tap for the fixture page">
      <button class="rdm-check" onclick="event.stopPropagation(); rdmToggleSel(${port}, '${esc(f.uid)}')"
        title="${sel.has(f.uid) ? 'Unselect' : 'Select'} for a batch action" aria-pressed="${sel.has(f.uid)}">${sel.has(f.uid) ? '☑' : '☐'}</button>
      ${rdmTileAddr(port, f, end)}
      <div class="rdm-tile-model">${esc(f.model || f.uid)}</div>
      <div class="rdm-tile-sub">${esc(pers)}${f.footprint ? ` · ${f.footprint} ch` : ''}</div>
      <div class="rdm-tile-sub">${esc(f.label) || '&nbsp;'}</div>
      ${problems.length ? `<div class="rdm-tile-why">${esc(problems.join('. '))}</div>` : ''}
      ${st ? `<div class="rdm-tile-st ${st.state}">${esc(st.text)}</div>` : ''}
    </div>`;
  }).join('')}</div>`;
}

function rdmToggleSel(port, uid) {
  if (!rdm.sel || rdm.sel.port !== port) rdm.sel = { port, uids: new Set() };
  rdm.sel.uids.has(uid) ? rdm.sel.uids.delete(uid) : rdm.sel.uids.add(uid);
  if (!rdm.sel.uids.size) rdm.sel = null;
  rdm.batch = null;
  renderRdm();
}

// rdmSelectRow is the row above the tiles: ALL, then one button per fixture
// type on the port. A type button adds all of that type to the selection, or
// takes them out when they are all in it already.
function rdmModelKey(f) { return `${f.manufacturerId}:${f.modelId}`; }

function rdmSelectRow(port) {
  const all = (rdm.read[port] || {}).fixtures || [];
  if (!all.length) return '';
  const sel = rdmSelected(port), types = new Map();
  for (const f of all) {
    if (f.error) continue;
    const k = rdmModelKey(f);
    if (!types.has(k)) types.set(k, { name: f.model || f.uid, list: [] });
    types.get(k).list.push(f.uid);
  }
  const allOn = sel.size === all.length;
  return `<div class="rdm-selrow"><span class="dim">Select</span>
    <button class="btn sm${allOn ? ' active-tab' : ''}" onclick="rdmSelectAll(${port})" title="Select or unselect every fixture on this port">${allOn ? '☑' : '☐'} ALL ${all.length}</button>
    ${[...types.entries()].map(([k, ty]) => {
      const on = ty.list.every(u => sel.has(u));
      return `<button class="btn sm${on ? ' active-tab' : ''}" onclick="rdmSelectType(${port}, '${esc(k)}')" title="${on ? 'Unselect' : 'Select'} every ${esc(ty.name)} on this port">${on ? '☑' : '☐'} ${esc(ty.name)} ×${ty.list.length}</button>`;
    }).join('')}
    ${sel.size ? `<button class="btn sm" onclick="rdm.sel = null; rdm.batch = null; renderRdm()">NONE</button>` : ''}
  </div>`;
}

function rdmSelectType(port, key) {
  const list = rdm.read[port].fixtures.filter(f => !f.error && rdmModelKey(f) === key).map(f => f.uid);
  const sel = new Set(rdmSelected(port));
  const allIn = list.every(u => sel.has(u));
  for (const u of list) allIn ? sel.delete(u) : sel.add(u);
  rdm.sel = sel.size ? { port, uids: sel } : null;
  rdm.batch = null;
  renderRdm();
}

function rdmSelectAll(port) {
  const all = (rdm.read[port] || {}).fixtures || [];
  const sel = rdmSelected(port);
  rdm.sel = sel.size === all.length ? null : { port, uids: new Set(all.map(f => f.uid)) };
  rdm.batch = null;
  renderRdm();
}

// ── Batch actions on the selection ──────────────────────────────────────────

function rdmSameModel(list) {
  return list.length > 0 && list.every(f => f.manufacturerId === list[0].manufacturerId && f.modelId === list[0].modelId && !f.error);
}

function rdmBatchBar(port) {
  const sel = rdmSelected(port);
  if (!sel.size) return '';
  const list = rdm.read[port].fixtures.filter(f => sel.has(f.uid));
  const same = rdmSameModel(list);
  const notSame = 'Select fixtures of one model only';
  const b = rdm.batch && rdm.batch.port === port ? rdm.batch : null;
  const running = b && b.running;
  const dis = running ? 'disabled title="Running…"' : '';
  let extra = '';
  if (b && b.kind === 'personality') {
    extra = b.options ? `<select id="rdm-b-pers">${b.options.map(p => `<option value="${p.number}">${p.number}: ${esc(p.name || 'personality ' + p.number)} (${p.footprint} ch)</option>`).join('')}</select>
      <button class="btn sm pri" onclick="rdmBatchRun('personality')" ${dis}>SET ${list.length}</button>`
      : `<span class="rdm-note">${esc(b.error || 'Reading personalities…')}</span>`;
  } else if (b && b.kind === 'setting') {
    if (!b.options) extra = `<span class="rdm-note">${esc(b.error || 'Reading settings…')}</span>`;
    else {
      const s = b.options.find(o => o.pid === b.pid) || b.options[0];
      extra = `<select onchange="rdm.batch.pid = +this.value; renderRdm()">${b.options.map(o => `<option value="${o.pid}" ${o === s ? 'selected' : ''}>${esc(o.name)}</option>`).join('')}</select>
        ${rdmValueInput(s, 'rdm-b-val')}
        <button class="btn sm pri" onclick="rdmBatchRun('setting')" ${dis}>SET ${list.length}</button>`;
    }
  } else if (b && b.kind === 'factory') {
    const keep = b.keepAddr !== false;
    extra = `${rdmKeepAddrButton(keep, 'rdm.batch.keepAddr', '', dis)}
      <span class="rdm-warn-text">Factory reset ${list.length} fixture${list.length === 1 ? '' : 's'}: ${keep ? 'personality, label and settings go back to factory defaults, then each goes back to its own address' : 'address, personality, label and settings go back to factory defaults'}. They may restart.</span>
      <button class="btn sm dan" onclick="rdmBatchRun('factory')" ${dis}>RESET ${list.length}</button>`;
  } else if (b && b.kind === 'readdress') {
    extra = rdmReaddressHtml(port, b, dis);
  }
  return `<div class="rdm-batch">
    <b>${list.length} selected</b>
    <button class="btn sm" onclick="rdmBatchIdentify(true)" ${dis || 'title="Identify every selected fixture"'}>ID ON</button>
    <button class="btn sm" onclick="rdmBatchIdentify(false)" ${dis || 'title="Stop every selected fixture identifying"'}>ID OFF</button>
    <button class="btn sm${b && b.kind === 'personality' ? ' active-tab' : ''}" onclick="rdmBatchOpen('personality')" ${dis || (same ? 'title="Set one personality on all of them"' : `disabled title="${notSame}"`)}>PERSONALITY…</button>
    <button class="btn sm${b && b.kind === 'setting' ? ' active-tab' : ''}" onclick="rdmBatchOpen('setting')" ${dis || (same ? 'title="Set one of the fixture\'s own settings on all of them"' : `disabled title="${notSame}"`)}>SETTING…</button>
    <button class="btn sm${b && b.kind === 'readdress' ? ' active-tab' : ''}" onclick="rdmBatchOpen('readdress')" ${dis || (list.some(f => f.start != null && !f.error) ? `title="Readdress them in the order they are patched now, spaced for a new mode${same ? '' : ' (mixed models: each keeps its mode)'} or by a number of channels"` : 'disabled title="None of them has a DMX address"')}>READDRESS…</button>
    <button class="btn sm dan${b && b.kind === 'factory' ? ' active-tab' : ''}" onclick="rdmBatchOpen('factory')" ${dis || 'title="Factory reset the selected fixtures (asks first, in the page)"'}>FACTORY RESET…</button>
    <button class="btn sm" onclick="rdm.sel = null; rdm.batch = null; renderRdm()" ${dis}>CLEAR</button>
    <button class="btn sm dan" onclick="rdm.batch.stop = true; renderRdm()" ${running ? (b.stop ? 'disabled title="Stopping after the fixture being changed now"' : 'title="Stop after the fixture being changed now. The rest are not sent"') : 'disabled title="Nothing is running"'}>STOP</button>
    ${extra ? `<div class="rdm-batch-extra">${extra}${b && !running ? ' <button class="btn sm" onclick="rdm.batch = null; renderRdm()">CANCEL</button>' : ''}</div>` : ''}
  </div>`;
}

async function rdmBatchOpen(kind) {
  const port = rdm.sel.port, list = rdm.read[port].fixtures.filter(f => rdm.sel.uids.has(f.uid));
  rdm.batch = { port, kind };
  renderRdm();
  const first = list[0], ip = currentIp;
  if (kind === 'readdress') {
    const addressed = list.filter(f => f.start != null && !f.error);
    Object.assign(rdm.batch, { mode: 0, start: addressed.length ? Math.min(...addressed.map(f => f.start)) : 1, gap: null, options: null });
    renderRdm();
    if (!rdmSameModel(list)) return; // mixed models: each keeps its own mode
  }
  try {
    if (kind === 'personality' || kind === 'readdress') {
      const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(first.uid)}/personalities`);
      if (rdm.batch && rdm.batch.kind === kind) rdm.batch.options = d.personalities;
    } else if (kind === 'setting') {
      const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(first.uid)}/details`);
      // Not SET-only ones: nothing says what they do, so they go one fixture at a time from its page.
      const opts = d.settings.filter(s => !s.readOnly && !s.error && s.kind !== 'action');
      if (rdm.batch && rdm.batch.kind === kind) {
        rdm.batch.options = opts;
        rdm.batch.pid = opts.length ? opts[0].pid : null;
        if (!opts.length) { rdm.batch.options = null; rdm.batch.error = 'This model has no settings this page can change.'; }
      }
    }
  } catch (e) {
    if (rdm.batch && rdm.batch.kind === kind) rdm.batch.error = kind === 'readdress' ? `Cannot read the modes (each keeps its own): ${e.message}` : e.message;
  }
  renderRdm();
}

// rdmBatchEach runs one request per selected fixture, one after another, and
// shows how each went on its tile.
async function rdmBatchEach(label, fn) {
  const port = rdm.sel.port, ip = currentIp;
  const list = rdm.read[port].fixtures.filter(f => rdm.sel.uids.has(f.uid));
  if (rdm.batch) rdm.batch.running = true; else rdm.batch = { port, running: true };
  const mine = rdm.batch;
  let good = 0, tried = 0, why = '';
  for (const f of list) {
    // STOP pressed, or the page now shows another node: the rest are not sent, and the toast below says so.
    if (mine.stop) { why = 'stopped'; break; }
    if (ip !== currentIp) { why = 'the page moved to another node'; break; }
    rdm.fxStatus[f.uid] = { state: 'busy', text: label + '…' };
    renderRdm();
    tried++;
    try {
      const d = await fn(f, ip, port);
      if (ip !== currentIp) { good++; why = 'the page moved to another node'; break; }
      if (d && d.fixture) rdmReplace(port, d.fixture);
      rdm.fxStatus[f.uid] = { state: 'ok', text: d && d.dryRun ? 'DRY RUN — not sent' : (d && d.note) || label + ' ✓' };
      good++;
    } catch (e) {
      rdm.fxStatus[f.uid] = { state: 'err', text: e.message };
    }
  }
  if (rdm.batch === mine) rdm.batch = null;
  const left = list.length - tried;
  if (why) toast(`${label}: ${why} after ${good} of ${list.length}. ${left} not sent${left ? ', so the rig is part changed' : ''}.`, true);
  else toast(`${label}: ${good} of ${list.length} done`, good < list.length);
  if (ip !== currentIp) return; // the tiles on screen are another node's
  renderRdm();
  if (rdm.panel && list.some(f => f.uid === rdm.panel.uid)) rdmLoadPanel(); // the open fixture page now shows old values
  setTimeout(() => { for (const f of list) if (rdm.fxStatus[f.uid] && rdm.fxStatus[f.uid].state !== 'err') delete rdm.fxStatus[f.uid]; renderRdm(); }, 6000);
}

// rdmReplace puts a fixture as read back into the port's list. A partial one
// (an identify change: only uid and identify) is merged into what is there.
function rdmReplace(port, fixture) {
  const list = rdm.read[port].fixtures, i = list.findIndex(x => x.uid === fixture.uid);
  if (i >= 0) list[i] = fixture.partial ? { ...list[i], identify: fixture.identify } : fixture;
  rdmMarkOverlaps(list);
}

function rdmBatchIdentify(on) {
  rdmBatchEach(on ? 'Identify on' : 'Identify off', (f, ip, port) =>
    call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(f.uid)}`, 'POST', { identify: on }));
}

function rdmBatchRun(kind) {
  const b = rdm.batch;
  if (kind === 'personality') {
    const n = +nc$('rdm-b-pers').value;
    rdmBatchEach(`Personality ${n}`, (f, ip, port) =>
      call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(f.uid)}`, 'POST', { personality: n }));
  } else if (kind === 'setting') {
    const s = b.options.find(o => o.pid === b.pid);
    const v = rdmReadValue(s, 'rdm-b-val');
    if (v.error) { toast(v.error, true); return; }
    rdmBatchEach(s.name, async (f, ip, port) => {
      const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(f.uid)}/param`, 'POST', { pid: s.pid, value: v.value });
      return { dryRun: d.dryRun, note: `${s.name}: ${rdmValueText(d.setting)}` };
    });
  } else if (kind === 'factory') {
    const keep = b.keepAddr !== false;
    rdmBatchEach('Factory reset', async (f, ip, port) => {
      const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(f.uid)}/factory-reset`, 'POST', keep ? { keepAddress: true } : null);
      return d.dryRun ? d : { ...d, note: d.note ? `Reset, ${d.note}` : 'Reset ✓' };
    });
  } else if (kind === 'readdress') {
    const plan = rdmReaddressPlan(b.port, b);
    if (plan.block) { toast(plan.block, true); return; }
    const by = new Map(plan.rows.map(r => [r.f.uid, r]));
    rdmBatchEach('Readdress', async (f, ip, port) => {
      const r = by.get(f.uid);
      if (!r) return { note: 'Skipped: no address' };
      const body = {};
      if (r.personality && r.personality !== f.personality) body.personality = r.personality;
      if (r.start !== f.start) body.start = r.start;
      if (!Object.keys(body).length) return { note: `Already at ${r.start}` };
      const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(f.uid)}`, 'POST', body);
      return d.dryRun ? d : { ...d, note: `${f.start} → ${r.start}${body.personality ? ', mode ' + body.personality : ''}` };
    });
  }
}

// rdmKeepAddrButton is the Keep address switch of a factory reset (the fixture
// page's and the batch bar's): ticked by default.
function rdmKeepAddrButton(keep, path, start, dis) {
  return `<button class="btn sm${keep ? ' active-tab' : ''}" onclick="${path} = ${!keep}; renderRdm()" ${dis || `title="${keep ? 'The address is read first and set back after the reset' : 'The address goes to the factory one too'}"`}>${keep ? '☑' : '☐'} KEEP ADDRESS${start}</button>`;
}

// ── Readdress: a new mode, the same order ───────────────────────────────────
// The selected fixtures go one after another in the order they are patched now
// (by address), from a start address: each the new mode's footprint after the
// last (or its own footprint, keeping its mode), or every N channels when a
// spacing is typed. With a new mode each fixture gets the personality and the
// address in one request (personality first, as an edit sends them).

function rdmReaddressPlan(port, b) {
  const sel = rdmSelected(port), all = rdm.read[port].fixtures;
  const list = all.filter(f => sel.has(f.uid) && !f.error && f.start != null)
    .sort((x, y) => x.start - y.start || x.uid.localeCompare(y.uid));
  const mode = b.mode && b.options ? b.options.find(o => o.number === b.mode) : null;
  const rows = [];
  let at = b.start, block = '';
  for (const f of list) {
    const footprint = mode ? mode.footprint : f.footprint;
    rows.push({ f, start: at, footprint, personality: mode ? mode.number : 0 });
    if (b.gap && b.gap < footprint && !block) block = `Spacing ${b.gap} is less than the ${footprint} channels each one takes`;
    at += b.gap || footprint;
  }
  const last = rows[rows.length - 1];
  const end = last ? last.start + Math.max(last.footprint, 1) - 1 : 0;
  if (!Number.isInteger(b.start) || b.start < 1 || b.start > 512) block = 'Start at an address from 1 to 512';
  else if (b.gap != null && (!Number.isInteger(b.gap) || b.gap < 1 || b.gap > 512)) block = 'Spacing takes 1 to 512 channels';
  else if (end > 512) block = `The last one would end at ${end}, past channel 512`;
  // Fixtures not being readdressed whose channels the new patch would share: a warning, not a stop (they may be next).
  const others = all.filter(o => !sel.has(o.uid) && o.start != null && o.footprint);
  const hits = others.filter(o => rows.some(r => r.footprint && r.start <= o.start + o.footprint - 1 && o.start <= r.start + r.footprint - 1));
  return { rows, end, block, hits, skipped: sel.size - rows.length };
}

function rdmReaddressPreview(port, b) {
  const p = rdmReaddressPlan(port, b);
  if (!p.rows.length) return '<span class="rdm-warn-text">None of them has a DMX address.</span>';
  const moves = p.rows.map(r => `${r.f.start}→${r.start}`).join(' · ');
  const warn = [p.block,
    p.hits.length ? `Would share channels with ${p.hits.map(o => `${o.label || o.model || o.uid} (${o.start}–${o.start + o.footprint - 1})`).join(', ')}, not selected.` : '',
    p.skipped ? `${p.skipped} without an address left as they are.` : ''].filter(Boolean).join(' ');
  return `<span>${p.rows.length} in patch order, ${p.rows[0].start}–${p.end}: ${moves}</span>${warn ? ` <span class="rdm-warn-text">${esc(warn)}</span>` : ''}`;
}

function rdmReaddressHtml(port, b, dis) {
  const list = rdm.read[port].fixtures.filter(f => rdmSelected(port).has(f.uid) && f.start != null && !f.error);
  const mode = b.mode && b.options ? b.options.find(o => o.number === b.mode) : null;
  const fit = mode ? mode.footprint : list.length && list.every(f => f.footprint === list[0].footprint) ? list[0].footprint : 'each';
  const modes = `<select onchange="rdm.batch.mode = +this.value; renderRdm()" ${dis} title="${b.options ? 'The mode (personality) to set on all of them' : 'Mixed models, or still reading: each keeps its own mode'}">
      <option value="0">Keep each one's mode</option>
      ${(b.options || []).map(o => `<option value="${o.number}" ${o.number === b.mode ? 'selected' : ''}>${o.number}: ${esc(o.name || 'personality ' + o.number)} (${o.footprint} ch)</option>`).join('')}</select>`;
  const plan = rdmReaddressPlan(port, b);
  return `${modes}
    <label class="dim">from <input type="number" inputmode="numeric" min="1" max="512" value="${b.start ?? ''}" oninput="rdmReaddressInput('start', this.value)" ${dis}></label>
    <label class="dim">every <input type="number" inputmode="numeric" min="1" max="512" placeholder="${fit === 'each' ? 'fit' : fit + ' (fit)'}" value="${b.gap ?? ''}" oninput="rdmReaddressInput('gap', this.value)" ${dis} title="Channels from one start address to the next. Empty: each takes its footprint, no gaps"> ch</label>
    <button id="rdm-ra-go" class="btn sm pri" onclick="rdmBatchRun('readdress')" ${dis || (plan.block || !plan.rows.length ? `disabled title="${esc(plan.block || 'Nothing to readdress')}"` : `title="Set the new patch on ${plan.rows.length} fixture${plan.rows.length === 1 ? '' : 's'}"`)}>READDRESS ${plan.rows.length}</button>
    ${b.error ? `<span class="rdm-note">${esc(b.error)}</span>` : ''}
    <div id="rdm-ra-preview" class="rdm-ra-preview">${rdmReaddressPreview(port, b)}</div>`;
}

// Typing in from/every updates the preview and the button in place: a full
// render would replace the input under the cursor.
function rdmReaddressInput(key, raw) {
  const b = rdm.batch;
  if (!b || b.kind !== 'readdress') return;
  b[key] = raw === '' ? null : Number(raw);
  const plan = rdmReaddressPlan(b.port, b), go = nc$('rdm-ra-go');
  nc$('rdm-ra-preview').innerHTML = rdmReaddressPreview(b.port, b);
  if (go && !b.running) {
    go.disabled = !!plan.block || !plan.rows.length;
    go.title = plan.block || (plan.rows.length ? `Set the new patch on ${plan.rows.length} fixtures` : 'Nothing to readdress');
  }
}

// ── Setting controls (shared by the panel and the batch bar) ───────────────

function rdmValueText(s) {
  if (s.kind === 'text' || s.kind === 'raw') return s.text || '—';
  if (s.value == null) return '—';
  if (s.kind === 'bool') return s.value ? 'On' : 'Off';
  if (s.kind === 'choice' && s.choices) return s.choices[s.value - s.min] || String(s.value);
  return `${s.value}${s.unit ? ' ' + s.unit : ''}${s.text ? ` (${s.text})` : ''}`;
}

function rdmValueInput(s, id) {
  if (s.kind === 'bool') return `<select id="${id}"><option value="1" ${s.value === 1 ? 'selected' : ''}>On</option><option value="0" ${s.value === 0 ? 'selected' : ''}>Off</option></select>`;
  if (s.kind === 'choice') return `<select id="${id}">${(s.choices || []).map((c, i) => `<option value="${s.min + i}" ${s.value === s.min + i ? 'selected' : ''}>${esc(c)}</option>`).join('')}</select>`;
  if (s.kind === 'text') return `<input id="${id}" type="text" maxlength="${s.maxLen || 32}" value="${esc(s.text || '')}">`;
  return `<input id="${id}" type="number" min="${s.min}" max="${s.max}" value="${s.value ?? s.default ?? s.min}" title="${s.min}-${s.max}">`;
}

function rdmReadValue(s, id) {
  const raw = nc$(id).value;
  if (s.kind === 'text') return { value: raw };
  const n = Number(raw);
  if (raw === '' || !Number.isInteger(n) || n < s.min || n > s.max) return { error: `${s.name} takes ${s.min}-${s.max}` };
  return { value: n };
}

// ── Tile taps ───────────────────────────────────────────────────────────────

// A tap on a tile picks the fixture and identifies it (a tap on the picked one
// stops that; picking another moves the identify there). A double tap opens the
// fixture page, or closes it if it was already open. Double taps are timed here
// rather than left to dblclick, which a touch screen does not always send.
let rdmLastTap = null;

function rdmTileTap(port, uid) {
  const now = Date.now();
  const dbl = rdmLastTap && rdmLastTap.port === port && rdmLastTap.uid === uid && now - rdmLastTap.t < 400;
  const panelWas = dbl ? rdmLastTap.panelWas : rdm.panel && rdm.panel.uid;
  rdmLastTap = dbl ? null : { port, uid, t: now, panelWas };
  // With the keypad open, a tap on another tile (a near miss on its address)
  // moves the keypad there rather than opening a fixture page beside it,
  // which left two fixtures identifying.
  if (rdm.typing) { rdmTypeStart(port, uid); return; }
  if (dbl) {
    rdm.pick = { port, uid };
    if (panelWas === uid) rdmClosePanel();
    else if (!rdm.panel || rdm.panel.uid !== uid) rdmOpenPanel(port, uid);
    else renderRdm();
    return;
  }
  const f = rdmFind(port, uid);
  if (!f || f.error) return;
  const on = !(rdm.pick && rdm.pick.port === port && rdm.pick.uid === uid);
  rdm.pick = on ? { port, uid } : null;
  // An open fixture page follows the pick, so only one fixture identifies.
  if (on && rdm.panel && rdm.panel.uid !== uid) rdmOpenPanel(port, uid);
  else renderRdm();
}

// ── Fixture page (side panel) ───────────────────────────────────────────────

async function rdmOpenPanel(port, uid) {
  if (rdm.typing) { rdmTypeStart(port, uid); return; }
  if (rdm.panel && rdm.panel.uid === uid) return;
  rdm.panel = { port, uid, details: null, error: null, confirm: null, confirmParam: null };
  renderRdm();
  await rdmLoadPanel();
}

async function rdmLoadPanel() {
  const p = rdm.panel, ip = currentIp;
  if (!p) return;
  p.loading = true;
  renderRdm();
  try {
    const d = await call(`/api/node/${ip}/rdm/${p.port}/fixture/${encodeURIComponent(p.uid)}/details`);
    if (rdm.panel !== p) return;
    p.details = d;
    p.error = null;
    rdmReplace(p.port, d.fixture);
  } catch (e) {
    if (rdm.panel === p) p.error = e.message;
  }
  if (rdm.panel === p) { p.loading = false; renderRdm(); }
}

function rdmClosePanel() { rdm.panel = null; renderRdm(); }

function rdmPanelHtml() {
  const p = rdm.panel;
  if (!p) return '';
  const f = rdmFind(p.port, p.uid) || { uid: p.uid };
  const d = p.details;
  const head = `<div class="rdm-side-head">
      <div><div class="rdm-side-title">${esc(f.model || f.uid)}</div>
      <div class="rdm-side-sub">${esc(f.manufacturer || '')}${f.software ? ' · fw ' + esc(f.software) : ''} · port ${p.port} · ${esc(f.uid)}</div></div>
      <button class="btn sm" onclick="rdmClosePanel()" title="Close the fixture page">✕</button></div>`;
  if (!d) return `<aside class="rdm-side">${head}<div class="rdm-note${p.error ? ' warn' : ''}">${esc(p.error || 'Reading the fixture…')}</div>${p.error ? `<button class="btn sm" onclick="rdmLoadPanel()">↻ TRY AGAIN</button>` : ''}</aside>`;
  const busy = rdm.fxBusy[p.uid] || (p.loading ? 'Reading…' : '');
  const dis = busy ? `disabled title="${esc(busy)}"` : '';
  const end = f.start != null && f.footprint ? f.start + f.footprint - 1 : null;
  const editing = rdm.edit && rdm.edit.uid === p.uid;
  const row = (k, v, cls = '') => `<div class="rdm-kv ${cls}"><span>${esc(k)}</span><span>${v}</span></div>`;

  const basics = `<div class="rdm-side-sec">PATCH
      <button class="btn sm${f.identify ? ' warn' : ''}" onclick="rdmIdentify(${p.port}, '${esc(f.uid)}')" ${dis}>${f.identify ? '◉ ID ON' : '○ ID'}</button>
      <button class="btn sm" onclick="rdmEditOpen(${p.port}, '${esc(f.uid)}')" ${dis || (editing ? 'disabled title="Being edited below"' : 'title="Change address, label or personality"')}>✎ EDIT</button></div>
    ${editing ? `<div class="rdm-side-edit">${rdmEditRow(p.port, f, rdm.read[p.port].fixtures, 0)}</div>` : `
    ${row('Address', f.start != null ? `${f.start}${end > f.start ? '–' + end : ''}` : '—')}
    ${row('Personality', f.personality ? `${esc(f.personalityName || '')} <span class="dim">${f.personality}/${f.personalities}</span>` : '—')}
    ${row('Label', esc(f.label) || '—')}`}
    ${d.hours != null ? row('Hours', `${d.hours}${d.lampHours != null ? ` <span class="dim">· lamp ${d.lampHours}</span>` : ''}`) : ''}`;
  // No "at factory defaults" row: a real Vortex 8S said No even straight after a factory reset.

  const settings = d.settings.length ? `<div class="rdm-side-sec">SETTINGS</div>` + d.settings.map(s => {
    const id = `rdm-p-${s.pid}`;
    if (s.error) return row(s.name, `<span class="dim">${esc(s.error)}</span>`);
    if (s.readOnly) return row(s.name, esc(rdmValueText(s)));
    if (s.kind === 'bool') return row(s.name, `<button class="btn sm${s.value ? ' sec' : ''}" onclick="rdmPanelSet(${s.pid}, ${s.value ? 0 : 1})" ${dis || `title="Turn ${esc(s.name)} ${s.value ? 'off' : 'on'}"`}>${s.value ? 'ON' : 'OFF'}</button>`);
    if (s.kind === 'action') {
      // A setting the fixture takes but cannot report: nothing says what it does
      // (it could be a reset), so it asks first, in the page.
      const ask = p.confirmParam && p.confirmParam.pid === s.pid ? p.confirmParam : null;
      return row(s.name, `${rdmValueInput(s, id)} <button class="btn sm warn" onclick="rdmPanelAskAction(${s.pid}, '${id}')" ${dis || 'title="Send this to the fixture (asks first)"'}>SEND…</button>`, 'edit') +
        (ask ? `<div class="rdm-confirm"><span>Send ${esc(s.name)} = ${esc(String(ask.value))}? The fixture does not say what this does, and it cannot be read back. It may act at once (a reset, a restart, a mode change).</span>
          <button class="btn sm warn" onclick="rdmPanelSendAction()" ${dis}>SEND</button>
          <button class="btn sm" onclick="rdm.panel.confirmParam = null; renderRdm()">CANCEL</button></div>` : '');
    }
    const now = `<span class="dim">${esc(rdmValueText(s))}</span> `;
    return row(s.name, `${now}${rdmValueInput(s, id)} <button class="btn sm" onclick="rdmPanelSetFrom(${s.pid}, '${id}')" ${dis}>SET</button>`, 'edit');
  }).join('') : '';

  const sensors = d.sensors.length ? `<div class="rdm-side-sec">SENSORS</div>` + d.sensors.map(sn => {
    const out = sn.value != null && (sn.value < sn.normalMin || sn.value > sn.normalMax);
    return row(sn.name, sn.value == null ? `<span class="dim">${esc(sn.error || '—')}</span>` : `${sn.value}${sn.unit ? ' ' + esc(sn.unit) : ''}`, out ? 'bad' : '');
  }).join('') : '';

  const c = p.confirm, keep = p.keepAddr !== false;
  const actions = (d.canFactoryReset || d.canRestart) ? `<div class="rdm-side-sec">FIXTURE</div><div class="rdm-side-actions">
      ${d.canRestart ? `<button class="btn sm warn" onclick="rdm.panel.confirm = 'restart'; renderRdm()" ${dis}>RESTART…</button>` : ''}
      ${d.canFactoryReset ? `<button class="btn sm dan" onclick="rdm.panel.confirm = 'factory-reset'; renderRdm()" ${dis}>FACTORY RESET…</button>` : ''}
      <button class="btn sm" onclick="rdmLoadPanel()" ${dis}>↻ READ AGAIN</button></div>
    ${c ? `<div class="rdm-confirm">${c === 'factory-reset' ? (f.start != null ? rdmKeepAddrButton(keep, 'rdm.panel.keepAddr', ' ' + f.start, '') : '<button class="btn sm" disabled title="This fixture has no DMX address">☐ KEEP ADDRESS</button>') : ''}<span>${c === 'restart' ? 'Restart the fixture: it goes dark and drops off the line for a moment.' : keep && f.start != null ? `Factory reset: personality, label and every setting go back to factory defaults, and the fixture may restart. Then the address goes back to ${f.start}.` : 'Factory reset: address, personality, label and every setting go back to factory defaults, and the fixture may restart.'}</span>
      <button class="btn sm ${c === 'restart' ? 'warn' : 'dan'}" onclick="rdmPanelAction('${c}')">${c === 'restart' ? 'RESTART' : 'FACTORY RESET'}</button>
      <button class="btn sm" onclick="rdm.panel.confirm = null; renderRdm()">CANCEL</button></div>` : ''}` : '';

  return `<aside class="rdm-side">${head}${basics}${settings}${sensors}${actions}</aside>`;
}

function rdmPanelSetFrom(pid, id) {
  const s = rdm.panel.details.settings.find(x => x.pid === pid);
  const v = rdmReadValue(s, id);
  if (v.error) { toast(v.error, true); return; }
  rdmPanelSet(pid, v.value);
}

function rdmPanelAskAction(pid, id) {
  const s = rdm.panel.details.settings.find(x => x.pid === pid);
  const v = rdmReadValue(s, id);
  if (v.error) { toast(v.error, true); return; }
  rdm.panel.confirmParam = { pid, value: v.value };
  renderRdm();
}

function rdmPanelSendAction() {
  const c = rdm.panel.confirmParam;
  rdm.panel.confirmParam = null;
  if (c) rdmPanelSet(c.pid, c.value);
}

async function rdmPanelSet(pid, value) {
  const p = rdm.panel, ip = currentIp;
  const s = p.details.settings.find(x => x.pid === pid);
  rdm.fxBusy[p.uid] = 'Sending…';
  renderRdm();
  try {
    const d = await call(`/api/node/${ip}/rdm/${p.port}/fixture/${encodeURIComponent(p.uid)}/param`, 'POST', { pid, value });
    if (rdm.panel === p) {
      const i = p.details.settings.findIndex(x => x.pid === pid);
      if (i >= 0) p.details.settings[i] = { ...d.setting, text: d.setting.text || (s.kind === 'number' ? '' : s.text) };
    }
    toast(d.dryRun ? `DRY RUN — ${s.name} not sent` : `${s.name}: ${rdmValueText(d.setting)}`);
  } catch (e) {
    toast(`${s.name}: ${e.message}`, true);
  }
  delete rdm.fxBusy[p.uid];
  renderRdm();
}

async function rdmPanelAction(action) {
  const p = rdm.panel, ip = currentIp;
  p.confirm = null;
  rdm.fxBusy[p.uid] = action === 'restart' ? 'Restarting…' : 'Factory reset…';
  renderRdm();
  try {
    const f = rdmFind(p.port, p.uid) || {};
    const keep = action === 'factory-reset' && p.keepAddr !== false && f.start != null;
    const d = await call(`/api/node/${ip}/rdm/${p.port}/fixture/${encodeURIComponent(p.uid)}/${action}`, 'POST', keep ? { keepAddress: true } : null);
    rdmReplace(p.port, d.fixture);
    toast(d.dryRun ? `DRY RUN — ${action} not sent${d.note ? ' (' + d.note + ')' : ''}` : `${d.fixture.model || p.uid}: ${action === 'restart' ? 'restarted' : 'factory reset'}${d.note ? ', ' + d.note : ''}, read back`);
  } catch (e) {
    toast(e.message, true);
  }
  delete rdm.fxBusy[p.uid];
  if (rdm.panel === p) await rdmLoadPanel(); else renderRdm();
}

// ── Typing addresses on the tiles (a keypad, made for the iPad) ────────────
// Tap a tile's address: a keypad opens at the bottom of the screen with the
// fixture, the address being entered (a suggestion first; the first digit
// replaces it) and a clash warning. SEND & NEXT sends it, reads it back and
// moves to the next tile in the order they were in when this started; SEND &
// DONE sends it and closes the keypad; SKIP, BACK and CLOSE need no keyboard. There is no text field, so the iPad keyboard
// never opens; a real keyboard still works (digits, ⌫, Enter, Tab, Esc).

function rdmTileAddr(port, f, end) {
  const t = rdm.typing;
  const id = f.identify ? ' <span class="rdm-id" title="Identifying">◉ ID</span>' : '';
  if (t && t.port === port && t.uid === f.uid) {
    return `<div class="rdm-tile-addr typing" onclick="event.stopPropagation()"><span class="rdm-typing-val">${esc(t.value === '' ? '—' : t.value)}</span>${f.footprint ? `<span class="rng">${f.footprint} ch</span>` : ''}${id}</div>
      <div class="rdm-tile-hint">${t.sending ? 'Sending…' : 'Entering on the keypad'}</div>`;
  }
  const tap = f.error ? '' : ` class="rdm-addr-tap" onclick="event.stopPropagation(); rdmTypeStart(${port}, '${esc(f.uid)}')" title="Set a new address with the keypad"`;
  return `<div class="rdm-tile-addr"><span${tap}>${f.start != null ? f.start : '—'}${end > f.start ? `<span class="rng">–${end}</span>` : ''}</span>${id}</div>`;
}

function rdmTypeStart(port, uid) {
  const list = rdm.read[port].fixtures, f = list.find(x => x.uid === uid);
  if (!f || f.error) return;
  if (rdm.typing && (rdm.typing.sending || (rdm.typing.port === port && rdm.typing.uid === uid))) return;
  if (rdm.panel && rdm.panel.uid !== uid) rdm.panel = null; // one fixture at a time: the one on the keypad
  rdm.pick = null;
  rdm.typing = { port, uid, value: f.start != null ? String(f.start) : '', fresh: true, order: list.filter(x => !x.error).map(x => x.uid) };
  renderRdm();
}

function rdmTypeMove(t, uid, value) {
  const f = rdmFind(t.port, uid);
  rdm.typing = { ...t, uid, value: value !== '' && value != null ? String(value) : (f && f.start != null ? String(f.start) : ''), fresh: true, sending: false };
  renderRdm();
}

function rdmKeyDigit(d) {
  const t = rdm.typing;
  if (!t || t.sending) return;
  const v = t.fresh ? String(d) : (t.value + d).replace(/^0+/, '');
  if (v.length > 3) return;
  t.value = v;
  t.fresh = false;
  renderRdm();
}
function rdmKeyBack() { const t = rdm.typing; if (t && !t.sending) { t.value = t.fresh ? '' : t.value.slice(0, -1); t.fresh = false; renderRdm(); } }
function rdmKeyClear() { const t = rdm.typing; if (t && !t.sending) { t.value = ''; t.fresh = false; renderRdm(); } }
function rdmTypeDone() { rdm.typing = null; renderRdm(); }
function rdmTypeSkip() { const t = rdm.typing; if (!t || t.sending) return; const f = rdmFind(t.port, t.uid); rdmTypeNext(t, f ? f.start : null); }
function rdmTypeBack() {
  const t = rdm.typing;
  if (!t || t.sending) return;
  const i = t.order.indexOf(t.uid);
  if (i > 0) rdmTypeMove(t, t.order[i - 1], '');
}

// rdmTypeCommit sends the address; SEND & NEXT then moves on, SEND & DONE
// (done) closes the keypad. A failed send keeps it open on this fixture.
async function rdmTypeCommit(done) {
  const t = rdm.typing;
  if (!t || t.sending) return;
  const f = rdmFind(t.port, t.uid), v = Number(t.value);
  if (!f) { rdm.typing = null; renderRdm(); return; }
  if (!Number.isInteger(v) || v < 1 || v > 512) { toast('The address must be 1-512', true); return; }
  if (v !== f.start) {
    t.sending = true;
    renderRdm();
    const ok = await rdmSend(t.port, f, { start: v });
    t.sending = false;
    if (rdm.typing !== t) return;
    if (!ok) { t.fresh = true; renderRdm(); return; } // stay on this one; the toast says why
  }
  if (done) { rdm.typing = null; renderRdm(); return; }
  rdmTypeNext(t, v);
}

// The step between suggested addresses: empty means "after this fixture's
// channels"; a number (e.g. 20) means every fixture starts that many after the last.
try { rdm.typeStep = localStorage.getItem('rdm.step') || ''; } catch (_) { rdm.typeStep = ''; }

function rdmStepValue() {
  const n = Number(rdm.typeStep);
  return rdm.typeStep !== '' && Number.isInteger(n) && n >= 1 && n <= 511 ? n : null;
}

function rdmSetStep(v) {
  rdm.typeStep = v === '' ? '' : String(Math.min(511, Math.max(1, v)));
  try { localStorage.setItem('rdm.step', rdm.typeStep); } catch (_) {}
  renderRdm();
}

function rdmStepAdj(delta) {
  const t = rdm.typing, f = t && rdmFind(t.port, t.uid);
  const base = rdmStepValue() || (f && f.footprint) || 0;
  rdmSetStep(Math.max(1, base + delta));
}

// rdmTypeNext moves to the next fixture, suggesting `from` plus the step (or
// this fixture's footprint), else the next one's own address.
function rdmTypeNext(t, from) {
  const i = t.order.indexOf(t.uid), next = t.order[i + 1];
  if (!next) { rdm.typing = null; toast('That was the last fixture on this port'); renderRdm(); return; }
  const cur = rdmFind(t.port, t.uid), nf = rdmFind(t.port, next);
  const step = rdmStepValue() || (cur && cur.footprint) || null;
  let s = from != null && step ? from + step : (nf && nf.start != null ? nf.start : '');
  if (s !== '' && (s < 1 || s > 512)) s = nf && nf.start != null ? nf.start : '';
  rdmTypeMove(t, next, s);
}


// rdmStepInfo says what the offset makes of it: the block each fixture takes
// (its channels plus any spare), and where the next fixture would land.
function rdmStepInfo(t, f, from, step) {
  const fp = f.footprint || 0, use = step || fp;
  if (!use) return '<div class="rdm-kp-fp"></div>'; // kept, so the keypad keeps its height
  const parts = [];
  if (step && fp) {
    parts.push(step > fp ? `Each takes ${step} ch: ${fp} used, ${step - fp} spare`
      : step === fp ? `Each takes ${step} ch, none spare`
      : `<span class="bad">Overlaps: ${fp} ch in a ${step} ch step</span>`);
  }
  const nf = rdmFind(t.port, t.order[t.order.indexOf(t.uid) + 1]);
  if (nf && from != null) {
    const ns = from + use, ne = ns + (nf.footprint || 1) - 1;
    parts.push(ns > 512 ? `<span class="bad">Next would start past 512</span>`
      : `Next: ${ns}${nf.footprint > 1 ? '–' + ne : ''}${ne > 512 ? ' <span class="bad">(past 512)</span>' : ''}`);
  } else if (!nf) parts.push('Last fixture on this port');
  return `<div class="rdm-kp-fp">${parts.map(p => `<div>${p}</div>`).join('')}</div>`;
}

// rdmKeypadHtml is the keypad panel, fixed to the bottom of the screen.
function rdmKeypadHtml() {
  const t = rdm.typing;
  if (!t || !rdm.read[t.port]) return '';
  const f = rdmFind(t.port, t.uid);
  if (!f) return '';
  const fixtures = rdm.read[t.port].fixtures, v = Number(t.value);
  const valid = t.value !== '' && Number.isInteger(v) && v >= 1 && v <= 512;
  let warn = '';
  if (t.value !== '' && !valid) warn = 'The address must be 1-512.';
  else if (valid && f.footprint) {
    const last = v + f.footprint - 1;
    const hits = fixtures.filter(o => o.uid !== f.uid && o.start != null && o.footprint && v <= o.start + o.footprint - 1 && o.start <= last);
    warn = [hits.length ? `Would share channels with ${hits.map(o => `${o.label || o.model || o.uid} (${o.start}–${o.start + o.footprint - 1})`).join(', ')}.` : '',
      last > 512 ? `Would run past 512 (to ${last}).` : ''].filter(Boolean).join(' ');
  }
  const at = t.order.indexOf(t.uid);
  const dis = t.sending ? 'disabled' : '';
  const range = valid && f.footprint ? `${v}–${v + f.footprint - 1}` : '';
  const step = rdmStepValue();
  const stepInfo = rdmStepInfo(t, f, valid ? v : f.start, step);
  const key = (label, fn, cls = '') => `<button class="rdm-key ${cls}" onclick="${fn}" ${dis}>${label}</button>`;
  return `<div class="rdm-keypad" role="dialog" aria-label="Set the address">
    <div class="rdm-kp-info">
      <div class="rdm-kp-name">${esc(f.model || f.uid)} <span>${esc(f.label || '')}</span></div>
      <div class="rdm-kp-sub">Port ${t.port} · fixture ${at + 1} of ${t.order.length} · now ${f.start != null ? f.start + (f.footprint ? '–' + (f.start + f.footprint - 1) : '') : '—'} · ${f.footprint || 0} ch${rdmFollowOn(t.port) ? ' · identifying' : ''}</div>
      <div class="rdm-kp-val${t.fresh ? ' fresh' : ''}">${esc(t.value === '' ? '—' : t.value)}<span>${esc(range)}</span></div>
      <div class="rdm-kp-warn" title="${esc(warn)}">${t.sending ? 'Sending…' : esc(warn)}</div>
      <div class="rdm-kp-step">Next = this +
        <button class="rdm-key sm" onclick="rdmStepAdj(-1)" ${dis} aria-label="Smaller step">−</button>
        <b>${step ? step : `its ${f.footprint || ''} ch`}</b>
        <button class="rdm-key sm" onclick="rdmStepAdj(1)" ${dis} aria-label="Bigger step">+</button>
        <button class="rdm-key sm${step ? '' : ' on'}" onclick="rdmSetStep('')" ${dis}>CH</button>
      </div>
      ${stepInfo}
    </div>
    <div class="rdm-kp-pad">
      ${[1, 2, 3, 4, 5, 6, 7, 8, 9].map(d => key(d, `rdmKeyDigit(${d})`)).join('')}
      ${key('C', 'rdmKeyClear()', 'fn')}${key('0', 'rdmKeyDigit(0)')}${key('⌫', 'rdmKeyBack()', 'fn')}
    </div>
    <div class="rdm-kp-act">
      ${key(t.sending ? 'SENDING…' : 'SEND &amp; NEXT', 'rdmTypeCommit()', 'go wide')}
      ${key('SEND &amp; DONE', 'rdmTypeCommit(true)', 'go2')}
      ${key('SKIP ▶', 'rdmTypeSkip()')}
      ${key('◀ BACK', 'rdmTypeBack()', at > 0 ? '' : 'off')}
      ${key('CLOSE', 'rdmTypeDone()')}
    </div>
  </div>`;
}

// A real keyboard, when there is one.
document.addEventListener('keydown', e => {
  if (!rdm.typing || e.metaKey || e.ctrlKey || e.altKey) return;
  const tag = (document.activeElement && document.activeElement.tagName) || '';
  if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') return;
  if (/^[0-9]$/.test(e.key)) { e.preventDefault(); rdmKeyDigit(Number(e.key)); }
  else if (e.key === 'Backspace') { e.preventDefault(); rdmKeyBack(); }
  else if (e.key === 'Enter') { e.preventDefault(); rdmTypeCommit(); }
  else if (e.key === 'Tab') { e.preventDefault(); rdmTypeSkip(); }
  else if (e.key === 'Escape') { e.preventDefault(); rdmTypeDone(); }
});

// ── Follow ID ───────────────────────────────────────────────────────────────
// A switch per port. While on, the fixture whose address is being typed (else
// the one whose page is open) and every ticked one identify; any fixture this
// turned on stops when it is none of those. An ID turned on by hand is left alone.

let rdmFollowRunning = false, rdmFollowAgain = false;
const rdmFollowFails = {}; // uid → failed offs in a row

// rdmFollowRelease turns off, without waiting, every ID Follow ID turned on:
// when the node changes or the page closes, so nothing is left identifying.
function rdmFollowRelease() {
  const ip = rdm.ip || currentIp;
  if (!ip || !rdm.followOwned) return;
  for (const [port, owned] of Object.entries(rdm.followOwned)) {
    for (const uid of owned) {
      fetch(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(uid)}`, {
        method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ identify: false }),
      }).catch(() => {});
    }
    owned.clear();
  }
}
window.addEventListener('pagehide', rdmFollowRelease);

// rdmIdOffAll turns identify off on every fixture on a port, whatever turned it
// on, and stops Follow ID there.
async function rdmIdOffAll(port) {
  const ip = currentIp, res = rdm.read[port];
  if (!res || rdmBusy(port)) return;
  if (rdm.follow) rdm.follow[port] = false;
  if (rdm.pick && rdm.pick.port === port) rdm.pick = null;
  if (rdm.followOwned && rdm.followOwned[port]) rdm.followOwned[port].clear();
  rdm.busy[port] = 'Identify off…';
  renderRdm();
  let bad = 0;
  for (const f of res.fixtures.filter(x => !x.error)) {
    try {
      const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(f.uid)}`, 'POST', { identify: false });
      if (ip !== currentIp) return;
      if (d.dryRun) d.fixture.identify = false;
      rdmReplace(port, d.fixture);
    } catch (_) {
      bad++;
    }
    renderRdm();
  }
  delete rdm.busy[port];
  toast(bad ? `Identify off: ${bad} fixture${bad === 1 ? '' : 's'} on port ${port} did not answer` : `Identify off on port ${port}`, !!bad);
  renderRdm();
}

function rdmFollowOn(port) { return !!(rdm.follow && rdm.follow[port]); }

function rdmFollowToggle(port) {
  rdm.follow = rdm.follow || {};
  rdm.follow[port] = !rdm.follow[port];
  renderRdm();
}

// rdmFollowWanted is the fixtures on a port that should identify now: the
// picked tile's (whether Follow ID is on or not), or under Follow ID the
// keypad's instead (never both), and under Follow ID every ticked one.
function rdmFollowWanted(port) {
  const s = new Set(), follow = rdmFollowOn(port);
  if (rdm.typing) { if (follow && rdm.typing.port === port) s.add(rdm.typing.uid); }
  else if (rdm.pick && rdm.pick.port === port) s.add(rdm.pick.uid);
  if (follow) for (const u of rdmSelected(port)) s.add(u);
  return s;
}

async function rdmFollowSync() {
  if (rdmFollowRunning) { rdmFollowAgain = true; return; }
  rdmFollowRunning = true;
  const ip = currentIp;
  rdm.followOwned = rdm.followOwned || {};
  try {
    do {
      rdmFollowAgain = false;
      for (const port of Object.keys(rdm.read).map(Number)) {
        const owned = rdm.followOwned[port] || (rdm.followOwned[port] = new Set());
        const want = rdmFollowWanted(port);
        for (const f of rdm.read[port].fixtures || []) {
          if (f.error) continue;
          let on = null;
          if (want.has(f.uid) && !f.identify) on = true;
          // One this turned on always gets an explicit off: the identify shown can be
          // stale (an address read-back that crossed with the ID on), so it is not trusted.
          else if (!want.has(f.uid) && owned.has(f.uid)) on = false;
          if (on === null) continue;
          if (on) owned.add(f.uid); // before sending: if the reply is lost it may still be on
          try {
            const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(f.uid)}`, 'POST', { identify: on });
            if (ip !== currentIp) return;
            if (d.dryRun) d.fixture.identify = on; // not sent: carry on as if it had been, so this settles
            rdmReplace(port, d.fixture);
            if (!on) owned.delete(f.uid);
            delete rdmFollowFails[f.uid];
          } catch (e) {
            if (ip !== currentIp) return;
            if (on && rdm.pick && rdm.pick.port === port && rdm.pick.uid === f.uid) {
              toast(`${f.label || f.uid}: identify failed: ${e.message}`, true);
              rdm.pick = null;
            } else if (on) {
              toast(`Follow ID stopped on port ${port}: ${e.message}`, true);
              rdm.follow[port] = false;
            } else if ((rdmFollowFails[f.uid] = (rdmFollowFails[f.uid] || 0) + 1) < 3) {
              setTimeout(rdmFollowSync, 800); // try the off again shortly
            } else {
              delete rdmFollowFails[f.uid];
              owned.delete(f.uid);
              toast(`Could not turn identify off on ${f.label || f.uid}: ${e.message}. Try ID OFF.`, true);
            }
          }
          renderRdm();
          if (rdmFollowAgain) break;
        }
      }
    } while (rdmFollowAgain);
  } finally {
    rdmFollowRunning = false;
  }
}

// rdmAfterRender runs after every render of the RDM tab: it lets Follow ID
// catch up with what is picked.
function rdmAfterRender() {
  const owned = rdm.followOwned && Object.values(rdm.followOwned).some(s => s.size);
  if ((rdm.follow && Object.values(rdm.follow).some(Boolean)) || owned || rdm.pick) rdmFollowSync();
}
