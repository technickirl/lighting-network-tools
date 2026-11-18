# Lighting Network Tools

[![CI](https://github.com/technickirl/lighting-network-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/technickirl/lighting-network-tools/actions/workflows/ci.yml)

**Luminex Commission**: one tool for commissioning Luminex LumiNode / LumiCore nodes
and GigaCore switches from a laptop on the same network. It is a single binary that
serves a local web page and finds devices over mDNS (`_luminex._tcp`).

- Scan, number, address and name devices, following the cabling.
- Factory reset nodes and switches, keeping the address if wanted.
- A Configure page per node: DMX ports, IOs, process blocks, Quick Setup, profiles.
- RDM fixtures over Art-Net(even if port is set to sACN): read, set address / personality / label, readdress, factory reset.
- Use it from an iPad on the show network, behind a PIN.

> **Unofficial.** This project is not made, endorsed or supported by Luminex Network
> Intelligence. "Luminex", "LumiNode", "LumiCore" and "GigaCore" are their trademarks,
> used here only to say what the tool works with. It can factory reset and re-address
> devices: there is no warranty (see `LICENSE`), so try it with `DRY_RUN=1` first and
> never on a running show.

## Download

Ready-built binaries for Mac, Windows and Raspberry Pi are attached to each
[release](https://github.com/technickirl/lighting-network-tools/releases). The
operator's guide is `Luminex-Commission-Quick-Guide.pdf` (made from
`docs/quick-guide.html`).

The Mac builds are not notarised: before the first launch run
`xattr -d com.apple.quarantine <file>`, or use System Settings → Privacy & Security →
Open Anyway. The page opens at `http://localhost:3800`.

`DRY_RUN=1` logs every write request instead of sending it. Use it for a first run on
real hardware.

## Access from other devices

Access from another device (an iPad) is **on at start**, on every network the computer
is on, Wi-Fi included, behind a six-digit PIN shown on the computer's own page. It is
plain HTTP, meant for a closed show network. Start with `--no-lan` to have it off, and
turn it off on a network you do not trust. Details are in
`luminex-commission-go/README.md`.

## Build and test

```bash
go build ./... && go vet ./... && go test ./...     # Go 1.25+
cd luminex-commission-go && go run . --no-open       # dev run; --port N to move it
./luminex-commission-go/scripts/build.sh             # binaries into luminex-commission-go/dist/
```

Testing without hardware:

```bash
node test-scripts/fake-device.js       # six fake devices on <LAN IP>:8080 (needs Node, no npm install)
go run ./cmd/fakenode                  # one fake LumiNode on :8081, fake RDM fixtures on UDP 6455
DEVICE_PORT=8080 go run ./luminex-commission-go
```

## Layout

| Path | What |
|---|---|
| `luminex-commission-go/` | The program: `main.go`, build scripts, its README. |
| `internal/commission` | Device list: scan, numbering, addressing, naming, reset jobs, and its page. |
| `internal/nodeconfig` | The Configure page: node API, live polling, RDM. |
| `internal/luminex` | Shared: mDNS discovery, device classification, the device HTTP client, access from other devices. |
| `internal/artnet` | Art-Net RDM controller. |
| `cmd/fakenode`, `test-scripts/` | Fake devices for testing. Not shipped. |
| `docs/` | Device calls and what real devices were seen to do; the quick guide. |

`CLAUDE.md` holds the working notes for contributors (human or AI): the safety rule,
conventions and how releases are made.

## Credits

Written by Nick Malbon and Alex Mead. MIT licence.

Bug reports and pull requests are welcome. To report a security problem, please use
GitHub's private "Report a vulnerability" on this repository instead of a public issue.
