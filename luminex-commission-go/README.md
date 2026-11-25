# Luminex Commission

One tool for Luminex gear: change a node's DMX, IOs and process blocks on a system
that is already addressed, or commission a new one by scanning, addressing, naming
and factory resetting devices.

The device list (scan, numbering, addressing, naming, reset) is `../internal/commission`
and the per-node Configure page is `../internal/nodeconfig`. Their device calls and what
real devices were seen to do are in `../docs/device-list.md`, `../docs/configure-page.md`
and `../docs/device-facts.md`.

## Views

Four views across the top: **Devices**, **Node**, **Fixtures** and **sACN**. Devices has three
parts (All devices, Address & reset, Groups) and sACN two (View, Control); each is described
below under the name it had as a view of its own, and the page remembers which part each was on.

The page opens on Devices, **All devices** (called **Configure** below), for changing what a node does on a system that is
already addressed: DMX, IOs, process blocks. It lists the devices with a Configure
button on each node. It has no ticks, no Apply and no Factory reset, so nothing in it
can re-address the system. A device Scan is not finding can be added by address here.

**Address & reset** is the commissioning page: the numbering table (each device type
Paired or Sequence, with its third octet, start and name prefix), what to apply
(addresses, device names, LED, web password off), ticks, Apply settings and Factory reset. A page reload stays on the view you were on; a new tab opens on All devices.

**Node** is one node's configure page, with a chip for every LumiNode in the device list along the top (they wrap onto more rows on a big rig). Click a chip to switch node: the tab you were on (RDM, say) stays. A node has five tabs: **Device** (what it is, Identify, Reboot, name and address; it opens here), **Quick Setup**, **Profiles**, **RDM** and **Advanced** (process blocks, IOs, DMX port state and startup buffers). On the RDM tab each port has an **RDM** tick box: ticked is on; ticking asks first, as discovery can make older fixtures flicker, and unticking turns it off at once. A chip's dot is green when the node can be opened, amber while a job has it and red when it cannot be opened, with the reason in its tooltip. The open node is kept in the page's address, so a reload or a bookmark comes back to it.

**Groups** reads each GigaCore Gen1's groups (Luminex's name for VLANs): name, VLAN ID, and the ports in each,
with the colour the switch gives each group (`POST /api/groups/read` sends GET requests to `/config/groups` and
`/config/ports`). **Name groups** sets names for groups 2 and up on the ticked switches (1 to 16 letters, numbers,
spaces, `- _ .`): it reads the groups, sends each renamed group back with its ports, tagged ports, VLAN ID and IGMP
settings unchanged, saves, then reads back and fails if a name did not take or anything else about the group changed.
Gen1 only.

**Profiles:** a set of groups plus a port layout for each switch size, remembered by the app. They are kept by the
tool in one file, `group-profiles.json`, in the user's config folder (`~/Library/Application Support/luminex-commission/`
on a Mac, `%AppData%` on Windows, `~/.config` on Linux; set `LUMINEX_PROFILES_FILE` to use another path), so they are
there the next time it starts. A profile's group names and VLAN IDs are the same on every switch; the ports are set per
size (10, 12, 16, 26 ports...) on a tab each, so one profile can hold a GigaCore 10 and a GigaCore 12 layout. A switch
is matched by the number of ports it reports; one with no layout in the profile is skipped and says why. *Add size* can
start a new layout empty or as a copy of another (ports beyond the new size are dropped). Profiles saved by the
earlier single-size version are read as a profile with one layout. *New profile* opens a grid of groups against ports: click a
cell to make that port untagged (U) or tagged (T) in that group; a port left in no group is in Management (click a
cell in the Management row to put a port back there). The **ISL** row (there by default, like groups 2 and 3) sets which ports
are links to other switches. Until you click one of its ports it leaves each switch's own ISL ports alone (and the
profile then refuses to put one of them in a group); once set, the ports shown are exactly the ISL ports, and set with none
ticked it turns every ISL port back into Management (*leave as they are* undoes that). Changing ISL ports is previewed with a dashed
outline and a warning, since it can cut switches off from each other. *Save as profile* on a switch's card starts one from what
that switch has now. *Export* and *Import file…* move a profile between computers. *Use* picks the profile; for each ticked
switch the page then draws its ports now and the ports after, side by side with an arrow, outlining the ports that change.
Apply sends each switch the layout for its own size (`action: groupprofile`, only the groups that differ), then reads every
group back and fails if one did not end up as the profile says. It refuses a port that is an ISL link on that switch and a
group number the switch does not have. (`GET /api/groups/profiles`, `POST /api/groups/profiles/save` and `/delete`.)

**Fixtures** lists the RDM fixtures on every LumiNode in the device list, from the ports with RDM
on: per node and port, a table by DMX address (channels, personality, manufacturer, model, label,
software, sensors), with fixtures that share channels or run past 512 in red. **Read all nodes**
asks each fixture over Art-Net RDM and changes nothing (addresses, labels, personalities, identify,
READDRESS… and factory reset keeping the address are on the node's RDM tab); it skips a node in a job, one sharing an
address and one this computer cannot reach. **Only fixtures with a problem** filters to the clashes,
and **Export CSV** saves everything read, UIDs included. RDM is turned on per port on the node's
Configure page, RDM tab (see `../docs/configure-page.md`, "RDM"). Used on a real rig of eight LumiNode 12s on
12 Nov 2026.

**sACN, View** shows what is arriving on each sACN universe: a chip per universe, wrapping onto more rows, with a dot for its state (green one source, grey nothing
arriving, amber two sources at the same priority, red could not listen), and for the one picked its source
and frame rate and all 512 levels (0-255, or **Levels in %**), updated eight
times a second. It only listens. Nothing in this view changes a level or a device; the one thing it
sends is the join for each universe's multicast group (an IGMP report), which is how a switch knows
to pass that universe to this computer. It starts listening (UDP 5568, shared with other sACN
software on the computer) the first time the view is opened.

- **Universes to listen to** takes a list such as `1-8, 33` (up to 128 at once) and is remembered
  between runs (`sacn.json` in the user config folder). Universes a source announces (E1.31
  universe discovery, which not every console sends) are added by themselves, and so is anything
  sent straight to this computer.
- A universe's sources are listed by priority, each with its address, priority and rate.
  **On the wire** is what a node would output: the highest priority wins, and where sources share
  it each channel takes the highest level. Two sources at the same priority turn the tile amber,
  since that is usually a mistake. Clicking a source shows that source alone; its tooltip has its
  slot count, lost and late packets and CID.
- A source that stops stays listed as lost for 30 seconds. Preview data is shown but never counts
  as on the wire. Per-address priority (start code 0xDD) is noted, not applied.
- On Linux a socket can join 20 groups unless `net.ipv4.igmp_max_memberships` is raised; a
  universe that could not be joined says so on its tile.

`go run ./cmd/fakesacn` is a pretend console for trying it. Its packets go to 127.0.0.1 unless
it is given `--multicast` or `--to`, which put levels on a real network. Checked against it only
(25 Nov 2026): not yet tried with a real console or through a real switch.

**sACN, Control** makes this tool a small sACN source, for testing a rig without a console.
**Nothing is sent until Start output, and it never starts by itself**: every run begins with the
output off and every level at zero. While it is sending, any node listening to those universes
puts the levels on its DMX outputs, so treat Start like bringing up a fader on a desk.

- **Universes**: a chip for each universe in the output (one with a fixture patched, a level set, or added
  here). Its **tick box** is whether it is sent: untick one and it is left out of the output, with its patch
  and levels kept, and receivers are told it stopped if the output is on. **Select all** and **Deselect all**
  tick or untick them all. **Add universe** adds one with nothing on it, ticked: it is sent with every channel
  at zero. Clicking a chip's name shows only that universe's fixtures (**All** shows every one) and points the
  Channels tab and the patch form at it. What is ticked is kept in `sacn-send.json` beside the patch.
- The bar: the network connection to send from (chosen by the operator, never guessed; it is
  remembered), the priority (0-200, default 100) and the source name, all fixed while sending.
  **Stop output** tells receivers the source has stopped, so nodes go back to their other sources
  at once; levels are kept. **Clear all levels** puts every fixture at its profile's defaults and
  everything else at zero. If another source is heard on a universe this tool sends, a banner names
  it and says who wins: the higher priority, or the higher level per channel when they are equal.
- **Fixtures**: add fixtures to the patch (name, profile, universe, address, how many in a row;
  names ending in a number count up). Each gets a tile with its dimmer (or all its channels when it
  has no dimmer), Off, Full, Highlight and Remove, and its other channels under More. **Remove
  selected**, beside Select all and Select none, takes every selected fixture off the patch at
  once (Select all, then Remove selected, empties it); there is no confirmation. **Starting
  from M select every N** then **Select** replaces the selection with the Mth fixture along the
  patch (in address order) and every Nth after it: from 1 every 2 is the odd ones, from 2 every 2
  the even ones.
  Two fixtures cannot share a channel.
- Click a fixture's name to select it. With two or more selected, the one at the lowest address
  (lowest universe first) is the **master**: its faders, Off, Full and Highlight move every
  selected fixture, each channel going to the channel of the same type on the others (so a Dimmer
  + RGBW master moves a plain Dimmer's dimmer and nothing else). The others still move on their
  own from their own tiles. The selection belongs to the page, not the tool, so a laptop and an
  iPad can each have their own.
- **Highlight** fades a fixture to its profile's highlight levels over half a second, to find it
  on the rig; clicking again fades it back to the levels it had before. The levels belong to the
  profile, one per channel: by default the dimmer at 50% (128), red, green and blue at full and
  cyan, magenta and yellow at zero, with other channels left where they are (a profile with no red, green or blue brings up its
  white, warm and cool instead). Several fixtures can be highlighted at once. A fader moved on a
  highlighted channel goes back to its earlier level when the highlight ends, and Clear all
  levels ends every highlight.
- **Channels**: all 512 channels of any universe. Click to select (Shift-click for a range), then
  the fader or 0 / 50% / Full. Setting a level on a universe with no fixtures adds it to the output;
  **Take this universe out** removes it from the output again (also how an added, empty universe is removed).
- **Profiles**: a profile is a name and its channels in DMX order, each with a type (dimmer, red,
  green, blue, white, amber, lime, uv, warm, cool, cyan, magenta, yellow, cct, pan, tilt, zoom,
  strobe, other), a
  default level and a highlight level (empty leaves the channel alone when highlighting). Nine plain ones are built in. **New profile** or **Copy** opens the editor;
  **Export** saves one as a `.sacn-profile.json` file and **Import a profile file…** loads one, for
  sharing between computers. On the Fixtures view, **Patch** on an RDM fixture adds it at its own
  universe, address and channel count, with a profile named after its model and personality. The
  fixture is asked over RDM what each channel is (SLOT_INFO for the type, SLOT_DESCRIPTION for the
  name, both reads; `GET /api/node/<ip>/rdm/<port>/fixture/<uid>/slots`), so the profile gets
  typed channels and the standard highlight: dimmer 50%, red, green and blue 100%, cyan, magenta
  and yellow 0. A fixture that answers neither gets plain numbered channels and no highlight until
  they are set in the editor. A profile already saved under that name is used as it is.
- **16-bit channels**: a channel ticked **Fine** in the editor is the low half of a 16-bit pair
  with the channel before it (pan and pan fine, dimmer and dimmer fine, any type). The pair is one
  fader on the tile, shown in percent, driving both DMX channels; Off and Full set both halves, and
  a fine channel's highlight follows its coarse one (full with it, otherwise zero, so 50% is 128
  and 0). A fine channel has to come straight after its coarse channel and takes its type. With a
  master, a 16-bit fader moves a 16-bit follower's pair and the coarse half of an 8-bit one. A
  fixture patched from RDM gets its pairs from its fine slots (a fine slot that does not follow
  its coarse one, as in pan, tilt, pan fine, tilt fine, is left as a plain channel).
- **Pan and tilt sit at half**: a profile made from RDM gives pan and tilt a default of 128, and
  picking either type in the editor fills 128 in, so a moving light starts centred and Clear all
  levels returns it there. Profiles saved earlier are not changed.
- It sends up to 32 universes at 40 Hz each, multicast with a TTL of 1, and announces them
  (universe discovery). The patch and the operator's profiles are kept in the user config folder
  (`sacn-patch.json`, `sacn-profiles.json`) and so are shared with an iPad; levels are not kept.
  Under `DRY_RUN=1` the output runs but nothing is sent. Quitting the tool while sending just stops
  the packets, and receivers time out after 2.5 s.
- `POST /api/sacn/out/start`, `/stop`, `/levels`, `/clear`, `/release`, `/fixtures/add`,
  `/fixtures/remove`, `/fixtures/highlight`, `/profiles/save`, `/profiles/delete`; `GET /api/sacn/out` is the whole state.

Sent to real nodes on 25 Nov 2026 (two LumiNode 4s, v2.9.1, ports on sACN U1-4): universe 1 at
priority 100, all zeros, then channel 1 at 128 for ten seconds. Port 1 of both nodes went from
`idle` to `streaming` within three seconds and stopped taking frames when the output stopped (see
`../docs/device-facts.md`). That was driven through the API; the page's own buttons and the
Fixtures, Profiles and Patch parts have only run in dry run, and nobody has yet watched a fixture
respond. `SACN_LIVE_IFACE=<name> go test -run LiveLoopback ./internal/sacn` checks the sockets on
a computer (all-zero levels on universe 63999).

## Using it from an iPad

**Other devices** at the top of the right-hand column turns on access from another device on the
network, such as an iPad on an access point on the show network. It is **on every time the tool starts**
(since 1.6.7; start it with `--no-lan` to have it off), with the saved PIN if there is one, else a new
random PIN shown on this computer's page and in its window. It listens on every network this computer is
on, Wi-Fi included, so anyone there who has the PIN can sign in: turn it off on a network you don't trust.
**Turn on** listens on each of this computer's own addresses as well (same port), lists the addresses
to open on the iPad (e.g. `http://192.168.1.201:3800`), and shows a six-digit PIN; the header shows
OTHER DEVICES ON. The iPad gets a PIN page first (it signs in as soon as the sixth digit is in, and sends the PIN in the background rather than as a form, so the browser has no insecure-form warning to show); the right PIN signs it in for 12 hours (a cookie
other sites cannot use). Five wrong PINs lock PIN entry for a minute. **New PIN** signs every device
out; **Turn off** closes the listeners and drops open connections. Only this computer can see the PIN
or switch access: on the iPad the switch is shown disabled. A signed-in device can do everything the
laptop can, including addressing and resets, so keep the PIN to the people running the job. Devices
still talk to the rig from this computer, over its wired port. If the computer's addresses change
while it is on (a cable plugged in), it starts or stops listening on them within a few seconds. On
Windows the firewall may ask the first time.

**Saved PIN.** Below the PIN, **Set a PIN** (or **Keep this PIN** while access is on) saves a six-digit
PIN on this computer, so it turns on with the same PIN every time and the iPad's PIN does not change
from show to show. **Change** picks another one; saving while access is
on uses it straight away and signs every device out. **New PIN** while on is a random one for this time
only. **Use random** forgets the saved PIN. It is kept, readable only by you, in
`lan-pin.json` in the tool's settings folder (`~/Library/Application Support/luminex-commission/` on a
Mac, `%AppData%\luminex-commission\` on Windows; `LUMINEX_LAN_PIN_FILE` moves it).

## Raspberry Pi (headless, starts at boot)

Not yet run on a real Pi. Download `LuminexCommission-<version>-linux-armv6.tar.gz` for 32-bit Pi
OS (any Pi, including a Pi 1 Model B) or `-linux-arm64.tar.gz` for 64-bit Pi OS on a Pi 3 or later
(`cat /proc/device-tree/model` says which Pi it is), or build both with `./scripts/build.sh`. Copy it over:

```bash
scp LuminexCommission-<version>-linux-armv6.tar.gz pi@<pi-address>:
```

On the Pi, unpack it, rename it and save the PIN that iPads and laptops will sign in with:

```bash
tar -xzf LuminexCommission-*-linux-armv6.tar.gz && mv LuminexCommission-*-linux-armv6 LuminexCommission
```

```bash
./LuminexCommission --save-pin 123456
```

Then create `/etc/systemd/system/luminex-commission.service`:

```ini
[Unit]
Description=Luminex Commission
After=network-online.target
Wants=network-online.target

[Service]
User=pi
ExecStart=/home/pi/LuminexCommission --no-open
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now luminex-commission
```

**Other devices** is on at start, which a Pi needs, because nobody can open its own page to
turn it on (`--lan` from 1.6.6 is still accepted and changes nothing). Open `http://<pi-address>:3800` on the iPad or laptop and sign in with the PIN. The page
itself is only on the Pi, so the switch and the PIN controls stay disabled for every other
device. To change the PIN, run `--save-pin` again and then `sudo systemctl restart luminex-commission`.
With no saved PIN, it picks a random one at each start and prints it to the log
(`journalctl -u luminex-commission`). `--save-pin ""` removes the saved PIN. The PIN file is
`~/.config/luminex-commission/lan-pin.json`.
Run it as the same user that saved the PIN (`User=pi`), or the service will not find the PIN file.
Logs: `journalctl -u luminex-commission -f`. For a dry run, add `Environment=DRY_RUN=1` under
`[Service]`.

Give `eth0` a static address on the show network (`nmcli`). Pi OS's mDNS service (avahi) shares
UDP 5353 with the tool's scanner, which should work but has not been tested; if scans find nothing,
`sudo systemctl disable --now avahi-daemon`. A Pi has no battery-backed clock, so on a network with
no time server the log timestamps are wrong.

## Which devices a batch includes

In Address & reset, a device is ticked when it first appears, and **Apply** and
**Factory reset** act on everything ticked, so check the list before you click. If you
untick a device it stays unticked across scans, page reloads and address changes until
you tick it again or quit the tool. Configure's Network dialog still changes its own
node, ticked or not.

## Getting to a node's configure page

- **Configure view:** a **Configure** button on each node, whether or not it is being
  addressed, ticked or not.
- **Address & reset view:** the same button on each row. As soon as a row shows
  Addressed ✓, it opens the node at its new address.
- **Addressed list:** each entry has the button too, and keeps it after that batch
  has been swapped out and left the scan list. It uses the device's current address
  if it is on the list, otherwise the last address it was given.

Each device opens on the **Node** tab of the same page (before 2.0.0 it was a browser tab of its own; an old `/configure/?ip=…` address still lands on that node).
The button is disabled, with a tooltip saying why, for a switch, a device in a
running job, a dry-run entry, or an address no adapter on this computer can reach.

## Run

```bash
DRY_RUN=1 ./dist/LuminexCommission-<version>-macos-arm64.command     # safe
./dist/LuminexCommission-<version>-macos-arm64.command               # live (or double-click it), opens http://localhost:3800
go run .                                                             # from source (Go 1.25+)
```

Options: `--no-open`, `--port N`, `PORT=N`, `--no-lan` (other devices off at start), `--save-pin NNNNNN` (save the PIN and exit), `MDNS_BACKEND=mdns|dns-sd`,
`DEVICE_PORT=N`, `DEVICE_ARTNET_PORT=N` and `ARTNET_LISTEN_PORT=N` (testing only). RDM
needs UDP 6454: close any other Art-Net software on the laptop, or the RDM views fall back
to what each node has overheard. Releases ship each Mac binary in a zip, which keeps
it executable. They are not notarised: the first time, use System Settings → Privacy &
Security → Open Anyway, or run `xattr -d com.apple.quarantine <file>` once. Build with `./scripts/build.sh` or `scripts\build.bat`.

## Real hardware

Checked on 4 Nov 2026 on a GigaCore 16tf (v1.9.2) and a LumiNode 12 (v2.9.1): both
addressed and named `.8` to `.9` and back, with both Configure buttons opening the
node at its new address. An open Configure tab locked during the job and moved
itself to `.9`, and the Network dialog moved the node back through a job. Add by
address recognised both as the devices Scan had found.

Passwords have only been tested on the fakes, not on a real device with one set.

## While a job runs

A Configure tab opened from the list follows its device by serial:

- **During a job:** the tab says why, its write buttons are disabled, and the server
  answers writes with 409. The tab buttons still work, and so do reads.
- **After an address change:** the tab moves itself to the new address.
- **Network dialog:** changing the address there runs a job, so mDNS confirms it. A
  mask or gateway change with the same address is sent directly.

## Passwords

A password entered once works for jobs and for Configure, and follows the device to
its new address. It is held in memory only, never written to disk or logged. Trying
order: none, then `admin` with a blank password, then ask.

## Add by address

Under the device list, for a device Scan is not finding. It asks the device what it
is (LumiNode, LumiCore or GigaCore Gen2; Gen1 has no API that can say), and the
device stays listed across scans.

Address changes are confirmed by mDNS, so a device mDNS cannot see will end as "Not
seen afterwards" even when the change worked. A device with a password set cannot be
added this way yet.

## Not done yet

- A dry-run edit on the configure page shows the badge, but its toast still says
  "updated" and the page shows the old values, because nothing was sent.
- A Configure tab opened by typing an address in its own IP box does not follow a
  device or lock.
