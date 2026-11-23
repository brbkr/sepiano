# Stahnke SE performance files: `.kb`, `.lp`, `.sp`

A performance for the Stahnke SE reproducing system is three files that share
a base name:

| File  | Contents                       | Sampling            |
|-------|--------------------------------|---------------------|
| `.kb` | keyboard: key down/up events   | event list, 1.25 ms resolution |
| `.lp` | **l**oud pedal = damper/sustain | one sample per 10 ms |
| `.sp` | **s**oft pedal = una corda      | one sample per 20 ms |

The names are confirmed by `SE/Bo2mf.exe` ("Reading keyboard file / Reading
loud pedal file / Reading soft pedal file").

Everything below was derived from the Z80 program inside `play.exe`
("STAHNKE SE/PC PERFORMANCE PLAYER, V50.45", the code that actually parses the
files on the instrument), and then checked against every file in this
collection:

* all 349 `.kb` and 698 `.lp`/`.sp` files parse, and re-encode **byte for
  byte** with the `sepiano/se` encoder;
* `tempAnatolyBoes/` holds the same performances as SE files *and* as Yamaha
  Disklavier MIDI files. Decoding with this spec gives identical note
  sequences (1011/1011 notes in *Nowak_BodyBends*), with timing within 1 ms
  over 3 min 37 s. The damper pedal tracks MIDI CC64 at about ×2.

Z80 addresses in brackets point into the player for anyone re-checking.

## Time base

One `.kb` tick is **1/800 s = 1.25 ms**. Pedals are sampled every 8 ticks
(`.lp`, 10 ms) and 16 ticks (`.sp`, 20 ms). [parse loop 0x049E–0x056E: after
each key event the damper stream advances while `t_lp < t_kb` in steps of 8,
the soft stream in steps of 16.]

## Common wrapper: 4-byte seed and scrambler

Every file starts with a 4-byte **seed**. The rest of the file is XORed with
a keystream generated from that seed [0x0BB9, 0x0E67, 0x0E7C]. A seed of
`00 00 00 00` gives an all-zero keystream, so the file is effectively in the clear.
All Zenph files use that seed. The one exception in this collection is
`Hamb/banneret.{kb,lp,sp}` (seed `35 CC C6 63`), which decodes correctly with
the algorithm below.

```
s[0..3] := seed[3], seed[2], seed[1], seed[0]
alt     := (seed[3] == 0)
for each byte c after the seed:
    out := c XOR s[3]
    hi  := s[3]<<8 | s[2]        (16-bit)
    lo  := s[1]<<8 | s[0]
    if alt:  a := s[3] ^ hibyte(hi<<4) ^ hibyte(lo<<1) ^ hibyte(lo<<3)
    else:    a := s[3] ^ hibyte(hi<<3) ^ hibyte(lo<<1) ^ s[0]
    s := [a, s[0], s[1], s[2]]   (shift the 32-bit register one byte)
```

The keystream does not depend on the data, so the same routine encrypts.

## `.kb`: key events

After the seed:

| Bytes | Meaning |
|-------|---------|
| 1 | header byte 0 ("format"; `0x0F` in every file) |
| 1 | header byte 1 ("model"; `0x11` in Zenph files, `0xF0` in `banneret`) |
| 2 | length *n* of an opaque extension, **big-endian** (always 0 here) |
| *n* | extension bytes, skipped by the player |
| … | header text up to, not including, the first `0x7F`; printed by the player on start-up (Zenph: copyright line + CR LF) |
| … | event stream, beginning with that `0x7F` |
| 1 | `0xFF` end of performance |
| 19 | `0xFF` padding (Zenph files; never read) |

The two header bytes are stored by the player (0x218B, 0x2186) but I found no
code that reads them.

Because the text runs until the first `0x7F`, **the first event must be a
`0x7F` filler**. Text must not contain `0x7F`.

### Event encoding [0x04C4]

Each event is a key byte, optional velocity bytes, then a 1-byte delta: the
number of ticks until the next event.

| First byte *a* | Following bytes | Event |
|----------------|-----------------|-------|
| `0x00`–`0x7F` | Δ | key *a* released |
| `0x80` | Δ | marker (key 0, no velocity) |
| `0x81`–`0xFE`, then *b* = 1…3 | *c*, Δ | key *a*&0x7F pressed, velocity = *b*·256 + *c* (256–1023) |
| `0x81`–`0xFE`, then *b* = 0 or 4…255 | Δ | key *a*&0x7F pressed, velocity = *b* |
| `0xFF` | — | end of performance |

The time of an event is the sum of all previous deltas, so event *i* happens
at tick Σ Δ₀…Δᵢ₋₁.

* **Key numbers are MIDI note numbers** (60 = middle C). The collection uses
  1–109. 21–108 is the standard keyboard. 12–20 are the Bösendorfer Imperial's
  extra bass keys, and they still appear in the Steinway renderings, where they
  have nothing to play. **Key 11** appears about once per file, always at
  velocity 350 and very short. It looks like a Zenph synchronisation mark.
* **Keys 0 and 127 are fillers.** They never sound and only advance time.
  Gaps longer than 255 ticks are bridged with `7F FF 7F FF … 7F rr` (the
  original encoder fills greedily: the real event takes up to 255, then each
  filler takes up to 255). `0x00`/`0x80` occur rarely and seem to be markers.
* **Velocity is 10 bits and runs inverse: bigger means softer.** It looks like
  a hammer time-of-flight. The test file `10Sec-C9-400` plays key 108 at
  velocity 400, matching its name. Velocities 1–3 cannot be encoded. Against
  the paired Yamaha MIDI renderings, the median MIDI velocity is:

  | SE  | 40 | 72 | 104 | 136 | 168 | 200 | 232 | 264 | 296 | 312 |
  |-----|----|----|-----|-----|-----|-----|-----|-----|-----|-----|
  | MIDI| 90 | 76 | 63  | 54  | 46  | 39  | 33  | 29  | 25  | 22  |

  (`sepiano/se.DefaultCurve`, used by `se2mid`/`mid2se`.) In practice SE
  values run from 33 to 899, with 90% between 61 and 243. The instrument's own
  lookup tables (installed by `put290.exe`, `putb.exe`, …) turn these into
  actuator drive, so files are portable between SE pianos. Zenph nevertheless
  rendered each performance separately per instrument.

  The "Steinway…" MIDI files in `tempAnatolyBoes/` do not hold ordinary MIDI
  velocities: they carry roughly SE÷8.

## `.lp` / `.sp`: pedal positions

After the seed, a PackBits-style run-length stream of 8-bit positions
(0 = up, 255 = fully down; half-pedalling is continuous) [0x0AB7, 0x0AF3]:

| Control byte *c* | Followed by | Meaning |
|------------------|-------------|---------|
| `0x01`–`0x7F` | *c* bytes | *c* literal samples |
| `0x81`–`0xFF` | 1 byte *v* | *v* repeated *c*&0x7F times |
| `0x00`, `0x80` | — | invalid: the player aborts |

Sample *i* applies from tick 8·*i* (`.lp`) or 16·*i* (`.sp`). There is no end
marker: the stream must last at least as long as the `.kb` events. The player
refills pedal data in 512-byte requests and **aborts if the file runs out**
[0x0DB3]. Zenph files run about 1 s longer than the key stream.

The original encoder, reproduced exactly: any repeat of 2 or more becomes a
run. A long run is split into 127-sample chunks, and even a 1-sample
remainder stays a run. Everything else goes in literal blocks of up to 127.

## Writing files the player accepts

`sepiano/se` (`Performance.Save`, `KB.Encode`, `Pedal.Encode`) does all of
this:

1. seed `00 00 00 00` (no scrambling);
2. header `0F 11 00 00`, then text (CR LF, no `0x7F`);
3. a leading `7F` filler, then events in time order with deltas ≤ 255, split
   with `7F` fillers; then `FF`;
4. pedal files that cover the key stream plus a margin.

The SE instrument only shows 8.3 file names (8-character name, 3-character
extension) if the Z80 program asks for the name. The player never asks, so
long names play fine.

## Related formats seen here

* `emi.txt` is a text dump from **Bo2mf V30.07** (1990) of an older
  Bösendorfer recording: µs timestamps and 8-bit velocities. The file it was
  made from isn't in this collection.
* `.zpf` (Zenph text "performance file") and `.sfl` (RIFF) appear once, in
  `Hamb/Laufer20110701-…`. They are not used by the player.
* `KLS2WAV.EXE` and `Alter.exe` are unrelated DOS utilities (audio conversion
  and file maintenance).
