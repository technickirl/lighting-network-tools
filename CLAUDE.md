# Lighting Network Tools: working notes (human or Claude)

One tool, **Luminex Commission**: a single Go binary serving a local web UI on port
3800 for commissioning Luminex LumiNode / LumiCore nodes and GigaCore switches. It has
a device list (scan, number, address, name, factory reset), a Configure page per node,
and RDM fixtures over Art-Net. Read this before touching anything.

- What the devices do, and what has and has not been checked on real hardware:
  `docs/device-facts.md`. Read it before changing any device call.
- The device list's numbering and calls: `docs/device-list.md`.
- The Configure page's calls, process blocks and RDM: `docs/configure-page.md`.
- What the operator sees: `luminex-commission-go/README.md` and `docs/quick-guide.html`.

Each device serves its own API documentation at `http://<ip>/api/doc` (a Redoc page
with the OpenAPI JSON inlined; a LumiNode wants `Accept: text/html`). The vendor's
specs are not kept in this repo.

## Safety rule

**Never send a reset, reboot, IP change or profile recall to a real device without
asking the person running the rig first.** The tool honours `DRY_RUN=1`, which logs
the exact request instead of sending it; use that for any first run on real hardware.
Reads (GET probes) still go out under DRY_RUN.

## Build, run, test

```bash
brew install go                                   # Go 1.25+
go build ./... && go vet ./... && go test ./...   # from the root
node internal/commission/combo_test.js            # the numbering planner
cd luminex-commission-go && go run . --no-open    # dev run; --port N to move it
./luminex-commission-go/scripts/build.sh          # dist/LuminexCommission-<version>-<platform>
```

Testing without hardware:

```bash
# Six fake devices (two LumiNodes, GigaCore Gen1, Gen2, an unknown and an unreachable
# one): HTTP on <LAN IP>:8080, mDNS via dns-sd -R. Needs Node, no npm install.
node test-scripts/fake-device.js [--password secret]
DEVICE_PORT=8080 go run ./luminex-commission-go

# One fake LumiNode on :8081 with the process-block/IO API, and fake RDM fixtures
# over Art-Net on UDP 6455 (the app holds 6454)
go run ./cmd/fakenode
DEVICE_PORT=8081 DEVICE_ARTNET_PORT=6455 go run ./luminex-commission-go
```

`DEVICE_PORT`, `DEVICE_ARTNET_PORT` and `ARTNET_LISTEN_PORT` exist only for these
fakes; real devices are always port 80 for HTTP and 6454 for Art-Net. Real devices on
the same network show up in a scan beside the fakes, so run with `DRY_RUN=1` and pick
targets by the fakes' serials.

Serial numbers and MAC addresses in tests, samples and docs are made up. Keep it that
way: do not commit a real device's serial or MAC.

## Releases

- **Every binary carries its version in its file name**:
  `LuminexCommission-1.7.0-macos-arm64.command`. The version is the `v*` tag without
  the `v` (`git describe` locally, the tag name in CI) and is also compiled into
  `main.version`, so the UI badge and the file name agree. `var version` in `main.go`
  is only the fallback for `go run .`; bump it when tagging.
- **Mac binaries end in `.command`**: once the name has dots, Finder takes everything
  after the last dot as the extension and will not open the file; `.command` opens in
  Terminal on a double-click.
- Binaries are published as GitHub Releases, not committed. CI builds them when a `v*`
  tag is pushed (`.github/workflows/release.yml`). Each Mac one goes in a zip and each
  Raspberry Pi one (`-linux-armv6`, `-linux-arm64`) in a `.tar.gz`, because a browser
  drops the executable flag from a raw download.
- Mac binaries are ad-hoc signed, not notarised. Quarantine applies, so the first run
  needs `xattr -d com.apple.quarantine <file>` (run it before the first launch) or Open
  Anyway in Privacy & Security.
- **The operator's quick guide** is `Luminex-Commission-Quick-Guide.pdf` (A4), made
  from `docs/quick-guide.html`. Edit the HTML when a release changes what the operator
  does, update its date and version line, and print it with headless Chrome:
  `"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --no-pdf-header-footer --print-to-pdf=Luminex-Commission-Quick-Guide.pdf docs/quick-guide.html`.

To publish:

```bash
git tag -a v1.7.1 -m "..." && git push origin v1.7.1
```

## Working on the repo

- Changes land on `main` by pull request, and the `build` check (gofmt, go vet,
  go test, go build for linux/mac/windows/pi) must be green.
- Work on a branch, push it, open a PR. The PR template has three tick-boxes.
- Commit with a plain message; builds and `node_modules` stay out of git.

## Conventions

- One Go module (`go.mod` at the root). `internal/luminex` is the shared base: `Scan`,
  `Device`, `ParseTxt`, `Reachability`, `LocalInterfaces`, `Logf/Warnf/Errorf`, and the
  device client `Fetch`, which logs every call and refuses writes under `DryRun`.
  `internal/commission` is the device list's server, jobs, device calls and page
  (`Mount`, `Start`). `internal/nodeconfig` is the Configure page's API, poller and
  page (`Mount`, `Assets`; RDM in `rdm.go`, its state shared by every page in
  `rdm_state.go`). `internal/artnet` is the Art-Net RDM controller (one per process,
  it holds UDP 6454; `Shared`, and a fake node for tests). `luminex-commission-go` is
  only the `main` around them; a full-flow test in its `main_test.go` runs everything
  with no network at all.
- The UI is vanilla JS rendering from a server state snapshot (SSE for the device
  list, a WebSocket for a node's live state). No frameworks, no CDN, system fonts: it
  must work offline in a warehouse.
- **Escape everything a device sent** before it goes into HTML (`esc()`), and never put
  device text inside an inline `on…=` handler. Names, modes, labels and colours all
  come from whatever answers on the network.
- Operator-UI rules: no `confirm()`/`alert()` pop-ups; buttons are disabled with a
  tooltip, never hidden; the server mirrors every guard; every device request and
  response is logged with a timestamp.
- Keep JSON field names between server and `public/*.js` in step; the UI is the
  contract.
- The page is served on 127.0.0.1 behind `luminex.LocalOnly`: every request must name
  localhost in `Host`, and writes and the WebSocket must come from the tool's own page.
  A website re-pointing its own name at the tool is refused that way.
- Access from other devices is `luminex.LANAccess` (`lan.go`): on at every start
  (`--no-lan` starts it off; `--save-pin` sets the PIN from a shell), switched from the
  computer's own page (`/api/lan`, refused to other devices). It is a second listener
  per local IPv4 address on the same port, answering only to its own address or a
  `.local` name in `Host`, with a six-digit PIN, a SameSite=Strict session cookie, and
  Origin checked on writes and WebSockets. A PIN can be saved (`lan-pin.json` in the
  user config dir, mode 0600).
