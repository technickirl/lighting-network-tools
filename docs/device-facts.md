# Device facts

What has been learned about LumiNode, LumiCore and GigaCore devices while building this tool, and what has and has not been checked on real hardware. Serial numbers and MAC addresses in this repo are made up; everything else is as seen on real devices. Each device serves its own API documentation at `http://<ip>/api/doc` (a LumiNode wants `Accept: text/html`).

Some entries name this tool's predecessors: the separate Reset tool, IP Setup and LumiNode Lite (and the older Node tools iptop2 and v28). Their code is now `internal/commission` and `internal/nodeconfig`, so what they did on real devices holds for Commission's device list and Configure page.

## Things not yet verified on real hardware

- mDNS discovery on a real Windows PC (native socket with SO_REUSEADDR; falls
  back to Bonjour's `dns-sd` if present).
- The Configure page's remaining *writes*: the per-IO edits
  (`PUT /api/IO/<n>`, `PUT /api/dmx/config`), startup buffers and reboot. Used on real nodes on 4 Nov 2026: connect,
  profile recall (`POST /api/profile/<n>`) and Quick Setup. v28 made all of
  the calls on a real node in June 2026. Its reads were all checked on a real
  LumiNode 12 on 4 Nov 2026 and the `outputs` shape question is answered in
  its README.
- The Gen1 IP-change call by the Go IP-setup build (iptop2 used it on real
  devices in June 2026).
- A LumiNode factory reset by the Go builds.
- A password set on any real device: the shared password flow has only run on fakes.
  RDM reads through the pages were used live on 12 Nov 2026 (see Device facts).
- The RDM tab's READDRESS… batch action (17 Nov 2026): fakes only. Its factory reset with KEEP ADDRESS worked on real
  fixtures (Nick, 1.7.0, 17 Nov 2026).
- IP Setup's numbering plan (Paired / Sequence) applied for real, and the Gen1 LED and identify writes: dry-run only so far (5 Nov 2026). Gen2 settings are from the spec only.

Verified on real hardware so far (4 Nov 2026): GigaCore Gen2 factory reset
(a GigaCore 16tf, firmware v1.9.2; the reset tool's `verified:true`);
discovery and classification of that switch and of a LumiNode 12 (firmware
v2.9.1) by all three tools; every LumiNode Lite read against that node; and
The Configure page's connect, profile recall and Quick Setup on real nodes, which
Nick used for commissioning the same day. Nick's verdict on the rest of Lite:
the process-block and IO views are not friendly enough to use for real work
(they don't show what he needs, e.g. the process type), so he used recalls
and Quick Setup instead.

Also verified by the IP Setup build on 4 Nov 2026, on a GigaCore 16tf (v1.9.2)
and a LumiNode 12 (v2.9.1), moved from `.8` to `.9` and back in one job each:
naming and IP change on both devices, confirmed by mDNS in about 5 s, with the
device list following. The switch's `ip_settings` and the node's `deviceinfo` and
`ipsettings` then matched what was recorded beforehand, as did the node's blocks.
Its IOs did too except `rdm_universe` (see `configure-page.md`). The Configure page's
identify worked on the same node, and on 6 Nov 2026 the IP Setup page's Identify flashed a real LumiNode (Alex).
On 5 Nov 2026, on five GigaCores (10 / 12 port, Gen1 v3.1.5) and four LumiNodes (4 / 12, v2.8.1): the LLDP topology read
worked on every device, and the Gen1 name write (`POST /config/switchlegend`, then `POST /config/icfg_conf_save`)
set all five switch names with description and ID unchanged.
The Configure page's block EDIT dialog also worked there, with the block's inputs and
outputs kept.
The Luminex Commission build did the same `.8` to `.9` and back through its page,
and its Configure buttons on the device row and in the Addressed list opened the
node at its new address. An open Configure tab locked during a job and followed
its node to the new address.

## Device facts learned so far

- Discovery is `_luminex._tcp`. TXT: `product_type, mac, firmware_version,
  serial, device_name, ip_address=a.b.c.d/nn`. The mDNS port is not the API
  port; the API is always on 80. A real GigaCore 16tf advertised
  `txtvers=1 product_type=GigaCore16tf device_name=GigaCore\ 16tf
  ip_address=192.168.5.50/16 ip_address_info=config_allowed`, instance name =
  serial (`LUM546436890`), mDNS port 11461. A real LumiNode 12 (v2.9.1):
  `product_type=LumiNode12 device_name=LumiNode73 ip_address=192.168.1.73/16
  ip_address_info=input+output+config_allowed`, instance = serial, port 11461.
  Full TXT as seen on 4 Nov 2026 (both devices also send `mac` and `serial`):
  `txtvers=1 product_type=GigaCore16tf mac=d0:69:9e:59:8e:ea
  firmware_version=v1.9.2 serial=LUM810196910 device_name=GigaCore 16tf - 2
  ip_address=192.168.11.2/16 ip_address_info=config_allowed` and
  `txtvers=1 product_type=LumiNode12 mac=d0:69:9e:23:be:8a
  firmware_version=v2.9.1 serial=LUM599849155 device_name=LumiNode12
  ip_address=2.168.2.200/8 ip_address_info=input+output+config_allowed`.
  A real GigaCore 12 (Gen1, firmware v3.1.4) advertised `txtvers=1
  product_type=GigaCore12 mac=d0:69:9e:dc:9d:ce firmware_version=v3.1.4
  serial=LUM5557061 device_name=DESK SWITCH ip_address=192.168.1.5/24`, instance =
  serial, port 11461, and is classified Gen1.
  `device_name` is the node's `short_name`; its `long_name` was just
  "LumiNode12".
- Device type from `product_type`: Gen1 regex `(10|12|14R|16XT|16RFO|26i)`,
  Gen2 regex `(10i|10t(-?IP)?|16i|16t|16tf|18t|20t|30i|O26|S30)`; LumiNode /
  LumiCore use the REST API (`/api/...`). See `internal/luminex/classify.go`.
- Auth: try none, then `admin:` (blank), then ask the user. GigaCore Gen1
  wants `Authorization: Basic admin:` even when the password is blank, and its
  config POST drops the TCP connection on success (treat reset/timeout after
  sending as "sent, awaiting confirmation").
- Gen1 IP form `POST /config/ip2_config` is field-order-sensitive; the Go code
  encodes it by hand (`url.Values` would sort alphabetically). Preflight
  `GET /config/ip2_ll_ip` first.
- LumiNode process block writes: `PATCH` replaces and needs `inputs` and `outputs`;
  `PUT` resets every field it leaves out; `PUT` with mode NONE clears the block and
  deletes the IOs attached to it. Send the whole config (see `configure-page.md`).
  Inputs a mode keeps: REROUTE 1, BACKUP 2, XFADE 2, SWITCH/HTP/LTP/CUSTOM 4. `PUT
  /input/<slot>` answers 204 for slots 0-3 in any mode, then drops what the mode does
  not take, so check the limit first. An input or output can be sACN or Art-Net, in
  a new IO with `io_type` input or output; removing one leaves its IO on the node.
  Control inputs: the IO's `io_type` must be the control's (`x-fade-ctrl`, ...), and
  **creating a control IO attaches it to every block with that control unset**, so the
  tools reuse one on that universe or take a new one back off the others. Never check
  a control test only after deleting its IOs: deleting clears it from every block.
- LumiNode: `POST /api/reset {keep_ip_settings:false, keep_user_profiles:false}`
  → 204; `PUT /api/ipsettings {ipaddress,netmask,gateway}` → 202.
- Factory reset keeping the IP, sent by a script on 9 Nov 2026 to five LumiNode 12s (v2.9.1,
  `keep_ip_settings:true, keep_user_profiles:true` → 204) and seven GigaCore 16tfs (v1.9.2,
  `keep_ip:true, keep_profiles:true` → 200 `{}`): every device came back on its address in 70-90 s with
  its profile list, but with its default name, and the nodes on the factory profile `Output ArtNet 0-11`,
  so no sACN until a recall. The 16tfs came back with querying on, RLinkX on every port and unknown
  flooding off. Gen1's keep-IP form `factory=yes` (Companion module) is untested. The tools' **Keep IP**
  tick-box sends these calls.
- GigaCore Gen2 (WebApi 1.5.0, documented at `http://<switch>/api/doc`, with the whole state at `GET /api`):
  `GET /api/device` answers without auth (`model, serial, mac_address,
  firmware.active, actual_ip{ip_address,prefix_length,default_gateway}`);
  `PUT /api/ip_settings {mode:"static", ip_address, prefix_length,
  default_gateway}` (the LumiNode `/api/ipsettings` path gives 404 here);
  `PUT /api/reset {keep_ip,keep_profiles,wait}` (+ optional `keep_tracing,
  keep_config, auto_reboot`); `PUT /api/reboot {wait}`; `PUT /api/identify
  {duration}`; `PUT /api/device/name "<name>"`. Every field has its own
  sub-path for partial writes.
- GigaCore Gen2 names take only letters, numbers, space, `-` and `_` (from the spec), so a name like `Switch 11.1`
  goes to a Gen2 switch as `Switch 11-1`.
- Device names: LumiNode `short_name` (max 17 chars) is what mDNS advertises
  as `device_name`; `PUT /api/deviceinfo` replaces the whole object, so read
  `ID` and `colors` first and send them back.
- Confirmation after a reset or IP change = the same **device** reappearing in
  mDNS with the new address (factory default is often 2.x.x.x/8, but not on every device, Nick 6 Nov 2026, so nothing may rely on it; a LumiNode reports its own as `default_ip` in `/api/deviceinfo`). Devices are matched
  on `Device.ID` (`luminex.SameDevice`): TXT `serial`, else `mac`, else the mDNS
  instance name (which is the serial on every real device seen so far). A
  device that comes back at its *old* address only counts as reset once it was
  seen to go down, and "down" needs both signals in the same verify pass: the
  HTTP probe of the old address failed *and* the mDNS scan did not list it. One
  missed 2.5 s probe alone is not enough (a device mid-change can miss one).
- Topology (who is plugged into what), read-only: LumiNode `GET /api/lldp/neighbor` → JSON array
  `{port, chassis_id (switch MAC), remote_port_id, mgmt_addresses[], system_description}`; GigaCore Gen1 (v3.1.5)
  `GET /config/lldp_neighbors` (Basic `admin:`) → text, neighbours split by `|`, fields by `?`:
  `Port N?<MAC, dash-separated>?<remote port>?<name>?<port desc>?<caps>?<mgmt addrs>`. The MACs equal the `mac` in each
  device's mDNS TXT, so links resolve to serials. Gen1 has no `/api/lldp/neighbor`. The IP setup's numbering is built on this.
- GigaCore Gen1 device-wide settings, read off the switch's own web UI (v3.1.5): `GET /config/switchlegend` →
  `name,descr(url-escaped),id,serial,board_rev,board_ass,MAC`; write `POST /config/switchlegend` form `sw_name` (max 16),
  `sw_id`, `sw_descr`, then `POST /config/icfg_conf_save save=1`. LED `GET|POST /config/lmx_led` (`brightness=0..4`, then save).
  SNMP (not offered by the tool, because it reboots the switch; Alex removed it) `GET /config/lmx_snmp` (mode is the second comma field of the first `/` part of the second `|` part) and
  `POST /config/lmx_snmp snmp_mode=1|0`, save, then the switch's UI reboots it (`POST /config/misc now=1`, the same path as the
  factory reset, and never sent to a real switch by this tool; if it comes back, check the switch goes down and returns). A real
  GigaCore 10 with SNMP off answered `0,1,None|,0/1/public/private/800007e5017f000001|0/0/public//0:0:0:0:0:0:0:0/1/1/0/1/5/1//None`.
  Gen2 is `PUT /api/snmp/enabled` (JSON boolean, spec only, no reboot), also not offered. Identify
  `POST /config/lmx wink=1`. Web password: only `POST /config/passwd` (`oldpass, pass1, pass2`), there is no on/off.
  LumiNode web UI password on/off is `PUT /api/deviceinfo/auth {web_auth_enabled}`; Gen2 `PUT /api/security/enabled` (JSON
  boolean, spec only). The settings page offers only "off" (on needs a password set), skips Gen1, and cannot turn it off on a
  device whose password it does not know. Tested against fakes only; not yet run on a real device. Worth checking on the first real
  node: the spec's `DeviceInfoAuth` also has a write-only `password` defaulting to null, which the tool leaves out, so see that this
  does not clear the stored password; and whether `web_auth_enabled` covers the API as well as the web page.
- GigaCore Gen2 LLDP, read off a real 16tf (v1.9.2) on 6 Nov 2026: `GET /api/lldp/neighbor` → JSON array
  `{port_number, chassis_id (upper-case MAC), sys_name, sys_description, remote_port, mgmt_address, source_address, ...}`, and it
  lists both switches and nodes. A LumiNode on v2.9.1 calls the neighbour's port `port_id`; on v2.8.1 it was `remote_port_id`.
- A device on the same address as this computer cannot be reached, because anything sent there stays on this computer. A
  GigaCore on the Mac's `en11` address was also missing from the scan on 6 Nov 2026, and appeared once the Mac moved. The page
  warns, including when only a neighbour reports the device.
- Devices that share an IP address cannot be told apart over IPv4: every request goes to the address, and only the device the
  computer's ARP cache points at answers. LumiNodes leave the hire company all on one address. Each device also has an IPv6
  link-local address made from its MAC (`d0:69:9e:7d:59:4e` → `fe80::d269:9eff:fe7d:594e`, in a URL `http://[fe80::...%25<zone>]/`).
  LumiNode 12 (v2.9.1) and GigaCore 16tf (v1.9.2) answer on it; a GigaCore 12 Gen1 (v3.1.4) does not, nor at the `169.254.x.x`
  from `/config/ip2_ll_ip`. Two LumiNode 12s sharing `.250` on 6 Nov 2026: `PUT /api/ipsettings` over IPv6 moved each one
  separately (202, new address reported within 2 s). After moving the cached one, IPv4 to the shared address stalled 52-56 s
  until the ARP entry changed. Fix duplicates in the Commission build then moved the same two off a shared `.250` for real, each confirmed
  by mDNS. Speed is the same as IPv4 (about 7 ms a request), and IPv4 stays the normal route, as IPv6
  link-local does not cross routers and Gen1 lacks it. **An IP change also turns a node's `GET /api/active_profile_name` from its
  profile name to `None` / modified**, seen over IPv6 on v2.9.1, with blocks and IOs unchanged.
- The reset tool and the IP Setup / Commission server read this computer's addresses every 2 s
  (`luminex.WatchInterfaces`). A new address (network card plugged in, cable connected, address added) starts a
  device scan a second later; a lost one only refreshes the page, so the device list stays under the link-down
  banner. Not yet tried with a real card being plugged in (12 Nov 2026).
- RDM, proven on LumiNode 12 - 8 (v2.9.1) on 12 Nov 2026, port 1 fed by **sACN** U1 from a grandMA2,
  with a Creamsource Vortex 8S (`4F75:000DC113`) on the line. The REST API has no RDM requests of its
  own: `GET /api/rdm/{port}/connected_devices` lists UIDs and `GET /api/rdm/uid` what the node has
  overheard; every GET/SET goes over Art-Net. With the DMX IO's `rdm` false the node ignores
  ArtTodRequest (no reply at all). `PUT /api/IO/100000` (whole object, `rdm:true`) → 202 and the node set
  `rdm_universe` to 1 (the sACN universe) by itself; afterwards every port read `rdm_universe` = its
  universe (1-12) though only port 1 was turned on, and turning rdm off leaves it. The node found the
  fixture in about 2 s. ArtPoll lists each DMX output at Art-Net port-address = its sACN universe, Status1
  "RDM capable", GoodOutputA bit 0 "sACN". ArtTodRequest / ArtRdm unicast to the node on 6454 at that
  address answered, **with the port still on sACN** (no switch to Art-Net needed). ArtRdm carries the RDM
  message from the sub-start code to the checksum (which counts the 0xCC start code). Afterwards
  `/api/rdm/uid` held every field our GETs fetched. DMX kept streaming; the PUT that set rdm back reset
  the port's frame counters. Captured packets are the tests in `internal/artnet`.
  `POST /api/dmx/ports/{n}/force_discovery` needs a JSON body (`{}`): without one the node answers 415 Unsupported
  Media Type (1.5.0 sent none; fixed in 1.5.1). On node 2, with 18 Vortex 8s on each of ports 4 and 5, the node's
  search after RDM on took longer than 2.5 s, so a Read straight away found nothing; the page now waits for the
  node's found count (`connected_devices`) to settle before reading.
  **The node's search** (node 2, port 10, 14 Nov 2026, from the app's log): after RDM on, `connected_devices` is empty
  until the search is done (13 s for 14 fixtures, 9 s for 18), then lists them all at once. Fixtures still powering up
  when RDM went on were missed, and the node did not add them by itself (still 14 six minutes later). A
  `force_discovery` keeps the old list until its search is done and then swaps in the new one (14 → 18), so before
  1.6.8 Rediscover read the old list, its count being steady. The page now waits for the list to change (up to 25 s).
  **Two ports on one universe** (LumiNode 12 - 7, ports 1 and 2 both fed by sACN U35, 13 Nov 2026) share an Art-Net
  port-address: one ArtTodRequest gets a table from each port. Before 1.6.4 a read took whichever table came first, so a
  port could show the other's fixtures or none. **BindIndex is not the port number**: on node 7 port 2's table carried
  BindIndex 2 and port 1 (nothing on the line) sent none, but on LumiNode 12 - 4 (ports 1 and 2 on sACN U33, v2.9.1,
  16 Nov 2026) port 1's table came as 2 and port 2's as 255, so a read of port 2 (1.6.9) took port 1's six fixtures as its
  own and added its five from the node's list. Since 1.6.10 `artnet.TodTables` keeps the tables apart, waits for one per
  port with RDM on at that address, and the read keeps the tables that share a device with the node's own list for the
  port (`connected_devices`). RDM GET/SET to that address goes out on both lines; the fixture on either answers.
  On LumiNode 12 - 5 (ports 9 and 10 on U19, 19 Nov 2026) **both tables came as BindIndex 255**, so 1.7.0 merged them:
  port 10's read showed port 9's two fixtures beside its own, and an address SET from port 10's tab reached a fixture on
  port 9. Since 1.7.2 `TodTables` also splits tables on a repeated block number, a read on a shared universe shows only
  the devices the node lists on that port and on no other at the address (any other device in the tables is noted, not
  shown), and every SET to a device the node does not list on the port is refused with nothing sent.
  The same day, live through the Commission build (Nick's OK): the node 8 RDM tab turned RDM on for port 1
  (one `PUT`), read the Vortex (personality "01: CCT-RGBW 8bit", 1 of 75, 10 ch at 1), the Fixtures view read
  all eight nodes and found it, and RDM off (one `PUT`) left the port streaming. No other write was sent.
  Later that day, with Nick's OK, the RDM tab's writes on the same fixture: SET IDENTIFY_DEVICE 1 then 0,
  SET DEVICE_LABEL "CSV-8S 901395 test" then back to "CSV-8S 901395"; each was ACKed in about 40 ms and read
  back right, and the node's `/api/rdm/uid` showed the original label afterwards. Those four SETs and the two
  RDM on/off `PUT`s were the only writes. Then SET DMX_START_ADDRESS 101 and back to 1 (Nick: any address
  is fine on that rig), each ACKed in about 40 ms, read back, and confirmed by a fresh read of the port.
  Then SET DMX_PERSONALITY 2 ("02: CCT 8bit", 5 ch) and back to 1 ("01: CCT-RGBW 8bit", 10 ch), likewise. The
  fixture's DEVICE_INFO footprint follows the personality straight away.
  Its SUPPORTED_PARAMETERS (read the same day, GETs only) list 46 PIDs: standard ones incl. FACTORY_DEFAULTS
  (GET 0 = not at defaults), E1.37-1 CURVE (1 of 4), DEVICE_HOURS 1605, LAMP_HOURS 887, five sensors (panel °C,
  four fan RPM), E1.37-2 network PIDs and RESET_DEVICE, plus 16 of Creamsource's own, each described by
  PARAMETER_DESCRIPTION (`docs/samples/vortex8s-rdm-parameters.txt`). **Those descriptions' default values are
  not its factory settings**: a SET FACTORY_DEFAULTS (Nick's OK, 12 Nov 2026) was ACKed in about 50 ms and applied at
  once (a label set to "reset test" came back as "CSV-8S 901395"), and CRMX Enabled, SACN Enabled, Master Mode and
  Display Always On all stayed on though their descriptions say default 0. GET FACTORY_DEFAULTS still read 0 straight
  after the reset, so that flag says nothing on this fixture; the page does not show it. The same day the fixture page
  toggled Display Always On (0x8513) off and on, read back each time.
- `luminex.Scan` returns an error only when nothing could browse at all (native
  UDP 5353 bind failed and `dns-sd` is missing). The reset and IP tools show it
  as the `scanError` banner instead of "No Luminex devices found"; LumiNode
  Lite's `/api/discover` answers 502 with the text.

