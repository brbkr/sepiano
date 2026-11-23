package link

import (
	"bytes"
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	f := Frame{Type: TypeDatAck, Ack: 7, Seq: 200, Chan: 2, Data: []byte{0xFF, 0xFF, 0xFF, 0x7F, 1, 2, 3}}
	var d Decoder
	stream := append([]byte{0, 0, 0xFF, 0xFF, 0xFF, 0xFF}, f.Encode()...) // extra FF before preamble
	stream = append(stream, Frame{Type: TypeIdle}.Encode()...)
	got := d.Feed(stream)
	if len(got) != 2 || got[0].String() != f.String() || got[1].Type != TypeIdle {
		t.Fatalf("decoded %v", got)
	}
	bad := f.Encode()
	bad[12] ^= 1
	if g := d.Feed(bad); len(g) != 0 || d.Bad != 1 {
		t.Fatalf("corrupt frame accepted: %v", g)
	}
}

// TestKnownCRC pins the CRC variant: a host idle frame is
// FF FF FF 7F 00 00 00 00 00 00 00 + CRC-32/IEEE of the 7 header bytes.
func TestKnownCRC(t *testing.T) {
	b := Frame{Type: TypeIdle}.Encode()
	want := []byte{0xFF, 0xFF, 0xFF, 0x7F, 0, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(b[:11], want) || len(b) != 15 {
		t.Fatalf("idle frame % x", b)
	}
}

// pair returns two connected, running links.
func pair(t *testing.T, baud int, flip, drop float64) (a, b *Link, stop func()) {
	pa, pb := NewPipe(flip, drop)
	a, b = New(), New()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, x := range []*Pump{
		{L: a, Port: pa, Baud: baud},
		{L: b, Port: pb, Baud: baud},
	} {
		wg.Add(1)
		go func(p *Pump) { defer wg.Done(); p.Run(ctx) }(x)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !(a.Up() && b.Up()) {
		if time.Now().After(deadline) {
			t.Fatal("handshake did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	return a, b, func() { cancel(); wg.Wait() }
}

func payload(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func transfer(t *testing.T, a, b *Link, n int) {
	t.Helper()
	ab, ba := payload(n, 10), payload(n, 20)
	var wg sync.WaitGroup
	recv := func(l *Link, ch int, want []byte) {
		defer wg.Done()
		got := make([]byte, 0, len(want))
		buf := make([]byte, 512)
		for len(got) < len(want) {
			k, err := l.Read(ch, buf)
			if err != nil {
				t.Errorf("read ch%d: %v", ch, err)
				return
			}
			got = append(got, buf[:k]...)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ch%d: data corrupted", ch)
		}
	}
	wg.Add(4)
	go func() { defer wg.Done(); a.Write(ChanFile, ab) }()
	go func() { defer wg.Done(); b.Write(ChanConsole, ba) }()
	go recv(b, ChanFile, ab)
	go recv(a, ChanConsole, ba)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("transfer timed out")
	}
}

func TestCleanTransfer(t *testing.T) {
	a, b, stop := pair(t, 2_000_000, 0, 0)
	defer stop()
	transfer(t, a, b, 100_000)
	if a.Retransmits+b.Retransmits > 0 {
		t.Logf("retransmits on a clean line: %d/%d", a.Retransmits, b.Retransmits)
	}
}

func TestNoisyTransfer(t *testing.T) {
	a, b, stop := pair(t, 2_000_000, 2e-4, 2e-4)
	defer stop()
	transfer(t, a, b, 100_000)
	t.Logf("retransmits %d/%d, naks %d/%d", a.Retransmits, b.Retransmits, a.Naks, b.Naks)
}

func TestFlowControl(t *testing.T) {
	a, b, stop := pair(t, 2_000_000, 0, 0)
	defer stop()
	data := payload(12_000, 3) // three times what the receiver buffers
	done := make(chan struct{})
	go func() { a.Write(ChanFile, data); close(done) }()
	time.Sleep(300 * time.Millisecond) // receiver not reading
	if n := b.Buffered(ChanFile); n > rxRingSize-1 || n < rxStopAt {
		t.Fatalf("receiver buffered %d bytes", n)
	}
	select {
	case <-done:
		t.Fatal("sender was not throttled")
	default:
	}
	got := make([]byte, 0, len(data))
	buf := make([]byte, 1000)
	for len(got) < len(data) {
		n, err := b.Read(ChanFile, buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, buf[:n]...)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("data corrupted under flow control")
	}
}

// At the real line rate the link should still move a file quickly enough.
func TestThroughputAt57600(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	a, b, stop := pair(t, 57600, 0, 0)
	defer stop()
	start := time.Now()
	transfer(t, a, b, 8131)
	el := time.Since(start)
	t.Logf("8131 bytes each way in %v (%.0f B/s)", el, 8131/el.Seconds())
}

// The instrument runs the original link logic. Start a hardened link against a
// Strict one with random timing many times; it must always come up.
func TestHandshakeAgainstOriginalLogic(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	for i := 0; i < 40; i++ {
		pa, pb := NewPipe(0, 0)
		a, b := New(), New()
		b.Strict = true
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		delay := time.Duration(rnd.Intn(30)) * time.Millisecond
		baud := []int{28800, 57600, 2_000_000}[rnd.Intn(3)]
		wg.Add(2)
		go func() { defer wg.Done(); (&Pump{L: b, Port: pb, Baud: baud}).Run(ctx) }()
		go func() {
			defer wg.Done()
			time.Sleep(delay)
			(&Pump{L: a, Port: pa, Baud: baud}).Run(ctx)
		}()
		deadline := time.Now().Add(time.Second)
		for !(a.Up() && b.Up()) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		up := a.Up() && b.Up()
		if up {
			// and data must flow both ways afterwards
			go a.Write(ChanConsole, []byte("hi"))
			go b.Write(ChanFile, []byte("yo"))
			buf := make([]byte, 2)
			if n, err := b.Read(ChanConsole, buf); err != nil || string(buf[:n]) != "hi" {
				t.Errorf("run %d: data a->b failed", i)
			}
		}
		cancel()
		wg.Wait()
		if !up {
			t.Fatalf("run %d (delay %v, %d baud): a up=%v b up=%v", i, delay, baud, a.Up(), b.Up())
		}
	}
}
