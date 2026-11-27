# The device list: numbering and device calls

Notes on the part of Luminex Commission that scans, numbers, addresses and names devices (`internal/commission`).

Some entries name this tool's predecessors: the separate Reset tool, IP Setup and LumiNode Lite (and the older Node tools iptop2 and v28). Their code is now `internal/commission` and `internal/nodeconfig`, so what they did on real devices holds for Commission's device list and Configure page.

## Numbering

**Numbering** at the top of the card chooses how numbers are picked.

- **One 4th octet per batch** needs no cabling. Every ticked device gets the **4th octet** on its own type's third
  octet (`11.5`, `4.5`, `12.5`), and the table's Next address column shows it. Once a batch is addressed the octet moves
  on by one, so you connect the next batch, Scan and Apply. A device from an earlier batch that is still plugged in stays
  where it is; clearing the Addressed list lets it be addressed again. Two ticked devices on one third octet would
  share an address, so Apply refuses that unless **Ignore address clashes** is ticked.
- **Follow the cabling** uses the table and the LLDP read, as below.

- **The first two octets are chosen on the page** (the **Network** boxes, or the list of the usual ones: `192.168`, `172.16`, `10.0`, `2.0`; remembered in the browser and saved in the numbering file). Every address is `<those two>.<third>.<n>`; the examples below use 192.168. A device on a different first two octets does not count as holding a number, so changing them renumbers everything. The page warns if this computer has no address on the new network.
- **Devices keep their numbers.** A device already on `<first two>.<its type's third octet>.<n>` keeps `n`; new
  devices take the numbers after the highest in use, in cabling order. Three combos on `2.1` to `2.3` and a new one
  plugged in gives `2.4` + `3.4`, and the **Next address** column shows what the next new device would get. Tick
  **Renumber all in cabling order** to ignore current addresses and number the whole chain afresh (the ↑/↓ buttons
  then reorder it; otherwise they only reorder devices that have no number yet).
  You can also drag the ⠿ handle on a combo or device to move it. The **Detailed / Compact / Tiles** switch above the
  device list changes how much room each device takes (Compact is one line each, plus a line for any status or warning).
- **Unticked devices take no new number**, so unticking one closes up the numbering. One already on an address in
  its type's range holds that number, so nothing is planned on top of it.
- **Paired** types form combos: a switch and the node cabled to it share a last
  octet (switch `192.168.2.1` + node `192.168.3.1`, then `2.2` + `3.2`, and so
  on), numbered along the chain of switches from one end. Use the ↑/↓ buttons on
  a combo to reorder. A second node on the same switch, or a paired node whose
  switch is not paired, takes the next number after the combos and is flagged.
- **Sequence** types ignore pairing and count up 1, 2, 3 on their own third octet
  from their own start (for example GigaCore 12s on `7.1`, `7.2` and LumiNode 12s
  on `4.1`). Each device has ↑/↓ to change who gets which number.
- Paired types share one first number, taken from the first paired type in the
  table. Numbers are counted per third octet, so types can share one and count up
  together: two switch types on `11` give `11.1`, `11.2`, `11.3` between them, and a
  combo skips a number a Sequence device holds on either of its octets. The exception is a
  Paired switch type and node type on one octet: a combo would put both on the same address,
  which is shown under the table, and Apply refuses unless **Ignore address clashes** is ticked.
- **Batches.** Numbers carry on from the **Addressed** list, so a batch swapped in after the last
  one has been unplugged does not repeat its addresses. Clear that list to start again from Start.
  Dry-run batches do not count.
- One **subnet mask** (255.0.0.0, 255.255.0.0 or 255.255.255.0) applies to every
  device addressed. Under /24 different third octets are different subnets, and the
  tool warns about that.
- Devices already on their planned address *and* mask are not re-sent (a node would reboot). If the page closes
  part-way through an Apply, press Apply settings again and it carries on from where it stopped.
- Defaults (change them in the table; yours are remembered in the browser): GigaCore 10 Paired, GigaCore 12 and
  Other GigaCore Sequence, all on `11`; LumiNode 4 Paired on `4`, LumiNode 12 Sequence on `12`, LumiCore Sequence on
  `10`. Third octets saved before the numbering table existed are still read.
- **Save numbering** and **Load numbering** keep the table, name prefixes, name style and mask in a file
  (`luminex-numbering.json`), to keep one per rig or move them to another computer.
- Apply waits for the cabling to be read before sending paired addresses, and refuses a new number that rests on cabling
  it could not establish: a paired switch whose neighbours could not be read, or a paired node with no switch. The
  tooltip names the device; read the topology again, untick it, or set its type to Sequence. Devices keeping their number never block.
- **Devices sharing an address** are flagged in a red note. A request to that address reaches only the device this
  computer's ARP cache points at, so the tool uses each device's IPv6 link-local address instead, made from its MAC. It
  needs no admin rights, and each device goes straight to its planned address. **Fix duplicates** does only that; **Apply
  settings** does it as well as anything else ticked. A device that does not answer over IPv6, such as a GigaCore Gen1,
  is refused until it is connected on its own. A node's active profile name may clear when its address changes.
- A device on the same address as this computer cannot be reached. A red note names it, including when only a neighbour
  reports it and it is missing from the list: change this computer's address, then Scan.
- An address change only counts as done once the device reports the new mask too, so a mask-only change is confirmed
  properly. Factory reset and Configure writes are blocked while an Apply is still sending.

Ticked options are sent after the addresses and read back to confirm:

| Setting | Devices | Notes |
|---|---|---|
| Device name | GigaCore, LumiNode, LumiCore | off by default. Either the type's name prefix + the last two octets of the address (`Switch 2.1`, `12p Node 4.1`), or the model + last octet (`LumiNode 12 - 3`). Gen2 names cannot hold a dot, so they use a dash: `Switch 11-1`. Length limits: Gen1 16, LumiNode 17, Gen2 64, so longer model names need prefix names on Gen1. |
| LED brightness | GigaCore | 0 to 4 |
| Web password off | LumiNode, LumiCore, GigaCore Gen2 | Anyone on the network can then change or reset the device, so the tick clears after each Apply. GigaCore Gen1 has no such setting: it joins the run and shows Skipped. Only off, not on. A device whose password this tool does not know is reported, not changed. Not yet run on a real device; Gen2 is from the spec |
| Identify | GigaCore, LumiNode, LumiCore | flashes the device's LEDs (and display); per-row button. LumiNode: `POST /api/identify {}` |

The Gen1 calls were read off the switch's own web UI; the name write was confirmed on
five real GigaCores on 5 Nov 2026. The Gen2 calls come from the 1.5.0 spec and are
marked unverified. **Devices that share an IP address cannot be told apart** (requests
go to the address), so connect them one at a time.

## Device calls

| Device | Set IP | Source |
|---|---|---|
| LumiNode / LumiCore | `PUT /api/ipsettings` `{"ipaddress","netmask","gateway"}` → 200/202, device reboots | iptop2 (used on real devices, June 2026) |
| GigaCore Gen2 | `PUT /api/ip_settings` `{"mode":"static","ip_address","prefix_length","default_gateway"}` (partial PUT; other fields kept) | GigaCore WebApi 1.5.0 `/api/doc` of a real GigaCore 16tf v1.9.2. The LumiNode path returns 404 on a switch. |
| GigaCore Gen1 | `GET /config/ip2_ll_ip` (preflight, as the web UI does), then `POST /config/ip2_config` form: `if_ct=1 if_vid_0=1 if_addr_0=<ip> if_mask_0=<cidr> if_dhcp_0=0 rt_ct=2 rt_del_0=1 rt_net_0=0.0.0.0 rt_mask_0=0 rt_dest_0=0.0.0.0 rt_net_1=0.0.0.0 rt_mask_1=0 rt_dest_1=<gw>`, Basic `admin:` | iptop2 (taken from the Gen1 web UI) |

| Device | Set name (a settings run, `POST /api/settings {action:"name"}`, after the addresses are confirmed) | Source |
|---|---|---|
| LumiNode / LumiCore | `GET /api/deviceinfo` for `ID` and `colors`, then `PUT /api/deviceinfo {"ID","short_name" (max 17),"long_name" (max 63),"colors"}` | WebAPI v2.7.2 / v2.9.1; same call as LumiNode Lite |
| GigaCore Gen2 | `PUT /api/device/name "<name>"` (JSON string; `[a-zA-Z0-9 _-]`, max 64) | GigaCore WebApi 1.5.0 (used on a real 16tf) |
| GigaCore Gen1 | `GET /config/switchlegend`, then `POST /config/switchlegend` `sw_name` (max 16) `sw_id` `sw_descr`, then `POST /config/icfg_conf_save save=1` | read off the Gen1 web UI; set five real switches on 5 Nov 2026 |

`POST /api/client-log {message, agent}` writes a page's script error into the tool's log as `page error from
<address>: …` (2.1.4): a page on an iPad has no console to read. The page also shows it under the device list.

`POST /api/apply` takes `{items:[{id,newIp}], netmask, gateway}` and only
addresses; it refuses a new address no device can take (0.x, loopback,
multicast, broadcast, or one of this computer's own). The page sends the
ticked settings as separate runs once the addresses are confirmed.

Factory reset calls are the same as the Luminex Reset Tool (see
the reset calls above); the Gen2 reset was confirmed on a real
GigaCore 16tf on 4 Nov 2026.

## Real hardware

Checked on 4 Nov 2026 on a GigaCore 16tf (v1.9.2) and a LumiNode 12 (v2.9.1):
naming and addressing both in one job, `.8` to `.9` and back. mDNS confirmed each
device at its new address within about 5 s, and the device list followed. The
switch's `ip_settings` and the node's `deviceinfo` and `ipsettings` then matched
what was recorded before the test, field by field.

On 5 Nov 2026, on five GigaCores (10 and 12 port, Gen1 v3.1.5) and four LumiNodes
(4 and 12, v2.8.1): the cabling topology was read from every device (16 links, none
unread), and the GigaCore Gen1 name write (`POST /config/switchlegend` then save)
set all five names, with their description and ID unchanged, confirmed by mDNS.
Address numbering and the LED and identify writes have only been run in dry-run.

Still to check: the Gen1 IP-change call by this build (the Node
iptop2 used it in June 2026), Gen1 LED / identify writes, GigaCore Gen2
settings from the spec, a LumiNode factory
reset, and Windows discovery.
