'use strict';

// ── State ─────────────────────────────────────────────────────────────────────
let currentIp  = null;
let ws         = null;
let ioFilter   = { cls: 'all', use: 'all', internal: false };
let lastSnap   = {};

// ── Utilities ─────────────────────────────────────────────────────────────────
// nc$ and not $: this runs in the device list's page, whose own $ throws on a
// missing element. esc() is that page's too (app.js).
const nc$ = id => document.getElementById(id);

function toast(msg, err) {
  const el = document.createElement('div');
  el.className = 'toast' + (err ? ' err' : '');
  el.textContent = msg;
  nc$('toast').appendChild(el);
  setTimeout(() => el.remove(), 3500);
}

async function call(url, method = 'GET', body = null) {
  const r = await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : null,
  });
  const d = await r.json();
  if (r.status === 401 && d.needsPassword) openPassword();
  if (!r.ok || (d.ok === false && d.error)) throw new Error(d.error || `HTTP ${r.status}`);
  return d;
}

function modeBadge(mode) {
  const m = (mode || '').toUpperCase();
  const cls = { HTP:'b-htp', LTP:'b-ltp', BACKUP:'b-bak',
    XFADE:'b-xfd', SWITCH:'b-bak' }[m] || 'b-dim';
  return `<span class="badge ${cls}">${esc(m || 'NONE')}</span>`;
}

function ioTypeBadge(ioType) {
  if (!ioType || ioType === 'idle') return `<span class="badge b-dim">IDLE</span>`;
  if (ioType === 'input')  return `<span class="badge b-in">INPUT</span>`;
  if (ioType === 'output') return `<span class="badge b-out">OUTPUT</span>`;
  return `<span class="badge b-dim">${esc(String(ioType).toUpperCase())}</span>`;
}

function activityBadge(state) {
  if (state === 'active')  return `<span class="badge b-ok">ACTIVE</span>`;
  if (state === 'inactive') return `<span class="badge b-err">INACTIVE</span>`;
  return `<span class="badge b-dim">${esc(String(state||'UNKNOWN').toUpperCase())}</span>`;
}

// ── Status ─────────────────────────────────────────────────────────────────────
function setStatus(state) {
  nc$('dot').className = 'dot' + (state === 'on' ? ' on' : state === 'err' ? ' err' : '');
  nc$('conn-text').textContent = state === 'on' ? `ONLINE  ${currentIp}` :
    state === 'err' ? 'ERROR' : 'DISCONNECTED';
}

// ── Tabs ───────────────────────────────────────────────────────────────────────
document.querySelectorAll('.tab').forEach(t => {
  t.onclick = () => {
    document.querySelectorAll('.tab').forEach(x => x.classList.remove('active'));
    document.querySelectorAll('.panel').forEach(x => x.classList.remove('active'));
    t.classList.add('active');
    nc$('p-' + t.dataset.tab).classList.add('active');
  };
});

// IO filters (class / usage / internal toggle)
document.querySelectorAll('[data-iofilter]').forEach(b => {
  b.onclick = () => {
    const which = b.dataset.iofilter;
    if (which === 'internal') ioFilter.internal = !ioFilter.internal;
    else ioFilter[which === 'class' ? 'cls' : 'use'] = b.dataset.val;
    if (lastSnap.allIOs) renderIOs(lastSnap);
    else syncIoFilterButtons();
  };
});

function syncIoFilterButtons() {
  document.querySelectorAll('[data-iofilter="class"]').forEach(x => x.classList.toggle('active-tab', x.dataset.val === ioFilter.cls));
  document.querySelectorAll('[data-iofilter="use"]').forEach(x => x.classList.toggle('active-tab', x.dataset.val === ioFilter.use));
  nc$('io-internal-toggle').classList.toggle('active-tab', ioFilter.internal);
}

// ── Connect ────────────────────────────────────────────────────────────────────
nc$('btn-refresh').onclick = () => currentIp && loadNode(currentIp);

function clearDisplays() {
  const empty = msg => `<div class="empty"><div class="icon">◈</div><p>${msg}</p></div>`;
  nc$('overview-body').innerHTML  = empty('No device connected');
  nc$('pb-body').innerHTML        = empty('No device connected');
  nc$('io-body').innerHTML        = empty('No device connected');
  nc$('io-count').textContent     = '';
  nc$('dmx-body').innerHTML       = empty('No device connected');
  nc$('net-body').innerHTML       = empty('No device connected');
  nc$('prof-body').innerHTML      = empty('No device connected');
  nc$('qs-ports').innerHTML       = empty('Connect to a device first');
  nc$('qs-hint').textContent      = 'Connect to a device first.';
  nc$('btn-qs-apply').disabled    = true;
  nc$('btn-identify').disabled    = true;
  nc$('btn-reboot').disabled      = true;
  nc$('btn-refresh').disabled     = true;
  ['live-pb','live-io','live-dmx'].forEach(id => nc$(id).style.display = 'none');
  rdmReset('No device connected');
  window._netCurrent = null;
  window._devCurrent = null;
  lastSnap = {};
  if (ws) {
    try { ws.send(JSON.stringify({ type: 'unsubscribe' })); } catch (_) {}
    ws.close();
    ws = null;
  }
  setStatus('err');
}

async function doConnect(addr) {
  currentIp = addr;
  setStatus('');
  nc$('btn-refresh').disabled = true;
  nc$('btn-identify').disabled = true;
  nc$('btn-reboot').disabled = true;
  // Another node may be opened while this one is still answering (a chip is one
  // click, and a node that is away takes 5 s to fail): only the node the page is
  // on now may draw, or clear, the page.
  try {
    await loadNode(addr);
    if (currentIp !== addr) return;
    setStatus('on');
    nc$('btn-refresh').disabled = false;
    nc$('btn-identify').disabled = false;
    nc$('btn-reboot').disabled = false;
    startWS(addr);
  } catch (e) {
    if (currentIp !== addr) return;
    setStatus('err');
    clearDisplays();
    toast(`Cannot reach ${addr}: ${e.message}`, true);
  }
}

// After an address change a real node took about 5 s to answer on the new
// address, so a single try after 3 s used to fail and clear the page. Try for
// up to 20 s, then connect (which reports the failure if it is still away).
async function reconnectWhenBack(addr) {
  for (const end = Date.now() + 20000; Date.now() < end;) {
    await new Promise(r => setTimeout(r, 2000));
    try { await call(`/api/node/${addr}/snapshot`); break; } catch (_) {}
  }
  doConnect(addr);
}

async function loadNode(addr) {
  const d = await call(`/api/node/${addr}/snapshot`);
  if (addr !== currentIp) return; // the page has moved to another node since this was asked
  lastSnap = d;
  if (d.device)        renderOverview(d.device, addr);
  if (d.processBlocks) renderProcessBlocks(d.processBlocks);
  if (d.dmxIOs || d.sacnIOs || d.allIOs) renderIOs(d);
  if (d.dmxPorts)      renderDmxPorts(d.dmxPorts);
  if (d.network)       renderNetwork(d.network);
  if (d.profiles)      renderProfiles(d.profiles);
  if (d.device)        renderQuickSetup(d.device.dmxPorts);
  if (rdm.ip && rdm.ip !== addr) rdmReset('Open this tab again to read RDM');
  if (nc$('p-rdm').classList.contains('active')) rdmLoad();
}

// ── Renderers ─────────────────────────────────────────────────────────────────

function renderOverview(d, addr) {
  window._devCurrent = d; // Store for network edit modal
  nc$('overview-body').innerHTML = `<div class="grid">
    ${kcard('Model',          d.model)}
    ${kcard('Short Name',     d.shortName)}
    ${kcard('Long Name',      d.longName)}
    ${kcard('Serial',         d.serial)}
    ${kcard('MAC Address',    d.mac)}
    ${kcard('IP Address',     addr, 'hi')}
    ${kcard('Default IP',     d.defaultIp)}
    ${kcard('DMX Ports',      d.dmxPorts)}
    ${kcard('Process Blocks', d.processBlocks)}
    ${kcard('Uptime',         d.uptime)}
    ${kcard('Auth Enabled',   d.authEnabled ? 'YES' : 'NO', d.authEnabled ? 'err' : 'ok')}
  </div>`;
}

function kcard(label, val, cls) {
  return `<div class="card"><div class="card-body">
    <div class="kv"><span class="k">${esc(label)}</span></div>
    <div style="font-family:var(--ff);font-size:13px;margin-top:3px;
      word-break:break-all;color:${cls==='hi'?'var(--cyan)':cls==='ok'?'var(--green)':cls==='err'?'var(--red)':'var(--bright)'}"
    >${esc(val) || '—'}</div>
  </div></div>`;
}

const MERGE_MODES = ['HTP', 'LTP', 'BACKUP', 'XFADE', 'SWITCH'];
// How many inputs each mode really keeps, measured on a LumiNode 12 (v2.9.1). The node
// accepts more and quietly drops them. The server checks this too.
const MODE_SLOTS = { REROUTE: 1, BACKUP: 2, XFADE: 2, SWITCH: 4, HTP: 4, LTP: 4, CUSTOM: 4 };
const slotsFor = mode => MODE_SLOTS[mode] || 1;

// One IO reference as the operator reads it: name (when the device has one
// that adds information), what it is, its settings, and the raw id last.
function ioRefHtml(ref) {
  if (!ref) return '<span class="none">none</span>';
  if (ref.missing) return `<span class="missing">${esc(ref.label)} (not in IO list)</span>`;
  const showName = ref.name && ref.name !== ref.label && !ref.label.endsWith(ref.name);
  return (showName ? `<span class="io-name">${esc(ref.name)}</span> ` : '') +
    `${esc(ref.label)}` +
    (ref.detail ? ` <span class="det">(${esc(ref.detail)})</span>` : '') +
    `<span class="ioid">#${ref.id}</span>`;
}

function ctrlLabel(role) {
  return { backup: 'BACKUP CTRL', switch: 'SWITCH CTRL', xfade: 'XFADE CTRL', masterlimit: 'MASTER LIMIT CTRL' }[role] || role.toUpperCase();
}

function pbButtons(pb) {
  return `<button class="btn sm sec" onclick="openSetInput(${pb.id})" title="Set this block's input slots and its outputs">INPUTS / OUTPUTS</button>
    <button class="btn sm" onclick="openEditPB(${pb.id})">EDIT</button>
    <button class="btn sm dan" onclick="clearPB(${pb.id})" title="Reset this block to NONE (fires immediately)">CLEAR</button>`;
}

function pbCard(pb) {
  const inputs = pb.inputs || [], outputs = pb.outputs || [], controls = pb.controls || [];
  const merge = MERGE_MODES.includes(pb.mode);
  let wiring;
  if (merge) {
    // Every slot (a block has four; show more if the node reports a higher
    // one), the active one marked, with the senders the node sees on it.
    const bySlot = {};
    inputs.forEach(i => bySlot[i.slot] = i);
    const nSlots = Math.max(slotsFor(pb.mode), ...inputs.map(i => i.slot + 1));
    wiring = `<div class="slots">${Array.from({ length: nSlots }, (_, n) => {
      const i = bySlot[n];
      if (!i) return `<div class="slot"><span class="sn">SLOT ${n + 1}</span><span class="sv vacant">empty</span></div>`;
      return `<div class="slot${i.active ? ' active' : ''}"><span class="sn">SLOT ${n + 1}</span>
        <span class="sv">${ioRefHtml(i.io)}${i.active ? '<span class="act">ACTIVE</span>' : ''}
          ${(i.sources || []).map(src => `<span class="src">seeing ${esc(src)}</span>`).join('')}</span></div>`;
    }).join('')}</div>
    <div class="out-line"><span class="arr" style="color:var(--amber);margin-right:6px">→</span>${outputs.length ? outputs.map(ioRefHtml).join('<span class="plus">,</span> ') : '<span class="none" style="color:var(--dim);font-style:italic">no output</span>'}</div>`;
  } else {
    const left = inputs.length ? inputs.map(i => ioRefHtml(i.io) + ((i.sources || []).length ? ` <span class="det">seeing ${esc(i.sources.join(', '))}</span>` : '')).join('<span class="plus">+</span>') : '<span class="none">no input</span>';
    const right = outputs.length ? outputs.map(ioRefHtml).join('<span class="plus">,</span> ') : '<span class="none">no output</span>';
    wiring = `<div class="flow">${left}<span class="arr">→</span>${right}</div>`;
  }
  const rows = [];
  controls.forEach(c => rows.push(`<div class="kv"><span class="k">${ctrlLabel(c.role)}</span><span class="v">${ioRefHtml(c.io)}</span></div>`));
  const wanted = MODE_CONTROL[pb.mode];
  if (wanted && !controls.some(c => c.role === wanted)) rows.push(`<div class="kv"><span class="k">${ctrlLabel(wanted)}</span><span class="v dim">not set</span></div>`);
  if (pb.masterlimit && pb.masterlimit !== 'NONE' && !controls.some(c => c.role === 'masterlimit'))
    rows.push(`<div class="kv"><span class="k">MASTER LIMIT</span><span class="v">${esc(pb.masterlimit)}</span></div>`);
  if (pb.mode === 'BACKUP') {
    rows.push(`<div class="kv"><span class="k">TIMEOUT</span><span class="v">${pb.timeout} ms</span></div>`);
    rows.push(`<div class="kv"><span class="k">AUTO-RECOVER</span><span class="v ${pb.backup ? 'ok' : 'dim'}">${pb.backup ? 'YES' : 'NO'}</span></div>`);
  }
  const chips = [];
  if (pb.startupBuffer) chips.push(`<span class="chip-s info" title="Startup buffer stored on the node">STARTUP BUFFER ${pb.startupBuffer} ch</span>`);
  if (pb.masterlimit && pb.masterlimit !== 'NONE') chips.push(`<span class="chip-s">${esc(pb.masterlimit)}</span>`);
  if (pb.ratelimit === false) chips.push(`<span class="chip-s warn" title="Output not capped at 64 fps">RATE LIMIT OFF</span>`);
  if (pb.takeoverActive) chips.push(`<span class="chip-s warn">TAKEOVER ACTIVE</span>`);
  return `<div class="card">
    <div class="card-head">
      <span class="card-title"><span class="swatch" style="background:${esc(pb.color || '#2d3650')}"></span>${esc(pb.name)}<span class="pbid">PB ${pb.id}</span></span>
      ${modeBadge(pb.mode)}
    </div>
    <div class="card-body">
      ${wiring}
      ${rows.join('')}
      ${chips.length ? `<div class="chips-row">${chips.join('')}</div>` : ''}
    </div>
    <div class="card-foot">${pbButtons(pb)}</div>
  </div>`;
}

function pbRow(pb) {
  return `<div class="pb-row">
    <span class="swatch" style="background:${esc(pb.color || '#2d3650')}"></span>
    <span class="pbid">PB ${pb.id}</span><span class="nm">${esc(pb.name)}</span>
    ${modeBadge(pb.mode)}
    <span style="display:flex;gap:6px">${pbButtons(pb)}</span>
  </div>`;
}

function renderProcessBlocks(list) {
  if (!list || !list.length) {
    nc$('pb-body').innerHTML = `<div class="empty"><div class="icon">⚙</div>
      <p>No process blocks found</p></div>`;
    return;
  }
  const used  = list.filter(pb => pb.configured);
  const spare = list.filter(pb => !pb.configured);
  let html = used.length
    ? `<div class="grid2">${used.map(pbCard).join('')}</div>`
    : `<div class="empty" style="padding:20px"><p>No process block is configured — use Quick Setup or INPUTS / OUTPUTS on a block below</p></div>`;
  if (spare.length) {
    html += `<div class="sub-head">${spare.length} UNUSED BLOCK${spare.length === 1 ? '' : 'S'} — MODE NONE, NOTHING ATTACHED<span class="line"></span></div>
      ${spare.map(pbRow).join('')}`;
  }
  nc$('pb-body').innerHTML = html;
}

function classBadge(cls) {
  const map = { sacn: ['b-sacn', 'sACN'], artnet: ['b-artnet', 'ART-NET'], dmx: ['b-dmx', 'DMX'], internal: ['b-int', 'INTERNAL'] };
  const [c, t] = map[cls] || ['b-dim', String(cls || '?').toUpperCase()];
  return `<span class="badge ${c}">${esc(t)}</span>`;
}

function useHtml(io) {
  if (!io.usedBy || !io.usedBy.length) return `<span class="v dim">unused</span>`;
  return `<span class="use">${io.usedBy.map(u => {
    const role = u.role === 'input' ? `input slot ${u.slot + 1}` : u.role === 'output' ? 'output' : `${u.role} ctrl`;
    return `PB ${u.pb} ${esc(u.pbName)} <span class="role">· ${role}</span>`;
  }).join('<br>')}</span>`;
}

function ioCard(io) {
  const kv = (k, v, cls) => `<div class="kv"><span class="k">${k}</span><span class="v${cls ? ' ' + cls : ''}">${v}</span></div>`;
  const rows = [];
  const network = io.ioClass === 'sacn' || io.ioClass === 'artnet';
  if (network) {
    rows.push(kv('UNIVERSE', io.universe, 'hi'));
    if (io.ioClass === 'sacn' && io.priority !== null)
      rows.push(kv('PRIORITY', `${io.priority}${io.perChannelPriority ? ' <span style="color:var(--dim)">per-channel</span>' : ''}`));
    if (io.group !== null) rows.push(kv('GROUP', io.group || '<span class="dim">0 — none</span>'));
    if (io.ip) rows.push(kv('SOURCE IP', io.ip === '0.0.0.0' ? 'any' : esc(io.ip)));
    if (io.takeOwnIp !== null)
      rows.push(kv('OWN IP AS SOURCE', io.takeOwnIp ? 'accepted' : '<span style="color:var(--amber)">filtered out</span>'));
  } else if (io.ioClass === 'dmx') {
    rows.push(kv('DMX PORT', io.portNumber + 1, 'hi'));
    rows.push(kv('RDM', io.rdm + (io.rdmUniverse !== '—' ? ` <span style="color:var(--dim)">universe ${io.rdmUniverse}</span>` : ''), io.rdm === 'ON' ? 'ok' : ''));
    rows.push(kv('INTERWEAVING', io.interweaving ? 'ON' : 'OFF'));
    if (io.backup) rows.push(kv('BACKUP', 'YES'));
    if (io.follow) rows.push(kv('FOLLOW', 'YES'));
    rows.push(kv('ACTIVITY TIMEOUT', `${io.streamActivityTime} ms`));
  } else if (io.ioClass === 'internal') {
    rows.push(kv('PROCESS BLOCK', io.pbNumber !== null ? `PB ${io.pbNumber}` : '—'));
  } else if (io.universe !== null) {
    rows.push(kv('UNIVERSE', io.universe, 'hi'));
  }
  rows.push(`<div class="kv"><span class="k">USED BY</span>${useHtml(io)}</div>`);
  const dup = (io.duplicateIos || []).length
    ? `<div class="chips-row"><span class="chip-s warn" title="The node reports another IO with the same configuration">DUPLICATE OF ${io.duplicateIos.map(d => '#' + d).join(', ')}</span></div>` : '';
  const title = io.name && io.name !== io.label
    ? `${esc(io.name)}<span class="dimlbl">${esc(io.label)}</span>` : esc(io.label);
  return `<div class="card">
    <div class="card-head">
      <span class="card-title">${title}<span class="ioid">#${io.id}</span></span>
      <span style="display:flex;gap:4px">${classBadge(io.ioClass)}${ioTypeBadge(io.ioType)}</span>
    </div>
    <div class="card-body">${rows.join('')}${dup}</div>
  </div>`;
}

const IO_GROUPS = [['sacn', 'sACN'], ['artnet', 'ART-NET'], ['dmx', 'DMX PORTS'], ['other', 'OTHER'], ['internal', 'INTERNAL']];

function renderIOs(snap) {
  syncIoFilterButtons();
  const all = snap.allIOs || [];
  const internal = all.filter(io => io.ioClass === 'internal');
  const shown = all.filter(io => {
    if (io.ioClass === 'internal' && !ioFilter.internal) return false;
    if (ioFilter.cls !== 'all' && io.ioClass !== ioFilter.cls) return false;
    if (ioFilter.use === 'used' && !io.inUse) return false;
    if (ioFilter.use === 'unused' && io.inUse) return false;
    return true;
  });
  const inUse = all.filter(io => io.inUse && io.ioClass !== 'internal').length;
  nc$('io-count').textContent = `${all.length - internal.length} IOs · ${inUse} in use` +
    (internal.length ? ` · ${internal.length} internal ${ioFilter.internal ? 'shown' : 'hidden'}` : '');
  nc$('io-internal-toggle').textContent = `${ioFilter.internal ? 'HIDE' : 'SHOW'} INTERNAL${internal.length ? ` (${internal.length})` : ''}`;

  if (!all.length) {
    nc$('io-body').innerHTML = `<div class="empty"><div class="icon">⬡</div><p>No IOs configured</p></div>`;
    return;
  }
  if (!shown.length) {
    nc$('io-body').innerHTML = `<div class="empty"><div class="icon">⬡</div><p>No IOs match these filters</p></div>`;
    return;
  }
  const groupOf = io => IO_GROUPS.some(([k]) => k === io.ioClass) ? io.ioClass : 'other';
  nc$('io-body').innerHTML = IO_GROUPS.map(([key, title]) => {
    const items = shown.filter(io => groupOf(io) === key);
    if (!items.length) return '';
    const used = items.filter(io => io.inUse).length;
    return `<div class="sub-head">${title} — ${items.length}${key === 'internal' ? '' : `, ${used} IN USE`}<span class="line"></span></div>
      <div class="grid2">${items.map(ioCard).join('')}</div>`;
  }).join('');
}

function renderDmxPorts(list) {
  if (!list || !list.length) {
    nc$('dmx-body').innerHTML = `<div class="empty"><div class="icon">◎</div>
      <p>No DMX port data</p></div>`;
    return;
  }
  nc$('dmx-body').innerHTML = `<div class="grid3">${list.map(p => `
    <div class="card">
      <div class="card-head">
        <span class="card-title">DMX PORT ${p.portNumber + 1}</span>
        ${activityBadge(p.activityState)}
      </div>
      <div class="card-body">
        <div class="kv"><span class="k">STATE</span>
          <span class="v">${esc(p.activityState)}</span></div>
        <div class="kv"><span class="k">BROKEN</span>
          <span class="v ${p.broken ? 'err' : 'ok'}">${p.broken ? 'YES' : 'NO'}</span></div>
        <div class="kv"><span class="k">BACKUP TX</span>
          <span class="v">${p.backupActive ? 'ACTIVE' : 'NO'}</span></div>
        <div class="kv"><span class="k">IN FRAMES</span>
          <span class="v">${p.inputFrames}</span></div>
        <div class="kv"><span class="k">OUT FRAMES</span>
          <span class="v">${p.outputFrames}</span></div>
        <div class="kv"><span class="k">PIPE FRAMES</span>
          <span class="v">${p.pipelineFrames}</span></div>
      </div>
    </div>`).join('')}</div>`;
}

function renderNetwork(d) {
  // Store current values for the edit modal
  window._netCurrent = d;
  nc$('net-body').innerHTML = `
    <div style="margin-bottom:16px">
      <button class="btn pri" onclick="openNetEdit()">✎ EDIT SETTINGS</button>
    </div>
    <div class="grid">
      ${kcard('IP Address',   d.ip,  'hi')}
      ${kcard('Netmask',      d.mask)}
      ${kcard('Gateway',      d.gateway)}
      ${kcard('Broadcast',    d.broadcast)}
      ${kcard('Basic Config', d.basicConfig ? 'YES' : 'NO')}
    </div>`;
}

function openNetEdit() {
  if (!currentIp) { toast('No device connected', true); return; }
  const d = window._netCurrent || {};
  const dev = window._devCurrent || {};

  nc$('net-long-name').value  = dev.longName  || '';
  nc$('net-short-name').value = dev.shortName || '';
  nc$('net-ip').value         = d.ip      || '';
  nc$('net-mask').value       = d.mask    || '';
  nc$('net-gateway').value    = d.gateway || '0.0.0.0';

  // Show IP change warning when user edits IP field
  nc$('net-ip').oninput = () => {
    const changed = nc$('net-ip').value.trim() !== (d.ip || '').trim();
    nc$('net-ip-warning').style.display = changed ? 'block' : 'none';
  };

  nc$('net-ip-warning').style.display = 'none';
  nc$('net-modal').classList.add('open');
}

function renderProfiles(list) {
  if (!list || !list.length) {
    nc$('prof-body').innerHTML = `<div class="empty"><div class="icon">◧</div>
      <p>No profiles saved on device</p></div>`;
    return;
  }
  nc$('prof-body').innerHTML = `<div class="grid">${list.map(p => `
    <div class="card"><div class="card-body">
      <div style="font-size:14px;font-weight:600;color:var(--bright);margin-bottom:4px">
        ${esc(p.name)}</div>
      <div style="font-family:var(--ff);font-size:10px;color:var(--dim);margin-bottom:10px">
        Profile ${esc(p.id)}</div>
      <div style="display:flex;gap:6px">
        <button class="btn sm dan" onclick="recallProfile(${p.id}, false)" title="Replaces every setting on the device, including its IP">▶ RECALL</button>
        <button class="btn sm warn" onclick="recallProfile(${p.id}, true)" title="Replaces every setting except the IP settings">
          ▶ KEEP IP</button>
      </div>
    </div></div>`).join('')}</div>`;
}

// ── App version ────────────────────────────────────────────────────────────────
// A page left open while the app is restarted as a newer release would carry on
// with the old code (it is only loaded once). The page asks the app's version
// whenever its live link (re)connects and reloads itself, on the same node and
// tab, when it has changed.
let appVersion = null;
async function checkAppVersion() {
  try {
    const v = (await call('/api/app-version')).version;
    if (!v) return;
    if (appVersion && v !== appVersion) {
      const tab = document.querySelector('.tab.active');
      try { if (tab) sessionStorage.setItem('nc_tab', tab.dataset.tab); } catch (_) {}
      location.reload();
      return;
    }
    appVersion = v;
  } catch (_) {}
}
checkAppVersion();
document.addEventListener('visibilitychange', () => { if (!document.hidden) checkAppVersion(); });

// ── WebSocket ──────────────────────────────────────────────────────────────────
// The live link reconnects every 2 s while it is down (the app restarting).
function startWS(addr, again = false) {
  if (ws) ws.close();
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const me = ws = new WebSocket(`${proto}://${location.host}/api/live`);
  ws.onopen = () => {
    if (again) checkAppVersion();
    ws.send(JSON.stringify({ type: 'subscribe', ip: addr }));
    ['live-pb','live-io','live-dmx'].forEach(id => nc$(`${id}`).style.display = 'inline-flex');
  };
  ws.onmessage = e => {
    try {
      const m = JSON.parse(e.data);
      if (m.type === 'error') {
        // Device not answering. The server keeps polling and sends an update
        // when it is back, so the page only shows the state meanwhile.
        toast(`Lost connection to ${m.ip || currentIp} — still trying`, true);
        setStatus('err');
        return;
      }
      if (m.type === 'rdm') { if (m.ip === currentIp && rdm.ip === m.ip) rdmApplyShared(m.rdm); return; }
      if (m.type !== 'update') return;
      // Successful update — ensure status shows online
      if (m.ip === currentIp) setStatus('on');
      // Merge into lastSnap and re-render only changed sections
      if (m.dmxIOs  || m.sacnIOs || m.allIOs) {
        lastSnap = { ...lastSnap, ...m };
        renderIOs(lastSnap);
      }
      // The server sends blocks only when they changed; guard again here so a
      // redraw (which rebuilds the buttons) never happens for identical data.
      if (m.processBlocks && JSON.stringify(m.processBlocks) !== JSON.stringify(lastSnap.processBlocks)) {
        lastSnap.processBlocks = m.processBlocks;
        renderProcessBlocks(m.processBlocks);
      }
      if (m.dmxPorts) {
        lastSnap.dmxPorts = m.dmxPorts;
        renderDmxPorts(m.dmxPorts);
      }
    } catch (_) {}
  };
  ws.onclose = () => {
    ['live-pb','live-io','live-dmx'].forEach(id => nc$(`${id}`).style.display = 'none');
    if (ws !== me) return; // replaced by a link to another node
    setTimeout(() => {
      if (ws !== me || currentIp !== addr) return;
      startWS(addr, true);
    }, 2000);
  };
}

// applyState merges a write's response (blocks and IOs, each resolved
// through the other) into lastSnap and redraws both pages.
function applyState(res) {
  if (!res) return;
  if (res.processBlocks) lastSnap.processBlocks = res.processBlocks;
  if (res.allIOs) { lastSnap.allIOs = res.allIOs; lastSnap.dmxIOs = res.dmxIOs; lastSnap.sacnIOs = res.sacnIOs; }
  if (res.processBlocks) renderProcessBlocks(lastSnap.processBlocks);
  if (res.allIOs) renderIOs(lastSnap);
}

// ── Process Block edit ─────────────────────────────────────────────────────────
function pbById(id) {
  return (lastSnap.processBlocks || []).find(p => p.id === id) || { id, name: '', mode: 'NONE', inputIds: [], timeout: 0, backup: true, ratelimit: true };
}

function openEditPB(id) {
  const pb = pbById(id);
  nc$('pb-modal-title').textContent = `EDIT PROCESS BLOCK ${pb.id}`;
  nc$('pb-idx').value       = pb.id;
  nc$('pb-name').value      = pb.name || '';
  nc$('pb-mode').value      = pb.mode || 'NONE';
  nc$('pb-timeout').value   = pb.timeout ?? 1000;
  nc$('pb-autorecov').checked = pb.backup;
  nc$('pb-ratelimit').checked = pb.ratelimit !== false;
  nc$('pb-masterlimit').value = pb.masterlimit || 'NONE';
  pbEditHasIOs = !!((pb.inputs && pb.inputs.length) || (pb.outputs && pb.outputs.length));
  pbEditSlots = (pb.inputs || []).map(i => i.slot);
  syncModeWarning();
  nc$('pb-modal').classList.add('open');
}

// Some modes lose something on the node when saved. Mode NONE clears the block: inputs
// and outputs go, the name resets and an IO left unused is deleted. A mode with fewer
// slots drops the inputs beyond them. Warn in place; Save still fires on the click.
let pbEditHasIOs = false, pbEditSlots = [];
function syncModeWarning() {
  const mode = nc$('pb-mode').value, limit = slotsFor(mode);
  const dropped = pbEditSlots.filter(s => s >= limit).map(s => s + 1);
  let text = '';
  if (mode === 'NONE' && pbEditHasIOs) text = "Saving NONE removes this block's inputs and outputs, resets its name and can delete an IO nothing else uses.";
  else if (mode !== 'NONE' && dropped.length) text = `${mode} takes ${limit} input${limit === 1 ? '' : 's'}. Saving drops slot ${dropped.join(', ')}, and changing back won't restore them.`;
  nc$('pb-none-warn').textContent = text;
  nc$('pb-none-warn').style.display = text ? 'block' : 'none';
}
nc$('pb-mode').onchange = syncModeWarning;

nc$('pb-modal-close').onclick =
nc$('pb-cancel').onclick = () => nc$('pb-modal').classList.remove('open');

nc$('pb-save').onclick = async () => {
  if (!currentIp) return;
  const idx = nc$('pb-idx').value;
  const body = {
    name:               nc$('pb-name').value,
    mode:               nc$('pb-mode').value,
    timeout:            Number.isNaN(parseInt(nc$('pb-timeout').value)) ? 1000 : parseInt(nc$('pb-timeout').value),
    backup_auto_recover:nc$('pb-autorecov').checked,
    ratelimiting:       nc$('pb-ratelimit').checked,
    masterlimit:        nc$('pb-masterlimit').value,
  };
  try {
    const res = await call(`/api/node/${currentIp}/processblock/${idx}`, 'PATCH', body);
    nc$('pb-modal').classList.remove('open');
    applyState(res);
    toast('Process block updated');
  } catch (e) { toast('Save failed: ' + e.message, true); }
};

nc$('pb-clear').onclick = async () => {
  const idx = nc$('pb-idx').value;
  try {
    const res = await call(`/api/node/${currentIp}/processblock/${idx}`, 'DELETE');
    nc$('pb-modal').classList.remove('open');
    applyState(res);
    toast(`Process block ${idx} cleared`);
  } catch (e) { toast('Clear failed: ' + e.message, true); }
};

async function clearPB(id) {
  try {
    const res = await call(`/api/node/${currentIp}/processblock/${id}`, 'DELETE');
    applyState(res);
    toast(`Process block ${id} cleared`);
  } catch (e) { toast('Failed: ' + e.message, true); }
}

// ── Profile recall ─────────────────────────────────────────────────────────────
async function recallProfile(id, keepIp) {
  try {
    toast(`Recalling profile ${id}${keepIp ? ' (keeping IP settings)' : ''}…`);
    await call(`/api/node/${currentIp}/profile/${id}/recall`, 'POST', { keep_ip_settings: keepIp });
    toast(`Profile ${id} recalled — refreshing...`);
    setTimeout(() => loadNode(currentIp), 1500);
  } catch (e) { toast('Recall failed: ' + e.message, true); }
}

// ── Identify / Reboot ──────────────────────────────────────────────────────────
nc$('btn-identify').onclick = async () => {
  try {
    await call(`/api/node/${currentIp}/identify`, 'POST');
    toast('Identify triggered — check device LEDs');
  } catch (e) { toast('Failed: ' + e.message, true); }
};

nc$('btn-reboot').onclick = async () => {
  try {
    await call(`/api/node/${currentIp}/reboot`, 'POST');
    toast('Reboot command sent');
    setStatus('');
    nc$('btn-refresh').disabled = true;
  } catch (e) { toast('Failed: ' + e.message, true); }
};

// ── Quick Setup ───────────────────────────────────────────────────────────────
let numDmxPorts = 4; // updated from device info on connect

function renderQuickSetup(n) {
  numDmxPorts = n || numDmxPorts;
  const container = nc$('qs-ports');
  if (!currentIp) {
    container.innerHTML = '<div class="empty"><div class="icon">⚡</div><p>Connect to a device first.</p></div>';
    return;
  }

  // Build port cards — read current config from lastSnap if available
  container.innerHTML = Array.from({ length: numDmxPorts }, (_, i) => {
    const portNum = i + 1;
    // Try to find current universe for this port from process blocks
    const pb = (lastSnap.processBlocks || []).find(p => p.id === i);
    // Find the sACN IO wired into this process block
    const inputId  = (pb && pb.inputIds && pb.inputIds.length > 0) ? pb.inputIds[0] : null;
    const inputIO  = (inputId !== null && inputId !== undefined)
      ? (lastSnap.allIOs || []).find(io => io.id === inputId)
      : null;
    const currentUni = (inputIO && inputIO.universe !== null && inputIO.universe !== undefined) ? inputIO.universe : '';
    const uniLabel   = inputIO
      ? (inputIO.ioClass === 'artnet' ? 'ARTNET UNIVERSE' : 'sACN UNIVERSE')
      : 'UNIVERSE';

    return `<div class="qs-port" id="qs-port-${portNum}">
      <div class="qs-port-head">
        <div>
          <div class="qs-port-num">PORT ${portNum}</div>
          <div class="qs-port-label">DMX Output</div>
        </div>
        ${currentUni ? `<span class="badge b-in">U${currentUni}</span>` : '<span class="badge b-dim">NOT SET</span>'}
      </div>
      <div class="qs-port-body">
        <span class="qs-uni-label" id="qs-uni-label-${portNum}">${uniLabel}</span>
        <input class="qs-uni-input" id="qs-uni-${portNum}" type="number"
          min="1" max="63999" placeholder="—"
          value="${currentUni}"
          onchange="validateQSPort(${portNum})">
      </div>
      <div class="qs-status" id="qs-status-${portNum}"></div>
    </div>`;
  }).join('');

  nc$('btn-qs-apply').disabled = false;
  nc$('qs-hint').textContent = 'All ports will be configured immediately.';
}

function validateQSPort(portNum) {
  const val = parseInt(nc$(`qs-uni-${portNum}`).value);
  const status = nc$(`qs-status-${portNum}`);
  if (!val || val < 1 || val > 63999) {
    status.textContent = 'Universe must be 1–63999';
    status.className = 'qs-status err';
  } else {
    status.textContent = `Will route Universe ${val} → DMX Port ${portNum}`;
    status.className = 'qs-status ok';
  }
}

nc$('btn-qs-apply').onclick = async () => {
  const btn = nc$('btn-qs-apply');

  // Collect port configs — skip any with blank universe
  const ports = [];
  for (let i = 1; i <= numDmxPorts; i++) {
    const val = parseInt(nc$(`qs-uni-${i}`) ? nc$(`qs-uni-${i}`).value : '');
    if (val && val >= 1 && val <= 63999) ports.push({ port: i, universe: val });
  }

  if (!ports.length) { toast('Enter at least one universe', true); return; }

  btn.disabled = true;
  btn.textContent = '⚡ APPLYING...';
  nc$('qs-hint').textContent = `Configuring ${ports.length} port(s)...`;

  try {
    const res = await call(`/api/node/${currentIp}/quicksetup`, 'POST', { ports });

    if (res.errors && res.errors.length) {
      res.errors.forEach(e => toast(e, true));
    }

    // Update all sections from returned snapshot
    lastSnap = { ...lastSnap, ...res };
    applyState(res);
    if (res.dmxPorts) renderDmxPorts(res.dmxPorts);

    // Re-render quick setup to show new current universes
    renderQuickSetup(numDmxPorts);

    // Mark each port status
    ports.forEach(({ port, universe }) => {
      const failed = (res.errors || []).some(e => e.startsWith(`Port ${port}`));
      const status = nc$(`qs-status-${port}`);
      if (status) {
        status.textContent = failed ? 'Failed — check console' : `✓ Configured U${universe}`;
        status.className = 'qs-status ' + (failed ? 'err' : 'ok');
      }
    });

    if (!res.errors || !res.errors.length) toast(`${ports.length} port(s) configured`);

  } catch (e) {
    toast('Apply failed: ' + e.message, true);
    nc$('qs-hint').textContent = 'Error — check console window.';
  } finally {
    btn.disabled = false;
    btn.textContent = '⚡ APPLY';
  }
};


// ── Set Input Modal ───────────────────────────────────────────────────────────

nc$('input-modal-close').onclick =
nc$('input-cancel').onclick = () => nc$('input-modal').classList.remove('open');

// A block's wiring: one row per input slot (four in a merge mode, one otherwise) and
// its outputs, which can be a network universe or a DMX port and can be several.
const universeLimits = { sacn: [1, 63999], artnet: [0, 32767] };
let wiringBlock = null;

function netIO(id) {
  const io = (lastSnap.allIOs || []).find(x => x.id === id);
  if (io && io.ioClass === 'sacn') return { protocol: 'sacn', universe: io.universe };
  if (io && io.ioClass === 'artnet') return { protocol: 'artnet', universe: io.artnetUniverse ?? io.universe };
  return null;
}

function inputRowHtml(slot, cur) {
  const net = cur ? netIO(cur.io.id) : null;
  const keep = cur && !net;
  const pick = net ? net.protocol : keep ? 'keep' : '';
  const opts = [['', 'Empty'], ['sacn', 'sACN'], ['artnet', 'Art-Net']]
    .concat(keep ? [['keep', 'Keep: ' + cur.io.label]] : [])
    .map(([v, t]) => `<option value="${v}" ${v === pick ? 'selected' : ''}>${esc(t)}</option>`).join('');
  return `<div class="wire-row" data-slot="${slot}" data-orig="${esc(JSON.stringify(net || (keep ? { keep: true } : {})))}">
    <span class="wire-label">SLOT ${slot + 1}</span>
    <select class="wire-kind" onchange="syncWireRow(this)">${opts}</select>
    <input class="wire-num" type="number" value="${net ? net.universe : ''}" placeholder="Universe">
  </div>`;
}

// Which control a mode uses; master limit goes with the block's MASTER / LIMIT setting.
const MODE_CONTROL = { XFADE: 'xfade', SWITCH: 'switch', BACKUP: 'backup' };
function controlRoles(pb) {
  const roles = new Set((pb.controls || []).map(c => c.role)); // anything set stays listed, so it can be taken off
  if (MODE_CONTROL[pb.mode]) roles.add(MODE_CONTROL[pb.mode]);
  if (pb.masterlimit && pb.masterlimit !== 'NONE') roles.add('masterlimit');
  return [...roles];
}
function controlRowHtml(pb, role) {
  const cur = (pb.controls || []).find(c => c.role === role);
  const net = cur ? netIO(cur.io.id) : null;
  const keep = cur && !net;
  const pick = net ? net.protocol : keep ? 'keep' : '';
  const opts = [['', 'Not set'], ['sacn', 'sACN'], ['artnet', 'Art-Net']]
    .concat(keep ? [['keep', 'Keep: ' + cur.io.label]] : [])
    .map(([v, t]) => `<option value="${v}" ${v === pick ? 'selected' : ''}>${esc(t)}</option>`).join('');
  const idle = role !== MODE_CONTROL[pb.mode] && role !== 'masterlimit' ? ' title="Stored on the block; this mode does not use it"' : '';
  return `<div class="wire-row" data-role="${role}" data-orig="${esc(JSON.stringify(net || (keep ? { keep: true } : {})))}">
    <span class="wire-label wire-ctrl"${idle}>${esc(ctrlLabel(role).replace(' CTRL', ''))}</span>
    <select class="wire-kind" onchange="syncWireRow(this)">${opts}</select>
    <input class="wire-num" type="number" value="${net ? net.universe : ''}" placeholder="Universe">
  </div>`;
}

function addedOutputRowHtml() {
  return `<div class="wire-row wire-new">
    <span class="wire-label">NEW</span>
    <select class="wire-kind" onchange="syncWireRow(this)">
      <option value="sacn">sACN</option><option value="artnet">Art-Net</option><option value="dmx">DMX port</option>
    </select>
    <input class="wire-num" type="number" placeholder="Universe">
    <button class="btn sm" type="button" onclick="this.closest('.wire-row').remove()" title="Don't add this output">✕</button>
  </div>`;
}

function syncWireRow(sel) {
  const num = sel.closest('.wire-row').querySelector('.wire-num');
  const kind = sel.value;
  num.disabled = kind === '' || kind === 'keep';
  if (num.disabled) num.value = '';
  num.placeholder = kind === 'dmx' ? `Port 1–${numDmxPorts}` : universeLimits[kind] ? `Universe ${universeLimits[kind].join('–')}` : '';
}

function openSetInput(id) {
  const pb = pbById(id);
  wiringBlock = pb.id;
  nc$('input-modal-title').textContent = `INPUTS / OUTPUTS — PB ${pb.id} ${pb.name || ''}`;
  const inputs = pb.inputs || [];
  const bySlot = {};
  inputs.forEach(i => bySlot[i.slot] = i);
  const limit = slotsFor(pb.mode);
  const nSlots = Math.max(limit, ...inputs.map(i => i.slot + 1));
  nc$('wire-inputs').innerHTML = Array.from({ length: nSlots }, (_, n) => inputRowHtml(n, bySlot[n])).join('');
  nc$('wire-mode-note').textContent = limit > 1
    ? `Mode ${pb.mode} takes ${limit} inputs. Change the mode with EDIT.`
    : `Mode ${pb.mode} takes one input. For more, choose a mode with EDIT first: BACKUP or XFADE take 2; HTP, LTP, SWITCH or CUSTOM take 4.`;
  nc$('wire-outputs').innerHTML = (pb.outputs || []).map(o => `<div class="wire-row" data-output="${o.id}">
      <span class="wire-out-name">${esc(o.label)}</span>
      <button class="btn sm dan" type="button" onclick="toggleRemoveOutput(this)" title="Take this output off the block">REMOVE</button>
    </div>`).join('') || '<div class="wire-none" id="wire-no-outputs">No output yet</div>';
  const roles = controlRoles(pb);
  nc$('wire-controls').innerHTML = roles.map(r => controlRowHtml(pb, r)).join('') || `<div class="wire-none">Mode ${esc(pb.mode)} has no control. MASTER / LIMIT in EDIT adds one.</div>`;
  document.querySelectorAll('#wire-inputs .wire-kind, #wire-controls .wire-kind').forEach(syncWireRow);
  nc$('input-modal').classList.add('open');
}

function toggleRemoveOutput(btn) {
  const row = btn.closest('.wire-row');
  const removing = row.classList.toggle('removing');
  btn.textContent = removing ? 'KEEP' : 'REMOVE';
}

nc$('wire-add-output').onclick = () => {
  const none = nc$('wire-no-outputs');
  if (none) none.remove();
  nc$('wire-outputs').insertAdjacentHTML('beforeend', addedOutputRowHtml());
  syncWireRow(nc$('wire-outputs').lastElementChild.querySelector('.wire-kind'));
};

// What the dialog changed; throws with the reason if a row is not valid.
function wiringChanges() {
  const inputs = [], addOutputs = [], removeOutputs = [], controls = [];
  const universeOf = (kind, raw, what) => {
    const [lo, hi] = universeLimits[kind];
    const u = parseInt(raw);
    if (raw === '' || isNaN(u) || u < lo || u > hi) throw new Error(`${what}: universe must be ${lo}–${hi} for ${kind === 'sacn' ? 'sACN' : 'Art-Net'}`);
    return u;
  };
  document.querySelectorAll('#wire-inputs .wire-row').forEach(row => {
    const slot = parseInt(row.dataset.slot), orig = JSON.parse(row.dataset.orig);
    const kind = row.querySelector('.wire-kind').value, raw = row.querySelector('.wire-num').value;
    if (kind === 'keep') return;
    if (kind === '') { if (orig.protocol || orig.keep) inputs.push({ slot, clear: true }); return; }
    const universe = universeOf(kind, raw, `Slot ${slot + 1}`);
    if (kind !== orig.protocol || universe !== orig.universe) inputs.push({ slot, protocol: kind, universe });
  });
  document.querySelectorAll('#wire-controls [data-role]').forEach(row => {
    const role = row.dataset.role, orig = JSON.parse(row.dataset.orig);
    const kind = row.querySelector('.wire-kind').value, raw = row.querySelector('.wire-num').value;
    if (kind === 'keep') return;
    if (kind === '') { if (orig.protocol || orig.keep) controls.push({ role, clear: true }); return; }
    const universe = universeOf(kind, raw, ctrlLabel(role));
    if (kind !== orig.protocol || universe !== orig.universe) controls.push({ role, protocol: kind, universe });
  });
  document.querySelectorAll('#wire-outputs [data-output].removing').forEach(row => removeOutputs.push(parseInt(row.dataset.output)));
  document.querySelectorAll('#wire-outputs .wire-new').forEach(row => {
    const kind = row.querySelector('.wire-kind').value, raw = row.querySelector('.wire-num').value;
    if (kind === 'dmx') {
      const port = parseInt(raw);
      if (isNaN(port) || port < 1 || port > numDmxPorts) throw new Error(`Output: DMX port must be 1–${numDmxPorts}`);
      addOutputs.push({ protocol: 'dmx', port });
    } else {
      addOutputs.push({ protocol: kind, universe: universeOf(kind, raw, 'Output') });
    }
  });
  return { inputs, removeOutputs, addOutputs, controls };
}

nc$('input-save').onclick = async () => {
  if (!currentIp) { toast('No device connected', true); return; }
  let changes;
  try { changes = wiringChanges(); } catch (e) { toast(e.message, true); return; }
  if (!changes.inputs.length && !changes.removeOutputs.length && !changes.addOutputs.length && !changes.controls.length) {
    nc$('input-modal').classList.remove('open');
    return;
  }
  const btn = nc$('input-save');
  btn.disabled = true;
  btn.textContent = 'SAVING...';
  try {
    const res = await call(`/api/node/${currentIp}/processblock/${wiringBlock}/wiring`, 'POST', changes);
    applyState(res);
    renderQuickSetup(lastSnap.device ? lastSnap.device.dmxPorts : numDmxPorts);
    if (res.errors && res.errors.length) {
      res.errors.forEach(e => toast(e, true));
      openSetInput(wiringBlock); // show what the node has now
    } else {
      nc$('input-modal').classList.remove('open');
      toast(`PB${wiringBlock} inputs and outputs updated`);
    }
  } catch (e) {
    toast('Failed: ' + e.message, true);
  } finally {
    btn.disabled = false;
    btn.textContent = 'SAVE';
  }
};


// ── Network Edit Modal ────────────────────────────────────────────────────────

nc$('net-modal-close').onclick =
nc$('net-cancel').onclick = () => nc$('net-modal').classList.remove('open');

nc$('net-save').onclick = async () => {
  if (!currentIp) return;

  const longName  = nc$('net-long-name').value.trim();
  const shortName = nc$('net-short-name').value.trim();
  const newIp     = nc$('net-ip').value.trim();
  const netmask   = nc$('net-mask').value.trim();
  const gateway   = nc$('net-gateway').value.trim() || '0.0.0.0';
  const ipChanged = newIp !== ((window._netCurrent || {}).ip || '').trim();

  // Basic IP validation
  const ipRe = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/;
  if (!ipRe.test(newIp))  { toast('Invalid IP address', true); return; }
  if (!ipRe.test(netmask)){ toast('Invalid netmask', true); return; }

  const btn = nc$('net-save');
  btn.disabled = true;
  btn.textContent = 'APPLYING...';

  try {
    // Apply device name first (non-disruptive)
    const dev = window._devCurrent || {};
    if (longName !== dev.longName || shortName !== dev.shortName) {
      await call(`/api/node/${currentIp}/devicename`, 'PUT', {
        long_name:  longName,
        short_name: shortName,
      });
      toast('Device name updated');
    }

    // A node from the device list: a job makes the change, mDNS confirms it, and
    // followDevice() brings this page to the new address.
    if (ipChanged && followId) {
      await call('/api/apply', 'POST', { items: [{ id: followId, newIp }], netmask, gateway });
      nc$('net-modal').classList.remove('open');
      toast(`Changing address to ${newIp}…`);
      return;
    }

    // Apply IP settings
    await call(`/api/node/${currentIp}/ipsettings`, 'PUT', {
      ipaddress: newIp,
      netmask,
      gateway,
    });

    nc$('net-modal').classList.remove('open');

    if (ipChanged) {
      toast(`IP changed to ${newIp} — waiting for it to come back...`);
      setStatus('');
      reconnectWhenBack(newIp);
    } else {
      toast('Network settings applied');
      await loadNode(currentIp);
    }

  } catch (e) {
    toast('Failed: ' + e.message, true);
  } finally {
    btn.disabled = false;
    btn.textContent = 'APPLY CHANGES';
  }
};


// ── Startup Buffer ────────────────────────────────────────────────────────────

async function renderStartup() {
  if (!currentIp || !lastSnap.processBlocks) {
    nc$('startup-body').innerHTML = `<div class="empty"><div class="icon">⊡</div>
      <p>No device connected</p></div>`;
    nc$('btn-apply-all-startup').disabled = true;
    return;
  }

  nc$('btn-apply-all-startup').disabled = false;

  // Fetch playlist data for each process block in parallel
  const pbs = lastSnap.processBlocks.filter(pb => pb.mode !== 'NONE' || pb.name !== `Process Block ${pb.id}`);
  // Show all process blocks
  const allPbs = lastSnap.processBlocks;

  const playlistResults = await Promise.allSettled(
    allPbs.map(pb => call(`/api/node/${currentIp}/startup/${pb.id}/snapshots`))
  );

  nc$('startup-body').innerHTML = `<div class="grid2">${allPbs.map((pb, i) => {
    const playlist = playlistResults[i].status === 'fulfilled'
      ? playlistResults[i].value.playlist : null;
    const snapshots = playlist ? (playlist.snapshots || []) : [];
    const hasBuffer = pb.ratelimit !== undefined; // always show

    // Get startup buffer size from raw process block if available
    const rawPb = (lastSnap.processBlocks || []).find(p => p.id === pb.id);

    return `<div class="card" id="startup-card-${pb.id}">
      <div class="card-head">
        <span class="card-title">PB ${esc(pb.id)} — ${esc(pb.name)}</span>
        <span class="badge ${pb.mode === 'NONE' ? 'b-dim' : 'b-htp'}">${esc(pb.mode)}</span>
      </div>
      <div class="card-body">
        <div class="kv">
          <span class="k">STARTUP BUFFER</span>
          <span class="v" id="startup-status-${pb.id}">Loading...</span>
        </div>
        <div class="fg" style="margin-top:10px;margin-bottom:0">
          <label>SNAPSHOT TO APPLY</label>
          <select id="startup-snap-${pb.id}" style="width:100%;font-family:var(--ff);
            font-size:12px;background:var(--bg);border:1px solid var(--border2);
            color:var(--text);padding:6px 9px;border-radius:2px;outline:none">
            ${snapshots.length
              ? snapshots.map(s => `<option value="${esc(s.id)}">${esc(s.id)}</option>`).join('')
              : '<option value="">No snapshots recorded</option>'
            }
          </select>
        </div>
      </div>
      <div class="card-foot">
        <button class="btn sm pri" onclick="applyStartupBuffer(${pb.id})"
          ${snapshots.length ? '' : 'disabled'}>⊡ APPLY</button>
        <button class="btn sm dan" onclick="clearStartupBuffer(${pb.id})">✕ CLEAR</button>
      </div>
    </div>`;
  }).join('')}</div>`;

  // Load startup buffer status for each PB
  allPbs.forEach(pb => loadStartupStatus(pb.id));
}

async function loadStartupStatus(pbIdx) {
  const el = nc$(`startup-status-${pbIdx}`);
  if (!el) return;
  try {
    const d = await call(`/api/node/${currentIp}/processblock/${pbIdx}/raw`);
    if (d.startup_buffer_length > 0) {
      el.textContent = `${d.startup_buffer_length} channels`;
      el.style.color = 'var(--green)';
    } else {
      el.textContent = 'Empty';
      el.style.color = 'var(--dim)';
    }
  } catch (_) { el.textContent = '—'; }
}

async function applyStartupBuffer(pbIdx) {
  const snapEl = nc$(`startup-snap-${pbIdx}`);
  if (!snapEl || !snapEl.value) { toast('No snapshot selected', true); return; }

  const snapshotId = snapEl.value;
  try {
    const res = await call(`/api/node/${currentIp}/startup/${pbIdx}/apply`, 'POST', { snapshotId });
    toast(`PB${pbIdx}: startup buffer set (${res.channels} channels)`);
    nc$(`startup-status-${pbIdx}`).textContent = `${res.channels} channels`;
    nc$(`startup-status-${pbIdx}`).style.color = 'var(--green)';
  } catch (e) {
    toast(`PB${pbIdx} failed: ${e.message}`, true);
  }
}

async function clearStartupBuffer(pbIdx) {
  try {
    await call(`/api/node/${currentIp}/startup/${pbIdx}`, 'DELETE');
    toast(`PB${pbIdx}: startup buffer cleared`);
    nc$(`startup-status-${pbIdx}`).textContent = 'Empty';
    nc$(`startup-status-${pbIdx}`).style.color = 'var(--dim)';
  } catch (e) {
    toast(`Failed: ${e.message}`, true);
  }
}

nc$('btn-apply-all-startup').onclick = async () => {
  if (!lastSnap.processBlocks) return;
  const pbs = lastSnap.processBlocks;
  let applied = 0, failed = 0;

  for (const pb of pbs) {
    const snapEl = nc$(`startup-snap-${pb.id}`);
    if (!snapEl || !snapEl.value) continue;
    try {
      const res = await call(`/api/node/${currentIp}/startup/${pb.id}/apply`, 'POST',
        { snapshotId: snapEl.value });
      nc$(`startup-status-${pb.id}`).textContent = `${res.channels} channels`;
      nc$(`startup-status-${pb.id}`).style.color = 'var(--green)';
      applied++;
    } catch (e) {
      failed++;
      toast(`PB${pb.id} failed: ${e.message}`, true);
    }
  }
  if (applied) toast(`${applied} startup buffer(s) set`);
};

// ── Advanced: Process, IOs, DMX and Startup share one tab ──────────────────────
function advShow(name) {
  document.querySelectorAll('#adv-tabs [data-adv]').forEach(b => b.classList.toggle('active-tab', b.dataset.adv === name));
  document.querySelectorAll('#p-advanced .subpanel').forEach(p => p.classList.toggle('active', p.id === 'p-' + name));
  if (name === 'startup' && currentIp) renderStartup(); // startup data is read when it is shown
}
document.querySelectorAll('#adv-tabs [data-adv]').forEach(b => { b.onclick = () => advShow(b.dataset.adv); });
document.querySelectorAll('.tab').forEach(t => {
  if (t.dataset.tab === 'advanced') t.addEventListener('click', () => {
    if (currentIp && nc$('p-startup').classList.contains('active')) renderStartup();
  });
});

// ── RDM ───────────────────────────────────────────────────────────────────────
// The node's REST API turns RDM on per DMX port and counts what it has found;
// the server reads each fixture over Art-Net RDM (or, when it cannot, shows
// what the node has overheard). Nothing here writes to a fixture.
let rdm = { ip: null, ports: [], artnet: null, dry: false, read: {}, busy: {}, confirm: null, edit: null, fxBusy: {},
  sel: null, panel: null, pick: null, batch: null, fxStatus: {}, view: 'tiles' };

function rdmReset(msg) {
  if (typeof rdmFollowRelease === 'function') rdmFollowRelease(); // IDs Follow ID turned on go off before the state goes
  rdm = { ip: null, ports: [], artnet: null, dry: false, read: {}, busy: {}, confirm: null, edit: null, fxBusy: {},
    sel: null, panel: null, pick: null, batch: null, fxStatus: {}, view: rdm.view };
  rdmPaint(nc$('rdm-body'), `<div class="empty"><div class="icon">✳</div><p>${esc(msg)}</p></div>`);
  rdmPaint(rdmKeypadHost(), '');
  nc$('rdm-banner').style.display = 'none';
  nc$('btn-rdm-read-all').disabled = true;
  nc$('btn-rdm-read-all').title = 'Connect to a device first';
  nc$('btn-rdm-csv').disabled = true;
}

async function rdmLoad() {
  if (!currentIp) { rdmReset('No device connected'); return; }
  const ip = currentIp;
  if (rdm.ip !== ip) { rdmReset('Reading DMX ports…'); rdm.ip = ip; }
  try {
    const d = await call(`/api/node/${ip}/rdm`);
    if (ip !== currentIp) return;
    rdmApply(d);
    if (d.state) rdmApplyShared(d.state);
  } catch (e) {
    if (ip === currentIp) rdmPaint(nc$('rdm-body'), `<div class="empty"><div class="icon">✳</div><p>Cannot read RDM: ${esc(e.message)}</p></div>`);
    return;
  }
  rdmAutoRead(ip);
}

// rdmAutoRead reads, one port after another, every port that already has RDM
// on and fixtures the node has found, so they show without pressing Read.
// Ports already read, or busy, are left alone.
async function rdmAutoRead(ip) {
  if (rdm.autoReading === ip) return;
  rdm.autoReading = ip;
  try {
    for (const p of rdm.ports.filter(p => p.rdm && p.count > 0)) {
      if (ip !== currentIp) return;
      if (rdm.read[p.port] || rdmBusy(p.port)) continue;
      await rdmRead(p.port);
    }
  } finally {
    if (rdm.autoReading === ip) rdm.autoReading = null;
  }
}

// rdmApplyShared takes the node's RDM state as the app holds it: the ports, every
// port's fixtures as last read or changed (from any device) and the reads
// running. It arrives with the ports list and then over /api/live on every
// change, so a read or edit on another device shows here at once. What this
// page is doing (keypad, ticks, fixture page, Follow ID) is not part of it.
function rdmApplyShared(s) {
  if (!s || (rdm.rev != null && s.rev <= rdm.rev)) return;
  rdm.rev = s.rev;
  rdm.sharedBusy = s.busy || {};
  if (s.ports && s.ports.length) rdm.ports = s.ports;
  const read = {};
  for (const [port, r] of Object.entries(s.read || {})) read[port] = { ...r, fixtures: r.fixtures || [], at: new Date(r.at) };
  rdm.read = read;
  if (rdm.sel) {
    const res = rdm.read[rdm.sel.port];
    for (const u of [...rdm.sel.uids]) if (!res || !res.fixtures.some(f => f.uid === u)) rdm.sel.uids.delete(u);
    if (!rdm.sel.uids.size) rdm.sel = null;
  }
  renderRdm(); // no auto-read from here: a port whose read fails would be read again on every push
}

// rdmBusy is what is running on a port: from this page, else from another
// device (a read it started), as the app reports it.
function rdmBusy(port) { return rdm.busy[port] || (rdm.sharedBusy || {})[port] || ''; }

function rdmApply(d) {
  rdm.ports = d.ports || [];
  rdm.artnet = d.artnet;
  rdm.dry = !!d.dryRun;
  rdm.ports.forEach(p => { if (!p.rdm) delete rdm.read[p.port]; });
  renderRdm();
}

// rdmPaint replaces an element's contents only when they changed. Follow ID
// renders after every reply; redrawing an unchanged keypad or tile under a
// finger loses the tap on an iPad (the button is gone before the click lands).
function rdmPaint(el, html) {
  if (el._html === html) return;
  el._html = html;
  el.innerHTML = html;
}

// The keypad lives in its own element beside the RDM body, so redrawing the
// tiles never touches it.
function rdmKeypadHost() {
  let h = nc$('rdm-kp-host');
  if (!h) {
    h = document.createElement('div');
    h.id = 'rdm-kp-host';
    nc$('rdm-body').insertAdjacentElement('afterend', h);
  }
  return h;
}

function renderRdm() {
  const a = rdm.artnet;
  nc$('rdm-banner').textContent = a && !a.ok ? `${a.error}. Fixtures come from what the node has overheard instead, which can be incomplete.` : '';
  nc$('rdm-banner').style.display = a && !a.ok ? 'block' : 'none';
  const anyOn = rdm.ports.some(p => p.rdm);
  nc$('btn-rdm-read-all').disabled = !anyOn;
  nc$('btn-rdm-read-all').title = anyOn ? 'Read the fixtures on every port with RDM on' : 'Turn RDM on for a port first';
  const anyRead = Object.values(rdm.read).some(r => r.fixtures.length);
  nc$('btn-rdm-csv').disabled = !anyRead;
  nc$('btn-rdm-csv').title = anyRead ? 'Save every fixture read so far as a CSV file' : 'Read some fixtures first';
  if (!rdm.ports.length) {
    rdmPaint(nc$('rdm-body'), `<div class="empty"><div class="icon">✳</div><p>This node has no DMX ports</p></div>`);
    rdmPaint(rdmKeypadHost(), '');
    return;
  }
  rdmPaint(nc$('rdm-body'), `<div class="rdm-layout${rdm.panel ? ' with-side' : ''}">
    <div class="rdm-ports">${rdm.ports.map(rdmPortHtml).join('')}</div>${rdmPanelHtml()}</div>`);
  rdmPaint(rdmKeypadHost(), rdmKeypadHtml());
  if (typeof rdmAfterRender === 'function') rdmAfterRender();
}

function rdmPortHtml(p) {
  const res = rdm.read[p.port], busy = rdmBusy(p.port);
  const output = p.ioType === 'output';
  const notOut = `Port ${p.port} is ${p.ioType === 'idle' ? 'not in use' : 'an input'}, so it cannot do RDM`;
  const plural = n => `${n} fixture${n === 1 ? '' : 's'}`;
  // While a search or read runs, the last read's list is out of date: show what
  // is happening, with the node's own count (kept current by every search poll
  // and read, here or on another device), not the old number.
  let count = '';
  if (p.rdm) count = busy.startsWith('Searching') ? busy
    : busy === 'Reading…' ? (p.count > 0 ? `Reading ${plural(p.count)}…` : 'Reading…')
    : res ? plural(res.fixtures.length) : p.count >= 0 ? `${plural(p.count)} found by the node` : '';
  const dis = busy ? `disabled title="${esc(busy)}"` : '';
  const actions = p.rdm
    ? `<button class="btn sm sec" onclick="rdmRead(${p.port})" ${dis || 'title="Ask each fixture for its settings"'}>${busy === 'Reading…' ? 'READING…' : '↻ READ'}</button>
       <button class="btn sm" onclick="rdmDiscover(${p.port})" ${dis || `title="Ask the node to search port ${p.port} for fixtures again"`}>⊹ REDISCOVER</button>`
    : '';
  // The tick box is the port's RDM state: ticked is on. Unticking turns it off at
  // once; ticking asks first (below), as discovery can make older fixtures flicker.
  const tickWhy = busy ? busy : !output && !p.rdm ? notOut : p.rdm ? 'RDM is on. Untick to stop RDM on this port; DMX carries on' : 'RDM is off. Tick to start RDM on this port';
  const tick = `<label class="rdm-tick${p.rdm ? ' on' : ''}" title="${esc(tickWhy)}"><input type="checkbox" ${p.rdm ? 'checked' : ''} ${busy || (!output && !p.rdm) ? 'disabled' : ''}
      onchange="this.checked ? rdmAskOn(${p.port}) : rdmSet(${p.port}, false)">RDM</label>`;
  const confirm = rdm.confirm === p.port && !p.rdm ? `<div class="rdm-confirm">
      <span>Turning RDM on starts fixture discovery on DMX port ${p.port}. DMX keeps flowing, but some older fixtures can flicker while discovery runs.</span>
      <button class="btn sm warn" onclick="rdmSet(${p.port}, true)">TURN RDM ON</button>
      <button class="btn sm" onclick="rdmCancelOn()">CANCEL</button></div>` : '';
  let body = '';
  if (res) {
    body = !res.fixtures.length ? `<div class="rdm-note">No fixtures found on this port yet. On a line with many fixtures the node's search can take a while: Read again shortly, or Rediscover.</div>`
      : rdmSelectRow(p.port) + rdmBatchBar(p.port) + (rdm.view === 'tiles' ? rdmTiles(p.port, res.fixtures) : `<div class="rdm-wrap">${rdmTable(p.port, res.fixtures)}</div>`);
    body += `<div class="rdm-note${res.source === 'overheard' ? ' warn' : ''}">${res.source === 'overheard' ? esc(res.note) + ' · ' : ''}Art-Net ${esc(rdmUniverseText(res.universe))} · read ${res.at.toLocaleTimeString()}</div>`;
  } else if (p.rdm && p.universe < 0) {
    body = `<div class="rdm-note warn">The node gives this port no RDM universe. Does the port have an input?</div>`;
  }
  return `<div class="rdm-port${p.rdm ? '' : ' off'}${!res ? ' empty-port' : ''}">
    <div class="rdm-head">
      <span class="rdm-title">PORT ${p.port}</span>
      <span class="rdm-src">${esc(p.source || (output ? 'no input' : p.ioType.toUpperCase()))}</span>
      ${tick}
      <span class="rdm-count">${esc(count)}</span>
      ${res && res.fixtures.length ? `<span class="rdm-view">
        <button class="btn sm${rdm.view === 'tiles' ? ' active-tab' : ''}" onclick="rdmSetView('tiles')" title="Show fixtures as tiles">TILES</button><button class="btn sm${rdm.view === 'table' ? ' active-tab' : ''}" onclick="rdmSetView('table')" title="Show fixtures as a table">TABLE</button>
        <button class="btn sm${rdmFollowOn(p.port) ? ' warn' : ''}" onclick="rdmFollowToggle(${p.port})" title="${rdmFollowOn(p.port) ? 'Stop Follow ID: fixtures it turned on stop identifying' : 'Identify whatever is picked: the open fixture page, the address being typed, and ticked tiles'}">${rdmFollowOn(p.port) ? '◉' : '○'} FOLLOW ID</button><button class="btn sm" onclick="rdmIdOffAll(${p.port})" ${rdmBusy(p.port) ? 'disabled title="Busy"' : `title="Turn identify off on all ${res.fixtures.length} fixtures on port ${p.port}, whatever turned it on, and stop Follow ID here"`}>ID OFF</button></span>` : ''}
      <span class="rdm-actions">${actions}</span>
    </div>${confirm}${rdmSharedNote(p)}${body}</div>`;
}

// rdmSharedNote warns when another port with RDM on is fed from the same
// universe: they share an Art-Net port-address, so every RDM request reaches
// both lines. Each port's list is still its own (the node says which port each
// table is for).
function rdmSharedNote(p) {
  if (!p.rdm || p.universe < 0) return '';
  const others = rdm.ports.filter(o => o.port !== p.port && o.rdm && o.universe === p.universe).map(o => o.port);
  if (!others.length) return '';
  return `<div class="rdm-note warn">Port${others.length > 1 ? 's' : ''} ${others.join(', ')} ${others.length > 1 ? 'are' : 'is'} on the same universe (Art-Net ${esc(rdmUniverseText(p.universe))}), so every RDM request goes out on both lines. Each port still lists only its own fixtures.</div>`;
}

function rdmUniverseText(u) {
  return u >= 0 ? `${u >> 8 & 0x7f}:${u >> 4 & 0xf}:${u & 0xf} (${u})` : 'none';
}

// Problems a fixture row explains in its NOTE cell.
function rdmProblems(f, all) {
  const out = [];
  if (f.error) out.push(f.error);
  if (f.overlaps && f.overlaps.length) {
    const who = f.overlaps.map(u => {
      const o = all.find(x => x.uid === u);
      return o ? `${o.label || o.model || u} (${o.start})` : u;
    });
    out.push(`Shares channels with ${who.join(', ')}`);
  }
  if (f.pastEnd) out.push('Runs past channel 512');
  return out;
}

function rdmTable(port, fixtures) {
  const problems = fixtures.map(f => rdmProblems(f, fixtures));
  const cols = 10; // a problem goes on its own line under the fixture
  const rows = fixtures.map((f, i) => {
    const end = f.start != null && f.footprint ? f.start + f.footprint - 1 : null;
    const pers = f.personality ? (f.personalityName ? `${esc(f.personalityName)}<span class="sub">${f.personality}/${f.personalities}</span>` : `${f.personality} of ${f.personalities}`) : '—';
    const cls = f.error ? 'err' : problems[i].length ? 'clash' : '';
    const editing = rdm.edit && rdm.edit.port === port && rdm.edit.uid === f.uid;
    const busy = rdm.fxBusy[f.uid];
    const dis = busy ? `disabled title="${esc(busy)}"` : f.error ? 'disabled title="This fixture did not answer"' : '';
    const actions = `<td class="act">
      <button class="btn sm${f.identify ? ' warn' : ''}" onclick="rdmIdentify(${port}, '${esc(f.uid)}')" ${dis || `title="${f.identify ? 'Stop identifying' : 'Make the fixture identify itself (it flashes or moves)'}"`}>${f.identify ? '◉ ID ON' : '○ ID'}</button>
      <button class="btn sm" onclick="rdmEditOpen(${port}, '${esc(f.uid)}')" ${dis || (editing ? 'disabled title="Being edited below"' : 'title="Change the address, label or personality"')}>✎ EDIT</button></td>`;
    const picked = rdmSelected(port).has(f.uid);
    return `<tr class="${cls}${picked ? ' picked' : ''}" title="UID ${esc(f.uid)}${f.category ? ' · ' + esc(f.category) : ''}">
      <td class="pick"><button class="rdm-check" onclick="rdmToggleSel(${port}, '${esc(f.uid)}')" title="${picked ? 'Unselect' : 'Select'} for a batch action">${picked ? '☑' : '☐'}</button></td>
      <td class="num addr">${f.start != null ? f.start + (end > f.start ? `<span class="sub">–${end}</span>` : '') : '—'}</td>
      <td class="num">${f.error && !f.footprint ? '—' : f.footprint}</td>
      <td>${pers}</td>
      <td>${esc(f.manufacturer) || '—'}</td>
      <td>${esc(f.model) || '—'}</td>
      <td>${esc(f.label) || (f.error ? esc(f.uid) : '—')}</td>
      <td>${esc(f.software) || '—'}</td>
      <td class="num">${f.error ? '—' : f.sensors}</td>
      ${actions}
    </tr>` + (problems[i].length ? `<tr class="${cls} why-row"><td></td><td></td><td colspan="${cols - 2}" class="why">${esc(problems[i].join('. '))}</td></tr>` : '') +
      (editing && !(rdm.panel && rdm.panel.uid === f.uid) ? rdmEditRow(port, f, fixtures, cols) : '');
  }).join('');
  return `<table class="rdm"><thead><tr><th></th><th>ADDR</th><th>CH</th><th>PERSONALITY</th><th>MANUFACTURER</th><th>MODEL</th>
    <th>LABEL</th><th>SOFTWARE</th><th>SENSORS</th><th></th></tr></thead><tbody>${rows}</tbody></table>`;
}

// The same rule as the server's markOverlaps, for a table changed by an edit.
function rdmMarkOverlaps(fx) {
  const end = f => f.start + f.footprint - 1;
  fx.sort((a, b) => (a.start == null) - (b.start == null) || (a.start ?? 0) - (b.start ?? 0) || (a.uid < b.uid ? -1 : 1));
  for (const f of fx) {
    f.overlaps = [];
    f.pastEnd = false;
    if (f.start == null) continue;
    f.pastEnd = end(f) > 512;
    for (const o of fx) if (o !== f && o.start != null && f.start <= end(o) && o.start <= end(f)) f.overlaps.push(o.uid);
  }
}

function rdmEditRow(port, f, fixtures, cols) {
  const e = rdm.edit;
  const pers = e.personalities;
  const footprint = pers ? ((pers.find(p => p.number === e.personality) || {}).footprint ?? f.footprint) : f.footprint;
  const start = Number(e.start);
  let clash = '';
  const bad = !Number.isInteger(start) || start < 1 || start > 512;
  if (bad) clash = 'Address must be 1-512.';
  else if (footprint) {
    const last = start + footprint - 1;
    const hits = fixtures.filter(o => o.uid !== f.uid && o.start != null && o.footprint && start <= o.start + o.footprint - 1 && o.start <= last);
    clash = [hits.length ? `Would share channels with ${hits.map(o => `${o.label || o.model || o.uid} (${o.start}–${o.start + o.footprint - 1})`).join(', ')}.` : '',
      last > 512 ? `Would run past channel 512 (to ${last}).` : ''].filter(Boolean).join(' ');
  }
  const persSel = pers
    ? `<select id="rdm-e-pers" onchange="rdmEditField('personality', +this.value)">${pers.map(p =>
        `<option value="${p.number}" ${p.number === e.personality ? 'selected' : ''}>${p.number}: ${esc(p.name || 'personality ' + p.number)} (${p.footprint} ch)</option>`).join('')}</select>`
    : `<select disabled><option>${e.persError ? esc(e.persError) : 'Reading personalities…'}</option></select>`;
  const range = footprint && Number.isInteger(start) ? `channels ${start}–${start + footprint - 1}` : '';
  if (!cols) return rdmEditForm(start, footprint, clash, persSel, range, f, e, bad); // the fixture page's own form
  return `<tr class="rdm-edit"><td colspan="${cols}">${rdmEditForm(start, footprint, clash, persSel, range, f, e, bad)}</td></tr>`;
}

function rdmEditForm(start, footprint, clash, persSel, range, f, e, bad) {
  return `<div class="rdm-edit-row">
      <label>ADDRESS <input id="rdm-e-start" type="number" min="1" max="512" value="${esc(e.start)}" oninput="rdmEditField('start', this.value, true)"></label>
      <label>LABEL <input id="rdm-e-label" type="text" maxlength="32" value="${esc(e.label)}" oninput="rdmEditField('label', this.value, true)"></label>
      <label>PERSONALITY ${persSel}</label>
      <span class="rdm-edit-range">${esc(range)}</span>
      <span class="rdm-edit-actions">
        <button class="btn sm pri" id="rdm-e-save" onclick="rdmEditSave()" ${rdm.fxBusy[f.uid] ? 'disabled title="Saving…"' : bad ? 'disabled title="The address must be 1-512"' : 'title="Send the changes to the fixture"'}>SAVE</button>
        <button class="btn sm" onclick="rdmEditClose()">CANCEL</button>
      </span>
    </div>
    <div class="rdm-edit-warn" id="rdm-e-warn">${esc(clash)}</div>`;
}

function rdmFind(port, uid) { return ((rdm.read[port] || {}).fixtures || []).find(f => f.uid === uid); }

async function rdmEditOpen(port, uid) {
  const f = rdmFind(port, uid);
  if (!f) return;
  rdm.edit = { port, uid, start: f.start ?? '', label: f.label || '', personality: f.personality, personalities: null };
  renderRdm();
  const ip = currentIp;
  try {
    const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(uid)}/personalities`);
    if (rdm.edit && rdm.edit.uid === uid) { rdm.edit.personalities = d.personalities; renderRdm(); }
  } catch (e) {
    if (rdm.edit && rdm.edit.uid === uid) { rdm.edit.persError = e.message; renderRdm(); }
  }
}

// Typing in the edit row only refreshes the range and the warning, so the cursor stays put.
function rdmEditField(k, v, typing) {
  if (!rdm.edit) return;
  rdm.edit[k] = v;
  if (!typing) { renderRdm(); return; }
  const port = rdm.edit.port, fixtures = rdm.read[port].fixtures, f = rdmFind(port, rdm.edit.uid);
  const tmp = document.createElement('tbody');
  tmp.innerHTML = rdmEditRow(port, f, fixtures, 1);
  if (!nc$('rdm-e-warn')) return;
  nc$('rdm-e-warn').textContent = tmp.querySelector('#rdm-e-warn').textContent;
  document.querySelector('.rdm-edit-range').textContent = tmp.querySelector('.rdm-edit-range').textContent;
  const save = tmp.querySelector('#rdm-e-save');
  nc$('rdm-e-save').disabled = save.disabled;
  nc$('rdm-e-save').title = save.title;
}

function rdmEditClose() { rdm.edit = null; renderRdm(); }

async function rdmEditSave() {
  const e = rdm.edit, f = e && rdmFind(e.port, e.uid);
  if (!f) return;
  const body = {};
  const start = Number(e.start);
  if (e.start !== '' && start !== f.start) body.start = start;
  if (e.label !== (f.label || '')) body.label = e.label;
  if (e.personalities && e.personality !== f.personality) body.personality = e.personality;
  if (!Object.keys(body).length) { rdmEditClose(); return; }
  const ok = await rdmSend(e.port, f, body);
  if (ok && rdm.edit && rdm.edit.uid === f.uid) rdmEditClose();
}

async function rdmIdentify(port, uid) {
  const f = rdmFind(port, uid);
  if (f) await rdmSend(port, f, { identify: !f.identify });
}

// rdmSend sends one edit and puts the fixture as read back into the table.
async function rdmSend(port, f, body) {
  const ip = currentIp;
  rdm.fxBusy[f.uid] = 'Sending…';
  renderRdm();
  let sent = false;
  try {
    const d = await call(`/api/node/${ip}/rdm/${port}/fixture/${encodeURIComponent(f.uid)}`, 'POST', body);
    if (ip !== currentIp) return false;
    rdmReplace(port, d.fixture);
    const what = Object.keys(body).map(k => k === 'identify' ? `identify ${body.identify ? 'on' : 'off'}` : k === 'start' ? `address ${body.start}` : `${k} ${body[k]}`).join(', ');
    toast(d.dryRun ? `DRY RUN — ${what} not sent` : `${d.fixture.label || d.fixture.uid}: ${what}`);
    sent = true;
  } catch (e) {
    toast(`${f.label || f.uid}: ${e.message}`, true);
  }
  delete rdm.fxBusy[f.uid];
  if (ip === currentIp) renderRdm();
  return sent;
}

function rdmAskOn(port) { rdm.confirm = port; renderRdm(); }
function rdmCancelOn() { rdm.confirm = null; renderRdm(); }

async function rdmSet(port, on) {
  const ip = currentIp;
  rdm.confirm = null;
  rdm.busy[port] = on ? 'Turning RDM on…' : 'Turning RDM off…';
  renderRdm();
  try {
    const d = await call(`/api/node/${ip}/rdm/${port}`, 'PUT', { on });
    if (ip !== currentIp) return;
    delete rdm.busy[port];
    // The node lists nothing until its search is done: say so from the start,
    // not "0 fixtures found by the node" from the ports read straight after.
    if (on && !d.dryRun) rdm.busy[port] = 'Searching…';
    rdmApply(d);
    if (d.dryRun) { toast(`DRY RUN — RDM ${on ? 'on' : 'off'} for port ${port} not sent`); return; }
    toast(`RDM ${on ? 'on' : 'off'} for port ${port}`);
    // The node starts searching the line: read once it has settled.
    if (on && await rdmWaitFound(port, 2000) && ip === currentIp) rdmRead(port);
  } catch (e) {
    delete rdm.busy[port];
    renderRdm();
    toast(`Port ${port}: ${e.message}`, true);
  }
}

// rdmWaitFound waits while the node searches a port after RDM goes on: it asks
// what the node has found every 1.5 s and returns once the number has stopped
// changing (after at least minMs), or after 30 s. 18 fixtures on one line took
// the node longer than the 2.5 s the page used to wait. False if the page moved on.
async function rdmWaitFound(port, minMs) {
  const ip = currentIp, start = Date.now();
  let last = -1, steady = 0;
  while (Date.now() - start < 30000) {
    await new Promise(r => setTimeout(r, 1500));
    if (ip !== currentIp) return false;
    let n = null;
    try { n = (await call(`/api/node/${ip}/rdm/${port}/found`)).count; } catch (_) {}
    if (n != null) {
      rdmSetCount(port, n);
      rdm.busy[port] = n ? `Searching… ${n} found` : 'Searching…'; // the node lists none until its search is done
      renderRdm();
      steady = n === last ? steady + 1 : 0;
      last = n;
      if (steady >= 1 && n > 0 && Date.now() - start >= minMs) break;
    }
  }
  delete rdm.busy[port];
  return ip === currentIp;
}

// rdmWaitRediscover waits for the node's search after a Rediscover. The node
// keeps its old list until the search is done and then swaps in the new one
// (a real LumiNode 12, 14 Nov 2026: about 10 s on a line of 18), so a steady
// count says nothing: the page waits for the list to change and settle, or for
// limitMs if it never changes (nothing new on the line). before is the list
// (sorted UIDs) just before the search. False if the page moved on.
async function rdmWaitRediscover(port, before, limitMs) {
  const ip = currentIp, start = Date.now(), was = before.join(',');
  let last = null, changed = false;
  while (Date.now() - start < limitMs) {
    await new Promise(r => setTimeout(r, 1500));
    if (ip !== currentIp) return false;
    let d = null;
    try { d = await call(`/api/node/${ip}/rdm/${port}/found`); } catch (_) {}
    if (d) {
      const now = (d.uids || []).join(','), left = Math.max(0, Math.ceil((limitMs - (Date.now() - start)) / 1000));
      if (now !== was) changed = true;
      rdmSetCount(port, d.count);
      // Until the list changes it is the one from before the search: no count yet.
      rdm.busy[port] = changed ? `Searching… ${d.count} found` : `Searching… up to ${left} s`;
      renderRdm();
      if (changed && d.count > 0 && now === last) break;
      last = now;
    }
  }
  delete rdm.busy[port];
  return ip === currentIp;
}

// rdmSetCount keeps a port's count as the node just gave it (the app pushes the
// same to every page; this covers the moment before that push lands).
function rdmSetCount(port, n) {
  const p = rdm.ports.find(p => p.port === port);
  if (p) p.count = n;
}

async function rdmRead(port) {
  const ip = currentIp;
  rdm.busy[port] = 'Reading…';
  renderRdm();
  try {
    const d = await call(`/api/node/${ip}/rdm/${port}/fixtures`);
    if (ip !== currentIp) return;
    rdm.read[port] = { ...d, at: new Date() };
    if (rdm.edit && rdm.edit.port === port) rdm.edit = null;
    if (rdm.sel && rdm.sel.port === port) {
      for (const u of [...rdm.sel.uids]) if (!d.fixtures.some(f => f.uid === u)) rdm.sel.uids.delete(u);
      if (!rdm.sel.uids.size) rdm.sel = null;
    }
  } catch (e) {
    toast(`Port ${port}: ${e.message}`, true);
  }
  delete rdm.busy[port];
  if (ip === currentIp) renderRdm();
}

async function rdmDiscover(port) {
  const ip = currentIp;
  rdm.busy[port] = 'Searching…';
  renderRdm();
  let before = [];
  try {
    before = (await call(`/api/node/${ip}/rdm/${port}/found`)).uids || [];
    await call(`/api/node/${ip}/rdm/${port}/discovery`, 'POST');
  } catch (e) {
    toast(`Port ${port}: ${e.message}`, true);
    delete rdm.busy[port];
    if (ip === currentIp) renderRdm();
    return;
  }
  if (await rdmWaitRediscover(port, before, 25000) && ip === currentIp) rdmRead(port);
}

nc$('btn-rdm-read-all').onclick = async () => {
  for (const p of rdm.ports.filter(p => p.rdm)) await rdmRead(p.port);
};

function csvCell(v) {
  const s = String(v == null ? '' : v);
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

nc$('btn-rdm-csv').onclick = () => {
  const dev = window._devCurrent || {};
  const node = dev.shortName || dev.longName || currentIp;
  const head = ['Node', 'Node IP', 'Port', 'Art-Net', 'Address', 'Channels', 'Personality', 'Personality name', 'Manufacturer',
    'Model', 'Label', 'Software', 'Sensors', 'Category', 'UID', 'Note'];
  const lines = [head];
  Object.keys(rdm.read).map(Number).sort((a, b) => a - b).forEach(port => {
    const res = rdm.read[port];
    res.fixtures.forEach(f => lines.push([node, currentIp, port, res.universe, f.start ?? '', f.footprint, f.personality || '',
      f.personalityName, f.manufacturer, f.model, f.label, f.software, f.sensors, f.category, f.uid, rdmProblems(f, res.fixtures).join('. ')]));
  });
  const blob = new Blob([lines.map(l => l.map(csvCell).join(',')).join('\r\n') + '\r\n'], { type: 'text/csv' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = `RDM ${node.replace(/[^A-Za-z0-9 _.-]/g, '_')}.csv`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
};

document.querySelectorAll('.tab').forEach(t => {
  if (t.dataset.tab === 'rdm') t.addEventListener('click', () => { if (currentIp) rdmLoad(); });
});

// ── Password ───────────────────────────────────────────────────────────────────
function openPassword() {
  nc$('pw-error').style.display = 'none';
  nc$('pw-pass').value = '';
  nc$('pw-modal').classList.add('open');
}
nc$('pw-modal-close').onclick = nc$('pw-cancel').onclick = () => nc$('pw-modal').classList.remove('open');
nc$('pw-save').onclick = async () => {
  const r = await fetch(`/api/node/${currentIp}/password`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: nc$('pw-user').value, password: nc$('pw-pass').value }),
  });
  if (r.ok) {
    nc$('pw-modal').classList.remove('open');
    doConnect(currentIp);
    return;
  }
  nc$('pw-error').textContent = r.status === 401 ? 'That password was rejected. Try again.' : 'The device did not answer.';
  nc$('pw-error').style.display = 'block';
};

// ── Following a device through a job ───────────────────────────────────────────
// While a job has this device, write buttons are disabled with the reason, and the
// server refuses writes too. Tab buttons and Cancel stay usable.
// The IO class and usage filters and SHOW INTERNAL only change the view, so they stay usable.
const LOCKABLE = '.content button:not(#io-filters button):not(#adv-tabs button), .content .rdm-tick input, .modal-foot button:not([id$="cancel"])';
let lockWhy = '';
function applyLock() {
  document.querySelectorAll(LOCKABLE).forEach(b => {
    if (lockWhy && !b.disabled) {
      b.dataset.unlockTitle = b.title;
      b.disabled = true;
      b.title = lockWhy;
    } else if (!lockWhy && 'unlockTitle' in b.dataset) {
      b.disabled = false;
      b.title = b.dataset.unlockTitle;
      delete b.dataset.unlockTitle;
    }
  });
}
new MutationObserver(() => { if (lockWhy) applyLock(); }).observe(document.body, { childList: true, subtree: true });

function setBanner(text) {
  nc$('follow-banner').textContent = text;
  nc$('follow-banner').style.display = text ? 'block' : 'none';
}

// followId is the device list's id for the node that is open (null for a node
// opened by address alone). The loop runs for as long as the page is open.
let followId = null;
async function followDevice() {
  const id = followId;
  if (id) {
    try {
      const r = await fetch(`/api/device/${encodeURIComponent(id)}`);
      if (r.ok && id === followId) applyFollow(await r.json());
    } catch (_) {}
  }
  setTimeout(followDevice, 1500);
}

// followIp is where the followed device was last seen. The page moves with the
// device only when the device itself moves and the page is still on it.
let followIp = null; // set by ncOpen

function applyFollow(d) {
  const moved = d.known && d.ip && d.ip !== followIp, onIt = currentIp === followIp;
  const wasLocked = !!lockWhy;
  lockWhy = d.locked && onIt ? d.why : '';
  applyLock();
  if (d.locked) { setBanner(onIt ? d.why : ''); return; }
  setBanner('');
  if (!moved) {
    if (wasLocked && onIt) doConnect(currentIp);
    return;
  }
  if (!onIt) { followIp = d.ip; return; } // on another node: just keep track
  if (!d.reachable) { setBanner(`This device is now at ${d.ip}. ${d.reachReason || 'This computer cannot reach it.'}`); return; }
  followIp = d.ip;
  toast(`This device is now at ${d.ip}`);
  window.dispatchEvent(new CustomEvent('nc-moved', { detail: { id: followId, ip: d.ip } })); // the hosting page keeps its address
  doConnect(d.ip);
}

// ── Init ───────────────────────────────────────────────────────────────────────
// Back on the tab the page was on before it reloaded for a new app version.
try {
  const t = sessionStorage.getItem('nc_tab');
  sessionStorage.removeItem('nc_tab');
  const el = t && document.querySelector(`.tab[data-tab="${t}"]`);
  if (el) el.click();
} catch (_) {}
// ncOpen is how the hosting page shows a node: its address, and the device
// list's id so the page stays on that device if a job moves it. Opening the node
// that is already open does nothing, so what is on the page stays; one that did
// not answer is tried again.
function ncOpen(ip, id) {
  if (ip === currentIp && (id || null) === followId && lastSnap.device) return;
  followId = id || null;
  followIp = ip;
  lockWhy = '';
  applyLock();
  setBanner('');
  doConnect(ip);
}
followDevice();
