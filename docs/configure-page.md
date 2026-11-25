# The Configure page: device calls, process blocks and RDM

Notes on the per-node Configure page of Luminex Commission (`internal/nodeconfig`), including what real LumiNodes were seen to do.

Since 1.9.0 it is not a page of its own: the device list's page loads `node.html`, `nodeconfig.css` and the two scripts from `/configure/` into its Node view (`internal/commission/public/node.js`) and calls `ncOpen(ip, id)` to show a node. So every rule in `nodeconfig.css` starts with `.nc`, and the scripts share one global scope with the device list's (`nc$` and `loadNode` are named that way because `$` and `load` are the device list's).

Some entries name this tool's predecessors: the separate Reset tool, IP Setup and LumiNode Lite (and the older Node tools iptop2 and v28). Their code is now `internal/commission` and `internal/nodeconfig`, so what they did on real devices holds for Commission's device list and Configure page.

## Device calls

All on port 80. Reads: `/api/deviceinfo`, `/api/processblock`, `/api/IO`,
`/api/dmx/ports`, `/api/ipsettings`, `/api/profile`, `/api/system/uptime`,
`/api/dmx/config`, `/api/play/playlist/{pb}[/snapshot/{id}]`. The live
poller (every 3 s while a page is connected) reads `/api/processblock`,
`/api/IO` and `/api/dmx/ports`; every write is followed by a re-read of the
blocks and IOs so the response carries both, resolved through each other. When
the node stops answering (a reboot, a recall, a pulled cable) the poller keeps
trying for as long as a page is connected: the page says so after three misses
and fills in again by itself when the node is back.
Writes: `PUT|DELETE /api/processblock/{pb}`, `PUT|DELETE
/api/processblock/{pb}/input/{slot}`, `PUT /api/processblock/{pb}/output`,
`DELETE /api/processblock/{pb}/output/{io}`, `POST|PUT|DELETE /api/IO[/{id}]`,
`POST /api/profile/{id}/recall`, `POST /api/identify`, `POST /api/reboot`,
`PUT /api/ipsettings`, `PUT /api/deviceinfo`, `PUT /api/dmx/config`.

RDM (the RDM tab, `../internal/nodeconfig/rdm.go`): `GET /api/IO`,
`GET /api/IO/{id}/artrdm_candidates` (what feeds each DMX port),
`GET /api/rdm/{port}/connected_devices`, `GET /api/rdm/uid?port=n` (what the node has
overheard, used when Art-Net is not available), and the writes `PUT /api/IO/{id}` (the whole
DMX IO back with `rdm` changed) and `POST /api/dmx/ports/{port}/force_discovery`. The
fixtures themselves are read over Art-Net RDM on UDP 6454 (`../internal/artnet`):
`ArtTodRequest` to the port's `rdm_universe`, then RDM GETs of DEVICE_INFO, the manufacturer,
model, label and software labels, the current personality's description and IDENTIFY_DEVICE.
Writes to a fixture are RDM SETs of DMX_PERSONALITY, DMX_START_ADDRESS, DEVICE_LABEL and
IDENTIFY_DEVICE, each read back. Page API: `GET /api/node/{ip}/rdm`, `PUT /api/node/{ip}/rdm/{port}`
`{"on": bool}`, `POST /api/node/{ip}/rdm/{port}/discovery`, `GET /api/node/{ip}/rdm/{port}/fixtures`,
`POST /api/node/{ip}/rdm/{port}/fixture/{uid}` `{"personality"?, "start"?, "label"?, "identify"?}` and
`GET /api/node/{ip}/rdm/{port}/fixture/{uid}/personalities`, and for the fixture page
`GET …/fixture/{uid}/details`, `POST …/fixture/{uid}/param` `{"pid", "value"}`, `POST …/fixture/{uid}/factory-reset`
(optional `{"keepAddress": true}`) and `POST …/fixture/{uid}/restart` (ports 1-based).

Quick Setup order matters: detach a block's IOs, `DELETE` the block, `PUT`
the new block, then assign input slot 0 and the DMX output. The device
tracks IO usage globally and refuses to reuse an IO that is still attached.

## Process and IOs pages

Both pages show the node the way an operator talks about it, not by IO id.
Every block and IO is resolved through the other list on the server
(`normalise.go`), so the page never has to look ids up.

**Process.** One card per configured block, headed by its colour, name, id
(`PB 0`, as the API numbers it) and mode badge. A REROUTE block (what Quick
Setup makes) is one line: `sACN U1 (prio 100) → DMX Port 1`, the raw ids in
small type after each IO. A merge block (HTP, LTP, BACKUP, XFADE, SWITCH)
lists all four input slots, empty ones included, with the active slot
(`summarized_active_input`) marked and the senders the node currently sees
on each slot (`sources`) underneath, then its outputs. Control inputs
(`backup_ctrl`, `switch_ctrl`, `xfade_ctrl`, `masterlimit_ctrl`) appear as
rows when set; BACKUP blocks show timeout and auto-recover; chips flag a
stored startup buffer (with its channel count), a master limit, rate
limiting off and an active takeover. An IO the block points at but the node
no longer lists shows in red as "not in IO list". Blocks in mode NONE with
nothing attached collapse to one row each under the cards, so a 16-block
node after Quick Setup shows twelve cards and four rows. The buttons (SET
INPUT, EDIT, CLEAR) are on every card and row.

**IOs.** Grouped by class (sACN, Art-Net, DMX ports, other, internal) with a
class filter, an in-use / unused filter and a count line. Each card is
titled by the IO's name when it has one (`Desk A main`, with `sACN U101`
beside it) or by what it is, with class and type badges and the raw id. sACN
and Art-Net cards show universe, priority (sACN, with per-channel when on),
group, source IP (`any` for 0.0.0.0) and whether the node's own IP is
accepted as a source; DMX cards show the port, RDM (with its universe),
interweaving, backup/follow when on and the activity timeout; every card
ends with USED BY (`PB 9 FOH backup · input slot 1`, `· output`, `· backup
ctrl`) or `unused`, and a DUPLICATE OF chip when the node reports another IO
with the same configuration. The internal IOs (one per block, no settings)
are hidden behind SHOW INTERNAL.

Both pages redraw from the live poll, so an active-input change on a BACKUP
block shows within 3 s.

## RDM

**Shared between devices.** The app, not the page, holds each node's RDM state: the ports, every
port's fixtures as last read, every change made since, and which ports a read is running on
(`internal/nodeconfig/rdm_state.go`). Every page with the node open gets it over `/api/live` as
`{type:"rdm", ip, rdm:{rev, ports, read, busy}}` whenever it changes, and `GET /api/node/{ip}/rdm` carries it
as `state`. So fixtures read on the laptop are already on the iPad when it opens the tab, an address,
label, personality or ID change on one shows on the other at once, and the other shows "Reading…" while a
read runs. What each page is doing stays its own: the keypad, ticked tiles, the fixture page, Tiles/Table
and Follow ID. The state lives until the app quits; turning RDM off on a port drops its fixtures.

The RDM tab lists the node's DMX ports with what feeds each one and whether RDM is on.
**Turn RDM on…** opens a warning in the page (discovery starts on that DMX line; DMX keeps
flowing, but some older fixtures flicker during discovery) and only its second button sends
the change. The page then shows "Searching… n found" (the node's own count, every 1.5 s) until the
number stops changing, and reads the port; a line of 18 fixtures took the node several seconds.
**Rediscover** asks the node to search the port again. The node keeps its old list until that search
is done and then swaps in the new one, so a steady count says nothing there: the page waits for the
list itself to change and settle, showing "Searching… n so far, up to 25 s", and reads after 25 s if it
never changes. Opening the tab reads, one port after
another, every port that already has RDM on and fixtures the node has found, so they show without
pressing Read. A Read also asks for every
fixture the node lists over HTTP, not only those in its Art-Net table. **Read** asks every fixture on the port over
Art-Net RDM and shows a table sorted by address: address range, channels, personality,
manufacturer, model, label, software and sensor count, with the UID in each row's tooltip.
Fixtures that share channels, or run past 512, are red with a note. **Export CSV** saves every fixture read, UID included.

Each fixture row has **ID** (identify on or off; the button stays orange while it is on, and **ID OFF (n)**
on the port turns them all off) and **Edit**, which opens a row under the fixture: DMX address, label
(32 plain ASCII characters) and personality (the list is read from the fixture when the row opens).
While you type, it shows the channels the fixture would take and warns if they would share channels
with another fixture on the port or run past 512; it still lets you save. Save sends personality first
(it can change the channel count), then address, label, and reads the fixture back; if the fixture
refuses one, the rest are not sent and the message says which already went. There is no pop-up:
Save and ID act at once. Under DRY_RUN the SETs are logged and not sent.

**Tiles, the fixture page and selection.** Each port shows its fixtures as tiles (address range,
model, personality, label; red for a clash, ◉ ID while identifying) or, with **TABLE**, as the
table above; the choice is remembered. **One tap on a tile identifies that fixture** (amber border; a
tap on it again stops it, a tap on another moves the identify there, Follow ID or not), and if the
fixture page is open it moves to that fixture too. **A double tap opens the fixture page** in a side
panel (a double tap on its tile again closes it); double taps are timed by the page (400 ms), since a
touch screen does not always send dblclick. The fixture page shows:
patch (with ID and EDIT, which edits address, label and personality in place), hours, whether it is
at factory defaults, then **every setting the fixture says it supports**: the page reads
SUPPORTED_PARAMETERS and, for the maker's own parameters, PARAMETER_DESCRIPTION (name, type,
range, default), so any fixture's settings appear without code per model. On/off settings are a
button that acts at once; numbers, choices and text have a field and SET; a SET-only parameter
(an action, such as the Vortex's "Restore DMX Preset") has SEND…, which asks first in the panel, since
nothing says what it does and it cannot be read back; such parameters are also left out of the batch
SETTING… action. A read-only "X Name" text is
shown next to the number X it names. Standard settings offered when supported: dimmer curve
(E1.37-1, names from CURVE_DESCRIPTION), display invert and level, pan/tilt invert and swap.
Then sensors (red outside their normal range), and RESTART… and FACTORY RESET…, each behind a
warning in the panel. Every change is read back.

**Factory reset, keeping the address** (Nick, 17 Nov 2026: change many settings and the mode, not the
patch). The reset's warning has a **KEEP ADDRESS** switch, on by default, in the fixture page and the
batch bar. With it on the app reads the fixture's DMX_START_ADDRESS first (if it cannot, nothing is sent),
sends FACTORY_DEFAULTS, waits up to 30 s for the fixture to answer, then sets the address back and reads
it. The personality stays as the reset left it, so the footprint can change: the tiles show any overlap,
and READDRESS… (below) respaces them. If the reset goes but the address does not, the error names the
address it should have gone back to. Under `DRY_RUN=1` neither SET is sent and both are logged. Used on
real fixtures by Nick with 1.7.0 on 17 Nov 2026: the address was kept.

**Setting addresses (made for the iPad: no keyboard needed).** Tap a tile's address (dashed underline):
a keypad opens at the bottom of the screen with the fixture, the address being entered in large type, a
live clash warning and big keys. The suggested address shows faded and the first digit replaces it.
**SEND & NEXT** sends it, reads it back and moves to the next tile (in the order they were in when you
started); **SEND & DONE** sends it, reads it back and closes the keypad; while the keypad is open, tapping another tile moves the keypad to that fixture (it does not open its fixture page); **SKIP ▶**, **◀ BACK** and **CLOSE** move or close without
sending. **Next = this + − / + / CH** sets the
step for the suggestions: CH (the default) follows on after each fixture's channels, a number (say 20)
starts every fixture that many after the last; it is remembered. Under it the keypad shows what the
step makes of it: the block each fixture takes ("Each takes 20 ch: 10 used, 10 spare", or an overlap
warning when the step is smaller than the fixture) and where the next fixture would land ("Next: 121–136"). There is no text field, so the iPad's
keyboard never opens; a real keyboard still works (digits, ⌫, Enter, Tab to skip, Esc).

**Select** (the row above the tiles): ALL, NONE, and one button per fixture type on the port
("Vortex 8S ×2"): a type button adds every fixture of that type to the selection, or takes them out.
On touch screens the tick boxes and the RDM tab's buttons are larger.

**Follow ID** (a switch on each port): while on, the fixture whose address is being typed (instead of the
tapped one; never both) and every ticked one identify; a fixture it turned on stops when it is none of those, and
turning it off stops them all. An ID turned on by hand is left alone. An identify change reads back only
IDENTIFY_DEVICE (two RDM requests), so it keeps up with clicking from tile to tile. A fixture it turned on
always gets an explicit off: the identify the page shows can be stale (an address read-back that crossed
with the ID going on reported it off, which used to leave fixtures identifying), so it is not trusted. A
failed off is tried three times. Closing the page or switching node turns its IDs off too. **ID OFF**
(beside it) sends identify off to every fixture on the port, whatever turned it on, and stops Follow ID.

**After an update.** A page loads its code once, so one left open while the app was restarted as a newer
release kept running the old code (on 14 Nov 2026 a page did Rediscover the 1.6.7 way under 1.6.8).
The live link now reconnects every 2 s after the app goes away, and each time it does the page asks
`GET /api/app-version`; if the version changed it reloads itself on the same node and tab. The device
list page does the same from its event stream.

The tab only redraws what changed, and the keypad is drawn on its own: on an iPad a tap that landed while
Follow ID redrew the page used to be lost, which made digits or BACK seem to do nothing.

Tick tiles (or table rows) on one port to act on them together: ID ON / ID OFF, PERSONALITY…
and SETTING… (offered only when every selected fixture is the same model; the choices are read
from the first one), READDRESS…, and FACTORY RESET… (warning first). They run one fixture after another, and
each tile shows how it went.

**READDRESS…** puts the selected fixtures one after another in the order they are patched now (by
address): a mode for all of them (same model only; mixed models each keep their own), **from** a start
address (the lowest selected one to begin with) and **every** N channels, or, left empty, each one's
footprint with no gaps. A line underneath shows every move (`1→1 · 11→17 · …`) and where it ends, and
warns about fixtures that are not selected whose channels the new patch would share. The button is
disabled (with the reason) when the spacing is smaller than the mode's footprint or the last fixture
would run past 512. Each fixture gets the personality and the address in one request (personality first),
and one already in place is left alone. Fixtures without an address are skipped. Tested against the fakes only so far.

Used live on 12 Nov 2026 on the Creamsource Vortex 8S on LumiNode 12 - 8 port 1 (Nick's OK): identify
on and off, the label changed and put back, the address moved 1 → 101 → 1, and the personality 1 → 2
(5 ch) → 1 (10 ch), each ACKed and read back. Save is disabled while the address is
outside 1-512, and the server refuses it too. Opening Edit on that fixture read its 75 personality
names, one GET each. Its full parameter list (16 of its own, including CRMX Enabled, CRMX Link State,
SACN Enabled and SACN Universe) is `../docs/samples/vortex8s-rdm-parameters.txt`; `cmd/fakenode`'s
fixtures answer with the same. On the real Vortex (12 Nov 2026, Nick's OK) the fixture page read
everything above, toggled Display Always On off and on, and ran a factory reset, which applied at once
and left its settings as they were: they were already its factory values, which are not the defaults
its parameter descriptions give (those say 0 for CRMX and sACN, which stay on). The fixture's own
"at factory defaults" flag read No even right after the reset, so the page does not show it. Restart
and the batches have run against the fakes only.

If the app cannot open UDP 6454 (another Art-Net program has it) the tab says so and shows
what the node has overheard instead (`/api/rdm/uid`), which is only complete for fixtures some
controller has asked about.

Checked on a real LumiNode 12 (v2.9.1) on 12 Nov 2026 with a Creamsource Vortex 8S on port 1,
fed by sACN U1 from a grandMA2, by a probe script making the same calls: `PUT /api/IO/100000`
with `rdm:true` set `rdm_universe` to 1 by itself, the node listed the fixture in about 2 s,
and ArtTodRequest and RDM GETs to Art-Net 0:0:1 answered with the port still on sACN. Turning
`rdm` off left `rdm_universe` at 1; afterwards every port on that node read `rdm_universe` 1-12.
Then, live through the Commission build with Nick's OK: the tab turned RDM on for that port, read
the fixture (address 1, 10 channels, personality "01: CCT-RGBW 8bit", 1 of 75, label, software 3.1.0,
5 sensors) and turned RDM off again, with DMX streaming throughout; Commission's Fixtures view read all
eight nodes of the rig and found it. Those two `PUT`s were the only writes.

## Inputs and outputs of a block

**INPUTS / OUTPUTS** on a block opens one dialog for all of it. Each input slot is
empty, sACN or Art-Net on a universe, and only as many slots show as the block's mode
takes. Outputs can be any mix of sACN, Art-Net and DMX ports; new network outputs are
sACN multicast or Art-Net broadcast (no unicast destination yet). A DMX port another
block drives is refused by name. Changing a slot to the universe it already has does
nothing, so that IO keeps its name and priority.

**Controls.** The same dialog sets the control for the block's mode (XFADE, SWITCH,
BACKUP) and the master limit, once MASTER or LIMIT is chosen in EDIT. A control on any
other role stays listed so it can be taken off. Not there yet: which channel a control
reads, priority on a new IO, and a DMX port as an input.

On a real LumiNode 12 (v2.9.1):

- A control's IO must be created with that control's `io_type` (`x-fade-ctrl`,
  `switch-ctrl`, `backup-ctrl`, `masterlimit-ctrl`). A plain input gets 500.
- **Creating a control IO attaches it to every block with that control unset,** before
  any assign call. So the tool reuses a control IO on that universe when there is one,
  which several blocks can share, and otherwise creates one and immediately takes it
  back off the blocks it spread to. For that moment, those blocks do have the control.
- Controls stay on a block through mode changes, the node accepts any control in any
  mode, an IO of one control type cannot be another, and deleting an IO clears it from
  every block.
- Each control also has a `*_ctrl_patch` list (`PUT .../<ctrl>/patch`), empty by default.
  The node stores what it is given, but what it does has not been tested with real signal.

## How a real node treats a process block write

Tested on a LumiNode 12 (v2.9.1) on 4 Nov 2026. `cmd/fakenode` does the same, so a
wrong call fails on the fake too.

| Write | What it does |
|---|---|
| `PATCH` | Replaces the block. Refused with 422 without `inputs` and `outputs`. |
| `PUT` with a mode | Every field left out returns to its default (a mode alone blanks the name). Inputs and outputs stay, including through a mode change. |
| `PUT` without a mode | Changed nothing on a NONE block. Not tried on a block in use. |
| `PUT` with mode NONE | Clears the block: inputs and outputs go, the name resets, and the IOs attached to it are deleted. |
| `PUT /input/{slot}` | Answers success for any of slots 0-3 in any mode, then drops what the mode does not take. Slot 4 and up get 500. |
| `DELETE` of an input or output | Comes off the block; the IO stays on the node, unused. |

Inputs each mode keeps: **REROUTE 1, BACKUP 2, XFADE 2, SWITCH 4, HTP 4, LTP 4, CUSTOM 4**.
Changing to a mode with fewer drops the rest for good. Outputs are kept through mode
changes, and a block can have several.

So the EDIT dialog's Save reads the block and sends all of its config back with
the form's fields on top. A `merge_map` (CUSTOM mode) is not in what `GET` returns,
so editing a CUSTOM block may lose it; not tested. Choosing NONE on a block with
IOs shows a warning in the dialog.

## What a real node sends (LumiNode 12, v2.9.1, after Quick Setup)

Raw `GET /api/processblock`, `/api/IO` and `/api/deviceinfo` from a real
LumiNode 12 (serial LUM553897152, named by IP setup as `LumiNode12-25`) are in
`docs/samples/luminode12-v2.9.1-*.json`, captured 4 Nov 2026. What they settle:

- A process block **does** come back with `mode` (`REROUTE` / `NONE` here; the
  OpenAPI spec's `ProcessBlockSerialize` omits it, the device does not), plus
  `summarized_active_input`, `sources.inputs[4]`, `status`, `takeover_active`,
  `colors`, `startup_buffer`, the `*_ctrl` ids (-1 = none) and `patches`
  (`{"0": [], ...}` per input slot).
- IO ids are namespaced by class: sACN/Art-Net IOs are small ints (0-11 here),
  **DMX port IOs are `100000 + port`** (`"Port 1"`, `port_number` 0, io_type
  `output`), and the 16 `internal` IOs are `101000 + pb` with a `pb_number`.
- After Quick Setup, block *n* is `REROUTE` with `inputs {"0": n}` (sACN
  universe *n*+1, priority 100, `take_own_ip` true) and `outputs
  {"100000+n": 100000+n}` (DMX port *n*+1). So the cards' "INPUTS 0 /
  OUTPUTS 100000" is correct and useless: the node knows them as "sACN U1" and
  "DMX Port 1", and that is what an operator needs to see.
- sACN IOs carry `group`, `per_channel_priority`, `source_ip` (`0.0.0.0` =
  any), `take_own_ip`, `duplicate_ios`; DMX IOs carry `rdm`, `rdm_universe`
  (-1 = off), `backup`, `follow`, `interweaving`, `stream_activity_time`. All
  of these are on the IO cards now.
- `rdm_universe` is not a setting you can rely on keeping. The bench node's DMX IOs
  read -1 before a test on 4 Nov 2026 and 1-12 after it, with `rdm` still false on
  all of them. `PUT /api/IO/100000` with -1 got a success response and still read
  back 1. What moved it is not known: that session changed the node's name and IP
  and sent `PATCH`es to block 15.
- `summarized_active_input` is the active slot index and is only non-null in
  BACKUP and SWITCH mode; `sources.inputs[slot]` is null, one sender
  `{ip, name, priority, group}` or a list of them; `status.output` is the
  literal `"$subscription-location"` on a plain GET (the real status comes
  over the node's subscription channel, which this app does not use).
- The tests in `normalise_test.go` run against these files.
