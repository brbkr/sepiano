package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.bug.st/serial"

	"sepiano/link"
)

// Baud rates the PC host tries, in order, when none is configured.
var Bauds = []int{57600, 28800}

// Port is an open connection to the instrument: a serial port, or one end of
// a simulated cable. Reads must time out briefly rather than block (see
// link.Port).
type Port interface {
	link.Port
	Close() error
}

// modemPort is the part of serial.Port that controls the modem lines.
type modemPort interface {
	SetDTR(bool) error
	SetRTS(bool) error
	ResetInputBuffer() error
	ResetOutputBuffer() error
}

// Conn is an open serial connection with a running link.
type Conn struct {
	Link *link.Link
	Pump *link.Pump
	Baud int

	port   Port
	stop   context.CancelFunc
	exited chan struct{}
	err    error
}

// ConnectOptions control Connect.
type ConnectOptions struct {
	Baud    int           // 0 tries each of Bauds
	Wait    time.Duration // time allowed for the handshake per attempt (default 250ms, as the original)
	OnFrame func(out bool, f link.Frame)
	RawRx   io.Writer
	RawTx   io.Writer
}

// ErrNoResponse means the instrument did not answer the handshake.
var ErrNoResponse = errors.New("no response from instrument")

// Connect opens the serial port and completes the link handshake.
func Connect(ctx context.Context, path string, o ConnectOptions) (*Conn, error) {
	bauds := Bauds
	if o.Baud != 0 {
		bauds = []int{o.Baud}
	}
	if o.Wait == 0 {
		o.Wait = 250 * time.Millisecond
	}
	var last error
	for _, baud := range bauds {
		c, err := dial(ctx, path, baud, o)
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func dial(ctx context.Context, path string, baud int, o ConnectOptions) (*Conn, error) {
	port, err := serial.Open(path, &serial.Mode{BaudRate: baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
	if err != nil {
		return nil, err
	}
	// As the PC host: DTR and RTS on, no flow control, short read timeout.
	port.SetDTR(true)
	port.SetRTS(true)
	port.SetReadTimeout(5 * time.Millisecond)
	port.ResetInputBuffer()
	port.ResetOutputBuffer()
	c, err := ConnectOn(ctx, port, baud, o)
	if errors.Is(err, ErrNoResponse) {
		return nil, fmt.Errorf("%w on %s at %d baud", ErrNoResponse, path, baud)
	} else if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ConnectOn completes the link handshake over an already open port, such as
// one end of a simulated cable. The port is closed if the handshake fails.
func ConnectOn(ctx context.Context, port Port, baud int, o ConnectOptions) (*Conn, error) {
	if o.Wait == 0 {
		o.Wait = 250 * time.Millisecond
	}
	l := link.New()
	p := &link.Pump{L: l, Port: port, Baud: baud, OnFrame: o.OnFrame, RawRx: o.RawRx, RawTx: o.RawTx}
	pctx, stop := context.WithCancel(ctx)
	c := &Conn{Link: l, Pump: p, Baud: baud, port: port, stop: stop, exited: make(chan struct{})}
	p.Silence = o.Wait + 500*time.Millisecond
	go func() { c.err = p.Run(pctx); close(c.exited) }()

	deadline := time.After(o.Wait)
	for !l.Up() {
		select {
		case <-deadline:
			c.Close()
			return nil, fmt.Errorf("%w at %d baud", ErrNoResponse, baud)
		case <-c.exited:
			c.Close()
			return nil, c.err
		case <-time.After(2 * time.Millisecond):
		}
	}
	p.Silence = 750 * time.Millisecond
	return c, nil
}

// Exited is closed when the link stops; Err then says why.
func (c *Conn) Exited() <-chan struct{} { return c.exited }

// Err is the reason the link stopped.
func (c *Conn) Err() error { return c.err }

// Close stops the link and releases the port, dropping RTS and DTR as the
// original does.
func (c *Conn) Close() error {
	c.stop()
	select {
	case <-c.exited:
	case <-time.After(time.Second):
	}
	if m, ok := c.port.(modemPort); ok {
		m.ResetOutputBuffer()
		m.SetRTS(false)
		m.SetDTR(false)
	}
	return c.port.Close()
}

// Session runs one Z80 program on a connected instrument.
type Session struct {
	Program *Program
	Files   *FileService
	Console io.Writer     // text the program prints (channel 1)
	Keys    <-chan []byte // keystrokes for the program, as PC console codes
}

// ErrQuit is returned when the Z80 program ends the session normally.
var ErrQuit = errors.New("host: program finished")

// Run uploads the program over l and serves it until it signals the end
// (ESC on channel 0 or 1), the context ends, or the link stops (linkExited).
func (s *Session) Run(ctx context.Context, l *link.Link, linkExited <-chan struct{}) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	end := make(chan error, 4)
	finish := func(err error) {
		select {
		case end <- err:
		default:
		}
	}
	go func() {
		if _, err := l.Write(link.ChanBoot, s.Program.Image); err != nil {
			finish(err)
		}
	}()
	go func() { // channel 0: ESC from the program means it is done
		buf := make([]byte, 64)
		for {
			n, err := l.Read(link.ChanBoot, buf)
			if err != nil {
				finish(err)
				return
			}
			for _, b := range buf[:n] {
				if b&0x7F == 0x1B {
					finish(ErrQuit)
					return
				}
			}
		}
	}()
	go func() { // channel 1: console text
		buf := make([]byte, 256)
		for {
			n, err := l.Read(link.ChanConsole, buf)
			if err != nil {
				finish(err)
				return
			}
			b := buf[:n]
			for i, x := range b {
				if x == 0x1B {
					s.Console.Write(b[:i])
					finish(ErrQuit)
					return
				}
			}
			s.Console.Write(b)
		}
	}()
	go func() { finish(s.Files.Serve(link.Channel{L: l, Ch: link.ChanFile})) }()
	if s.Keys != nil {
		go func() {
			for {
				select {
				case k, ok := <-s.Keys:
					if !ok {
						return
					}
					if _, err := l.Write(link.ChanConsole, k); err != nil {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	var err error
	select {
	case err = <-end:
	case <-linkExited:
		if err = l.Err(); err == nil {
			err = errors.New("link stopped")
		}
	case <-ctx.Done():
		err = ctx.Err()
	}
	if errors.Is(err, ErrQuit) {
		// Let the acknowledgement of the final frames go out.
		t := time.Now()
		for !l.Drained() && time.Since(t) < 200*time.Millisecond {
			time.Sleep(5 * time.Millisecond)
		}
	}
	return err
}
