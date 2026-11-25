'use strict';
// sACN view: the levels arriving on each universe and who sends them, from
// /api/sacn (internal/sacn). The server listens; this page only asks it what it
// has heard: the list of universes once a second, and the levels of the universe
// being looked at eight times a second. Nothing is asked while another view is up.
// Source names come off the network, so they go through esc() like device text.

const sacnEls = {
  status: $('sacnStatus'), watch: $('sacnWatch'), listen: $('btnSacnWatch'), percent: $('sacnPercent'),
  banner: $('sacnBanner'), universes: $('sacnUniverses'), detail: $('sacnDetail'),
};

let sacnOn = false;
let sacnState = null;       // the last /api/sacn answer
let sacnUniverse = null;    // the universe whose levels are shown
let sacnSource = '';        // a source's CID, or '' for the merged result
let sacnLevels = null;      // the last /api/sacn/universe/<n> answer
let sacnError = '';         // why the last Listen was refused
let sacnBusy = { status: false, levels: false };
let sacnTimers = [];
let sacnPercent = load('commission.sacnPercent', false);
sacnEls.percent.checked = sacnPercent;

// Called by app.js whenever the views are drawn.
function sacnShown(on) {
  if (on === sacnOn) return;
  sacnOn = on;
  sacnTimers.forEach(clearInterval);
  sacnTimers = [];
  if (!on) return;
  sacnPollStatus();
  sacnTimers.push(setInterval(sacnPollStatus, 1000), setInterval(sacnPollLevels, 125));
}

async function sacnPollStatus() {
  if (sacnBusy.status || document.hidden) return;
  sacnBusy.status = true;
  try {
    sacnState = await fxCall('/api/sacn');
  } catch (e) {
    sacnState = { error: 'This page cannot reach the tool: ' + e.message, universes: [], discovered: [], interfaces: [], watch: '' };
  }
  sacnBusy.status = false;
  sacnRender();
}

async function sacnPollLevels() {
  if (sacnBusy.levels || document.hidden || sacnUniverse == null) return;
  sacnBusy.levels = true;
  const u = sacnUniverse;
  try {
    const d = await fxCall(`/api/sacn/universe/${u}`);
    if (u === sacnUniverse) { sacnLevels = d; sacnDrawLevels(); }
  } catch { /* the status poll shows what is wrong */ }
  sacnBusy.levels = false;
}

function sacnBytes(b64) {
  const s = atob(b64 || '');
  const out = new Uint8Array(512);
  for (let i = 0; i < s.length && i < 512; i++) out[i] = s.charCodeAt(i);
  return out;
}

function sacnSourceLine(s) {
  const flags = [];
  if (!s.live) flags.push(s.terminated ? 'stopped' : `lost ${Math.round(s.ageMs / 1000)} s ago`);
  if (s.preview) flags.push('preview');
  return `${esc(s.name || 'unnamed')} <span class="muted">${esc(s.ip)} · priority ${s.priority}${s.live ? ` · ${s.fps} Hz` : ''}${flags.length ? ' · ' + flags.join(', ') : ''}</span>`;
}

// What a universe's tile says, and how worried it looks.
function sacnSummary(u) {
  const live = u.sources.filter(s => s.live && !s.preview);
  const winners = live.filter(s => s.winning);
  if (u.joinError) return { cls: 'bad', text: esc(u.joinError) };
  if (!live.length) return { cls: 'none', text: u.sources.length ? 'Source lost' : 'Nothing arriving' };
  if (winners.length > 1) return { cls: 'warn', text: `${winners.length} sources at priority ${winners[0].priority}: highest level wins` };
  const w = winners[0];
  return { cls: 'ok', text: `${esc(w.name || 'unnamed')} · ${w.fps} Hz` + (live.length > 1 ? ` · +${live.length - 1} at lower priority` : '') };
}

function sacnRender() {
  const st = sacnState;
  if (!st) return;
  const live = st.universes.filter(u => u.sources.some(s => s.live)).length;
  sacnEls.status.textContent = st.error ? '— not listening' : `— ${st.universes.length} universe${st.universes.length === 1 ? '' : 's'}, ${live} with a source`;

  if (document.activeElement !== sacnEls.watch && !sacnEls.watch.dataset.dirty) sacnEls.watch.value = st.watch || '';
  sacnWatchButton();

  let banner = '', cls = 'banner';
  if (st.error) { banner = `Cannot listen for sACN: ${esc(st.error)}`; cls = 'banner err'; }
  else if (sacnError) { banner = esc(sacnError); cls = 'banner err'; }
  else if (!st.interfaces.length) banner = 'This computer has no network connection that can receive sACN. Plug into the lighting network.';
  sacnEls.banner.className = banner ? cls : 'banner hidden';
  sacnEls.banner.innerHTML = banner;

  const found = st.discovered.length
    ? `<div class="sacn-found">Announced on the network: ${st.discovered.map(d => `<strong>${esc(d.name || 'unnamed')}</strong> <span class="muted">${esc(d.ip)} · universes ${esc(d.universes || 'none')}</span>`).join(' &nbsp; ')}</div>`
    : '';
  const where = st.interfaces.length ? `<div class="muted sacn-where">Listening on ${st.interfaces.map(esc).join(', ')}, UDP ${st.port}.</div>` : '';
  if (!st.universes.length) {
    sacnEls.universes.innerHTML = found + (st.error ? '' : `<div class="empty-state small">No universes yet. Type the ones to listen to, for example <code>1-8</code>, and click <strong>Listen</strong>.</div>`) + where;
  } else {
    // A chip per universe, wrapping. The dot is its state; the words are in the
    // tooltip and, for the one that is picked, beside its number below.
    sacnEls.universes.innerHTML = found + `<div class="uni-chips sacn-chips">` + st.universes.map(u => {
      const s = sacnSummary(u);
      return `<button class="uni-chip${u.universe === sacnUniverse ? ' active' : ''}" data-universe="${u.universe}" title="Universe ${u.universe}: ${s.text.replace(/"/g, '&quot;')}">
        <span class="uni-dot ${s.cls}"></span>U${u.universe}</button>`;
    }).join('') + `</div>` + where;
  }
  if (sacnUniverse != null && !st.universes.some(u => u.universe === sacnUniverse)) sacnSelect(null);
  if (sacnUniverse == null && st.universes.length) sacnSelect(st.universes[0].universe); // one is always shown
  if (sacnUniverse == null) sacnEls.detail.innerHTML = '';
  const head = document.getElementById('sacnHeadText'), picked = st.universes.find(u => u.universe === sacnUniverse);
  if (head && picked) { const s = sacnSummary(picked); head.className = 'sacn-head-text sacn-text-' + s.cls; head.innerHTML = s.text; }
}

function sacnSelect(u) {
  sacnUniverse = u;
  sacnSource = '';
  sacnLevels = null;
  sacnEls.detail.innerHTML = '';
  if (u == null) return;
  // The 512 cells are made once; the level poll only changes their numbers.
  let cells = '';
  for (let i = 1; i <= 512; i++) cells += `<div class="sacn-cell"><i>${i}</i><b>·</b></div>`;
  sacnEls.detail.innerHTML = `<div class="sacn-detail-head">Universe ${u} <span id="sacnHeadText" class="sacn-head-text"></span></div>
    <div id="sacnSources" class="sacn-sources"></div>
    <div id="sacnNote" class="fx-note"></div>
    <div id="sacnGrid" class="sacn-grid">${cells}</div>`;
  sacnPollLevels();
}

function sacnDrawLevels() {
  const d = sacnLevels, grid = document.getElementById('sacnGrid');
  if (!d || !grid) return;
  if (sacnSource && !d.sources.some(s => s.cid === sacnSource)) sacnSource = '';
  const shown = d.sources.find(s => s.cid === sacnSource);
  const levels = sacnBytes(shown ? shown.levels : d.merged);
  const slots = shown ? shown.slots : Math.max(0, ...d.sources.filter(s => s.winning).map(s => s.slots));

  const winners = d.sources.filter(s => s.winning);
  const btn = (cid, label, title) => `<button class="btn btn-small sacn-src${cid === sacnSource ? ' active' : ''}" data-cid="${esc(cid)}" title="${title}">${label}</button>`;
  const html = !d.sources.length ? '' :
    btn('', 'On the wire', 'What a node would output: the highest priority, and the highest level where sources share it') +
    d.sources.map(s => btn(s.cid, sacnSourceLine(s), `Only this source. Slots ${s.slots}, lost packets ${s.lost}, late packets ${s.outOfOrder}${s.perAddressPriority ? ', sends per-address priority' : ''}${s.sync ? ', synchronised on universe ' + s.sync : ''}. CID ${esc(s.cid)}`)).join('');
  const src = document.getElementById('sacnSources');
  if (src.dataset.html !== html) { src.innerHTML = html; src.dataset.html = html; }

  let note = '';
  if (d.joinError) note = `<span class="fx-bad">${esc(d.joinError)}</span>`;
  else if (!d.sources.length) note = 'Nothing is arriving on this universe. Check that the source sends it as multicast (or unicast to this computer) and that this computer is on the same network or group.';
  else if (!winners.length) note = '<span class="fx-warn">No live source: these are the last levels heard.</span>';
  else if (winners.length > 1 && !shown) note = `<span class="fx-warn">${winners.length} sources share priority ${winners[0].priority}, so each channel takes the highest level of them.</span>`;
  if (d.sources.some(s => s.perAddressPriority)) note += ' A source here sends per-address priority, which this view does not apply.';
  document.getElementById('sacnNote').innerHTML = note;

  const cells = grid.children;
  for (let i = 0; i < 512; i++) {
    const v = levels[i], c = cells[i];
    const text = i >= slots ? '·' : sacnPercent ? String(Math.round(v / 2.55)) : String(v);
    if (c._v === v && c._t === text) continue;
    c._v = v; c._t = text;
    c.lastChild.textContent = text;
    c.style.setProperty('--l', (v / 2.55).toFixed(1) + '%');
    c.classList.toggle('on', v > 0 && i < slots);
    c.classList.toggle('off', i >= slots);
  }
}

function sacnWatchButton() {
  const changed = sacnState && sacnEls.watch.value.trim() !== (sacnState.watch || '');
  const why = sacnState && sacnState.error ? 'Not listening: see the message below' : changed ? '' : 'Change the list of universes first';
  sacnEls.listen.disabled = !!why;
  sacnEls.listen.title = why || 'Listen to these universes (replaces the list)';
}

async function sacnListen() {
  if (sacnEls.listen.disabled) return;
  sacnError = '';
  try {
    const r = await fetch('/api/sacn/watch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ universes: sacnEls.watch.value }) });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    sacnState = d;
    delete sacnEls.watch.dataset.dirty;
    sacnEls.watch.value = d.watch || '';
  } catch (e) {
    sacnError = 'Could not listen to those universes: ' + e.message;
  }
  sacnRender();
}

sacnEls.watch.addEventListener('input', () => { sacnEls.watch.dataset.dirty = '1'; sacnWatchButton(); });
sacnEls.watch.addEventListener('keydown', ev => { if (ev.key === 'Enter') sacnListen(); });
sacnEls.listen.addEventListener('click', sacnListen);
sacnEls.percent.addEventListener('change', () => { sacnPercent = sacnEls.percent.checked; save('commission.sacnPercent', sacnPercent); sacnDrawLevels(); });
sacnEls.universes.addEventListener('click', ev => {
  const t = ev.target.closest('[data-universe]');
  if (!t) return;
  sacnSelect(Number(t.dataset.universe));
  sacnRender();
});
sacnEls.detail.addEventListener('click', ev => {
  const t = ev.target.closest('.sacn-src');
  if (!t) return;
  sacnSource = t.dataset.cid;
  document.getElementById('sacnSources').dataset.html = '';
  sacnDrawLevels();
});
