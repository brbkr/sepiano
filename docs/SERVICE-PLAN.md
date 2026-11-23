# Proposed piano service

**Status: proposal.** Nothing here is built yet. It's a starting point for the
piano's owner to change. The early decisions are listed below so they can be
revisited.

## Goal

`sehost` on a Raspberry Pi 4 already plays full performances on the SE piano
(see the README). The next step is a headless service, since the Pi will be
hidden inside the piano. It should provide:

- a library of performances that can be played;
- play / pause / stop;
- recording (start and stop);
- volume up and down;
- uploading new files and downloading existing ones.

It needs a web UI first, and an iPhone app later.

## Decided so far (revisit freely)

| Decision | Choice | Why |
|----------|--------|-----|
| Where it lives | One daemon, `sepianod`, in this repo | It owns the serial port and serves the API and web UI from one static binary, so the API and UI change together. |
| Web UI | First pass, as a mobile-first PWA | It's the only way to use the piano without SSH. "Add to Home Screen" gives an iPhone app right away, and it proves the API. |
| iPhone app | Later, in a separate repo (`sepiano-ios`, SwiftUI) | Different toolchain and release cycle. It talks to the daemon through a checked-in OpenAPI spec. Build it once the PWA shows which native features matter (lock screen, Siri/Shortcuts, widgets). |
| How playback is controlled | Drive Stahnke's own Z80 player and recorder | Every control maps onto a key those programs already understand, and their console text confirms each state change. Nothing is reimplemented on the Z80. |
| Access | Home LAN only, no login | Simplest. Destructive actions get confirmations in the UI. |
| Default renderings | Prefer `Steinway_SED133291` (New York) | This piano is that instrument; other renderings stay available. |
| Power | Assume it can be cut without warning | Undecided, so the SD card must survive sudden power loss. |

## Open decisions for the owner

- How the Pi is powered inside the piano: switched with the piano, or always on.
- Whether to use Wi-Fi or Ethernet inside the closed piano.
- Which features come first, and whether recording belongs in the first pass.
- Whether the web UI needs anything beyond the screens listed below.
- Whether remote access (outside the home network) is ever wanted.

## Architecture

```
 phone / browser ──HTTP+SSE──▶ sepianod (Pi, systemd, http://piano.local)
                                ├─ api/      REST /api/v1 + SSE /api/v1/events, serves web/dist
                                ├─ deck/     one actor goroutine: serial port, sessions, state machine
                                │             └─ host.Connect / host.Session / host.FileService (existing)
                                ├─ library/  SQLite index + file store (+ se package for parse/convert)
                                └─ sim/      fake instrument for development without the piano
```

### Deck (the instrument controller)

- **One goroutine owns `host.Conn`.** Commands arrive on a channel, so many
  clients can't race each other. State is broadcast over SSE.
- **States:** Offline → Connecting → Idle → Loading (upload, about 2.1 s) →
  Playing ⇄ Paused → Idle. Recording runs Armed → Recording → Saving → Idle.
  Fault carries a message and retries.
- **Controls map to player keys,** sent through the existing
  `host.Session.Keys` channel (codes in `host/keys.go`). Each state change is
  confirmed by parsing the console, not assumed. The parser is a small writer
  wrapping `Session.Console`.

  | Control | Key | Confirmed by |
  |---------|-----|--------------|
  | pause / resume | Space | "STOP PLAYING AT TIME=…" / "RESUME PLAYING" |
  | stop | Enter | "QUIT PLAYING", then ESC |
  | restart | Esc | "RESTART PERFORMANCE" |
  | volume | Down / Up | "LEVEL=0 (NORMAL)" / "LEVEL=-n" |

- **Volume = the player's level,** from 0 (as recorded) down to −N. The player
  can only subdue, not go louder. The level lives in firmware RAM at Z80
  `0xEE80`, so it may persist between songs. If it doesn't, the deck re-applies
  the chosen level after "START PLAYING". A "louder than recorded" velocity
  trim, applied while serving the file, is phase 2.
- **Live position comes from the piano's own 800 Hz clock.** Its frames carry
  the clock in `ts`, idle frames included: 87,220 of 88,080 idle frames had it
  during a 3:49 piece. Capture it through the existing `link.Pump.OnFrame`
  hook, unwrap 16 bits, divide by 800, and push it over SSE at about 5 Hz.
  Duration comes from `se.KB.End`.
- **Recording uses `record.exe`'s Z80 program**, with prompts "DEPRESS SPACE BAR
  TO START RECORDING" and "DEPRESS ENTER TO END RECORDING". The steps:
  1. arm: upload the recorder, with the file service pointed at a staging name;
  2. start: Space;
  3. stop: Enter.
  The recorder writes through the existing file-service commands
  (`0x12`/`0x13`/`0x15` in `host/files.go`), and the deck imports the result as
  a new piece. "KEYBOARD INTERLOCK FAILURE" becomes a Fault with its text.
- **Link lifetime.** Keep the link up while idle, so the UI shows
  Online/Offline at once and a piano power cycle is noticed (`link.ErrSilent`
  starts a reconnect loop). Run one session per piece on the open link if the
  Phase 0 spike shows the firmware accepts a new upload; otherwise reconnect
  per piece (already proven, about 2.5 s to the first note).

### Library

- **Files are the source of truth; SQLite is the index.**
  - Each piece is stored as `/var/lib/sepiano/library/<id>.{kb,lp,sp}` plus
    `<id>.json` metadata. `<id>` is 8 characters, because the Z80 rejects
    longer names if it asks for one.
  - The database can be rebuilt by rescanning the folder.
  - Writes go to a temp file, then fsync and rename. SQLite runs in WAL mode
    with `synchronous=FULL`, using pure-Go `modernc.org/sqlite`, so the
    cross-compile stays static.
- **Schema:**
  - `pieces`: id, title, composer, performer, recorded_on, instrument,
    perf_key, duration_ms, notes, origin (import | upload | midi | recorded),
    kb_sha256, added_at
  - `playlists`, `playlist_items`, `plays` (history), `settings`
- **Metadata** is parsed from the Zenph naming convention, e.g.
  `02-Rachmaninoff19280404-Rachmaninoff_PreludeOp3_2-Steinway_SED133291`,
  falling back to the `.kb` header text. Fields are editable.
- **Renderings.** `perf_key` (performer + date + work) groups the renderings
  of one performance. The default rendering comes from the
  `settings.preferred_instrument` setting.
- **Import:** `sepianod import <dir>` scans for `.kb/.lp/.sp` sets, validates
  them with `se.Load`, and dedupes by hash. It also turns the `.bat` files into
  playlists.
- **Upload** accepts a `.kb/.lp/.sp` set, a `.zip` of sets, or a `.mid`
  (converted with `se.FromMIDI`, original kept).
- **Download** offers:
  - the original set as a `.zip`;
  - MIDI (`Performance.ToMIDI`);
  - a whole-library backup `.zip`, which matters with an SD card.

### API (`/api/v1`, OpenAPI in `api/openapi.yaml`)

| Area | Endpoints |
|------|-----------|
| Live state | `GET /status`; `GET /events` (SSE: state, position, level, console, library-changed) |
| Playback | `POST /deck/play {piece \| playlist}`, `/deck/pause`, `/deck/resume`, `/deck/stop`, `/deck/restart`, `/deck/level {delta \| level}` |
| Recording | `POST /record/arm {title}`, `/record/start`, `/record/stop`, `/record/discard` |
| Library | `GET /pieces?q=&composer=&performer=&instrument=`, `GET/PATCH/DELETE /pieces/{id}`, `POST /pieces` (multipart), `GET /pieces/{id}/download?format=se\|midi`, `GET /export` |
| Lists | `/playlists` CRUD, `GET/PUT /queue` |
| Instrument | `GET /instrument` (port, baud, link stats, program banners), `PUT /programs/{play\|record}` (upload the .exe) |

The API uses the stdlib `net/http` with Go 1.22 method routes. It sends SSE
rather than WebSockets: it's stdlib-only, `EventSource` reconnects by itself,
and iOS can read it.

### Web UI (`web/`, Vite + Preact + TypeScript, built output embedded)

The screens:
- **Now Playing:** title and performer, a progress bar from the piano clock,
  pause/stop/restart, and level −/+ with a readout.
- **Library:** search, filters, renderings grouped together.
- **Piece:** play, queue, download as SE or MIDI, edit, delete.
- **Playlists / Queue.**
- **Record:** arm → start → stop → name it.
- **Upload.**
- **Status & Settings:** link health, programs, preferred instrument, backup.

Node is needed on the development machine only; the Pi gets one binary.

### Running hidden in the piano

- **systemd unit** `sepianod.service`:
  - runs as a dedicated `sepiano` user in group `dialout`, with
    `Restart=always`;
  - gets `CAP_NET_BIND_SERVICE` so the UI lives at `http://piano.local/`. The
    hostname comes through mDNS/avahi, which needs no DNS changes.
- **Discovery:** an avahi service file advertises `_http._tcp` and
  `_sepiano._tcp`, for the iOS app's Bonjour discovery later.
- **Stable port:** a udev rule creates `/dev/sepiano-piano`, matching the
  PL2303 by USB path, because `ttyUSB` numbers change with plug order.
- **Programs:** `play.exe` and `record.exe` live in
  `/var/lib/sepiano/programs` and are never in git. They're uploaded once via
  the API or scp.
- **Sudden power loss:** besides the atomic-write and SQLite measures above,
  phase 2 can switch on Raspberry Pi OS's overlay (read-only) root, with data
  on a separate writable partition.
- **Deploy:** `make deploy` cross-compiles (`GOOS=linux GOARCH=arm64`), copies
  over SSH, and restarts the unit.

## Phases

**Phase 0 — hardware spikes at the piano.** Short, using `sehost` in an
interactive session (tmux or `ssh -t`). Someone has to be at the piano to watch
and listen. Each answer changes deck details:
1. Do Space, Enter, Esc and Up/Down work, and what does each print?
2. How low does the level go, and does it persist across songs?
3. Does a second upload on a link that is still up start a new program?
4. Run `record.exe` end to end: what files does it write, and does it ask for
   the name?
5. What happens on the link when the piano is power-cycled?

**Phase 1 — a usable piano service** (milestones in this order):
1. `sim/`: a fake instrument with an echoing handshake, the real prompt
   strings, an 800 Hz clock, key reactions, and file reads through the file
   service. Grown from `fakeZ80` in `host/host_test.go`.
   `sepianod -simulate` then runs everything without the piano.
2. `deck/` with play, pause, resume, stop, restart and level, tested against
   `sim/`.
3. `library/` with import, metadata, upload, download and export.
4. `api/` with SSE, plus `api/openapi.yaml`.
5. The `web/` PWA: Now Playing, Library, Piece, Upload, Status.
6. Recording, end to end.
7. `deploy/` (unit, avahi, udev) and a Makefile.

**Phase 2:**
- queue and playlists, including the `.bat` imports;
- play history;
- "start at" seeking, by serving a performance cut with `se`;
- the velocity trim for louder-than-recorded;
- the overlay root.

**Phase 3:** `sepiano-ios` (SwiftUI, Bonjour, an OpenAPI-generated client), and
remote access if it's ever wanted.

## Files

New:
- `cmd/sepianod/main.go`
- `deck/deck.go`, `deck/console.go` (console text → events), `deck/deck_test.go`
- `sim/sim.go`
- `library/store.go`, `library/files.go`, `library/names.go`, `library/import.go`
- `api/server.go`, `api/sse.go`, `api/openapi.yaml`
- `web/…`, `web/embed.go`
- `deploy/sepianod.service`, `deploy/sepiano-avahi.service`, `deploy/99-sepiano.rules`
- `Makefile`

Reused unchanged:
- `host.Connect`, `host.Session`, `host.FileService`, `host.LoadProgram` and the
  key codes (`host/`);
- `link.Pump.OnFrame` and `link.ErrSilent` (`link/`);
- `se.Load`, `se.ParseKB`, `Performance.ToMIDI`, `se.FromMIDI` and
  `Performance.Save` (`se/`).

Likely small changes: running several `Session`s on one `Conn` (it already
takes a link), and pointing `FileService` at library names.

## Verification

- **Unit tests:** `go test ./...` with deck scenarios against `sim/`:
  - play → pause → resume → level → stop;
  - end of performance;
  - recording produces files that `se.Load` accepts;
  - a link drop moves the deck to Offline, then recovery.
  Library tests: import the corpus, the Zenph name parser, the upload and
  download round trip (bytes identical; the MIDI round trip uses the existing
  tests' tolerance), and rebuilding the DB from files.
- **Without the piano:** `sepianod -simulate` plus the web UI in a browser;
  check every control and live progress.
- **On the piano:** deploy to the Pi, open `http://piano.local` on a phone,
  then:
  1. play a known piece and pause, resume, change the level, stop;
  2. upload a MIDI file and play it;
  3. download a piece as MIDI;
  4. record a short phrase and play it back;
  5. power-cycle the piano while idle: the UI should show Offline, then recover.
