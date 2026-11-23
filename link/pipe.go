package link

import (
	"io"
	"math/rand"
	"sync"
	"time"
)

// wire is one direction of a simulated serial line.
type wire struct {
	mu      sync.Mutex
	cond    *sync.Cond
	buf     []byte
	rnd     *rand.Rand
	flip    float64 // probability a byte is corrupted
	drop    float64 // probability a byte is lost
	closed  bool
	timeout time.Duration
}

func newWire(seed int64, flip, drop float64) *wire {
	w := &wire{rnd: rand.New(rand.NewSource(seed)), flip: flip, drop: drop, timeout: 5 * time.Millisecond}
	w.cond = sync.NewCond(&w.mu)
	return w
}

func (w *wire) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if w.rnd.Float64() < w.drop {
			continue
		}
		if w.rnd.Float64() < w.flip {
			b ^= byte(1 << w.rnd.Intn(8))
		}
		w.buf = append(w.buf, b)
	}
	w.cond.Broadcast()
	return len(p), nil
}

func (w *wire) Read(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) == 0 && !w.closed {
		timer := time.AfterFunc(w.timeout, func() { w.mu.Lock(); w.cond.Broadcast(); w.mu.Unlock() })
		w.cond.Wait()
		timer.Stop()
	}
	if len(w.buf) == 0 && w.closed {
		return 0, io.EOF
	}
	n := copy(p, w.buf)
	w.buf = w.buf[n:]
	return n, nil
}

func (w *wire) close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.cond.Broadcast()
}

type duplex struct{ r, w *wire }

func (d duplex) Read(p []byte) (int, error)  { return d.r.Read(p) }
func (d duplex) Write(p []byte) (int, error) { return d.w.Write(p) }

// Close unplugs the cable: once buffered bytes are read, both ends' reads
// return io.EOF.
func (d duplex) Close() error {
	d.r.close()
	d.w.close()
	return nil
}

// NewPipe returns the two ends of a simulated serial cable. Each direction
// corrupts a byte with probability flip and loses one with probability drop.
// Reads time out after a few milliseconds, like a serial port. Both ends also
// implement io.Closer; closing either one unplugs the cable.
func NewPipe(flip, drop float64) (Port, Port) {
	ab, ba := newWire(1, flip, drop), newWire(2, flip, drop)
	return duplex{r: ba, w: ab}, duplex{r: ab, w: ba}
}
