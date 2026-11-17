'use strict';
// Fixtures view: every RDM fixture on every LumiNode in the device list, read
// through the node configure API (/api/node/<ip>/rdm...), which only exists in a
// tool that mounts the configure page. Reading asks each fixture over Art-Net
// RDM and changes nothing; RDM is turned on per port on the node's RDM tab.

const fxEls = {
  view: $('fixturesView'), status: $('fixturesStatus'), body: $('fixturesList'), banner: $('fixturesBanner'),
  read: $('btnReadFixtures'), csv: $('btnFixturesCsv'), clashes: $('fixturesClashesOnly'),
};

// Per node: { name, ip, id, ports: [...], read: { port: result }, error, at }
let fixtureNodes = [];
let fixturesReading = false;

function fixtureNodeList() {
  return (state && state.devices || []).filter(d => d.apiKind === 'luminode' && d.ip);
}

function fixtureWhyNot(d) {
  if (inRunningJob(d.id)) return 'An address or reset job has this node';
  if (d.sharedIp) return `Shares ${d.ip} with another device`;
  if (!d.reachable) return 'This computer cannot reach it';
  return '';
}

async function readAllFixtures() {
  if (fixturesReading) return;
  const devs = fixtureNodeList();
  fixturesReading = true;
  fixtureNodes = devs.map(d => ({ name: d.name || d.serial, ip: d.ip, id: d.id, ports: [], read: {}, error: fixtureWhyNot(d), skipped: !!fixtureWhyNot(d) }));
  renderFixtures();
  let done = 0;
  const one = async n => {
    if (n.skipped) return;
    try {
      const d = await fxCall(`/api/node/${n.ip}/rdm`);
      n.ports = d.ports || [];
      n.artnet = d.artnet;
      for (const p of n.ports.filter(p => p.rdm)) {
        try {
          n.read[p.port] = await fxCall(`/api/node/${n.ip}/rdm/${p.port}/fixtures`);
        } catch (e) {
          n.read[p.port] = { error: e.message, fixtures: [] };
        }
        renderFixtures();
      }
    } catch (e) {
      n.error = e.message;
    }
    n.at = new Date();
    done++;
    fxEls.status.textContent = `— reading ${done} of ${fixtureNodes.filter(x => !x.skipped).length} nodes…`;
    renderFixtures();
  };
  // Nodes in parallel, a few at a time; each node's ports one after another (the server does one RDM request at a time per node).
  const queue = [...fixtureNodes];
  await Promise.all(Array.from({ length: Math.min(4, queue.length) }, async () => { while (queue.length) await one(queue.shift()); }));
  fixturesReading = false;
  renderFixtures();
}

async function fxCall(url) {
  const r = await fetch(url);
  const d = await r.json().catch(() => ({}));
  if (!r.ok || d.ok === false) throw new Error(d.error || `HTTP ${r.status}`);
  return d;
}

function fxProblems(f, all) {
  const out = [];
  if (f.error) out.push(f.error);
  if (f.overlaps && f.overlaps.length) {
    out.push('Shares channels with ' + f.overlaps.map(u => {
      const o = all.find(x => x.uid === u);
      return o ? `${o.label || o.model || u} (${o.start})` : u;
    }).join(', '));
  }
  if (f.pastEnd) out.push('Runs past channel 512');
  return out;
}

function fxUniverse(u) { return u >= 0 ? `${u >> 8 & 0x7f}:${u >> 4 & 0xf}:${u & 0xf} (${u})` : 'none'; }

function fxTable(fixtures, clashesOnly) {
  const problems = fixtures.map(f => fxProblems(f, fixtures));
  const shown = fixtures.map((f, i) => [f, problems[i]]).filter(([, p]) => !clashesOnly || p.length);
  if (!shown.length) return '';
  // A problem goes on its own line under the fixture: the view is too narrow for a note column.
  return `<table class="fx-table"><thead><tr><th>Addr</th><th>Ch</th><th>Personality</th><th>Manufacturer</th><th>Model</th><th>Label</th>
    <th>Software</th><th>Sensors</th></tr></thead><tbody>` + shown.map(([f, p]) => {
    const end = f.start != null && f.footprint ? f.start + f.footprint - 1 : null;
    const pers = f.personality ? (f.personalityName ? `${esc(f.personalityName)} <span class="muted">${f.personality}/${f.personalities}</span>` : `${f.personality} of ${f.personalities}`) : '—';
    const cls = f.error ? 'fx-err' : p.length ? 'fx-clash' : '';
    return `<tr class="${cls}${p.length ? ' fx-has-why' : ''}" title="UID ${esc(f.uid)}${f.category ? ' · ' + esc(f.category) : ''}">
      <td class="fx-num fx-addr">${f.start != null ? f.start + (end > f.start ? `<span class="muted">–${end}</span>` : '') : '—'}</td>
      <td class="fx-num">${f.error && !f.footprint ? '—' : f.footprint}</td><td>${pers}</td>
      <td>${esc(f.manufacturer) || '—'}</td><td>${esc(f.model) || '—'}</td><td>${esc(f.label) || (f.error ? esc(f.uid) : '—')}</td>
      <td>${esc(f.software) || '—'}</td><td class="fx-num">${f.error ? '—' : f.sensors}</td></tr>` +
      (p.length ? `<tr class="${cls} fx-why-row"><td></td><td colspan="7" class="fx-why">${esc(p.join('. '))}</td></tr>` : '');
  }).join('') + '</tbody></table>';
}

function renderFixtures() {
  if (!fxEls.view || fxEls.view.classList.contains('hidden')) return;
  const nodes = fixtureNodeList();
  fxEls.read.disabled = fixturesReading || !nodes.length;
  fxEls.read.title = fixturesReading ? 'Reading…' : nodes.length ? 'Ask every fixture on every node with RDM on (changes nothing)' : 'No LumiNodes found yet. Scan on Configure.';
  fxEls.read.textContent = fixturesReading ? 'Reading…' : '⟳ Read all nodes';
  const total = fixtureNodes.reduce((n, x) => n + Object.values(x.read).reduce((m, r) => m + r.fixtures.length, 0), 0);
  fxEls.csv.disabled = !total;
  fxEls.csv.title = total ? 'Save every fixture as a CSV file' : 'Read the fixtures first';
  if (!fixturesReading) fxEls.status.textContent = fixtureNodes.length ? `— ${total} fixture(s) on ${fixtureNodes.length} node(s)` : '';

  const artErr = fixtureNodes.map(n => n.artnet).find(a => a && !a.ok);
  fxEls.banner.textContent = artErr ? `${artErr.error}. Fixtures come from what each node has overheard instead, which can be incomplete.` : '';
  fxEls.banner.classList.toggle('hidden', !artErr);

  if (!fixtureNodes.length) {
    fxEls.body.innerHTML = `<div class="empty-state">${nodes.length ? `${nodes.length} LumiNode(s) in the list. Click <strong>Read all nodes</strong>.` : 'No LumiNodes found yet. Scan on the Configure view.'}</div>`;
    return;
  }
  const clashesOnly = fxEls.clashes.checked;
  fxEls.body.innerHTML = fixtureNodes.map(n => {
    const on = n.ports.filter(p => p.rdm), off = n.ports.filter(p => !p.rdm && p.ioType === 'output');
    const live = fixtureNodeList().find(d => d.id === n.id) || n;
    let summary;
    if (n.skipped) summary = `Skipped: ${esc(n.error)}`;
    else if (n.error) summary = `<span class="fx-bad">${esc(n.error)}</span>`;
    else if (!n.at && !n.ports.length) summary = '<span class="spin">reading</span>';
    else summary = on.length ? `RDM on: port ${on.map(p => p.port).join(', ')}` + (off.length ? ` · off on ${off.length} output port(s)` : '')
      : `RDM is off on every port. Turn it on per port on the node's RDM tab.`;
    const ports = on.map(p => {
      const res = n.read[p.port];
      let inner;
      if (!res) inner = '<div class="fx-note"><span class="spin">reading</span></div>';
      else if (res.error) inner = `<div class="fx-note fx-bad">${esc(res.error)}</div>`;
      else {
        const t = fxTable(res.fixtures, clashesOnly);
        if (clashesOnly && !t) return '';
        inner = (t || '<div class="fx-note">No fixtures answered on this port.</div>') +
          (res.source === 'overheard' ? `<div class="fx-note fx-warn">${esc(res.note)}</div>` : '');
      }
      const count = res && !res.error ? ` · ${res.fixtures.length} fixture${res.fixtures.length === 1 ? '' : 's'}` : '';
      return `<div class="fx-port"><div class="fx-port-head">Port ${p.port} <span class="muted">${esc(p.source)} · Art-Net ${esc(fxUniverse(p.universe))}${count}</span></div>${inner}</div>`;
    }).join('');
    return `<div class="fx-node">
      <div class="fx-node-head"><b>${esc(n.name)}</b> <span class="mono muted">${esc(n.ip)}</span>
        <span class="fx-summary muted">${summary}</span>${configureButton(n.id, live.ip, configureWhyNot('luminode', n.id, live.ip))}</div>
      ${ports}</div>`;
  }).join('');
}

function fixturesCsv() {
  const cell = v => { const s = String(v ?? ''); return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s; };
  const lines = [['Node', 'Node IP', 'Port', 'Art-Net', 'Address', 'Channels', 'Personality', 'Personality name', 'Manufacturer',
    'Model', 'Label', 'Software', 'Sensors', 'Category', 'UID', 'Note']];
  for (const n of fixtureNodes) {
    for (const port of Object.keys(n.read).map(Number).sort((a, b) => a - b)) {
      const res = n.read[port];
      for (const f of res.fixtures) lines.push([n.name, n.ip, port, res.universe, f.start ?? '', f.footprint, f.personality || '', f.personalityName,
        f.manufacturer, f.model, f.label, f.software, f.sensors, f.category, f.uid, fxProblems(f, res.fixtures).join('. ')]);
    }
  }
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([lines.map(l => l.map(cell).join(',')).join('\r\n') + '\r\n'], { type: 'text/csv' }));
  a.download = `RDM fixtures ${new Date().toISOString().slice(0, 16).replace('T', ' ').replace(':', '.')}.csv`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

fxEls.read.addEventListener('click', readAllFixtures);
fxEls.csv.addEventListener('click', fixturesCsv);
fxEls.clashes.addEventListener('change', renderFixtures);
fxEls.body.addEventListener('click', onConfigureClick);
