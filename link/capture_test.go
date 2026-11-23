package link

import (
	"bytes"
	"os"
	"testing"
)

// testdata/playexe-scan-read-at-57600.bin is real traffic: the original
// play.exe (V50.45) on Windows XP scanning for the instrument, recorded at
// 57600 baud by a Raspberry Pi on the other end of a null-modem cable, with no
// instrument answering. It sends SYNC2 for 250 ms at 57600, then for 250 ms
// at 28800 (garbage when read at 57600), then one stray frame at 57600.
func TestRealPlayExeHandshakeCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/playexe-scan-read-at-57600.bin")
	if err != nil {
		t.Fatal(err)
	}
	var d Decoder
	frames := d.Feed(raw)
	if len(frames) != 92 || d.Bad != 0 {
		t.Fatalf("decoded %d frames, %d bad; want 92, 0", len(frames), d.Bad)
	}
	for i, f := range frames {
		if f.Type != TypeSync2 || f.Ack != 0 || f.Seq != 0 || f.Chan != 0 || len(f.Data) != 0 || f.TS != 0 {
			t.Fatalf("frame %d: %v", i, f)
		}
	}
	// Our encoder produces exactly the bytes the original sends, each
	// 16-byte refill being one frame plus one 0x00 filler byte.
	chunk := append(Frame{Type: TypeSync2}.Encode(), 0x00)
	if !bytes.HasPrefix(raw, bytes.Repeat(chunk, 91)) {
		t.Fatal("first 91 frames are not byte-identical to our encoding")
	}
}

// testdata/piano-handshake-57600.bin is what the real SE instrument sent
// while sepiano's sehost -probe connected to it at 57600 baud (via a PL2303
// adapter on a Raspberry Pi). The instrument started out connected from an
// earlier session, then echoed each handshake step back, always alternating
// with IDLE frames, then settled into IDLE frames only.
func TestRealInstrumentHandshakeCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/piano-handshake-57600.bin")
	if err != nil {
		t.Fatal(err)
	}
	var d Decoder
	frames := d.Feed(raw)
	if d.Bad > 1 { // the port opened in the middle of a frame
		t.Fatalf("%d bad frames", d.Bad)
	}
	stage := map[byte]int{TypeSync2: 1, TypeSync1: 2, TypeSync3: 3}
	last, prevSync, firstSync3 := 0, false, -1
	for i, f := range frames {
		s, isSync := stage[f.Type]
		switch {
		case isSync:
			if prevSync {
				t.Errorf("frame %d: two handshake frames in a row", i)
			}
			if s < last {
				t.Errorf("frame %d: %v after a later handshake step", i, f)
			}
			last = s
			if s == 3 && firstSync3 < 0 {
				firstSync3 = i
			}
		case f.Type != TypeIdle:
			t.Errorf("frame %d: unexpected %v", i, f)
		}
		prevSync = isSync
	}
	if last != 3 || len(frames)-firstSync3 < 100 {
		t.Fatalf("handshake did not end with SYNC3 then IDLE frames")
	}

	// Our link, fed these frames, must come up exactly at the first SYNC3:
	// the IDLE frames the instrument sends mid-handshake must not count.
	l := New()
	for i, f := range frames {
		l.Receive(f)
		if up := l.Up(); up != (i >= firstSync3) {
			t.Fatalf("frame %d (%v): link up=%v", i, f, up)
		}
	}
}
