package link

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Port is a serial port. Read must return (0, nil) after a short timeout
// when no data is available rather than blocking indefinitely.
type Port interface {
	io.Reader
	io.Writer
}

// ErrSilent means the peer stopped transmitting.
var ErrSilent = errors.New("link: no data from instrument")

// Pump drives a Link over a Port.
//
// The original host refills the UART only when Windows reports EV_TXEMPTY,
// sixteen bytes at a time, so the line is always busy but never more than one
// FIFO ahead. USB serial adapters report EV_TXEMPTY unreliably and buffer
// kilobytes, which delays acknowledgements by hundreds of milliseconds. Pump
// instead paces writes by the clock, slightly under line rate, so whatever the
// adapter, no more than Headroom bytes wait in the transmit path.
type Pump struct {
	L    *Link
	Port Port
	Baud int

	Headroom int           // bytes allowed ahead of line rate (default 32)
	Silence  time.Duration // receive watchdog (default 750ms, as the original)
	Tick     time.Duration // scheduling interval (default 2ms)

	OnFrame func(out bool, f Frame) // optional trace hook (called with the link unlocked)
	RawRx   io.Writer               // optional capture of received bytes
	RawTx   io.Writer               // optional capture of transmitted bytes

	// Stamp, if set, supplies the ts field of each transmitted frame. The PC
	// host leaves it unset (ts = 0); a simulated instrument uses it to send
	// its performance clock.
	Stamp func() uint16

	Decoder Decoder
	TxBytes int64
	RxBytes int64
}

// Run pumps until ctx is cancelled or the link fails.
func (p *Pump) Run(ctx context.Context) error {
	if p.Headroom == 0 {
		p.Headroom = 32
	}
	if p.Silence == 0 {
		p.Silence = 750 * time.Millisecond
	}
	if p.Tick == 0 {
		p.Tick = 2 * time.Millisecond
	}
	rate := float64(p.Baud) / 10 * 0.97 // bytes per second, 8N1, with margin

	rxc := make(chan []byte, 64)
	rerr := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		buf := make([]byte, 1024)
		for ctx.Err() == nil {
			n, err := p.Port.Read(buf)
			if err != nil {
				rerr <- err
				return
			}
			if n > 0 {
				select {
				case rxc <- append([]byte(nil), buf[:n]...):
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	tick := time.NewTicker(p.Tick)
	defer tick.Stop()
	start := time.Now()
	lastRx := start
	var sent float64 // bytes written since start (paced budget)
	var cur []byte   // remainder of the frame being transmitted
	out := make([]byte, 0, 256)
	for {
		select {
		case <-ctx.Done():
			p.L.Fail(ctx.Err())
			return ctx.Err()
		case err := <-rerr:
			err = fmt.Errorf("link: read: %w", err)
			p.L.Fail(err)
			return err
		case b := <-rxc:
			lastRx = time.Now()
			p.RxBytes += int64(len(b))
			if p.RawRx != nil {
				p.RawRx.Write(b)
			}
			for _, f := range p.Decoder.Feed(b) {
				if p.OnFrame != nil {
					p.OnFrame(false, f)
				}
				p.L.Receive(f)
			}
			if err := p.L.Err(); err != nil {
				return err
			}
			continue
		case <-tick.C:
		}
		now := time.Now()
		if now.Sub(lastRx) > p.Silence {
			p.L.Fail(ErrSilent)
			return ErrSilent
		}
		budget := now.Sub(start).Seconds()*rate + float64(p.Headroom)
		if budget-sent > float64(4*p.Headroom) {
			// We fell behind (scheduler stall); don't burst to catch up.
			sent = budget - float64(p.Headroom)
		}
		out = out[:0]
		for sent+float64(len(out)) < budget {
			if len(cur) == 0 {
				f := p.L.Next()
				if p.Stamp != nil {
					f.TS = p.Stamp()
				}
				if p.OnFrame != nil {
					p.OnFrame(true, f)
				}
				cur = f.Encode()
			}
			n := min(len(cur), int(budget-sent)-len(out)+1)
			out = append(out, cur[:n]...)
			cur = cur[n:]
		}
		if len(out) == 0 {
			continue
		}
		if _, err := p.Port.Write(out); err != nil {
			err = fmt.Errorf("link: write: %w", err)
			p.L.Fail(err)
			return err
		}
		if p.RawTx != nil {
			p.RawTx.Write(out)
		}
		sent += float64(len(out))
		p.TxBytes += int64(len(out))
		if err := p.L.Err(); err != nil {
			return err
		}
	}
}
