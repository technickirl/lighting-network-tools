'use strict';
// The Node view: one node's configure page inside this page, with a chip per
// node to switch between them. The configure page itself (markup, styles and
// scripts) belongs to the tool that mounts it at state.configureUrl; it is
// loaded once, the first time a node is opened, into #nodeHost.

const nodeEls = { chips: $('nodeChips'), hint: $('nodeHint'), host: $('nodeHost') };
let nodeOpen = null;     // { ip, id } of the node the view shows
let nodePage = null;     // a promise: the configure page being loaded, or loaded
let nodePageError = '';
let nodeChipsHtml = '';  // what the chip row shows, so it is only redrawn on a change

function nodeLoadScript(src) {
  return new Promise((res, rej) => {
    const s = document.createElement('script');
    s.src = src;
    s.onload = res;
    s.onerror = () => rej(new Error('could not load ' + src));
    document.body.appendChild(s);
  });
}
function nodeLoadStyle(href) {
  return new Promise((res, rej) => {
    const l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = href;
    l.onload = res;
    l.onerror = () => rej(new Error('could not load ' + href));
    document.head.appendChild(l);
  });
}
function nodeLoadPage() {
  if (nodePage) return nodePage;
  const base = state.configureUrl;
  nodePage = (async () => {
    const r = await fetch(base + 'node.html', { cache: 'no-store' });
    if (!r.ok) throw new Error(`could not load the node page (HTTP ${r.status})`);
    const html = await r.text();
    await nodeLoadStyle(base + 'nodeconfig.css');
    nodeEls.host.innerHTML = html;
    await nodeLoadScript(base + 'nodeconfig.js');
    await nodeLoadScript(base + 'rdm-fixtures.js');
  })();
  return nodePage;
}

// openNode shows a node in the Node view. show: false keeps the view the page
// is on (a reload that was on another view).
async function openNode(ip, id, show = true) {
  nodeOpen = { ip, id: id || '' };
  if (show) setView('node');
  render();
  // Keeping the node in the address is a convenience; a browser that refuses it must not stop the node opening.
  try { nodeSaveAddress(); } catch (e) { reportPageError('Could not keep the node in the address: ' + e.message); }
  try { await nodeLoadPage(); }
  catch (e) {
    nodePage = null; // let the next tap try again: one failed load (a Wi-Fi blip) must not stop every later one
    nodePageError = `The node page did not load: ${e.message}. Tap the node again.`;
    reportPageError(nodePageError);
    render();
    return;
  }
  nodePageError = '';
  if (typeof ncOpen !== 'function') { nodePageError = 'The node page loaded but its script did not run. Reload this page.'; reportPageError(nodePageError); render(); return; }
  if (nodeOpen && nodeOpen.ip === ip) ncOpen(ip, nodeOpen.id);
  render();
}

// The open node is kept in the address (#node=<ip>&id=<device>), so a reload or
// a bookmark comes back to it.
function nodeSaveAddress() {
  const q = new URLSearchParams({ node: nodeOpen.ip, id: nodeOpen.id });
  window.history.replaceState(null, '', '#' + q);
}
let nodeAddressRead = false;
function nodeFromAddress() {
  if (nodeAddressRead || !state.configureUrl) return;
  nodeAddressRead = true;
  const q = new URLSearchParams(location.hash.slice(1));
  const ip = q.get('node') || '';
  if (!/^[0-9A-Za-z.:%[\]-]+$/.test(ip)) return;
  let seen = false; // a view saved in this tab means a reload: stay on it
  try { seen = !!sessionStorage.getItem('commission.view'); } catch {}
  openNode(ip, q.get('id') || '', !seen);
}
// A job moved the open node to a new address: the configure page followed it.
window.addEventListener('nc-moved', ev => {
  if (!nodeOpen || nodeOpen.id !== ev.detail.id) return;
  nodeOpen.ip = ev.detail.ip;
  try { nodeSaveAddress(); } catch {}
  render();
});

function nodeChip(d) {
  const it = jobItem(d.id);
  const ip = (it && it.status === 'done' && it.newIp) || d.ip;
  const on = !!nodeOpen && nodeOpen.id === d.id;
  const busy = inRunningJob(d.id);
  const why = configureWhyNot(d.apiKind, d.id, ip);
  const label = d.name || d.model || d.serial;
  const title = why || `${d.model} at ${ip}`;
  // The open node's chip stays lit while a job has it: its page shows the lock.
  return `<button class="node-chip ${on ? 'active' : ''}" data-configure-id="${esc(d.id)}" data-configure-ip="${esc(ip)}" title="${esc(title)}" ${why && !on ? 'disabled' : ''}>` +
    `<span class="node-chip-dot ${busy ? 'busy' : why ? 'down' : ''}"></span><span>${esc(label)}</span><span class="node-chip-ip">${esc(ip ? '.' + ip.split('.').pop() : '?')}</span></button>`;
}
function renderNode() {
  if (!state.configureUrl) return;
  nodeFromAddress();
  const list = rows().filter(d => d.apiKind === 'luminode' && !d._ghost);
  let html = list.map(nodeChip).join('');
  // A node opened by address that the scan has not listed still gets its chip.
  if (nodeOpen && !list.some(d => d.id === nodeOpen.id)) {
    html += `<button class="node-chip active" data-configure-id="${esc(nodeOpen.id)}" data-configure-ip="${esc(nodeOpen.ip)}" title="Not in the device list"><span class="node-chip-dot off"></span><span>${esc(nodeOpen.ip)}</span></button>`;
  }
  if (html !== nodeChipsHtml) { nodeChipsHtml = html; nodeEls.chips.innerHTML = html; }
  nodeEls.hint.textContent = nodePageError ? '› ' + nodePageError
    : nodeOpen ? ''
    : list.length ? '› Pick a node.'
    : state.scanning ? '› Scanning the network…'
    : '› No LumiNode found yet. Scan on Devices.';
  nodeEls.hint.classList.toggle('hidden', !nodeEls.hint.textContent);
  nodeEls.host.classList.toggle('hidden', !nodeOpen || !!nodePageError);
}
nodeEls.chips.addEventListener('click', onConfigureClick);
