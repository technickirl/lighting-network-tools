'use strict';
// node combo_test.js — checks public/combo.js against the topology read from the real kit on 4 Nov 2026:
// five GigaCores in a line (0582703 — 0587670 — 0582898 — 0361052 — 0364742) with a node on four of them.
const assert = require('node:assert/strict');
const { buildPlan, validPrefix } = require('./public/combo.js');

const sw = (serial, type) => ({ serial, role: 'switch', type });
const nd = (serial, type) => ({ serial, role: 'node', type });
const devices = [
  sw('LUM7433564', 'gc10'), sw('LUM2346220', 'gc10'), sw('LUM7264427', 'gc10'),
  sw('LUM6613257', 'gc12'), sw('LUM6666680', 'gc12'),
  nd('LUM182159547', 'ln4'), nd('LUM669433015', 'ln4'), nd('LUM326164803', 'ln4'), nd('LUM491425257', 'ln12'),
];
const links = [
  // each node reports its switch…
  { from: 'LUM669433015', to: 'LUM2346220' }, { from: 'LUM326164803', to: 'LUM7264427' },
  { from: 'LUM182159547', to: 'LUM7433564' }, { from: 'LUM491425257', to: 'LUM6613257' },
  // …and each switch reports its neighbours
  { from: 'LUM7264427', to: 'LUM2346220' }, { from: 'LUM7264427', to: 'LUM6613257' }, { from: 'LUM7264427', to: 'LUM326164803' },
  { from: 'LUM2346220', to: 'LUM7264427' }, { from: 'LUM2346220', to: 'LUM7433564' }, { from: 'LUM2346220', to: 'LUM669433015' },
  { from: 'LUM7433564', to: 'LUM2346220' }, { from: 'LUM7433564', to: 'LUM182159547' },
  { from: 'LUM6613257', to: 'LUM491425257' }, { from: 'LUM6613257', to: 'LUM7264427' }, { from: 'LUM6613257', to: 'LUM6666680' },
  { from: 'LUM6666680', to: 'LUM6613257' },
];

// Alex's rig: GigaCore 10 + LumiNode 4 are combos (2.n / 3.n); the GigaCore 12s and the LumiNode 12 count up on their own.
const cfg = {
  gc10: { mode: 'paired', third: 2, start: 1 }, gc12: { mode: 'sequence', third: 7, start: 1 },
  ln4: { mode: 'paired', third: 3, start: 1 }, ln12: { mode: 'sequence', third: 4, start: 1 },
};
let r = buildPlan({ devices, links, cfg });
// the chain is walked from its lowest-serial end (LUM6666680), so the paired switches come out 0582898, 0587670, 0582703
assert.deepEqual(r.combos.map(c => c.sw), ['LUM7264427', 'LUM2346220', 'LUM7433564']);
assert.equal(r.ips.LUM7264427, '192.168.2.1'); assert.equal(r.ips.LUM326164803, '192.168.3.1');
assert.equal(r.ips.LUM2346220, '192.168.2.2'); assert.equal(r.ips.LUM669433015, '192.168.3.2');
assert.equal(r.ips.LUM7433564, '192.168.2.3'); assert.equal(r.ips.LUM182159547, '192.168.3.3');
// the 12s are not combos and do not take combo numbers
assert.equal(r.ips.LUM6666680, '192.168.7.1'); assert.equal(r.ips.LUM6613257, '192.168.7.2');
assert.equal(r.ips.LUM491425257, '192.168.4.1');
assert.deepEqual(r.extras, []); assert.deepEqual(r.warnings, []); assert.deepEqual(r.clashes, []);
assert.deepEqual(r.sequences.map(s => s.type), ['gc12', 'ln12']);

// Reordering a group, and a different start number.
r = buildPlan({ devices, links, cfg: { ...cfg, gc12: { mode: 'sequence', third: 7, start: 10 } }, order: ['LUM6613257', 'LUM6666680', 'LUM7433564'] });
assert.equal(r.ips.LUM6613257, '192.168.7.10'); assert.equal(r.ips.LUM6666680, '192.168.7.11');
assert.equal(r.ips.LUM7433564, '192.168.2.1'); assert.equal(r.ips.LUM182159547, '192.168.3.1');   // moved to the front of the combos

// If the 12s become combos too, they join the chain numbering and the sequences go away.
r = buildPlan({ devices, links, cfg: { ...cfg, gc12: { mode: 'paired', third: 2, start: 1 }, ln12: { mode: 'paired', third: 3, start: 1 } } });
assert.deepEqual(r.combos.map(c => c.sw), ['LUM6666680', 'LUM6613257', 'LUM7264427', 'LUM2346220', 'LUM7433564']);
assert.equal(r.ips.LUM491425257, '192.168.3.2'); assert.equal(r.sequences.length, 0);

// A paired node on a switch that is not paired, and a second node on one switch, take the next numbers after the combos.
r = buildPlan({ devices, links, cfg: { ...cfg, gc10: { mode: 'paired', third: 2, start: 1 }, gc12: { mode: 'sequence', third: 7, start: 1 }, ln12: { mode: 'paired', third: 3, start: 1 } } });
assert.equal(r.ips.LUM491425257, '192.168.3.4');
assert.deepEqual(r.extras.map(e => e.reason), ['its switch is not paired']);
r = buildPlan({ devices: [sw('S1', 'gc10'), nd('N1', 'ln4'), nd('N2', 'ln4'), nd('N3', 'ln4')], links: [{ from: 'N1', to: 'S1' }, { from: 'N2', to: 'S1' }], cfg });
assert.equal(r.ips.N1, '192.168.3.1'); assert.equal(r.ips.N2, '192.168.3.2'); assert.equal(r.ips.N3, '192.168.3.3');
assert.deepEqual(r.extras.map(e => e.reason), ['more than one node on this switch', 'switch not identified']);

// Types can share a third octet: they count up together instead of colliding.
r = buildPlan({ devices, links, cfg: { ...cfg, gc12: { mode: 'sequence', third: 2, start: 1 } } });
assert.deepEqual(r.clashes, []);
assert.equal(r.ips.LUM7264427, '192.168.2.1'); assert.equal(r.ips.LUM6666680, '192.168.2.4'); assert.equal(r.ips.LUM6613257, '192.168.2.5');
assert.equal(r.next.gc12, 6); assert.equal(r.next.gc10, 6);
// Two Sequence types on one octet, with one already holding a number, as a GigaCore 12 and a GigaCore 16tf would.
const shared = { gc12: { mode: 'sequence', third: 11, start: 1 }, gc: { mode: 'sequence', third: 11, start: 1 } };
r = buildPlan({ devices: [{ serial: 'G12', role: 'switch', type: 'gc12', ip: '192.168.1.5' }, { serial: 'G16', role: 'switch', type: 'gc', ip: '192.168.5.5' }], links: [], cfg: shared });
assert.equal(r.ips.G12, '192.168.11.1'); assert.equal(r.ips.G16, '192.168.11.2'); assert.deepEqual(r.clashes, []);
r = buildPlan({ devices: [{ serial: 'G12', role: 'switch', type: 'gc12', ip: '192.168.11.1' }, { serial: 'G16', role: 'switch', type: 'gc', ip: '2.1.1.1' }], links: [], cfg: shared });
assert.equal(r.ips.G12, '192.168.11.1'); assert.equal(r.ips.G16, '192.168.11.2'); assert.equal(r.next.gc, 3);
// A combo's number has to be free on both its octets: the Sequence switch holds 2.1, so the combo starts at 2.
r = buildPlan({ devices: [{ serial: 'X', role: 'switch', type: 'gc12', ip: '192.168.2.1' }, { serial: 'S', role: 'switch', type: 'gc10', ip: '2.1.1.1' }, { serial: 'N', role: 'node', type: 'ln4', ip: '2.1.1.2' }],
  links: [{ from: 'N', to: 'S' }], cfg: { gc12: { mode: 'sequence', third: 2, start: 1 }, gc10: { mode: 'paired', third: 2, start: 1 }, ln4: { mode: 'paired', third: 3, start: 1 } } });
assert.equal(r.ips.S, '192.168.2.2'); assert.equal(r.ips.N, '192.168.3.2'); assert.deepEqual(r.clashes, []);
// A Paired switch and its node cannot share one: that is reported, not hidden.
r = buildPlan({ devices: [{ serial: 'S', role: 'switch', type: 'gc10' }, { serial: 'N', role: 'node', type: 'ln4' }], links: [{ from: 'N', to: 'S' }],
  cfg: { gc10: { mode: 'paired', third: 2, start: 1 }, ln4: { mode: 'paired', third: 2, start: 1 } } });
assert.deepEqual(r.clashes, [{ ip: '192.168.2.1', serials: ['S', 'N'] }]);

// ── Keep existing numbers: a second batch continues the numbering ──────────────────────────────────────────
// Three combos already on 2.1/3.1, 2.2/3.2, 2.3/3.3 (not in chain order), then one more combo is plugged in.
const held = (serial, role, type, ip) => ({ serial, role, type, ip });
const rig = [
  held('LUM2346220', 'switch', 'gc10', '192.168.2.1'), held('LUM669433015', 'node', 'ln4', '192.168.3.1'),
  held('LUM7264427', 'switch', 'gc10', '192.168.2.3'), held('LUM326164803', 'node', 'ln4', '192.168.3.3'),
  held('LUM7433564', 'switch', 'gc10', '192.168.2.2'), held('LUM182159547', 'node', 'ln4', '192.168.3.2'),
  held('NEWSW', 'switch', 'gc10', '192.168.1.30'), held('NEWND', 'node', 'ln4', '192.168.1.30'),
];
const rigLinks = [
  { from: 'LUM669433015', to: 'LUM2346220' }, { from: 'LUM326164803', to: 'LUM7264427' }, { from: 'LUM182159547', to: 'LUM7433564' },
  { from: 'NEWND', to: 'NEWSW' }, { from: 'LUM7433564', to: 'LUM2346220' }, { from: 'LUM2346220', to: 'LUM7264427' }, { from: 'LUM7264427', to: 'NEWSW' },
];
const rigCfg = { gc10: { mode: 'paired', third: 2, start: 1 }, ln4: { mode: 'paired', third: 3, start: 1 } };
r = buildPlan({ devices: rig, links: rigLinks, cfg: rigCfg });
assert.equal(r.ips.LUM2346220, '192.168.2.1'); assert.equal(r.ips.LUM7433564, '192.168.2.2'); assert.equal(r.ips.LUM7264427, '192.168.2.3');   // nobody is shuffled
assert.equal(r.ips.LUM326164803, '192.168.3.3');
assert.equal(r.ips.NEWSW, '192.168.2.4'); assert.equal(r.ips.NEWND, '192.168.3.4');                                                        // the new combo is 2.4 + 3.4
assert.deepEqual(r.combos.map(c => c.n), [1, 2, 3, 4]); assert.deepEqual(r.combos.map(c => c.kept), [true, true, true, false]);
assert.equal(r.next.gc10, 5); assert.equal(r.next.ln4, 5);                                                                                                           // and the next one would be 5
// "Renumber all" ignores the current addresses and numbers the whole chain in order.
r = buildPlan({ devices: rig, links: rigLinks, cfg: rigCfg, renumber: true });
assert.deepEqual(r.combos.map(c => c.sw), ['LUM7433564', 'LUM2346220', 'LUM7264427', 'NEWSW']);
assert.ok(r.combos.every(c => !c.kept));
// A combo whose switch and node disagree takes the switch's number; two devices on one number: the first keeps it.
r = buildPlan({ devices: [held('S1', 'switch', 'gc10', '192.168.2.3'), held('N1', 'node', 'ln4', '192.168.3.5'), held('S2', 'switch', 'gc10', '192.168.2.3'), held('N2', 'node', 'ln4', '')],
  links: [{ from: 'N1', to: 'S1' }, { from: 'N2', to: 'S2' }, { from: 'S1', to: 'S2' }], cfg: rigCfg });
assert.equal(r.ips.S1, '192.168.2.3'); assert.equal(r.ips.N1, '192.168.3.3');
assert.equal(r.ips.S2, '192.168.2.4'); assert.equal(r.ips.N2, '192.168.3.4');
// Sequence types continue too, and Start is only a minimum.
const seqCfg = { gc12: { mode: 'sequence', third: 7, start: 1 } };
r = buildPlan({ devices: [held('A', 'switch', 'gc12', '192.168.7.1'), held('B', 'switch', 'gc12', '192.168.7.2'), held('C', 'switch', 'gc12', '192.168.98.169')], links: [], cfg: seqCfg });
assert.equal(r.ips.C, '192.168.7.3'); assert.equal(r.next.gc12, 4);
r = buildPlan({ devices: [held('A', 'switch', 'gc12', '192.168.7.1'), held('C', 'switch', 'gc12', '192.168.98.169')], links: [], cfg: { gc12: { mode: 'sequence', third: 7, start: 10 } } });
assert.equal(r.ips.A, '192.168.7.1'); assert.equal(r.ips.C, '192.168.7.10');   // a held number is kept even below Start; new ones start at Start
// A second node on one switch keeps its own number when it already has one.
r = buildPlan({ devices: [held('S1', 'switch', 'gc10', '192.168.2.1'), held('N1', 'node', 'ln4', '192.168.3.1'), held('N2', 'node', 'ln4', '192.168.3.6')], links: [{ from: 'N1', to: 'S1' }, { from: 'N2', to: 'S1' }], cfg: rigCfg });
assert.equal(r.ips.N2, '192.168.3.6'); assert.equal(r.extras[0].kept, true);

// A batch swapped in after the last one carries on from it, though the last one has left the scan.
const nextBatch = [held('SB', 'switch', 'gc10', '2.1.1.3'), held('NB', 'node', 'ln4', '2.1.1.4')];
const batchLinks = [{ from: 'NB', to: 'SB' }];
const addressedEarlier = ['192.168.2.1', '192.168.3.1', '192.168.2.2', '192.168.3.2', 'not an address'];
r = buildPlan({ devices: nextBatch, links: batchLinks, cfg: rigCfg, reserved: addressedEarlier });
assert.equal(r.ips.SB, '192.168.2.3'); assert.equal(r.ips.NB, '192.168.3.3'); assert.equal(r.next.gc10, 4);
r = buildPlan({ devices: [], links: [], cfg: rigCfg, reserved: addressedEarlier });
assert.equal(r.next.gc10, 3);                                                               // the table shows it before anything is plugged in
r = buildPlan({ devices: [held('A', 'switch', 'gc12', '2.1.1.1')], links: [], cfg: seqCfg, reserved: ['192.168.7.5'] });
assert.equal(r.ips.A, '192.168.7.6');
// A device that was addressed earlier and is still here keeps its number, and Renumber all starts again.
r = buildPlan({ devices: [held('S1', 'switch', 'gc10', '192.168.2.1'), held('N1', 'node', 'ln4', '192.168.3.1')], links: [{ from: 'N1', to: 'S1' }], cfg: rigCfg, reserved: addressedEarlier });
assert.equal(r.ips.S1, '192.168.2.1'); assert.equal(r.combos[0].kept, true);
r = buildPlan({ devices: nextBatch, links: batchLinks, cfg: rigCfg, reserved: addressedEarlier, renumber: true });
assert.equal(r.ips.SB, '192.168.2.1');

// Cabling that could not be established stops only the devices whose new number rests on it.
const { unsure, readNumberingFile } = require('./public/combo.js');
const ticked = (...s) => new Set(s);
r = buildPlan({ devices: [sw('S1', 'gc10'), nd('N1', 'ln4')], links: [{ from: 'N1', to: 'S1' }], cfg });
assert.deepEqual(unsure(r, ticked('S1', 'N1'), new Set()), []);
assert.deepEqual(unsure(r, ticked('S1', 'N1'), new Set(['S1'])), ['S1']);                   // a Gen1 switch that did not answer
assert.deepEqual(unsure(r, ticked('N1'), new Set(['S1'])), []);                             // not ticked, so not sent
r = buildPlan({ devices: [sw('S1', 'gc10'), nd('N1', 'ln4')], links: [], cfg });            // read fine, but an empty LLDP table
assert.deepEqual(unsure(r, ticked('S1', 'N1'), new Set()), ['N1']);
assert.deepEqual(unsure(r, ticked('S1'), new Set()), []);
r = buildPlan({ devices: [held('S1', 'switch', 'gc10', '192.168.2.1'), held('N1', 'node', 'ln4', '192.168.3.1')], links: [], cfg });
assert.deepEqual(unsure(r, ticked('S1', 'N1'), new Set(['S1', 'N1'])), []);                 // keeping their numbers, so the cabling does not matter

// One 4th octet per batch: no cabling, each device on its own type's third octet.
const { simplePlan } = require('./public/combo.js');
const simpleCfg = { gc10: { mode: 'paired', third: 11, start: 1 }, ln4: { mode: 'paired', third: 4, start: 1 }, ln12: { mode: 'sequence', third: 12, start: 1 } };
const dev = (serial, type, ip) => ({ serial, type, ip });
r = simplePlan({ devices: [dev('S', 'gc10', '2.1.1.1'), dev('A', 'ln4', '2.1.1.2'), dev('B', 'ln12', '2.1.1.3')], cfg: simpleCfg, octet: 5, ticked: ticked('S', 'A', 'B') });
assert.deepEqual(r.ips, { S: '192.168.11.5', A: '192.168.4.5', B: '192.168.12.5' });
assert.deepEqual(r.clashes, []); assert.deepEqual(r.combos, []); assert.equal(r.next.ln12, 5); assert.equal(r.simple, true);
// The last batch, still plugged in, stays put while the new one gets the next octet.
r = simplePlan({ devices: [dev('S', 'gc10', '192.168.11.5'), dev('N', 'gc10', '2.1.1.9')], cfg: simpleCfg, octet: 6, ticked: ticked('S', 'N'), keep: new Map([['S', '192.168.11.5']]) });
assert.deepEqual(r.ips, { S: '192.168.11.5', N: '192.168.11.6' }); assert.deepEqual(r.clashes, []);
// …unless it has since moved somewhere else.
r = simplePlan({ devices: [dev('S', 'gc10', '2.1.1.1')], cfg: simpleCfg, octet: 6, keep: new Map([['S', '192.168.11.5']]) });
assert.equal(r.ips.S, '192.168.11.6');
// Two ticked devices on one third octet would share an address; an unticked one is not sent, so it is no clash.
const twoSwitches = [dev('S1', 'gc10', '2.1.1.1'), dev('S2', 'gc10', '2.1.1.2')];
assert.deepEqual(simplePlan({ devices: twoSwitches, cfg: simpleCfg, octet: 5, ticked: ticked('S1', 'S2') }).clashes, [{ ip: '192.168.11.5', serials: ['S1', 'S2'] }]);
assert.deepEqual(simplePlan({ devices: twoSwitches, cfg: simpleCfg, octet: 5, ticked: ticked('S1') }).clashes, []);

// Unticked devices take no new number, so the numbering closes up; the real rig of 6 Nov 2026, leaving out DESK SWITCH.
const tickRig = [held('DESK', 'switch', 'gc12', '192.168.1.5'), held('S57', 'switch', 'gc', '192.168.1.57'), held('S56', 'switch', 'gc', '192.168.1.56'), held('N250', 'node', 'ln12', '192.168.1.250'), held('N100', 'node', 'ln12', '192.168.1.100')];
const tickLinks = [{ from: 'N250', to: 'S57' }, { from: 'N100', to: 'S56' }, { from: 'S56', to: 'S57' }, { from: 'DESK', to: 'S57' }];
const tickCfg = { gc12: { mode: 'sequence', third: 11, start: 1 }, gc: { mode: 'sequence', third: 11, start: 1 }, ln12: { mode: 'sequence', third: 12, start: 1 } };
r = buildPlan({ devices: tickRig, links: tickLinks, cfg: tickCfg });
assert.equal(r.ips.DESK, '192.168.11.1'); assert.equal(r.ips.S57, '192.168.11.2'); assert.equal(r.ips.S56, '192.168.11.3');   // ticked, or no ticks given: as before
r = buildPlan({ devices: tickRig, links: tickLinks, cfg: tickCfg, ticked: ticked('S57', 'S56', 'N250', 'N100') });
assert.equal(r.ips.DESK, undefined); assert.equal(r.ips.S57, '192.168.11.1'); assert.equal(r.ips.S56, '192.168.11.2'); assert.equal(r.next.gc, 3);
assert.deepEqual(r.sequences.find(q => q.type === 'gc12'), undefined);
// An unticked device already on a number holds it, so nothing lands on top of it.
r = buildPlan({ devices: [held('A', 'switch', 'gc12', '192.168.11.1'), held('B', 'switch', 'gc', '2.1.1.1')], links: [], cfg: tickCfg, ticked: ticked('B') });
assert.equal(r.ips.A, '192.168.11.1'); assert.equal(r.ips.B, '192.168.11.2'); assert.deepEqual(r.clashes, []);
// Paired: an unticked switch with no number is out of the combos and its node is numbered on its own, closing up the rest.
const tickPaired = { gc10: { mode: 'paired', third: 2, start: 1 }, ln4: { mode: 'paired', third: 3, start: 1 } };
const pairRig = [held('S1', 'switch', 'gc10', '2.1.1.1'), held('N1', 'node', 'ln4', '2.1.1.2'), held('S2', 'switch', 'gc10', '2.1.1.3'), held('N2', 'node', 'ln4', '2.1.1.4')];
const pairLinks = [{ from: 'N1', to: 'S1' }, { from: 'N2', to: 'S2' }];
r = buildPlan({ devices: pairRig, links: pairLinks, cfg: tickPaired, ticked: ticked('S2', 'N2') });
assert.equal(r.ips.S2, '192.168.2.1'); assert.equal(r.ips.N2, '192.168.3.1'); assert.equal(r.ips.S1, undefined); assert.equal(r.ips.N1, undefined);
assert.deepEqual(r.combos.map(c => c.sw), ['S2']);
r = buildPlan({ devices: pairRig, links: pairLinks, cfg: tickPaired, ticked: ticked('N1', 'S2', 'N2') });
assert.deepEqual(r.combos.map(c => [c.sw, c.node]), [['S2', 'N2']]); assert.equal(r.ips.N1, '192.168.3.2');
assert.deepEqual(r.extras.map(e => [e.node, e.reason]), [['N1', 'its switch is not ticked']]);
assert.deepEqual(unsure(r, ticked('N1', 'S2', 'N2'), new Set()), []);                   // its cabling is known, so it does not block Apply
// An unticked switch keeping its number still pairs with a ticked node, and two on one number: only the ticked one moves.
r = buildPlan({ devices: [held('S1', 'switch', 'gc10', '192.168.2.4'), held('N1', 'node', 'ln4', '2.1.1.2')], links: [{ from: 'N1', to: 'S1' }], cfg: tickPaired, ticked: ticked('N1') });
assert.equal(r.ips.S1, '192.168.2.4'); assert.equal(r.ips.N1, '192.168.3.4');
r = buildPlan({ devices: [held('S1', 'switch', 'gc10', '192.168.2.4'), held('S2', 'switch', 'gc10', '192.168.2.4'), held('N2', 'node', 'ln4', '2.1.1.4')], links: [{ from: 'N2', to: 'S2' }], cfg: tickPaired, ticked: ticked('N2') });
assert.equal(r.ips.S1, '192.168.2.4'); assert.equal(r.ips.S2, undefined); assert.equal(r.ips.N2, '192.168.3.5');

// Numbering files.
const file = JSON.stringify({ kind: 'luminex-numbering', version: 1, netmask: '255.255.0.0', nameStyle: 'model',
  types: { a: { mode: 'paired', third: 11, start: 3 }, b: { mode: 'nonsense', third: 999, start: 0 }, ignored: { third: 1 } }, namePrefixes: { a: 'Switch', b: 7 } });
assert.deepEqual(readNumberingFile(file, ['a', 'b'], ['255.255.0.0']), { types: { a: { mode: 'paired', third: 11, start: 3 }, b: {} }, namePrefixes: { a: 'Switch' }, prefix: null, netmask: '255.255.0.0', nameStyle: 'model', method: null });
assert.throws(() => readNumberingFile('not json', ['a'], []), /not a JSON file/);
assert.throws(() => readNumberingFile('{"kind":"other"}', ['a'], []), /not a saved numbering file/);

// The first two octets can be anything usable: 172.16, 10.0, 2.0...
for (const p of ['192.168', '172.16', '10.0', '10.20', '2.0', '2.255']) assert.ok(validPrefix(p), p);
for (const p of ['', '192', '0.5', '127.0', '224.1', '256.1', '10.256', '10.0.1', 'a.b', '10. 1']) assert.ok(!validPrefix(p), p);
const pre = (prefix, extra = {}) => buildPlan({ devices, links, cfg, prefix, ...extra });
r = pre('172.16');
assert.equal(r.ips.LUM7264427, '172.16.2.1'); assert.equal(r.ips.LUM326164803, '172.16.3.1'); assert.equal(r.ips.LUM6666680, '172.16.7.1');
assert.equal(pre('10.0').ips.LUM2346220, '10.0.2.2'); assert.equal(pre('2.0').ips.LUM491425257, '2.0.4.1');
assert.deepEqual(buildPlan({ devices, links, cfg }).ips, buildPlan({ devices, links, cfg, prefix: '192.168' }).ips);   // 192.168 is still the default
// A device keeps its number only on the same prefix: on another one it counts as new and is numbered from the start.
const onOld = devices.map(d => d.serial === 'LUM7264427' ? { ...d, ip: '192.168.2.5' } : d);
r = buildPlan({ devices: onOld, links, cfg, prefix: '192.168' }); assert.equal(r.ips.LUM7264427, '192.168.2.5');
r = buildPlan({ devices: onOld, links, cfg, prefix: '10.0' }); assert.equal(r.ips.LUM7264427, '10.0.2.1');
// Addresses given out earlier on another prefix do not push the numbering along.
r = buildPlan({ devices, links, cfg, prefix: '10.0', reserved: ['192.168.2.40'] }); assert.equal(r.ips.LUM7264427, '10.0.2.1');
r = buildPlan({ devices, links, cfg, prefix: '10.0', reserved: ['10.0.2.40'] }); assert.equal(r.ips.LUM7264427, '10.0.2.41');
// One 4th octet per batch uses the prefix too.
assert.equal(simplePlan({ devices: [{ serial: 'S1', type: 'gc10', ip: '' }], cfg, octet: 9, prefix: '172.16' }).ips.S1, '172.16.2.9');
assert.equal(simplePlan({ devices: [{ serial: 'S1', type: 'gc10', ip: '' }], cfg, octet: 9 }).ips.S1, '192.168.2.9');
// A numbering file carries it.
assert.equal(readNumberingFile(JSON.stringify({ kind: 'luminex-numbering', prefix: '172.16' }), ['a'], []).prefix, '172.16');
assert.equal(readNumberingFile(JSON.stringify({ kind: 'luminex-numbering', prefix: '300.1' }), ['a'], []).prefix, null);

// Out of range.
assert.equal(buildPlan({ devices: [sw('S1', 'a'), sw('S2', 'a')], links: [], cfg: { a: { mode: 'sequence', third: 2, start: 254 } } }).warnings.length, 1);
console.log('combo_test.js: ok');
