'use strict';
// Access from other devices (an iPad on the show network): the switch, the
// addresses to open and the PIN. Only the computer running the tool can see the
// PIN or switch it; on another device /api/lan answers 403 and this says so.
// A tool without the setting answers 404 and the box stays hidden.

const lanEls = { box: $('lanBox'), btn: $('btnLan'), body: $('lanBody') };
let lanState = null, lanRemote = false, lanBusy = false;
let lanEditing = false; // the saved-PIN field is open: the poll leaves the box alone

async function lanFetch(method, body) {
  const r = await fetch('/api/lan', { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  if (r.status === 404) return { missing: true };
  if (r.status === 403) return { remote: true };
  const d = await r.json();
  if (!r.ok || d.ok === false) throw new Error(d.error || `HTTP ${r.status}`);
  return d;
}

async function lanPoll() {
  try {
    const d = await lanFetch('GET');
    if (d.missing) { lanEls.box.classList.add('hidden'); return; }
    lanRemote = !!d.remote;
    lanState = d.lan || null;
    if (!lanEditing) renderLan();
  } catch (_) {}
  setTimeout(lanPoll, 3000);
}

function renderLan() {
  lanEls.box.classList.remove('hidden');
  const b = lanEls.btn;
  let pill = document.getElementById('lanPill');
  if (lanRemote) {
    b.textContent = 'On';
    b.disabled = true;
    b.title = 'Turned on and off only on the computer running the tool';
    lanEls.body.innerHTML = `<div class="muted">You are connected from another device. Access is managed on the computer running the tool.</div>`;
    if (pill) pill.remove();
    return;
  }
  const s = lanState || { enabled: false, urls: [] };
  b.disabled = lanBusy;
  b.textContent = s.enabled ? 'Turn off' : 'Turn on';
  b.className = 'btn btn-small' + (s.enabled ? ' btn-danger' : '');
  b.title = lanBusy ? 'Working…' : s.enabled ? 'Stop access from other devices and sign them all out' : 'Let an iPad or another computer on the network use this tool, with a PIN';
  if (!s.enabled) {
    lanEls.body.innerHTML = `<div class="muted">Off. Turn on to use this tool from an iPad on the same network. It asks for a PIN, and turns off when the tool quits.</div>${lanSavedHtml(s)}`;
    lanWireSaved();
    if (pill) pill.remove();
    return;
  }
  if (!pill) {
    pill = document.createElement('div');
    pill.id = 'lanPill';
    pill.className = 'pill lan';
    pill.title = 'Other devices can use this tool with the PIN';
    document.querySelector('header').insertBefore(pill, $('connPill'));
  }
  pill.textContent = 'OTHER DEVICES ON';
  const pin = s.pin ? `${s.pin.slice(0, 3)} ${s.pin.slice(3)}` : '';
  lanEls.body.innerHTML = `
    <div class="muted">Open on the iPad:</div>
    ${s.urls.length ? s.urls.map(u => `<div class="lan-url mono">${esc(u)}</div>`).join('') : '<div class="warn-text">This computer has no network address yet.</div>'}
    <div class="lan-pin-row"><span class="muted">PIN</span> <span class="lan-pin mono">${esc(pin)}</span>
      <button class="btn btn-small" id="btnLanPin" ${lanBusy ? 'disabled title="Working…"' : `title="Make a new random PIN and sign every device out${s.savedPin ? '. The saved PIN comes back next time it is turned on' : ''}"`}>New PIN</button></div>
    <div class="muted">${s.devices ? `${s.devices} device${s.devices === 1 ? '' : 's'} signed in` : 'No device signed in yet'}</div>
    ${(s.errors || []).map(e => `<div class="warn-text">${esc(e)}</div>`).join('')}${lanSavedHtml(s)}`;
  const np = document.getElementById('btnLanPin');
  if (np) np.addEventListener('click', () => lanSet(true));
  lanWireSaved();
}

// The saved PIN: used every time access is turned on, so the iPad's PIN stays
// the same from show to show. Set on this computer only (it has a keyboard).
function lanSavedHtml(s) {
  if (!s.canSave) return '';
  const busy = lanBusy ? 'disabled title="Working…"' : '';
  const fmt = p => `${p.slice(0, 3)} ${p.slice(3)}`;
  if (lanEditing) {
    return `<div class="lan-saved"><span class="muted">Saved PIN</span>
      <input id="lanPinInput" class="lan-pin-input mono" inputmode="numeric" maxlength="6" placeholder="6 digits" autocomplete="off" value="${esc(s.savedPin || '')}">
      <button class="btn btn-small" id="btnLanPinSave" ${busy || 'disabled title="Six digits"'}>Save</button>
      <button class="btn btn-small" id="btnLanPinCancel">Cancel</button></div>`;
  }
  if (s.savedPin) {
    return `<div class="lan-saved"><span class="muted">Saved PIN <b class="mono">${esc(fmt(s.savedPin))}</b>, used every time it turns on</span>
      <button class="btn btn-small" id="btnLanPinEdit" ${busy || 'title="Choose a different saved PIN"'}>Change</button>
      <button class="btn btn-small" id="btnLanPinClear" ${busy || 'title="Forget the saved PIN: a new random PIN each time it turns on"'}>Use random</button></div>`;
  }
  const keep = s.enabled && s.pin
    ? `<button class="btn btn-small" id="btnLanPinKeep" ${busy || 'title="Save this PIN and use it every time it turns on"'}>Keep this PIN</button>` : '';
  return `<div class="lan-saved"><span class="muted">A new random PIN each time it turns on</span>
    ${keep}<button class="btn btn-small" id="btnLanPinEdit" ${busy || 'title="Choose your own PIN, used every time it turns on"'}>Set a PIN</button></div>`;
}

function lanWireSaved() {
  const on = (id, fn) => { const b = document.getElementById(id); if (b) b.addEventListener('click', fn); };
  on('btnLanPinEdit', () => { lanEditing = true; renderLan(); const i = document.getElementById('lanPinInput'); if (i) { i.focus(); i.select(); } });
  on('btnLanPinCancel', () => { lanEditing = false; renderLan(); });
  on('btnLanPinClear', () => lanSavePin(''));
  on('btnLanPinKeep', () => lanSavePin(lanState.pin));
  on('btnLanPinSave', () => lanSavePin(document.getElementById('lanPinInput').value));
  const i = document.getElementById('lanPinInput');
  if (!i) return;
  const check = () => {
    i.value = i.value.replace(/\D/g, '').slice(0, 6);
    const b = document.getElementById('btnLanPinSave');
    b.disabled = lanBusy || i.value.length !== 6;
    b.title = i.value.length === 6 ? 'Save, and use it every time it turns on' + (lanState && lanState.enabled ? ' (also now: every device signs in again)' : '') : 'Six digits';
  };
  i.addEventListener('input', check);
  i.addEventListener('keydown', e => {
    if (e.key === 'Enter' && i.value.length === 6) lanSavePin(i.value);
    if (e.key === 'Escape') { lanEditing = false; renderLan(); }
  });
  check();
}

async function lanSavePin(pin) {
  lanBusy = true;
  renderLan();
  try {
    const d = await lanFetch('POST', { pin });
    if (d.lan) lanState = d.lan;
    lanEditing = false;
  } catch (e) {
    lanBusy = false;
    renderLan();
    lanEls.body.insertAdjacentHTML('beforeend', `<div class="warn-text">${esc(e.message)}</div>`);
    return;
  }
  lanBusy = false;
  renderLan();
}

async function lanSet(on) {
  lanBusy = true;
  renderLan();
  try {
    const d = await lanFetch('POST', { enabled: on });
    if (d.lan) lanState = d.lan;
  } catch (e) {
    lanEls.body.insertAdjacentHTML('beforeend', `<div class="warn-text">${esc(e.message)}</div>`);
  }
  lanBusy = false;
  renderLan();
}

lanEls.btn.addEventListener('click', () => lanSet(!(lanState && lanState.enabled)));
lanPoll();
