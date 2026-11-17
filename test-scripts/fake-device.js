#!/usr/bin/env node
'use strict';
// Fake Luminex devices for testing on a Mac with no hardware attached.
// Registers mDNS services with `dns-sd -R` and answers the reset calls on
// this machine's own LAN IP. The API port can be overridden because port 80
// needs root: run the tool with DEVICE_PORT=<port> to match.
//
//   node test-scripts/fake-device.js [--port 8080] [--password secret]
//
// Needs Node, but no npm install: it only uses what ships with Node.
//
// After a reset is accepted, the fake device "reboots": it disappears from
// mDNS for ~8 s, then comes back with a 2.x.x.x TXT ip_address and a default
// name, which is what a real device does. A keep-IP reset (keep_ip_settings /
// keep_ip true, or Gen1 factory=yes) brings it back on its old address instead.

const http = require('http');
const os = require('os');
const { spawn } = require('child_process');

const args = process.argv.slice(2);
const PORT = Number(args[args.indexOf('--port') + 1] || 8080);
const PASSWORD = args.includes('--password') ? args[args.indexOf('--password') + 1] : null;

const lanIp = Object.values(os.networkInterfaces()).flat().find(a => a.family === 'IPv4' && !a.internal)?.address;
if (!lanIp) { console.error('No LAN IP'); process.exit(1); }

const FAKES = [
  { kind: 'luminode', instance: 'LumiNode4 Fake A',  txt: { product_type: 'LumiNode4',   mac: 'aa:bb:cc:00:00:01', firmware_version: 'v2.7.2', serial: 'LUM838009434', device_name: 'Stage Left Node' } },
  { kind: 'luminode', instance: 'LumiNode12 Fake B', txt: { product_type: 'LumiNode12',  mac: 'aa:bb:cc:00:00:02', firmware_version: 'v2.7.2', serial: 'LUM760404119', device_name: 'FOH Node' }, needsPassword: true },
  { kind: 'gen1',     instance: 'GigaCore16Xt Fake', txt: { product_type: 'GigaCore16Xt', mac: 'aa:bb:cc:00:00:03', firmware_version: 'v2.3.3', serial: 'GC1600000003', device_name: 'Rack Switch' } },
  { kind: 'gen2',     instance: 'GigaCore10i Fake',  txt: { product_type: 'GigaCore10i',  mac: 'aa:bb:cc:00:00:04', firmware_version: 'v3.1.0', serial: 'GC1000000004', device_name: 'Stage Switch' } },
  { kind: 'unknown',  instance: 'Mystery Fake',      txt: { product_type: 'GigaCore99',   mac: 'aa:bb:cc:00:00:05', firmware_version: 'v1.0.0', serial: 'GC9900000005', device_name: 'Odd One' } },
  { kind: 'luminode', instance: 'LumiNode4 Far',     txt: { product_type: 'LumiNode4',   mac: 'aa:bb:cc:00:00:06', firmware_version: 'v2.7.2', serial: 'LUM607697448', device_name: 'Unreachable Node' }, ip: '192.168.4.55/16' },
];

// One HTTP server answers for all fakes; they are told apart by... nothing,
// since they share an IP. Good enough: the first matching kind per path wins,
// and state is keyed by path so each kind reboots independently.
const state = new Map();   // kind -> { down: bool }
const webAuth = { luminode: true, gen2: true };
function authOk(req, fake) {
  const want = PASSWORD && fake.kind !== 'gen1' ? PASSWORD : null;   // --password makes every REST fake require it
  if (fake.kind === 'gen1' && !req.headers.authorization) return false;  // Gen1 wants the header always
  if (!want) return true;
  const h = req.headers.authorization || '';
  const [u, p] = Buffer.from(h.replace(/^Basic\s+/i, ''), 'base64').toString().split(':');
  return u === 'admin' && p === want;
}

const srv = http.createServer((req, res) => {
  let body = '';
  req.on('data', c => body += c);
  req.on('end', () => {
    console.log(`fake ← ${req.method} ${req.url} auth=${req.headers.authorization ? 'yes' : 'no'} body=${body}`);
    const key = req.method + ' ' + req.url;
    let kind = { 'GET /api/deviceinfo': 'luminode', 'POST /api/reset': 'luminode',
                 'GET /api/device': 'gen2', 'PUT /api/reset': 'gen2', 'PUT /api/ip_settings': 'gen2',
                 'PUT /api/deviceinfo': 'luminode', 'PUT /api/device/name': 'gen2',
                 'GET /stat/ports': 'gen1', 'POST /config/misc': 'gen1',
                 'GET /config/ip2_ll_ip': 'gen1', 'POST /config/ip2_config': 'gen1',
                 'GET /api/lldp/neighbor': 'luminode', 'GET /config/lldp_neighbors': 'gen1',
                 'GET /api/deviceinfo/auth': 'luminode', 'PUT /api/deviceinfo/auth': 'luminode',
                 'GET /api/security/enabled': 'gen2', 'PUT /api/security/enabled': 'gen2' }[key];
    // LumiNode: PUT /api/ipsettings {ipaddress}. GigaCore Gen2: PUT /api/ip_settings
    // {mode,ip_address,prefix_length,default_gateway} (real 16tf, Nov 2026).
    let newIp = null, prefix = 16;
    const bits = mask => String(mask || '').split('.').reduce((n, o) => n + (+o).toString(2).replace(/0/g, '').length, 0) || 16;
    if (key === 'PUT /api/ipsettings') { kind = 'luminode'; try { const b = JSON.parse(body); newIp = b.ipaddress; prefix = bits(b.netmask); } catch {} }
    if (key === 'PUT /api/ip_settings') { try { const b = JSON.parse(body); newIp = b.ip_address; prefix = b.prefix_length || 16; } catch {} }
    if (key === 'POST /config/ip2_config') { const b = new URLSearchParams(body); newIp = b.get('if_addr_0'); prefix = +b.get('if_mask_0') || 16; }
    const fake = kind && FAKES.find(f => f.kind === kind);
    if (!fake) { res.writeHead(404); return res.end('nope'); }
    if (state.get(fake.kind)?.down) { req.socket.destroy(); return; }
    if (!authOk(req, fake)) { res.writeHead(401, { 'WWW-Authenticate': 'Basic realm="x"' }); return res.end('Unauthorized'); }

    const isReset = (fake.kind === 'luminode' && req.method === 'POST' && req.url === '/api/reset')
                 || (fake.kind === 'gen2' && req.method === 'PUT' && req.url === '/api/reset')
                 || (fake.kind === 'gen1' && req.method === 'POST' && req.url === '/config/misc');
    // Web password on/off: LumiNode PUT /api/deviceinfo/auth {web_auth_enabled}; Gen2 PUT /api/security/enabled <bool>.
    if (key === 'GET /api/deviceinfo/auth') { res.writeHead(200, { 'Content-Type': 'application/json' }); return res.end(JSON.stringify({ web_auth_enabled: webAuth.luminode })); }
    if (key === 'GET /api/security/enabled') { res.writeHead(200, { 'Content-Type': 'application/json' }); return res.end(String(webAuth.gen2)); }
    if (key === 'PUT /api/deviceinfo/auth' || key === 'PUT /api/security/enabled') {
      try { const b = JSON.parse(body); webAuth[fake.kind] = typeof b === 'boolean' ? b : b.web_auth_enabled; } catch { res.writeHead(400); return res.end('bad body'); }
      console.log(`fake ${fake.kind}: web password ${webAuth[fake.kind] ? 'on' : 'off'}`);
      res.writeHead(fake.kind === 'gen2' ? 200 : 204); return res.end();
    }
    // The cabling: both fake nodes sit on the fake Gen1 switch (they share an IP, so they give one answer).
    if (key === 'GET /api/lldp/neighbor') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify([{ port: 'eth1', chassis_id: 'aa:bb:cc:00:00:03', remote_port_id: '1' }]));
    }
    if (key === 'GET /config/lldp_neighbors') {
      res.writeHead(200, { 'Content-Type': 'text/plain' });
      return res.end('Port 1?AA-BB-CC-00-00-01?eth1?LUM838009434?eth1?Other(-), Bridge(+)?|Port 2?AA-BB-CC-00-00-02?eth1?LUM760404119?eth1?Other(-), Bridge(+)?|');
    }
    if (isReset) {
      if (fake.kind === 'gen1') { req.socket.destroy(); }          // Gen1 returns no page and just reboots
      else { res.writeHead(204); res.end(); }
      let keepIp = false;
      if (fake.kind === 'gen1') keepIp = new URLSearchParams(body).has('factory') && !new URLSearchParams(body).has('factory_full');
      else { try { const b = JSON.parse(body); keepIp = fake.kind === 'gen2' ? b.keep_ip === true : b.keep_ip_settings === true; } catch {} }
      console.log(`fake ${fake.kind}: RESET accepted${keepIp ? ' (keeping IP)' : ''}, rebooting`);
      reboot(fake.kind, null, 16, keepIp);
      return;
    }
    // Device name: LumiNode PUT /api/deviceinfo {short_name,long_name,ID,colors}; Gen2 PUT /api/device/name "name".
    if (key === 'PUT /api/deviceinfo' || key === 'PUT /api/device/name') {
      let name = null;
      try { const b = JSON.parse(body); name = typeof b === 'string' ? b : b.short_name; } catch {}
      if (name == null) { res.writeHead(400); return res.end('bad body'); }
      for (const f of FAKES.filter(x => x.kind === fake.kind)) f.txt.device_name = name;
      console.log(`fake ${fake.kind}: name set to "${name}"`);
      res.writeHead(200, { 'Content-Type': 'application/json' }); return res.end('{}');
    }
    if (newIp) {
      if (fake.kind === 'gen1') { req.socket.destroy(); }          // Gen1 reboots straight away
      else { res.writeHead(202, { 'Content-Type': 'application/json' }); res.end('{}'); }
      console.log(`fake ${fake.kind}: IP change to ${newIp}/${prefix} accepted, rebooting`);
      reboot(fake.kind, newIp, prefix);
      return;
    }
    if (key === 'GET /config/ip2_ll_ip') { res.writeHead(200); return res.end('OK'); }
    res.writeHead(200, { 'Content-Type': fake.kind === 'gen1' ? 'text/plain' : 'application/json' });
    res.end(fake.kind === 'gen1' ? '123|1/0/0/0/0' : JSON.stringify({ type: fake.txt.product_type, serial: fake.txt.serial, ID: 0, colors: [], short_name: fake.txt.device_name, long_name: fake.txt.product_type }));
  });
});

const procs = new Map();
function register(f, ipField, name) {
  f.cur = ipField;
  const txt = { ...f.txt, device_name: name, ip_address: ipField };
  const args = ['-R', f.instance, '_luminex._tcp', 'local', String(11461), ...Object.entries(txt).map(([k, v]) => `${k}=${v}`)];
  const p = spawn('dns-sd', args, { stdio: 'ignore' });
  procs.set(f.kind + f.instance, p);
}
function unregister(f) { const p = procs.get(f.kind + f.instance); if (p) { p.kill(); procs.delete(f.kind + f.instance); } }

function reboot(kind, newIpArg, prefix = 16, keepIp = false) {
  state.set(kind, { down: true });
  for (const f of FAKES.filter(x => x.kind === kind)) {
    unregister(f);
    setTimeout(() => {
      // Back with factory defaults: 2.x.x.x address derived from MAC, default name.
      const mac = f.txt.mac.split(':');
      const oldIp = f.cur;
      const newIp = newIpArg ? `${newIpArg}/${prefix}` : keepIp ? oldIp : `2.${parseInt(mac[3], 16)}.${parseInt(mac[4], 16)}.${parseInt(mac[5], 16)}/8`;
      register(f, newIp, newIpArg ? f.txt.device_name : f.txt.product_type);
      state.set(kind, { down: false });
      console.log(`fake ${kind}: back as ${newIp}`);
    }, keepIp ? 20000 : 8000);   // a keep-IP reset is only confirmed once the device is seen gone, so stay down long enough to be seen (real devices take over a minute)
  }
}

srv.listen(PORT, lanIp, () => {
  console.log(`Fake devices listening on http://${lanIp}:${PORT} (run the tool with DEVICE_PORT=${PORT})`);
  for (const f of FAKES) register(f, f.ip || `${lanIp}/21`, f.txt.device_name);
  console.log(`Registered ${FAKES.length} fake _luminex._tcp services via dns-sd -R`);
});
process.on('SIGINT', () => { for (const p of procs.values()) p.kill(); process.exit(0); });
process.on('SIGTERM', () => { for (const p of procs.values()) p.kill(); process.exit(0); });
