package main

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"go.bug.st/serial"

	"sepiano/link"
)

// fakeTap returns, at each rate, whatever traffic[rate] produces; at rates
// without an entry it returns random bytes, like a line read at the wrong rate.
type fakeTap struct {
	rate    int
	traffic map[int]func() []byte
	rnd     *rand.Rand
	sets    int
}

func (f *fakeTap) SetMode(m *serial.Mode) error { f.rate = m.BaudRate; f.sets++; return nil }
func (f *fakeTap) ResetInputBuffer() error      { return nil }
func (f *fakeTap) Read(p []byte) (int, error) {
	time.Sleep(time.Millisecond)
	var b []byte
	if gen := f.traffic[f.rate]; gen != nil {
		b = gen()
	} else {
		b = make([]byte, 64)
		f.rnd.Read(b)
	}
	return copy(p, b), nil
}

func frames(t byte) func() []byte {
	return func() []byte { return link.Frame{Type: t}.Encode() }
}

func TestDetectBaud(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	quiet := func(string) {}
	for _, want := range []int{57600, 28800} {
		tap := &fakeTap{traffic: map[int]func() []byte{want: frames(link.TypeIdle)}, rnd: rand.New(rand.NewSource(1))}
		got, seen, err := detectBaud(ctx, tap, []int{57600, 28800}, 50*time.Millisecond, anyFrames, quiet)
		if err != nil || got != want || link.ValidFrames(seen) < 3 {
			t.Errorf("want %d: got %d (%v), %d frames kept", want, got, err, link.ValidFrames(seen))
		}
	}
}

// While play.exe scans, the PC line carries SYNC2 at 57600 even though the
// instrument runs at 28800; the PC-side criterion must not lock onto that.
func TestDetectBaudIgnoresScan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tap := &fakeTap{traffic: map[int]func() []byte{
		57600: frames(link.TypeSync2),
		28800: frames(link.TypeIdle),
	}, rnd: rand.New(rand.NewSource(2))}
	got, _, err := detectBaud(ctx, tap, []int{57600, 28800}, 50*time.Millisecond, afterHandshake, func(string) {})
	if err != nil || got != 28800 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestDetectBaudReportsSilence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	tap := &fakeTap{traffic: map[int]func() []byte{57600: func() []byte { return nil }, 28800: func() []byte { return nil }}}
	var msgs []string
	_, _, err := detectBaud(ctx, tap, []int{57600, 28800}, 20*time.Millisecond, anyFrames, func(m string) { msgs = append(msgs, m) })
	if err == nil {
		t.Fatal("detected a rate on a silent line")
	}
	if len(msgs) != 1 {
		t.Errorf("status messages: %q (want one, not repeated)", msgs)
	}
}
