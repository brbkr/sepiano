# sepiano

Open-source replacement for the Windows "SE Program Suite" that drives a
Stahnke SE reproducing piano. The piano is a Steinway with the SE system
(rendering name `Steinway_SED133291`, "New York").

- `README.md` covers status and the tools.
- `docs/SE-FILE-FORMAT.md` and `docs/SE-SERIAL-PROTOCOL.md` are the
  reverse-engineered specifications. Trust them; every claim is backed by the
  decompiled code, the file collection or captures from the real piano.
- `docs/SERVICE-PLAN.md` is the proposed next step: a service on a Raspberry
  Pi hidden in the piano, with a web UI and later an iPhone app. **It is a
  proposal. The piano's owner decides what to build and in what order.** Check
  its "Open decisions" before starting large work.

## How it works, in one paragraph

`play.exe` is not really a player. It uploads a Z80 program (Stahnke's) into
the piano's controller, then acts as a terminal (channel 1: text out, keys in)
and a file server (channel 2: the Z80 asks for blocks of `.kb/.lp/.sp`).
`sehost` does the same. It reads that Z80 program from the user's own copy of
`play.exe` (`host.LoadProgram`) and never ships it.

## Layout

| Package | Role |
|---------|------|
| `se/` | `.kb/.lp/.sp` codec (byte-exact on the whole corpus), scrambler, SE ⇄ MIDI |
| `smf/` | minimal MIDI file reader/writer |
| `link/` | frames, CRC, ARQ link, clock-paced pump, simulated cable (`NewPipe`) |
| `host/` | Z80 program extraction, session, console keys, file service |
| `cmd/` | `sedump`, `se2mid`, `mid2se`, `sehost`, `sesniff` |

## Commands

```bash
go vet ./... && go test ./...      # all tests; link tests take about 25 s
gofmt -l .                         # must print nothing
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/ ./cmd/...   # for the Pi
```

- Corpus tests read the recordings from the parent folder or `$SE_CORPUS`, and
  skip when they aren't there.
- The `play.exe` extraction test needs `SE_SUITE=<folder with play.exe>`.
- `go build` needs `-buildvcs=false` only outside a git checkout.

## The hardware

- **Raspberry Pi 4**, Raspberry Pi OS Bookworm, arm64. Go is **not** installed
  on it: cross-compile, then `scp` the binaries over. By convention:
  - binaries in `~/sepiano-bin/`;
  - `play.exe`, `record.exe` and songs in `~/sepiano-data/`.
- **SSH:** each developer adds their own key and a host alias (e.g. `pi`) in
  `~/.ssh/config`. The home network's DNS can't be changed; use the IP in the
  alias, or mDNS (`<hostname>.local`).
- **Two USB serial adapters** are on the Pi. Check
  `ls -l /dev/serial/by-id/`, because the `ttyUSB` numbers depend on plug-in
  order:
  - **Prolific PL2303 → the piano**, wired directly. It has been `/dev/ttyUSB1`.
  - **Keyspan USA-19HS → the old XP ThinkPad**, through a null-modem adapter,
    used to capture the original `play.exe`. It has been `/dev/ttyUSB0`.
- **The piano talks at 57600 baud.** It is silent until a host sends a
  handshake.

### Working with the piano

1. **Check the link first** with `sehost -probe -port <dev>`. It does a
   handshake only and uploads nothing. If no bytes come back, it's a cabling
   or power problem, not a protocol one: try `sehost -lines`, then check the
   adapter.
2. **Play:** `sehost -exe play.exe -port <dev> -trace -capture runN SONG`, run
   from `~/sepiano-data`. Use tmux or `ssh -t` when someone needs the player's
   keys: Space stop/resume, Enter quit, Esc restart, Up/Down level.
3. **You cannot see or hear the piano.** Ask the person at the piano what
   happened, and say what they should expect before playing.
4. `10Sec-C9-400` is Zenph's near-silent lead-in file. Use a real piece (e.g.
   a Rachmaninoff file) to check that keys move.

## Hard rules

- **Never commit copyrighted material:**
  - Stahnke's programs (`*.exe`, extracted Z80 images);
  - the recordings (`.kb/.lp/.sp`, `.mid`);
  - **full session captures** (`sehost -capture` `.tx` files contain the
    uploaded Z80 program).

  `.gitignore` blocks most of these. The repository is public. `link/testdata/`
  holds only small link-frame captures; check new test data with `strings`
  before adding it.
- Keep `se` byte-exact: `go test ./se` re-encodes every corpus file and must
  stay identical.
- Protocol changes must keep passing the real-capture tests in
  `link/capture_test.go`, which replay real `play.exe` and piano traffic.

## Facts that are easy to get wrong

- SE velocity is an **inverse** scale: bigger means softer. It's 10 bits.
- `.lp` is the damper ("loud") pedal, sampled every 10 ms. `.sp` is the soft
  pedal, sampled every 20 ms. A `.kb` tick is 1.25 ms.
- **The piano's firmware does not use the PC's handshake logic.** It echoes
  each handshake frame and interleaves IDLE frames. `link` only treats 16
  non-handshake frames in a row as "peer is up" (`quietUp`).
- The piano's frames, idle ones included, carry its 800 Hz playback clock in
  `ts`, which gives a live position.
- The player's level only goes from 0 (as recorded) down. It can't go louder.
- The suite never creates its `HKLM\Software\SE Program Suite` registry key,
  so the original programs rescan the ports every launch.

## Status (end of the first working session)

- Verified on the real piano from the Pi: the handshake, the upload, the file
  service, and a full 3:49 piece. The piano's clock ran at 799.99 of 800
  ticks/s, file replies took ≤ 4 ms, there were no link errors, and the pacing
  sounded right by ear.
- Not yet tried: the player keys over the link, `record.exe`, re-uploading on
  a link that's still up, and piano power cycles. That's Phase 0 in
  `docs/SERVICE-PLAN.md`, which needs someone at the piano.
