package se

import (
	"errors"
	"fmt"
)

// Pedal is a decoded .lp (damper) or .sp (soft) file: one position sample
// (0 = released, 255 = fully depressed) per DamperPeriod or SoftPeriod ticks.
type Pedal struct {
	Seed    [4]byte
	Samples []byte
}

// ParsePedal decodes a pedal file. The body is PackBits-style run-length
// coded: a control byte c < 0x80 is followed by c literal samples; c > 0x80
// is followed by one sample repeated c&0x7F times. Counts of zero are errors.
func ParsePedal(raw []byte) (*Pedal, error) {
	if len(raw) < 4 {
		return nil, errShort
	}
	d := append([]byte(nil), raw...)
	Crypt(d)
	p := &Pedal{Seed: [4]byte{d[0], d[1], d[2], d[3]}}
	for i := 4; i < len(d); {
		c := d[i]
		i++
		n := int(c & 0x7F)
		if n == 0 {
			return nil, fmt.Errorf("se: zero pedal count at offset %d", i-1)
		}
		if c < 0x80 {
			if i+n > len(d) {
				return nil, errShort
			}
			p.Samples = append(p.Samples, d[i:i+n]...)
			i += n
		} else {
			if i >= len(d) {
				return nil, errShort
			}
			for range n {
				p.Samples = append(p.Samples, d[i])
			}
			i++
		}
	}
	return p, nil
}

// PedalMinRun is the shortest repeat the encoder stores as a run. Matching
// the original encoder lets parsed files re-encode byte for byte.
const PedalMinRun = 2

// Encode run-length codes the samples.
func (p *Pedal) Encode() ([]byte, error) {
	if len(p.Samples) == 0 {
		return nil, errors.New("se: empty pedal stream")
	}
	out := append([]byte(nil), p.Seed[:]...)
	s := p.Samples
	var lit []byte
	flush := func() {
		for len(lit) > 0 {
			n := min(len(lit), 127)
			out = append(out, byte(n))
			out = append(out, lit[:n]...)
			lit = lit[n:]
		}
	}
	for i := 0; i < len(s); {
		r := 1
		for i+r < len(s) && s[i+r] == s[i] {
			r++
		}
		if r >= PedalMinRun {
			// Whole run first, split into 127-sample chunks; even a
			// one-sample remainder stays a run, as in the original encoder.
			flush()
			for left := r; left > 0; left -= min(left, 127) {
				out = append(out, 0x80|byte(min(left, 127)), s[i])
			}
			i += r
			continue
		}
		lit = append(lit, s[i])
		i++
		if len(lit) == 127 {
			flush()
		}
	}
	flush()
	Crypt(out)
	return out, nil
}

// At returns the pedal position in effect at tick t for a stream sampled every
// period ticks. Past the end of the stream the last sample holds.
func (p *Pedal) At(t uint32, period uint32) byte {
	if len(p.Samples) == 0 {
		return 0
	}
	i := int(t / period)
	if i >= len(p.Samples) {
		i = len(p.Samples) - 1
	}
	return p.Samples[i]
}
