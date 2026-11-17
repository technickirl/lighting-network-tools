'use strict';
// Device settings: name and identify (GigaCores and LumiNodes), and GigaCore LED brightness. Loaded after app.js and hooks into its
// render(). The ticked options are sent by the Apply settings button (app.js) once the addresses are
// done: runStage() below rescans, then sends each ticked setting to the ticked switches in turn. The
// server does the writing and reads each value back (settings.go). The device list is drawn by app.js,
// which asks window.Settings for each switch's new name and the status of the latest settings run.

(function () {
  const $s = id => document.getElementById(id);
  const el = {
    name: $s('optName'), led: $s('optLed'), auth: $s('optAuth'),
    ledLevel: $s('ledSelect'), nameExample: $s('nameExample'), note: $s('settingsNote'), nameStyle: $s('nameStyle'),
  };
  const ACTION_LABEL = { name: 'Name', led: 'LED', identify: 'Identify', webauth: 'Web password' };
  let handledRun = null, stageActive = false;
  let earlierRuns = [];                                         // this Apply's settings before the current one: the server only keeps the latest

  const limit = d => (d.apiKind === 'gen2' ? 64 : d.apiKind === 'luminode' ? 17 : 16);
  const switches = () => selectedDevices().filter(isSwitch);   // LED is GigaCore-only
  const named = () => selectedDevices();                          // names can be set on GigaCores and LumiNodes
  // The web password can be turned off on LumiNodes, LumiCores and GigaCore Gen2; Gen1 has no such setting.
  const canAuth = d => d.apiKind === 'luminode' || d.apiKind === 'gen2';
  const authTargets = () => selectedDevices().filter(canAuth);
  const running = () => !!(state && state.settings && !state.settings.done);

  el.nameStyle.value = load('iptop.nameStyle', 'prefix') === 'model' ? 'model' : 'prefix';
  function setStyle(v) { el.nameStyle.value = v; save('iptop.nameStyle', v); }

  // "LumiNode 12 - 3", "GigaCore 16tf - 3": the model split from its number, then the last octet.
  function prettyModel(m) { const x = /^([A-Za-z]+)(\d.*)$/.exec(m || ''); return x ? `${x[1]} ${x[2]}` : (m || ''); }

  // A name is either the prefix for the device's type plus the last two octets of the address it is getting
  // (or has): "Switch 2.1", "12p Switch 2.4"; or its model and last octet: "LumiNode 12 - 3".
  function nameFor(d) {
    const o = ((document.getElementById('optIp').checked && newIpFor(d)) || d.ip || '').split('.');   // the planned address only counts when addresses are being sent
    if (el.nameStyle.value === 'model') return `${prettyModel(d.model)} - ${o[3]}`;
    const p = (typeOf(d) ? prefixOf(typeOf(d)) : '').trim();
    const name = (p ? p + ' ' : '') + `${o[2]}.${o[3]}`;
    return d.apiKind === 'gen2' ? name.replace(/\./g, '-') : name;   // Gen2 names cannot hold a dot: "Switch 11-1"
  }
  const nameTooLong = d => nameFor(d).length > limit(d);
  const nameValues = () => Object.fromEntries(named().map(d => [d.id, nameFor(d)]));

  // What to send, in this order.
  function chosen() {
    const a = [];
    if (el.name.checked) a.push({ action: 'name' });
    if (el.led.checked) a.push({ action: 'led', value: el.ledLevel.value });
    if (el.auth.checked) a.push({ action: 'webauth', value: 'off' });
    return a;
  }

  // Why the ticked settings cannot be applied, or ''.
  function problem() {
    if (el.led.checked && !switches().length) return 'LED: tick a GigaCore';
    if (el.name.checked && !named().length) return 'Tick a device to name';
    const badChars = el.name.checked && named().find(d => d.apiKind === 'gen2' && !/^[A-Za-z0-9\-_ ]*$/.test(nameFor(d)));
    if (badChars) return `"${nameFor(badChars)}": Gen2 names allow letters, numbers, space, - and _`;
    if (el.auth.checked && !authTargets().length) return 'Web password: tick a LumiNode, LumiCore or Gen2 switch';
    const bad = el.name.checked && named().find(nameTooLong);
    return bad ? `"${nameFor(bad)}" is over ${limit(bad)} characters: ${el.nameStyle.value === 'model' ? 'untick it or use prefix names' : 'shorten the prefix'}` : '';
  }

  // A device's settings results from this Apply, oldest first.
  function items(id) {
    const runs = [...earlierRuns, ...(state && state.settings ? [state.settings] : [])].filter((r, i, a) => a.findIndex(x => x.id === r.id) === i);
    return runs.flatMap(r => r.items.filter(i => i.id === id).map(it => ({ status: it.status, label: it.label, detail: it.detail, prefix: ACTION_LABEL[r.action] })));
  }

  const sleep = ms => new Promise(r => setTimeout(r, ms));
  async function waitFor(cond, ms, what) {
    for (const end = Date.now() + ms; Date.now() < end; await sleep(400)) if (cond()) return;
    throw new Error('timed out waiting for ' + what);
  }
  async function rescan() {
    const t0 = Date.now();
    await waitFor(() => !state.scanning, 30000, 'the current scan');
    await post('/api/scan');
    await waitFor(() => state.lastScanAt >= t0 && !state.scanning, 60000, 'the rescan');
  }

  // Send each ticked setting to the ticked switches, one after the other. If addresses were just
  // changed, rescan first so the settings go to the new addresses.
  async function runStage(run) {
    stageActive = true;
    try {
      if (run.addressed) { log('Addresses done — rescanning…'); await rescan(); }
      const results = [];
      for (const a of run.actions) {
        const targets = a.action === 'name' || a.action === 'webauth' ? named() : switches();   // Gen1 joins a web password run, to show it was skipped
        const items = targets.map(d => ({ id: d.id, value: a.action === 'name' ? (run.names[d.id] || nameFor(d)) : a.value }));
        if (!items.length) { log(`Nothing ticked for ${ACTION_LABEL[a.action]} — skipped.`); continue; }
        log(`Sending ${ACTION_LABEL[a.action]} to ${items.length} device(s)…`);
        const { runId } = await post('/api/settings', { action: a.action, items });
        await waitFor(() => state.settings && state.settings.id === runId && state.settings.done, 6 * 60 * 1000, `the ${ACTION_LABEL[a.action]} change`);
        const r = state.settings, c = k => r.items.filter(i => i.status === k).length;
        earlierRuns.push(r);
        results.push(`${ACTION_LABEL[a.action]}: ${c('done')} done${c('dry_run') ? `, ${c('dry_run')} dry-run` : ''}${c('skipped') ? `, ${c('skipped')} skipped` : ''}${c('failed') ? `, ${c('failed')} failed` : ''}`);
        if (a.action === 'webauth') el.auth.checked = false;   // it removes protection, so it never carries over to the next batch
      }
      if (results.length) log((state.dryRun ? 'Dry run — ' : '') + results.join(' · '));
      post('/api/scan').catch(() => {});   // show the names and addresses as they are now
    } finally { stageActive = false; }
  }

  async function runOne(action, items) {
    log(`Sending ${ACTION_LABEL[action]} to ${items.length} device(s)…`);
    try { await post('/api/settings', { action, items }); } catch (e) { log('Could not start: ' + e.message); }
  }

  function renderSettings() {
    if (!state) return;
    const busy = jobActive() || !!comboRun || !!applyRun || running() || state.scanning;
    for (const c of [el.name, el.nameStyle, el.led, el.auth, el.ledLevel]) c.disabled = !!(jobActive() || comboRun || applyRun);
    const first = named()[0] || { ip: `${netPrefix}.2.1`, family: 'gigacore', model: 'GigaCore10' };
    el.nameExample.textContent = nameFor(first);
    el.note.textContent = switches().some(d => d.apiKind === 'gen2') ? 'Gen2 calls are from the spec, not confirmed on a real switch.' : '';

    // A standalone run (Identify) that changed something: show the result and refresh the list.
    const r = state.settings;
    if (r && r.done && r.id !== handledRun) {
      handledRun = r.id;
      if (!stageActive) {
        if (r.action !== 'identify' && r.items.some(i => i.status === 'done')) post('/api/scan').catch(() => {});
        const c = k => r.items.filter(i => i.status === k).length;
        log(`${r.dryRun ? 'Dry run: ' : ''}${ACTION_LABEL[r.action]}: ${c('done')} done, ${c('dry_run')} dry-run, ${c('failed')} failed.`);
      }
    }
    return busy;
  }

  window.Settings = {
    nameFor, nameTooLong, nameValues, chosen, problem, items, runStage,
    clearResults: () => { earlierRuns = []; },
    naming: () => el.name.checked, style: () => el.nameStyle.value, setStyle,
    running: () => !!(state && (running() || applyRun)),
    identify: id => runOne('identify', [{ id, value: '' }]),
  };

  const baseRender = window.render;
  window.render = function () { baseRender(); renderSettings(); };

  // A tick changes what Apply would send, so redraw now rather than at the next server update.
  for (const c of [$s('optIp'), el.name, el.led, el.auth, el.ledLevel]) c.addEventListener('change', () => window.render());
  el.nameStyle.addEventListener('change', () => { save('iptop.nameStyle', el.nameStyle.value); window.render(); });
})();
