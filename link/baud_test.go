package link

import (
	"math/rand"
	"testing"
)

// uart renders bytes as an 8N1 line at txBaud and decodes the line with a
// receiver running at rxBaud, as a real UART would: wait for the line to go
// low, then sample each bit in its middle. Framing errors still deliver a
// byte, as most drivers do.
func uart(data []byte, txBaud, rxBaud float64) []byte {
	var bits []byte
	for i := 0; i < 20; i++ {
		bits = append(bits, 1) // idle before the first byte
	}
	for _, b := range data {
		bits = append(bits, 0)
		for i := 0; i < 8; i++ {
			bits = append(bits, b>>i&1)
		}
		bits = append(bits, 1)
	}
	end := float64(len(bits)) / txBaud
	level := func(t float64) byte {
		i := int(t * txBaud)
		if i >= len(bits) {
			return 1
		}
		return bits[i]
	}
	var out []byte
	step := 1 / (txBaud * 16)
	for t := 0.0; t < end; {
		if level(t) != 0 {
			t += step
			continue
		}
		var b byte
		for i := 0; i < 8; i++ {
			b |= level(t+(float64(i)+1.5)/rxBaud) << i
		}
		out = append(out, b)
		t += 9.5 / rxBaud // stop bit sample; then hunt for the next start bit
	}
	return out
}

func lineTraffic(n int) []byte {
	rnd := rand.New(rand.NewSource(5))
	var b []byte
	for seq := 0; len(b) < n; seq++ {
		if seq%3 == 0 {
			b = append(b, Frame{Type: TypeIdle}.Encode()...)
			continue
		}
		d := make([]byte, rnd.Intn(MaxData+1))
		rnd.Read(d)
		b = append(b, Frame{Type: TypeData, Seq: byte(seq), Chan: byte(seq % 3), Data: d}.Encode()...)
	}
	return b
}

func TestValidFramesDiscriminatesBaud(t *testing.T) {
	traffic := lineTraffic(20_000)
	for _, c := range []struct {
		tx, rx float64
		ok     bool
	}{
		{57600, 57600, true}, {28800, 28800, true},
		{57600, 28800, false}, {28800, 57600, false},
		{57600, 57600 * 1.02, true}, // 2% clock error is within UART tolerance
	} {
		n := ValidFrames(uart(traffic, c.tx, c.rx))
		if c.ok && n < 100 || !c.ok && n != 0 {
			t.Errorf("sent at %.0f, read at %.0f: %d valid frames", c.tx, c.rx, n)
		}
	}
}
