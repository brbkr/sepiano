package main

import (
	"context"
	"time"

	"go.bug.st/serial"

	"sepiano/link"
)

// tapPort is the part of serial.Port that baud detection needs.
type tapPort interface {
	SetMode(*serial.Mode) error
	ResetInputBuffer() error
	Read([]byte) (int, error)
}

// anyFrames accepts a rate once a few frames with valid CRCs have been seen.
func anyFrames(fs []link.Frame) bool { return len(fs) >= 3 }

// afterHandshake accepts only frames that are sent once the link is up. While
// the PC program scans ports and rates it sends nothing but SYNC2, so those
// cannot fool the detection.
func afterHandshake(fs []link.Frame) bool {
	for _, f := range fs {
		switch f.Type {
		case link.TypeIdle, link.TypeAck, link.TypeNak, link.TypeData, link.TypeDatAck:
			return true
		}
	}
	return false
}

// detectBaud cycles p through rates, listening for window at each, until
// accept is satisfied by the frames decoded at one of them. It returns that
// rate and the bytes read at it, so they can still be decoded.
func detectBaud(ctx context.Context, p tapPort, rates []int, window time.Duration,
	accept func([]link.Frame) bool, status func(string)) (int, []byte, error) {
	buf := make([]byte, 1024)
	lastMsg := ""
	for round := 0; ctx.Err() == nil; round++ {
		heard := false
		for _, r := range rates {
			if err := p.SetMode(&serial.Mode{BaudRate: r}); err != nil {
				return 0, nil, err
			}
			p.ResetInputBuffer()
			var got []byte
			var dec link.Decoder
			var frames []link.Frame
			for end := time.Now().Add(window); time.Now().Before(end) && ctx.Err() == nil; {
				n, err := p.Read(buf)
				if err != nil {
					return 0, nil, err
				}
				got = append(got, buf[:n]...)
				frames = append(frames, dec.Feed(buf[:n])...)
				if accept(frames) {
					return r, got, nil
				}
			}
			heard = heard || len(got) > 0
		}
		msg := "  no traffic yet (start the PC program, or check the tap wiring)"
		if heard {
			msg = "  traffic, but no valid frames at any rate yet (still trying; check wiring, adapter levels, or a different rate with -baud)"
		}
		if msg != lastMsg {
			status(msg)
			lastMsg = msg
		}
	}
	return 0, nil, ctx.Err()
}
