package link

import "testing"

// echoPeer replays what the real SE firmware did in a capture: it answers each
// handshake frame with the same type, alternating with IDLE frames, and is up
// once it has received SYNC3.
type echoPeer struct {
	up   bool
	last byte
	odd  bool
}

func (p *echoPeer) receive(f Frame) {
	switch f.Type {
	case TypeSync1, TypeSync2, TypeSync3:
		p.last = f.Type
		if f.Type == TypeSync3 {
			p.up = true
		}
	}
}

func (p *echoPeer) next() Frame {
	p.odd = !p.odd
	if p.odd || p.last == 0 {
		return Frame{Type: TypeIdle}
	}
	t := p.last
	p.last = 0
	return Frame{Type: t}
}

// The link must not declare itself up before the instrument has seen SYNC3,
// even though the instrument sends IDLE frames throughout the handshake.
// Frames take `delay` steps to cross the cable in each direction, as on the
// real line where several frames are in flight.
func TestHandshakeWithEchoingFirmware(t *testing.T) {
	for delay := 0; delay <= 4; delay++ {
		l := New()
		p := &echoPeer{up: true} // connected from an earlier session
		toPeer := make([]Frame, delay)
		toLink := make([]Frame, delay)
		for i := range toPeer {
			toPeer[i], toLink[i] = Frame{Type: TypeIdle}, Frame{Type: TypeIdle}
		}
		for i := 0; i < 400 && !(l.Up() && p.up); i++ {
			toPeer = append(toPeer, l.Next())
			f := toPeer[0]
			toPeer = toPeer[1:]
			if f.Type == TypeSync2 {
				p.up = false // our SYNC2 restarts its handshake
			}
			p.receive(f)
			toLink = append(toLink, p.next())
			g := toLink[0]
			toLink = toLink[1:]
			wasUp := l.Up()
			l.Receive(g)
			if !wasUp && l.Up() && !p.up {
				t.Fatalf("delay %d, step %d: link came up before the instrument received SYNC3", delay, i)
			}
		}
		if !l.Up() || !p.up {
			t.Fatalf("delay %d: handshake incomplete: link up=%v, instrument up=%v", delay, l.Up(), p.up)
		}
	}
}
