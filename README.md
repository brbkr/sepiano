# sepiano

Open-source tools for the Stahnke SE reproducing piano system. The goal is to
replace the Windows "SE Program Suite" (`play.exe` and friends) with a Go
program that runs on a Raspberry Pi, and later a network service an iPhone
app can drive.

* `docs/SE-FILE-FORMAT.md` — the `.kb/.lp/.sp` performance files, fully
  decoded
* `docs/SE-SERIAL-PROTOCOL.md` — the PC ⇄ instrument link, program upload,
  console and file service, plus why USB adapters misbehave

## The big finding

`play.exe` is not really a player. It **uploads a Z80 program into the
piano's controller**, then acts as a terminal and file server for it. The Z80
program parses the song files and drives the piano. A replacement host only
has to speak the link protocol, upload that same Z80 program (taken from your
own copy of `play.exe`), and serve files. That is what `sehost` does.

## Status

| Piece | State |
|-------|-------|
| `.kb/.lp/.sp` decoding and encoding (`se`) | **Verified**: all 349 + 698 files re-encode byte-identically. Notes match independent MIDI renderings exactly. |
| SE ⇄ MIDI conversion (`se2mid`, `mid2se`) | Works. The velocity curve is empirical (fitted to Zenph's paired renderings). |
| Link protocol (`link`) | Matches the decompiled host. Tested in simulation (noise, loss, flow control, original-logic peer). **Not yet tried on the instrument.** |
| Host (`sehost`) | Upload, console, file service. Tested end-to-end against a simulated instrument. **Not yet tried on the instrument.** |
| Sniffer (`sesniff`) | Decodes captures and live taps. Tested on simulated traffic. |

## Tools

```bash
cd sepiano
go build -o bin/ ./cmd/...        # or: GOOS=linux GOARCH=arm64 go build -o bin-pi/ ./cmd/...
```

| Command | Purpose |
|---------|---------|
| `sedump [-pedals] song` | list notes and pedal moves as text |
| `se2mid song ...` | SE → Standard MIDI File (800 ticks/s, CC64 damper, CC67 soft) |
| `mid2se -o SONG in.mid` | MIDI → SE files the player can play |
| `sehost -exe play.exe -port /dev/ttyUSB0 song` | play on the instrument (replaces `play.exe`) |
| `sehost -list` | list serial ports |
| `sesniff -pc a.bin -piano b.bin` / `-tap-pc P1 -tap-piano P2` | decode traffic |

`sehost` needs a suite executable to take the Z80 program from. It is
installed on the XP machine. You can also unpack
`se_program_suite_v0.95_setup.exe` with 7-Zip (`7z x …`). Other suite programs
run the same way: `-exe record.exe` records through the same file service.
Add `-trace` to log frames, and `-capture run1` to save raw traffic for
`sesniff`.

While playing: **Space** stop/resume, **Enter** quit, **Esc** restart,
**Up/Down** restore/subdue level, **Ctrl-C** abort.

## Next steps on real hardware

1. **Read the XP registry** `HKLM\Software\SE Program Suite`, values `Port`
   and `Baudrate`. That gives the instrument's baud rate (57600 or 28800).
2. **Capture a known-good session.** Tap the cable between the ThinkPad and
   the piano: two USB-serial adapters, receive pins on each TXD line, grounds
   joined. Then run `sesniff -baud N -tap-pc … -tap-piano … -dump cap/`. This
   checks the handshake, the CRC and the timing against the real firmware,
   and `cap/pc-ch0.bin` is the exact program image.
3. **Run `sehost -trace -capture try1`** from a Linux machine on a good
   adapter (FTDI), then from the Pi. On the Pi, prefer the built-in UART
   (`/dev/serial0`) through a **MAX3232 RS-232 level shifter**. Never wire the
   Pi's 3.3 V pins straight to the RS-232 cable.
4. Then wrap `host.Session` in a small HTTP/WebSocket service (song library,
   play/stop/level), which the iPhone app can call.

## Layout

```
se/     file formats, MIDI conversion       link/  frames, ARQ link, pacing pump
smf/    tiny MIDI file reader/writer        host/  Z80 upload, file service, console
cmd/    sedump se2mid mid2se sehost sesniff docs/  specifications
```

`go test ./...` runs the unit tests. The corpus tests read the recordings in
the parent directory (or `$SE_CORPUS`). The `play.exe` extraction test needs
`SE_SUITE=<dir with play.exe>`.

## Copyright

The Z80 programs and the Windows suite are © Wayne Stahnke. The recordings
are © Zenph and others. This repository contains neither: `sehost` reads the
Z80 program from your copy at run time. Keep the recordings and the suite out
of any public repository.
