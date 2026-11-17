'use strict';
// Address planning. Every device type is either
//   paired    switches and their nodes form combos that share a last octet (switch 192.168.2.n, node
//             192.168.3.n), numbered along the cabling, and
//   sequence  the devices of that type just count up 1, 2, 3… on the type's own third octet, ignoring pairing.
// Pure function so it can be tested with node (see ../combo_test.js); the browser loads it before app.js.
//
// The first two octets are `prefix` ("192.168" unless given, e.g. "172.16", "10.0" or "2.0"); a device on another
// prefix does not count as holding a number, so changing the prefix renumbers it.
//
// A device that already sits on <prefix>.<its type's third octet>.<n> keeps n (unless `renumber` is set), so adding a second
// batch continues the numbering instead of repeating it: three combos on 2.1 to 2.3 and a new one gives 2.4 and 3.4. New
// devices take the next numbers after the highest in use, in chain order.
//
// Numbers are counted per third octet, so types can share one: two switch types on 192.168.11 count up together
// instead of colliding. `reserved` lists addresses given out earlier to devices that may have left the scan, so a
// batch swapped in after the last one carries on from it.
//
// `ticked` (a Set of serials) says who is being numbered; leave it out for everybody. An unticked device takes no new
// number, so unticking one closes up the numbering. It only holds a number it is already on, so nothing lands on top of it.
//
// Switches are walked in chain order: start at one end of a chain and follow it, so neighbouring devices get
// neighbouring numbers. `order` (serials) overrides that for any group; devices it does not mention follow
// in the default order.

(function (root) {
  function cmp(a, b) { return a < b ? -1 : a > b ? 1 : 0; }

  // devices: [{serial, role: 'switch'|'node', type, ip}]   cfg: {type: {mode, third, start}} (insertion order = display order)
  // links: [{from, to}] serials, from LLDP   pairedStart: first combo number
  const esc = s => s.replace(/\./g, '\\.');
  function buildPlan({ devices, links, cfg, prefix = '192.168', pairedStart = 1, order = [], renumber = false, reserved = [], ticked = null }) {
    const own = new RegExp('^' + esc(prefix) + '\\.(\\d+)\\.(\\d+)$');   // an address on this prefix: third and fourth octet
    const byS = new Map(devices.map(d => [d.serial, d]));
    const switches = devices.filter(d => d.role === 'switch').map(d => d.serial);
    const nodes = devices.filter(d => d.role === 'node').map(d => d.serial);
    const swSet = new Set(switches), nodeSet = new Set(nodes);
    const modeOf = s => (cfg[byS.get(s).type] || {}).mode;
    const thirdOf = s => (cfg[byS.get(s).type] || {}).third;

    // Switch ↔ switch adjacency across all switches (a link reported from either end counts).
    const adj = new Map(switches.map(s => [s, new Set()]));
    for (const l of links) {
      if (swSet.has(l.from) && swSet.has(l.to) && l.from !== l.to) { adj.get(l.from).add(l.to); adj.get(l.to).add(l.from); }
    }
    // Which switch each node hangs off: the node's own report wins, the switch's report fills gaps.
    const nodeSwitch = new Map();
    for (const l of links) if (nodeSet.has(l.from) && swSet.has(l.to) && !nodeSwitch.has(l.from)) nodeSwitch.set(l.from, l.to);
    for (const l of links) if (swSet.has(l.from) && nodeSet.has(l.to) && !nodeSwitch.has(l.to)) nodeSwitch.set(l.to, l.from);

    // Default order: per connected group of switches, walk from a chain end (fewest neighbours, then lowest serial).
    const seen = new Set(), walk = [];
    const visit = s => { seen.add(s); walk.push(s); for (const n of [...adj.get(s)].sort(cmp)) if (!seen.has(n)) visit(n); };
    for (const s of [...switches].sort((a, b) => adj.get(a).size - adj.get(b).size || cmp(a, b))) if (!seen.has(s)) visit(s);
    const rank = new Map(walk.map((s, i) => [s, [i, 0]]));
    for (const n of nodes) { const sw = nodeSwitch.get(n); rank.set(n, [rank.has(sw) ? rank.get(sw)[0] : 1e6, 1]); }
    const byDefault = (a, b) => rank.get(a)[0] - rank.get(b)[0] || rank.get(a)[1] - rank.get(b)[1] || cmp(a, b);
    const prio = new Map(order.map((s, i) => [s, i]));
    const arrange = list => [
      ...list.filter(s => prio.has(s)).sort((a, b) => prio.get(a) - prio.get(b)),
      ...list.filter(s => !prio.has(s)).sort(byDefault),
    ];

    const ips = {}, combos = [], extras = [], sequences = [], warnings = [], clashes = [];
    const ip = (s, n) => `${prefix}.${thirdOf(s)}.${n}`;

    // The number a device already holds, if its address is in its type's range.
    const heldN = s => {
      if (renumber) return null;
      const m = own.exec(byS.get(s).ip || '');
      return m && +m[1] === thirdOf(s) && +m[2] >= 1 && +m[2] <= 254 ? +m[2] : null;
    };
    // Numbers in use, per third octet, whichever type holds them.
    const pool = new Map();
    const used = t => { if (!pool.has(t)) pool.set(t, new Set()); return pool.get(t); };
    // The highest number given out earlier on each third octet: new devices go after it, but nobody is moved off it.
    const given = new Map();
    if (!renumber) for (const a of reserved) {
      const m = own.exec(a || '');
      if (m) given.set(+m[1], Math.max(given.get(+m[1]) || 0, +m[2]));
    }
    const highest = t => Math.max(given.get(t) || 0, ...used(t));
    const wanted = s => !ticked || ticked.has(s);
    const counted = s => wanted(s) || heldN(s) !== null;   // an unticked device only counts for a number it holds

    // Paired: combos along the chain, switches first in each.
    const pairedSw = arrange(switches.filter(s => modeOf(s) === 'paired' && counted(s)));
    const pairedSet = new Set(pairedSw);
    const nodesOn = new Map(pairedSw.map(s => [s, []]));
    const loose = [];
    for (const nd of arrange(nodes.filter(s => modeOf(s) === 'paired' && counted(s)))) {
      const s = nodeSwitch.get(nd);
      if (s && pairedSet.has(s)) nodesOn.get(s).push(nd); else loose.push(nd);
    }
    // A combo's number has to be free on both its third octets, and is unique across all combos.
    // Everybody keeps a free held number first, in every group; only then do the rest take new ones.
    const takenP = new Set(), number = new Map();
    const comboThirds = s => [thirdOf(s), ...(nodesOn.get(s)[0] ? [thirdOf(nodesOn.get(s)[0])] : [])];
    const take = (it, n, kept, thirds) => { number.set(it, { n, kept }); thirds.forEach(t => used(t).add(n)); };
    const keepPaired = (it, h, thirds) => {
      if (h === null || takenP.has(h) || thirds.some(t => used(t).has(h))) return;
      takenP.add(h); take(it, h, true, thirds);
    };
    const fillPaired = (it, thirds) => {
      if (number.has(it)) return;
      const n = Math.max(pairedStart - 1, ...takenP, ...thirds.map(highest)) + 1;
      takenP.add(n); take(it, n, false, thirds);
    };
    const seqTypes = Object.entries(cfg).filter(([, c]) => c.mode === 'sequence')
      .map(([type, c]) => ({ type, c, members: arrange(devices.filter(d => d.type === type && counted(d.serial)).map(d => d.serial)) }));

    for (const s of pairedSw) keepPaired(s, heldN(s) ?? (nodesOn.get(s)[0] ? heldN(nodesOn.get(s)[0]) : null), comboThirds(s));
    // An unticked switch that could not keep its number is out of the combos, so its nodes go with the extras.
    const comboSw = pairedSw.filter(s => number.has(s) || wanted(s));
    const comboSet = new Set(comboSw);
    // A second node on one switch, or a paired node whose switch is not in a combo, is numbered after the combos.
    const extraList = [...comboSw.flatMap(s => nodesOn.get(s).slice(1)), ...pairedSw.filter(s => !comboSet.has(s)).flatMap(s => nodesOn.get(s)), ...loose];
    // An extra only needs its number free on its own octet: a node on 3.1 that cannot be matched to the switch on 2.1 stays put.
    for (const nd of extraList) {
      const h = heldN(nd);
      if (h !== null && !used(thirdOf(nd)).has(h)) { takenP.add(h); take(nd, h, true, [thirdOf(nd)]); }
    }
    for (const { c, members } of seqTypes) for (const m of members) {
      const h = heldN(m);
      if (h !== null && !used(c.third).has(h)) take(m, h, true, [c.third]);
    }
    for (const s of comboSw) fillPaired(s, comboThirds(s));
    for (const nd of extraList) if (wanted(nd)) fillPaired(nd, [thirdOf(nd)]);
    for (const { c, members } of seqTypes) for (const m of members) {
      if (!number.has(m) && wanted(m)) take(m, Math.max(c.start - 1, highest(c.third)) + 1, false, [c.third]);
    }

    for (const s of comboSw) {
      const [first] = nodesOn.get(s), { n, kept } = number.get(s);
      combos.push({ n, sw: s, node: first || null, swIp: ip(s, n), nodeIp: first ? ip(first, n) : '', kept });
      ips[s] = ip(s, n);
      if (first) ips[first] = ip(first, n);
    }
    combos.sort((a, b) => a.n - b.n);
    for (const nd of extraList) {
      if (!number.has(nd)) continue;
      const { n, kept } = number.get(nd), sw = nodeSwitch.get(nd) || null;
      const reason = sw && comboSet.has(sw) ? 'more than one node on this switch' : sw && modeOf(sw) === 'paired' ? 'its switch is not ticked' : sw ? 'its switch is not paired' : 'switch not identified';
      extras.push({ n, node: nd, sw, ip: ip(nd, n), kept, reason });
      ips[nd] = ip(nd, n);
    }
    extras.sort((a, b) => a.n - b.n);
    if (Math.max(0, ...takenP) > 254) warnings.push(`Paired numbers reach ${Math.max(...takenP)}; the maximum is 254.`);

    for (const { type, members } of seqTypes) {
      const list = members.filter(sn => number.has(sn)).map(sn => { const { n, kept } = number.get(sn); ips[sn] = ip(sn, n); return { serial: sn, n, ip: ips[sn], kept }; }).sort((a, b) => a.n - b.n);
      if (!list.length) continue;
      const top = list[list.length - 1].n;
      if (top > 254) warnings.push(`${type} numbers reach ${top}; the maximum is 254.`);
      sequences.push({ type, devices: list });
    }

    // What the next new device of each type would get.
    const pairedThirds = Object.values(cfg).filter(c => c.mode !== 'sequence').map(c => c.third);
    const nextPaired = Math.max(pairedStart - 1, ...takenP, ...pairedThirds.map(highest)) + 1;
    const next = Object.fromEntries(Object.entries(cfg).map(([type, c]) => [type, c.mode === 'sequence' ? Math.max(c.start - 1, highest(c.third)) + 1 : nextPaired]));

    // Two devices planned onto one address (a Paired switch and node types set to the same third octet).
    const owners = {};
    for (const [s, a] of Object.entries(ips)) (owners[a] = owners[a] || []).push(s);
    for (const [a, who] of Object.entries(owners)) if (who.length > 1) clashes.push({ ip: a, serials: who });

    return { combos, extras, sequences, ips, warnings, clashes, next };
  }

  // One 4th octet per batch, with no cabling at all: every device gets `octet` on its own type's third octet. A device
  // still on the address `keep` (serial -> address) says it was given stays there, so the last batch does not move again
  // if it is still plugged in. Only ticked devices are sent, so only they can clash.
  function simplePlan({ devices, cfg, octet, prefix = '192.168', ticked = new Set(), keep = new Map() }) {
    const ips = {}, owners = {};
    for (const d of devices) {
      ips[d.serial] = d.ip && keep.get(d.serial) === d.ip ? d.ip : `${prefix}.${cfg[d.type].third}.${octet}`;
      if (ticked.has(d.serial)) (owners[ips[d.serial]] = owners[ips[d.serial]] || []).push(d.serial);
    }
    const clashes = Object.entries(owners).filter(([, who]) => who.length > 1).map(([ip, serials]) => ({ ip, serials }));
    const next = Object.fromEntries(Object.keys(cfg).map(type => [type, octet]));
    return { simple: true, combos: [], extras: [], sequences: [], ips, warnings: [], clashes, next };
  }

  // Ticked devices about to get a new number from cabling we could not establish: a paired switch whose neighbours
  // could not be read (the chain order may be wrong), or a paired node with no switch.
  function unsure(plan, ticked, unread) {
    const out = [];
    for (const c of plan.combos) if (!c.kept && ticked.has(c.sw) && unread.has(c.sw)) out.push(c.sw);
    for (const e of plan.extras) if (!e.kept && ticked.has(e.node) && e.reason === 'switch not identified') out.push(e.node);
    return out;
  }

  // The first two octets of every address: a usable first octet (not 0, not loopback, not multicast) and any second.
  function validPrefix(s) {
    const m = /^(\d{1,3})\.(\d{1,3})$/.exec(s || '');
    return !!m && +m[1] >= 1 && +m[1] <= 223 && +m[1] !== 127 && +m[2] <= 255;
  }

  // A saved numbering file, cut down to what is valid. Throws with a reason the page can show.
  function readNumberingFile(text, typeKeys, masks) {
    let f;
    try { f = JSON.parse(text); } catch { throw new Error('it is not a JSON file'); }
    if (!f || f.kind !== 'luminex-numbering') throw new Error('it is not a saved numbering file');
    const num = (v, lo, hi) => Number.isInteger(v) && v >= lo && v <= hi;
    const pre = typeof f.prefix === 'string' && validPrefix(f.prefix) ? f.prefix : null;
    const out = { types: {}, namePrefixes: {}, prefix: pre, netmask: masks.includes(f.netmask) ? f.netmask : null, nameStyle: f.nameStyle === 'prefix' || f.nameStyle === 'model' ? f.nameStyle : null, method: f.method === 'cabling' || f.method === 'simple' ? f.method : null };
    for (const k of typeKeys) {
      const t = (f.types || {})[k] || {}, c = {};
      if (t.mode === 'paired' || t.mode === 'sequence') c.mode = t.mode;
      if (num(t.third, 0, 255)) c.third = t.third;
      if (num(t.start, 1, 254)) c.start = t.start;
      out.types[k] = c;
      const p = (f.namePrefixes || {})[k];
      if (typeof p === 'string') out.namePrefixes[k] = p.slice(0, 12);
    }
    return out;
  }

  const api = { buildPlan, simplePlan, unsure, readNumberingFile, validPrefix };
  if (typeof module !== 'undefined' && module.exports) module.exports = api; else root.Combo = api;
})(typeof window !== 'undefined' ? window : globalThis);
