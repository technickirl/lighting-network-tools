'use strict';
// Groups view: reads each GigaCore's groups (what other switches call VLANs) and shows them with the colour the
// switch gives each group. Read groups only sends GET requests; Apply names and Apply profile write to the ticked
// switches through the settings run (groups.go).

(function () {
  const $g = id => document.getElementById(id);
  const listEl = $g('groupsList'), statusEl = $g('groupsStatus'), btn = $g('btnReadGroups');
  let result = null, reading = false;
  const namesEl = $g('groupsNames');
  const typed = {};          // group number → name typed in the naming panel (kept across re-reads)
  const picked = new Set();  // ids of the switches the names will go to
  let running = false, runId = null;   // no switch is ticked until the user ticks it: every Apply here writes to real switches
  const profEl = $g('groupsProfile');
  const editEl = $g('groupsEditor');
  let profile = null, profileNote = '';
  const NAME_OK = /^[A-Za-z0-9 _.\-]{1,16}$/;

  // The switch's own colours are bright; a swatch is enough, text stays in the normal colour.
  const swatch = c => `<span class="grp-swatch" style="background:${esc(c || '#999')}"></span>`;
  const portList = a => (a && a.length ? a.join(', ') : '—');

  function groupOfPort(groups, port) {
    return groups.find(g => g.type !== 'ISL' && g.ports.includes(port)) || groups.find(g => g.ports.includes(port));
  }

  function card(s) {
    const tick = s.error ? '' : `<input type="checkbox" data-pick="${esc(s.id)}" ${picked.has(s.id) ? 'checked' : ''} title="Send the group names above to this switch"> `;
    const head = `<div class="grp-head">${tick}<b>${esc(s.name || s.serial)}</b> <span class="muted">${esc(s.model || '')} · ${esc(s.ip)} · ${esc(s.serial)}</span>${s.error ? '' : ` <button class="btn btn-small grp-save" data-save="${esc(s.id)}" title="Start a profile from this switch's groups, to adjust and keep in the app">Save as profile</button>`}</div>`;
    if (s.error) return `<div class="grp-card">${head}<div class="muted grp-error">${esc(s.error)}</div></div>`;
    const used = s.groups.filter(g => g.type === 'Management' || g.type === 'ISL' ? g.ports.length || g.type === 'Management' : g.ports.length || g.tagged.length);
    const idle = s.groups.length - used.length;
    // One chip per port, in the colour of the group it is in (untagged).
    let chips = '';
    for (let p = 1; p <= (s.portCount || 0); p++) {
      const g = groupOfPort(s.groups, p);
      const tagged = s.groups.filter(x => x.tagged.includes(p)).map(x => x.name);
      chips += `<span class="grp-port" style="background:${esc(g ? g.color : '#444')}" title="Port ${p}: ${esc(g ? g.name : 'no group')}${tagged.length ? ' · tagged in ' + esc(tagged.join(', ')) : ''}">${p}${tagged.length ? '<i>t</i>' : ''}</span>`;
    }
    const rows = used.map(g => `<tr><td>${swatch(g.color)}</td><td>${esc(g.name)}${g.type !== "Normal" && g.type !== g.name ? ` <span class="muted">${esc(g.type)}</span>` : ""}</td><td class="mono">${g.type === 'ISL' ? '—' : g.vid}</td><td class="mono">${esc(portList(g.ports))}</td><td class="mono">${esc(portList(g.tagged))}</td></tr>`).join('');
    // With a profile in use, a ticked switch that would change shows ports now → after in place of the plain row;
    // otherwise the plain coloured row stays, with a short note under it when there is one.
    const pv = profile && picked.has(s.id) ? preview(s) : { replaces: '', note: '' };
    return `<div class="grp-card">${head}${pv.replaces || `<div class="grp-ports">${chips}</div>`}${pv.note}
      <table class="grp-table"><thead><tr><th></th><th>Group</th><th>VLAN ID</th><th>Ports</th><th>Tagged ports</th></tr></thead><tbody>${rows}</tbody></table>
      ${idle > 0 ? `<div class="muted">${idle} more group${idle === 1 ? '' : 's'} with no ports.</div>` : ''}<div class="grp-status" data-sid="${esc(s.id)}"></div></div>`;
  }

  const typedNames = () => Object.fromEntries(Object.entries(typed).filter(([, v]) => v.trim() !== '').map(([k, v]) => [k, v.trim()]));
  function namesProblem() {
    const n = typedNames();
    if (!Object.keys(n).length) return 'Type a name for at least one group';
    const bad = Object.entries(n).find(([, v]) => !NAME_OK.test(v));
    if (bad) return `"${bad[1]}" cannot be a group name: use 1 to 16 letters, numbers, spaces, - _ or .`;
    if (!picked.size) return 'Tick at least one switch below';
    if (running || (window.Settings && window.Settings.running())) return 'Busy';
    return '';
  }

  // The group numbers any switch has (2 and up), with the current name and VLAN ID from the first switch that has them.
  function drawNames() {
    const first = new Map();
    for (const s of result || []) for (const g of s.groups || []) if (g.type === 'Normal' && !first.has(g.nr)) first.set(g.nr, g);
    if (!first.size) { namesEl.innerHTML = ''; return; }
    const rows = [...first.values()].sort((a, b) => a.nr - b.nr).map(g => `<label class="grp-name-row" title="VLAN ID ${g.vid}">${swatch(g.color)}<span class="mono grp-nr">${g.nr}</span><input type="text" data-nr="${g.nr}" maxlength="16" placeholder="${esc(g.name)}" value="${esc(typed[g.nr] || '')}"><span class="muted mono">VLAN ${g.vid}</span></label>`).join('');
    const why = namesProblem();
    namesEl.innerHTML = `<div class="grp-names"><div class="grp-names-title">Name groups on the ticked switches <span class="muted">— leave a box empty to keep the name it has. Ports and VLAN IDs are not changed.</span></div><div class="grp-name-grid">${rows}</div><div class="btn-row"><button class="btn btn-primary" id="btnApplyNames" ${why ? 'disabled' : ''} title="${esc(why || 'Rename these groups on the ticked switches')}">Apply names to ${picked.size} switch${picked.size === 1 ? '' : 'es'}</button><span class="muted" id="groupsNamesNote"></span></div></div>`;
  }
  function updateApply() {
    const pb = $g('btnApplyProfile');
    if (pb) { const w = profileProblem(); pb.disabled = !!w; pb.title = w || 'Apply the profile to the ticked switches'; pb.textContent = `Apply profile to ${willApply()} switch${willApply() === 1 ? '' : 'es'}`; }
    const b = $g('btnApplyNames');
    if (!b) return;
    const why = namesProblem();
    b.disabled = !!why; b.title = why || 'Rename these groups on the ticked switches';
    b.textContent = `Apply names to ${picked.size} switch${picked.size === 1 ? '' : 'es'}`;
  }

  function draw() {
    if (!result) { listEl.innerHTML = '<div class="empty-state">Press <b>Read groups</b> to read every GigaCore found by the last scan.</div>'; statusEl.textContent = ''; drawProfile(); return; }
    statusEl.textContent = `— ${result.length} switch${result.length === 1 ? '' : 'es'}`;
    listEl.innerHTML = result.length ? result.map(card).join('') : '<div class="empty-state">No GigaCores found yet. Scan on the Address &amp; reset tab first.</div>';
    drawNames();
    drawProfile();
  }

  // ─── Profiles: groups (the same everywhere) and a port layout for each switch size, remembered by the app ───
  // A saved profile: { name, groups: [{ nr, name, vid }], layouts: [{ portCount, isl?: { ports }, groups: [{ nr, ports, tagged }] }] }
  let saved = [], savedPath = '';
  // The profile being edited: { name, rows: [{ nr, name, vid }], layouts: { [portCount]: { isl: { set, cells } | null, cells: { [nr]: { [port]: 'U' | 'T' } } } }, active }
  let draft = null;
  const DEFAULT_COLOR = { 2: '#E80000', 3: '#32CD32', 4: '#00E8E8', 5: '#CC00CC', 6: '#FF8200', 7: '#E8E800', 8: '#FF0099', 9: '#20B2AA', 10: '#FA8072', 11: '#0033FF', 12: '#008000', 13: '#BB5555', 14: '#8B0000', 15: '#4B0082', 16: '#999900', 17: '#7CE800', 18: '#660066', 19: '#2F4F4F', 20: '#0066CC' };
  const colourOf = nr => {
    for (const s of result || []) { const g = (s.groups || []).find(x => x.nr === nr && x.type === 'Normal'); if (g) return g.color; }
    return DEFAULT_COLOR[nr] || '#999';
  };
  const list = a => (a && a.length ? a.join(',') : 'none');
  const sorted = a => [...a].sort((x, y) => x - y);
  const defaultName = nr => 'Group' + String(nr).padStart(2, '0');
  // The name a group gets when it is added to a profile: what was typed for it in "Name groups" above, otherwise the
  // name the switches have now, otherwise the default.
  const nameFor = nr => {
    const t = (typed[nr] || '').trim();
    if (NAME_OK.test(t)) return t;
    for (const s of result || []) { const g = (s.groups || []).find(x => x.nr === nr && x.type === 'Normal'); if (g) return g.name; }
    return defaultName(nr);
  };
  const sizesOf = p => p.layouts.map(l => l.portCount).sort((a, b) => a - b);

  // One layout of a saved profile as the single-size profile that is applied to a switch (and sent to the server).
  function resolved(p, portCount) {
    const l = p && p.layouts.find(x => x.portCount === portCount);
    if (!l) return null;
    return {
      name: p.name, portCount, ...(l.isl ? { isl: l.isl } : {}),
      groups: p.groups.map(g => { const lg = l.groups.find(x => x.nr === g.nr) || {}; return { nr: g.nr, name: g.name, vid: g.vid, ports: lg.ports || [], tagged: lg.tagged || [] }; }),
    };
  }

  const emptyLayout = () => ({ isl: { set: false, cells: {} }, cells: {} });
  function draftFromSaved(p) {
    // A profile can come from a file, so everything that should be a number is made one before it reaches the page.
    const int = v => (Number.isInteger(+v) ? +v : 0), ints = a => (Array.isArray(a) ? a : []).map(int).filter(x => x > 0);
    const groups = (Array.isArray(p.groups) ? p.groups : []).filter(g => g && int(g.nr) >= 2 && int(g.nr) <= 99);
    const layouts = (Array.isArray(p.layouts) ? p.layouts : []).filter(l => l && int(l.portCount) >= 1 && int(l.portCount) <= 52);
    if (!layouts.length) throw new Error('it has no port layout for a switch size');
    const d = { name: String(p.name || ''), rows: groups.map(g => ({ nr: int(g.nr), name: String(g.name || ''), vid: String(int(g.vid)) })), layouts: {}, active: 0 };
    for (const l of layouts) {
      const cells = {};
      for (const g of Array.isArray(l.groups) ? l.groups : []) { const nr = int(g && g.nr); cells[nr] = {}; for (const x of ints(g.ports)) cells[nr][x] = 'U'; for (const x of ints(g.tagged)) cells[nr][x] = 'T'; }
      d.layouts[int(l.portCount)] = { isl: l.isl ? { set: true, cells: Object.fromEntries(ints(l.isl.ports).map(x => [x, 'U'])) } : { set: false, cells: {} }, cells };
    }
    d.active = Math.min(...Object.keys(d.layouts).map(Number));
    return d;
  }
  // A new profile from what a switch has now: the groups that have ports or have been named or given another VLAN ID.
  function draftFromSwitch(s) {
    const gs = s.groups.filter(g => g.type === 'Normal' && (g.ports.length || g.tagged.length || g.name !== defaultName(g.nr) || g.vid !== g.nr * 100));
    return draftFromSaved({
      name: `${s.name || s.serial} groups`, groups: gs.map(g => ({ nr: g.nr, name: g.name, vid: g.vid })),
      layouts: [{ portCount: s.portCount, groups: gs.map(g => ({ nr: g.nr, ports: g.ports, tagged: g.tagged })) }],
    });
  }
  function newDraft(portCount) {
    return { name: '', rows: [2, 3].map(nr => ({ nr, name: nameFor(nr), vid: String(nr * 100) })), layouts: { [portCount]: emptyLayout() }, active: portCount };
  }
  function savedFromDraft(d) {
    const portsOf = (cells, k) => sorted(Object.keys(cells || {}).filter(p => cells[p] === k).map(Number));
    return {
      name: d.name.trim(),
      groups: d.rows.map(r => ({ nr: r.nr, name: r.name.trim(), vid: parseInt(r.vid, 10) || 0 })).sort((a, b) => a.nr - b.nr),
      layouts: Object.keys(d.layouts).map(Number).sort((a, b) => a - b).map(n => {
        const L = d.layouts[n];
        return {
          portCount: n,
          // the ISL row only counts once it has been used; with no ports that means "turn them all back into Management"
          ...(L.isl && L.isl.set ? { isl: { ports: sorted(Object.keys(L.isl.cells).map(Number)) } } : {}),
          groups: d.rows.map(r => ({ nr: r.nr, ports: portsOf(L.cells[r.nr], 'U'), tagged: portsOf(L.cells[r.nr], 'T') })).sort((a, b) => a.nr - b.nr),
        };
      }),
    };
  }
  function draftProblem(d) {
    if (!d.name.trim()) return 'Give the profile a name';
    if (d.name.trim().length > 60) return 'The profile name is too long (60 characters at most)';
    if (!d.rows.length) return 'Add at least one group';
    if (!Object.keys(d.layouts).length) return 'Add a port layout for at least one switch size';
    const vids = new Set();
    for (const r of d.rows) {
      if (!NAME_OK.test(r.name.trim())) return `Group ${r.nr}: the name must be 1 to 16 letters, numbers, spaces, - _ or .`;
      const v = parseInt(r.vid, 10);
      if (!(v >= 2 && v <= 4094)) return `Group ${r.nr}: the VLAN ID must be 2 to 4094`;
      if (vids.has(v)) return `Two groups use VLAN ID ${v}`;
      vids.add(v);
    }
    return '';
  }

  // What applying the profile would do to switch s, worked out from what was read. The server checks again before it writes.
  function planFor(s) {
    const out = { changes: [], problems: [], leaving: [], islChanged: [], before: [], after: [] };
    if (!profile) return out;
    const prof = resolved(profile, s.portCount);
    if (!prof) { out.problems.push(`${profile.name} has no ${s.portCount}-port layout yet (it has ${sizesOf(profile).join(', ')})`); return out; }
    const islGroup = s.groups.find(g => g.type === 'ISL') || { ports: [], color: '#999999', name: 'ISL' };
    const isl = prof.isl ? [] : islGroup.ports;   // a profile that does not set the ISL ports keeps the switch's own where they are
    const mgmt = (s.groups.find(g => g.type === 'Management') || { ports: [] }).ports;
    for (const pg of prof.groups) {
      const c = s.groups.find(g => g.nr === pg.nr && g.type === 'Normal');
      if (!c) { out.problems.push(`has no group ${pg.nr}`); continue; }
      const bad = [...pg.ports, ...pg.tagged].filter(p => isl.includes(p));
      if (bad.length) { out.problems.push(`port ${bad.join(', ')} is a link to another switch (ISL) here`); continue; }
      const parts = [];
      if (c.name !== pg.name) parts.push(`name ${c.name} → ${pg.name}`);
      if (c.vid !== pg.vid) parts.push(`VLAN ${c.vid} → ${pg.vid}`);
      if (list(sorted(c.ports)) !== list(pg.ports)) parts.push(`ports ${list(c.ports)} → ${list(pg.ports)}`);
      if (list(sorted(c.tagged)) !== list(pg.tagged)) parts.push(`tagged ${list(c.tagged)} → ${list(pg.tagged)}`);
      if (parts.length) out.changes.push(`Group ${pg.nr} (${pg.name}): ${parts.join('; ')}`);
      for (const p of pg.ports) if (mgmt.includes(p) && !out.leaving.includes(p)) out.leaving.push(p);
    }
    out.leaving.sort((a, b) => a - b);
    if (prof.isl && list(sorted(islGroup.ports)) !== list(prof.isl.ports)) {
      out.changes.push(`ISL ports ${list(islGroup.ports)} → ${list(prof.isl.ports)}`);
      out.islChanged = [...new Set([...islGroup.ports.filter(x => !prof.isl.ports.includes(x)), ...prof.isl.ports.filter(x => !islGroup.ports.includes(x))])].sort((a, b) => a - b);
    }
    // The ports as they are now and as they would be, for the side-by-side picture.
    const mg = s.groups.find(g => g.type === 'Management') || { name: 'Management', color: '#325197' };
    const inProfile = nr => prof.groups.some(g => g.nr === nr);
    for (let p = 1; p <= s.portCount; p++) {
      const cur = groupOfPort(s.groups, p);
      const curTag = s.groups.filter(g => g.tagged.includes(p)).map(g => g.name);
      out.before.push({ name: cur ? cur.name : 'no group', color: cur ? cur.color : '#444', tagged: curTag });
      const claim = prof.groups.find(g => g.ports.includes(p));
      let after;
      if (prof.isl && prof.isl.ports.includes(p)) after = { name: islGroup.name, color: islGroup.color };
      else if (claim) after = { name: claim.name, color: colourOf(claim.nr) };
      else if (cur && cur.type === 'ISL' && !prof.isl) after = { name: cur.name, color: cur.color };
      else if (cur && cur.type === 'Normal' && !inProfile(cur.nr)) after = { name: cur.name, color: cur.color };
      else after = { name: mg.name, color: mg.color };
      const tag = [...prof.groups.filter(g => g.tagged.includes(p)).map(g => g.name), ...s.groups.filter(g => g.type === 'Normal' && !inProfile(g.nr) && g.tagged.includes(p)).map(g => g.name)];
      out.after.push({ ...after, tagged: tag, changed: after.name !== (cur ? cur.name : 'no group') || list(tag) !== list(curTag) });
    }
    return out;
  }
  const willApply = () => (result || []).filter(s => picked.has(s.id) && !s.error && profile && !planFor(s).problems.length && planFor(s).changes.length).length;
  function profileProblem() {
    if (!profile) return 'Choose a saved profile first (Use)';
    const ticked = (result || []).filter(s => picked.has(s.id) && !s.error);
    if (!ticked.length) return 'Tick at least one switch below';
    const usable = ticked.filter(s => !planFor(s).problems.length);   // a switch that cannot take the profile is skipped, not a blocker
    if (!usable.length) return 'None of the ticked switches can take this profile (see below)';
    if (usable.every(s => !planFor(s).changes.length)) return 'The ticked switches already match this profile';
    if (running || (typeof Settings !== 'undefined' && Settings.running())) return 'Busy';
    return '';
  }
  const summary = p => {
    const used = p.groups.filter(g => p.layouts.some(l => { const lg = l.groups.find(x => x.nr === g.nr); return lg && (lg.ports.length || lg.tagged.length); }));
    return `${sizesOf(p).join(' / ')} ports · ${used.length ? used.map(g => `${g.nr} ${g.name}`).join(' · ') : 'no ports assigned'}`;
  };

  // The ports of a ticked switch now → after the chosen profile, drawn in the switch's card.
  function preview(s) {
    const p = planFor(s);
    const chip = (c, n, leaving) => `<span class="grp-port ${c.changed ? 'chg' : ''} ${leaving ? 'leave' : ''}" style="background:${esc(c.color)}" title="Port ${n}: ${esc(c.name)}${c.tagged.length ? ' · tagged in ' + esc(c.tagged.join(', ')) : ''}">${n}${c.tagged.length ? '<i>t</i>' : ''}</span>`;
    if (p.problems.length) return { replaces: '', note: `<ul class="grp-prob">${p.problems.map(x => `<li class="bad">${esc(x)}</li>`).join('')}</ul>` };
    if (!p.changes.length) return { replaces: '', note: '<div class="muted grp-same">Already matches this profile.</div>' };
    const notes = p.changes.filter(x => /name |VLAN |^ISL/.test(x)).map(x => `<li>${esc(x.replace(/; ?(ports|tagged) [^;]*/g, ''))}</li>`);
    const portsChange = p.after.some(c => c.changed);   // names or VLAN IDs alone leave the squares as they are
    return { replaces: !portsChange ? '' : `<div class="grp-cmp"><div class="grp-ports">${p.before.map((c, i) => chip(c, i + 1, false)).join('')}</div><span class="grp-arrow">→</span><div class="grp-ports">${p.after.map((c, i) => chip(c, i + 1, p.leaving.includes(i + 1) || p.islChanged.includes(i + 1))).join('')}</div></div>`, note: `
      ${p.islChanged.length ? `<div class="grp-leave">ISL port${p.islChanged.length === 1 ? '' : 's'} ${p.islChanged.join(', ')} (dashed) change${p.islChanged.length === 1 ? 's' : ''}: these are links to other switches, so changing them can cut switches off from each other.</div>` : ''}${p.leaving.length ? `<div class="grp-leave">Port${p.leaving.length === 1 ? '' : 's'} ${p.leaving.join(', ')} (dashed) leave${p.leaving.length === 1 ? 's' : ''} Management and will no longer reach this switch's address.</div>` : ''}${notes.length ? `<ul>${notes.join('')}</ul>` : ''}` };
  }

  function drawProfile() {
    const seen = [...new Set([10, 12, 16, ...(result || []).filter(s => s.portCount).map(s => s.portCount)])].sort((a, b) => a - b);
    let body = `<div class="grp-names-title">Profiles <span class="muted">— groups plus a port layout for each switch size. The app remembers them${savedPath ? ` (in ${esc(savedPath)})` : ''}.</span></div>`;
    body += saved.length ? saved.map(p => `<div class="grp-saved ${profile && profile.name === p.name ? 'sel' : ''}"><div><b>${esc(p.name)}</b> <span class="muted">· ${esc(summary(p))}</span></div>
      <div class="grp-saved-btns"><button class="btn btn-small" data-use="${esc(p.name)}">${profile && profile.name === p.name ? 'In use ✓' : 'Use'}</button><button class="btn btn-small" data-edit="${esc(p.name)}">Edit</button><button class="btn btn-small" data-export="${esc(p.name)}" title="Save this profile to a file">Export</button><button class="btn btn-small btn-danger" data-del="${esc(p.name)}" title="Delete this saved profile now. Switches that were set from it are not changed.">Delete</button></div></div>`).join('')
      : '<div class="muted">No saved profiles yet. Make one below, or press <b>Save as profile</b> on a switch card to start from its current layout.</div>';
    body += `<div class="btn-row"><select id="newPortCount">${seen.map(n => `<option value="${n}">${n}-port switch</option>`).join('')}</select><button class="btn" id="btnNewProfile">New profile</button><button class="btn" id="btnLoadProfile" title="Open a profile that was exported to a file">Import file…</button><input type="file" id="profileFile" accept=".json,application/json" class="hidden"><span class="muted" id="profileNote">${esc(profileNote)}</span></div>`;
    if (profile && result && result.some(s => !s.error)) {
      const tk = (result || []).filter(s => picked.has(s.id) && !s.error);
      const bad = tk.filter(s => planFor(s).problems.length).length, same = tk.filter(s => !planFor(s).problems.length && !planFor(s).changes.length).length;
      body += `<div class="grp-names-title" style="margin-top:12px">Apply “${esc(profile.name)}” to the ticked switches <span class="muted">— each ticked switch below shows its ports now → after, using the layout for its size. Outlined ports change.${bad ? ` <span class="grp-leave">${bad} ticked switch${bad === 1 ? '' : 'es'} cannot take it and will be skipped.</span>` : ''}${same ? ` ${same} already match${same === 1 ? 'es' : ''}.` : ''}</span></div>`;
      const why = profileProblem();
      body += `<div class="btn-row"><button class="btn btn-primary" id="btnApplyProfile" ${why ? 'disabled' : ''} title="${esc(why || 'Apply the profile to the ticked switches')}">Apply profile to ${willApply()} switch${willApply() === 1 ? '' : 'es'}</button></div>`;
    }
    profEl.innerHTML = `<div class="grp-names">${body}</div>`;
  }

  // The editor: one tab per switch size; in each, a grid of groups (rows) against ports (columns). Click a cell to cycle:
  // untagged (U), tagged (T), none. A port is untagged in one group at most; a port that is in none is in Management.
  // Group names and VLAN IDs are shared by every size; the ports are per size.
  function drawEditor() {
    if (!draft) { editEl.innerHTML = ''; return; }
    const n = draft.active, L = draft.layouts[n];
    const cellOf = (r, p) => (L.cells[r.nr] || {})[p] || '';
    const ports = Array.from({ length: n }, (_, i) => i + 1);
    const islCell = p => !!(L.isl && L.isl.cells[p]);
    const inGroup = p => islCell(p) || draft.rows.some(r => cellOf(r, p) === 'U');
    const sizes = Object.keys(draft.layouts).map(Number).sort((a, b) => a - b);
    const tabs = sizes.map(k => `<button type="button" class="grp-tab ${k === n ? 'on' : ''}" data-tab="${k}">${k}-port${sizes.length > 1 ? ` <span class="grp-tab-x" data-rmlayout="${k}" title="Remove the ${k}-port layout">×</span>` : ''}</button>`).join('');
    const addable = [...new Set([10, 12, 16, 26, ...(result || []).filter(s => s.portCount).map(s => s.portCount)])].filter(k => !draft.layouts[k]).sort((a, b) => a - b);
    const head = `<tr><th></th><th>Group</th><th>Name</th><th>VLAN ID</th>${ports.map(p => `<th class="grp-pn">${p}</th>`).join('')}<th></th></tr>`;
    const mgmt = `<tr><td><span class="grp-swatch" style="background:#325197"></span></td><td>Management</td><td class="muted">the ports left over</td><td class="muted">1</td>${ports.map(p => `<td><button type="button" class="grp-cell ${inGroup(p) ? '' : 'U'}" data-mgmt data-port="${p}" style="${inGroup(p) ? '' : 'background:#325197;border-color:#325197'}" title="${inGroup(p) ? 'Click to put port ' + p + ' back in Management' : 'Port ' + p + ' is in Management'}">${inGroup(p) ? '' : 'U'}</button></td>`).join('')}<td></td></tr>`;
    // The ISL row is like a group row; a layout whose ISL row is untouched leaves each switch's ISL ports alone.
    const isl = !L.isl ? '' : `<tr><td><span class="grp-swatch" style="background:#999999"></span></td><td>ISL</td><td colspan="2" class="muted">${L.isl.set ? `links to other switches: set to the ports shown${Object.keys(L.isl.cells).length ? '' : ' (none = back to Management)'} <button type="button" class="btn btn-small" data-islreset>leave as they are</button>` : 'links to other switches: left as they are (click a port to set them)'}</td>${ports.map(p => `<td><button type="button" class="grp-cell ${islCell(p) ? 'U' : ''}" data-isl data-port="${p}" style="${islCell(p) ? 'background:#999999;border-color:#999999' : ''}" title="Port ${p}: ${islCell(p) ? 'a link to another switch (ISL)' : 'not an ISL link'}">${islCell(p) ? 'U' : ''}</button></td>`).join('')}<td></td></tr>`;
    const rows = draft.rows.map((r, i) => `<tr><td>${swatch(colourOf(r.nr))}</td><td class="mono">${esc(r.nr)}</td>
      <td><input type="text" data-f="name" data-row="${i}" maxlength="16" value="${esc(r.name)}"></td><td><input type="text" class="grp-vid" data-f="vid" data-row="${i}" inputmode="numeric" value="${esc(r.vid)}"></td>
      ${ports.map(p => { const c = cellOf(r, p); const col = esc(colourOf(r.nr)); return `<td><button type="button" class="grp-cell ${c}" data-cell data-row="${i}" data-port="${p}" style="${c === 'U' ? `background:${col};border-color:${col}` : c === 'T' ? `border-color:${col};color:${col}` : ''}" title="Group ${r.nr}, port ${p}: ${c === 'U' ? 'untagged' : c === 'T' ? 'tagged' : 'not in this group'}">${c}</button></td>`; }).join('')}
      <td><button type="button" class="btn btn-small" data-rmrow="${i}" title="Take this group out of the profile (all sizes)">×</button></td></tr>`).join('');
    const free = Array.from({ length: 19 }, (_, i) => i + 2).filter(k => !draft.rows.some(r => r.nr === k));
    const opts = (L.isl ? '' : '<option value="isl">ISL · links to other switches</option>') + free.map(k => `<option value="${k}">${k} · ${esc(nameFor(k))}</option>`).join('');
    editEl.innerHTML = `<div class="grp-names"><div class="grp-names-title">${draft.original ? 'Edit profile' : 'New profile'} <span class="muted">— click a cell: U = untagged (the port is in this group), T = tagged, empty = not in it. Group names and VLAN IDs are the same for every size; ports are set per size. The ISL row is the links between switches: until you click one of its ports it leaves each switch's own ISL ports alone.</span></div>
      <div class="btn-row"><label>Name <input type="text" id="draftName" maxlength="60" value="${esc(draft.name)}"></label></div>
      <div class="grp-tabs">${tabs}<span class="grp-tab-add"><select id="newLayoutSize" ${addable.length ? '' : 'disabled'}>${addable.map(k => `<option value="${k}">${k}-port</option>`).join('')}</select><select id="copyLayoutFrom" title="Start the new layout as a copy of an existing one (ports above the new size are dropped)"><option value="">empty</option>${sizes.map(k => `<option value="${k}">copy of ${k}-port</option>`).join('')}</select><button type="button" class="btn btn-small" id="btnAddLayout" ${addable.length ? '' : 'disabled'}>Add size</button></span></div>
      <div class="grp-grid-wrap"><table class="grp-grid"><thead>${head}</thead><tbody>${mgmt}${isl}${rows}</tbody></table></div>
      <div class="btn-row"><select id="addGroupNr" ${opts ? '' : 'disabled'}>${opts}</select><button class="btn" id="btnAddGroup" ${opts ? '' : 'disabled'}>Add group</button><span class="spacer"></span>
        <button class="btn btn-primary" id="btnSaveDraft">Save profile</button><button class="btn" id="btnCancelDraft">Cancel</button></div><div class="muted" id="draftNote"></div></div>`;
    updateDraft();
  }
  function updateDraft() {
    const b = $g('btnSaveDraft');
    if (!b) return;
    const why = draftProblem(draft);
    b.disabled = !!why; b.title = why || 'Keep this profile in the app';
    $g('draftNote').textContent = why;
  }
  function openEditor(d, original) { draft = d; draft.original = original || null; drawEditor(); editEl.scrollIntoView({ block: 'nearest' }); }
  function download(name, obj) {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([JSON.stringify(obj, null, 2)], { type: 'application/json' }));
    a.download = name; document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }
  async function loadSaved() {
    try {
      const r = await fetch('/api/groups/profiles').then(x => x.json());
      saved = r.profiles || []; savedPath = r.path || '';
      if (profile) profile = saved.find(p => p.name === profile.name) || null;
    } catch (e) { profileNote = 'Could not read the saved profiles: ' + e.message; }
    drawProfile();
  }

  // Progress of the latest names run, from the same settings run the Address page uses.
  function drawStatus() {
    const r = state && state.settings;
    for (const el of listEl.querySelectorAll('.grp-status')) {
      const it = r && (r.action === 'groupnames' || r.action === 'groupprofile') && r.items.find(i => i.id === el.dataset.sid);
      el.textContent = it ? `${it.label}${it.detail ? ' — ' + it.detail : ''}` : '';
      el.className = 'grp-status' + (it ? ' ' + it.status : '');
    }
    if (r && (r.action === 'groupnames' || r.action === 'groupprofile') && r.id === runId && r.done && running) { running = false; runId = null; read(); }   // show the new names
    updateApply();
  }

  async function read() {
    if (reading) return;
    reading = true; btn.disabled = true; statusEl.textContent = '— reading…';
    try {
      result = (await post('/api/groups/read', {})).switches;
      for (const id of [...picked]) if (!result.some(s => s.id === id && !s.error)) picked.delete(id);
    }
    catch (e) { statusEl.textContent = '— ' + e.message; }
    reading = false; btn.disabled = false; draw();
  }

  btn.addEventListener('click', read);
  namesEl.addEventListener('input', ev => { const i = ev.target.closest('input[data-nr]'); if (i) { typed[i.dataset.nr] = i.value; updateApply(); } });
  listEl.addEventListener('change', ev => {
    const c = ev.target.closest('input[data-pick]');
    if (c) { c.checked ? picked.add(c.dataset.pick) : picked.delete(c.dataset.pick); updateApply(); draw(); }
  });
  namesEl.addEventListener('click', async ev => {
    if (!ev.target.closest('#btnApplyNames') || namesProblem()) return;
    const value = JSON.stringify(typedNames());
    const items = [...picked].map(id => ({ id, value }));
    try {
      running = true; updateApply();
      const { runId: id } = await post('/api/settings', { action: 'groupnames', items });
      runId = id;
    } catch (e) { running = false; updateApply(); $g('groupsNamesNote').textContent = e.message; }
  });
  listEl.addEventListener('click', ev => {
    const sv = ev.target.closest('[data-save]');
    const s = sv && (result || []).find(x => x.id === sv.dataset.save);
    if (!s) return;
    try { openEditor(draftFromSwitch(s), null); }
    catch (e) { profileNote = `Could not start a profile from ${s.name || s.serial}: ${e.message}`; drawProfile(); }   // its ports could not be counted
  });
  profEl.addEventListener('click', async ev => {
    const t = ev.target;
    const named = k => { const e = t.closest(`[data-${k}]`); return e && saved.find(p => p.name === e.dataset[k]); };
    let p;
    if ((p = named('use'))) { profile = p; draw(); return; }
    if ((p = named('edit'))) { openEditor(draftFromSaved(p), p.name); return; }
    if ((p = named('export'))) { download(`${p.name.replace(/[^A-Za-z0-9_-]+/g, '-')}.json`, p); return; }
    if ((p = named('del'))) {
      try { const r = await post('/api/groups/profiles/delete', { name: p.name }); saved = r.profiles; if (profile && profile.name === p.name) profile = null; } catch (e) { profileNote = e.message; }
      draw(); return;
    }
    if (t.closest('#btnNewProfile')) { openEditor(newDraft(parseInt($g('newPortCount').value, 10)), null); return; }
    if (t.closest('#btnLoadProfile')) { $g('profileFile').click(); return; }
    if (!t.closest('#btnApplyProfile') || profileProblem()) return;
    // Each switch gets the layout for its own size.
    const items = (result || []).filter(s => picked.has(s.id) && !s.error && !planFor(s).problems.length && planFor(s).changes.length)
      .map(s => ({ id: s.id, value: JSON.stringify(resolved(profile, s.portCount)) }));
    try {
      running = true; updateApply();
      const { runId: id } = await post('/api/settings', { action: 'groupprofile', items });
      runId = id;
    } catch (e) { running = false; profileNote = e.message; drawProfile(); }
  });
  profEl.addEventListener('change', async ev => {
    if (ev.target.id !== 'profileFile' || !ev.target.files[0]) return;
    try {
      const p = JSON.parse(await ev.target.files[0].text());
      let sp;
      if (p && Array.isArray(p.layouts)) sp = p;
      else if (p && Array.isArray(p.groups) && p.portCount) {   // a file from the single-size version
        sp = { name: p.name || 'Imported', groups: p.groups.map(g => ({ nr: g.nr, name: g.name, vid: g.vid })), layouts: [{ portCount: p.portCount, ...(p.isl ? { isl: p.isl } : {}), groups: p.groups.map(g => ({ nr: g.nr, ports: g.ports || [], tagged: g.tagged || [] })) }] };
      } else throw new Error('not a group profile');
      profileNote = '';
      openEditor(draftFromSaved(sp), null);   // open it in the editor so it can be checked and kept
    } catch (e) { profileNote = 'That file is not a group profile: ' + e.message; }
    ev.target.value = '';
    drawProfile();
  });
  editEl.addEventListener('input', ev => {
    const t = ev.target;
    if (t.id === 'draftName') draft.name = t.value;
    else if (t.dataset.f) draft.rows[+t.dataset.row][t.dataset.f] = t.value;
    updateDraft();
  });
  editEl.addEventListener('click', async ev => {
    const t = ev.target;
    const L = draft && draft.layouts[draft.active];
    const rmL = t.closest('[data-rmlayout]');
    if (rmL) { delete draft.layouts[+rmL.dataset.rmlayout]; draft.active = Math.min(...Object.keys(draft.layouts).map(Number)); drawEditor(); return; }
    const tab = t.closest('[data-tab]');
    if (tab) { draft.active = +tab.dataset.tab; drawEditor(); return; }
    if (t.closest('#btnAddLayout')) {
      const size = +$g('newLayoutSize').value, from = $g('copyLayoutFrom').value;
      const nl = emptyLayout();
      if (from) {   // a copy of another size: the ports that exist on the new one
        const src = draft.layouts[+from];
        for (const [nr, cells] of Object.entries(src.cells)) nl.cells[nr] = Object.fromEntries(Object.entries(cells).filter(([p]) => +p <= size));
        nl.isl = { set: src.isl.set, cells: Object.fromEntries(Object.entries(src.isl.cells).filter(([p]) => +p <= size)) };
      }
      draft.layouts[size] = nl; draft.active = size; drawEditor(); return;
    }
    const mg = t.closest('[data-mgmt]');
    if (mg) {   // put the port back in Management: untagged in no group, and not an ISL link
      const p = +mg.dataset.port;
      for (const r of draft.rows) if ((L.cells[r.nr] || {})[p] === 'U') delete L.cells[r.nr][p];
      if (L.isl) delete L.isl.cells[p];
      drawEditor(); return;
    }
    const il = t.closest('[data-isl]');
    if (il && L.isl) {
      const p = +il.dataset.port;
      L.isl.set = true;
      if (L.isl.cells[p]) delete L.isl.cells[p];
      else { L.isl.cells[p] = 'U'; for (const r of draft.rows) if (L.cells[r.nr]) delete L.cells[r.nr][p]; }   // a link port is in no group
      drawEditor(); return;
    }
    const cell = t.closest('[data-cell]');
    if (cell) {
      const r = draft.rows[+cell.dataset.row], p = +cell.dataset.port;
      const cells = L.cells[r.nr] = L.cells[r.nr] || {};
      const next = { undefined: 'U', U: 'T', T: '' }[cells[p]];
      if (next) cells[p] = next; else delete cells[p];
      if (next === 'U') for (const o of draft.rows) if (o !== r && (L.cells[o.nr] || {})[p] === 'U') delete L.cells[o.nr][p];   // untagged in one group only
      if (next && L.isl) delete L.isl.cells[p];   // a port in a group is not an ISL link
      drawEditor(); return;
    }
    if (t.closest('[data-islreset]')) { L.isl.set = false; L.isl.cells = {}; drawEditor(); return; }
    const rm = t.closest('[data-rmrow]');
    if (rm) { const nr = draft.rows[+rm.dataset.rmrow].nr; draft.rows.splice(+rm.dataset.rmrow, 1); for (const l of Object.values(draft.layouts)) delete l.cells[nr]; drawEditor(); return; }
    if (t.closest('[data-rmisl]')) { L.isl = null; drawEditor(); return; }
    if (t.closest('#btnAddGroup')) {
      if ($g('addGroupNr').value === 'isl') { L.isl = { set: false, cells: {} }; drawEditor(); return; }
      const nr = +$g('addGroupNr').value;
      draft.rows.push({ nr, name: nameFor(nr), vid: String(nr * 100) });
      draft.rows.sort((a, b) => a.nr - b.nr); drawEditor(); return;
    }
    if (t.closest('#btnCancelDraft')) { draft = null; drawEditor(); return; }
    if (t.closest('#btnSaveDraft')) {
      if (draftProblem(draft)) return;
      const p = savedFromDraft(draft), original = draft.original;
      try {
        if (original && original.toLowerCase() !== p.name.toLowerCase()) await post('/api/groups/profiles/delete', { name: original });   // a rename
        const r = await post('/api/groups/profiles/save', p);
        saved = r.profiles; profile = saved.find(x => x.name.toLowerCase() === p.name.toLowerCase()) || null; profileNote = '';
        draft = null; drawEditor(); draw();
      } catch (e) { $g('draftNote').textContent = e.message; }
    }
  });
  const baseRender = window.render;
  window.render = function () { baseRender(); drawStatus(); };
  document.getElementById('viewTabs').addEventListener('click', ev => {
    const t = ev.target.closest('.view-tab');
    if (t && t.dataset.view === 'groups' && !result) read();
  });
  draw();
  loadSaved();
})();
