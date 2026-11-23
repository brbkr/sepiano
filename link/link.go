package link

import (
	"errors"
	"sync"
)

// Buffer sizes and flow-control thresholds, as in the PC host.
const (
	txRingSize = 256  // per-channel outgoing buffer
	rxRingSize = 4096 // per-channel incoming buffer
	rxStopAt   = 0xAA9
	rxResumeAt = 0x7FF
	window     = 8 // data frames in flight before go-back-N

	// quietUp is how many consecutive frames without a handshake frame, while
	// waiting in state 0, mean the peer is already up. The instrument
	// alternates IDLE and handshake frames, so during a handshake it never
	// sends more than one IDLE in a row.
	quietUp = 16
)

// States of the handshake.
const (
	stateNone = -1
	stateS0   = 0
	stateS1   = 1
	stateUp   = 2
)

// ErrProtocol reports a frame type the protocol does not define.
var ErrProtocol = errors.New("link: unexpected frame type from peer")

// ErrReset means the instrument restarted the handshake during a session.
var ErrReset = errors.New("link: instrument reset the link")

type ring struct {
	buf        []byte
	head, tail int // read at head, write at tail
}

func newRing(n int) ring        { return ring{buf: make([]byte, n)} }
func (r *ring) len() int        { return (r.tail - r.head + len(r.buf)) % len(r.buf) }
func (r *ring) free() int       { return len(r.buf) - 1 - r.len() }
func (r *ring) reset()          { r.head, r.tail = 0, 0 }
func (r *ring) put(b byte)      { r.buf[r.tail] = b; r.tail = (r.tail + 1) % len(r.buf) }
func (r *ring) get() (b byte)   { b = r.buf[r.head]; r.head = (r.head + 1) % len(r.buf); return }
func (r *ring) peek(i int) byte { return r.buf[(r.head+i)%len(r.buf)] }

// Link is one end of the protocol. It is symmetric: the same code works as
// the PC host or, for tests, as a stand-in for the instrument.
//
// The link has no goroutines of its own. A driver (see Pump) feeds it
// received frames with Receive and asks it for frames to transmit with Next.
// Applications use Read and Write on channels from other goroutines.
type Link struct {
	mu   sync.Mutex
	cond *sync.Cond
	err  error

	state       int
	hello       []byte  // handshake frames owed to the peer
	quiet       int     // consecutive non-handshake frames received in state 0
	rxq         []Frame // Strict: received frames not yet handled
	recvNext    byte    // next sequence number expected from the peer
	needAck     bool
	needNak     bool
	ackPending  bool
	ackSeq      byte
	nakPending  bool
	nakSeq      byte
	sendSeq     byte
	unacked     []*Frame // sent data frames awaiting acknowledgement, oldest first
	resend      int      // index into unacked of the next frame to retransmit, or -1
	purge       int      // channel being purged, or -1
	purgeDone   bool
	flowDirty   bool
	localStop   [NumChannels]bool // we asked the peer to stop sending on ch
	peerStop    [NumChannels]bool // the peer asked us to stop sending on ch
	tx          [NumChannels]ring
	rx          [NumChannels]ring
	upCallbacks []func()

	// Strict disables this package's handshake hardening so the link behaves
	// exactly like the original host (used to test against original logic).
	Strict bool

	// Statistics.
	Retransmits int
	Naks        int
}

// New returns a link in the unsynchronised state.
func New() *Link {
	l := &Link{state: stateNone, resend: -1, purge: -1}
	l.cond = sync.NewCond(&l.mu)
	for i := range l.tx {
		l.tx[i] = newRing(txRingSize)
		l.rx[i] = newRing(rxRingSize)
	}
	return l
}

// Up reports whether the handshake has completed.
func (l *Link) Up() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == stateUp
}

// Err returns the error that stopped the link, if any.
func (l *Link) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Stats returns the retransmission and NAK counters, safely while the link runs.
func (l *Link) Stats() (retransmits, naks int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Retransmits, l.Naks
}

// Fail stops the link; blocked readers and writers return err.
func (l *Link) Fail(err error) {
	l.mu.Lock()
	if l.err == nil {
		l.err = err
	}
	l.mu.Unlock()
	l.cond.Broadcast()
}

// Receive processes one frame from the peer.
func (l *Link) Receive(f Frame) {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer l.cond.Broadcast()
	if l.Strict {
		// The original queues up to 16 received frames and handles one per
		// transmit opportunity.
		if len(l.rxq) < 16 {
			l.rxq = append(l.rxq, f)
		}
		return
	}
	l.receive(f)
}

func (l *Link) receive(f Frame) {
	handshake := f.Type == TypeSync1 || f.Type == TypeSync2 || f.Type == TypeSync3
	if handshake || l.state != stateS0 {
		l.quiet = 0
	} else if l.quiet++; !l.Strict && l.quiet >= quietUp {
		// Waiting in state 0 for a SYNC3 that never comes: a peer that went
		// 1 -> 2 directly sends nothing but idle or data frames from then on.
		// The real instrument interleaves IDLE with its handshake frames, so
		// only a long run without any handshake frame counts. The original
		// host lacks this rule and would wait for ever.
		l.state = stateUp
	}
	switch f.Type {
	case TypeIdle:
		return
	case TypeSync1, TypeSync2, TypeSync3:
		next := map[byte]int{TypeSync1: stateS0, TypeSync2: stateS1, TypeSync3: stateUp}[f.Type]
		if !l.Strict && l.state == stateUp && next != stateUp {
			// The instrument restarted the handshake mid-session: its Z80
			// program and sequence numbers are gone.
			if l.err == nil {
				l.err = ErrReset
			}
			return
		}
		if next != l.state && !l.Strict {
			// Make sure the peer sees one frame for every state we pass
			// through, even if the next transition arrives before Next runs.
			l.hello = append(l.hello, map[int]byte{stateS0: TypeSync3, stateS1: TypeSync1, stateUp: TypeSync3}[next])
		}
		l.state = next
		return
	case TypePurge:
		if l.purge >= 0 && int(f.Chan) == l.purge {
			l.rx[l.purge].reset()
			l.tx[l.purge].reset()
			l.localStop[l.purge] = false
			l.peerStop[l.purge] = false
			l.purge = -1
			l.purgeDone = true
		}
		return
	case TypeNak:
		if l.state == stateUp {
			l.nakSeq, l.nakPending = f.Ack, true
			l.Naks++
		}
		return
	case TypeAck:
		if l.state == stateUp {
			l.ackSeq, l.ackPending = f.Ack, true
		}
		return
	case TypeDatAck:
		if l.state != stateUp {
			return
		}
		l.ackSeq, l.ackPending = f.Ack, true
	case TypeData:
		if l.state != stateUp {
			return
		}
	default:
		if l.err == nil {
			l.err = ErrProtocol
		}
		return
	}
	// data frame
	if f.Seq == l.recvNext {
		l.deliver(f)
		l.recvNext++
	} else if int8(f.Seq-l.recvNext) > 0 {
		l.needNak = true // a frame was lost; ask for a resend from recvNext
		return
	}
	l.needAck = true // also re-acknowledge duplicates
}

func (l *Link) deliver(f Frame) {
	if f.Chan == ChanFlow {
		for i := 0; i < NumChannels && i < len(f.Data); i++ {
			l.peerStop[i] = f.Data[i] != 0
		}
		return
	}
	if int(f.Chan) >= NumChannels {
		return
	}
	r := &l.rx[f.Chan]
	fill := r.len()
	if len(f.Data) > r.free() {
		if l.err == nil {
			l.err = errors.New("link: receive buffer overflow")
		}
		return
	}
	for _, b := range f.Data {
		r.put(b)
	}
	if !l.localStop[f.Chan] && fill > rxStopAt {
		l.localStop[f.Chan], l.flowDirty = true, true
	}
}

// Next returns the next frame to transmit. It is called whenever the
// transmitter is ready for another frame, and always returns one: idle
// frames keep the line busy when there is nothing else to send.
func (l *Link) Next() Frame {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer l.cond.Broadcast()
	if l.Strict && len(l.rxq) > 0 {
		l.receive(l.rxq[0])
		l.rxq = l.rxq[1:]
	}
	if len(l.hello) > 0 {
		t := l.hello[0]
		l.hello = l.hello[1:]
		return Frame{Type: t}
	}
	switch l.state {
	case stateNone:
		return Frame{Type: TypeSync2}
	case stateS0:
		return Frame{Type: TypeSync3}
	case stateS1:
		return Frame{Type: TypeSync1}
	}
	if l.purge >= 0 {
		for _, u := range l.unacked {
			if int(u.Chan) == l.purge {
				u.Data = nil
			}
		}
		return Frame{Type: TypePurge, Chan: byte(l.purge)}
	}
	if l.nakPending {
		l.nakPending = false
		if l.resend < 0 {
			for len(l.unacked) > 0 {
				d := int8(l.unacked[0].Seq - l.nakSeq)
				if d == 0 {
					l.resend = 0
					break
				}
				if d > 0 {
					break
				}
				l.unacked = l.unacked[1:] // implicitly acknowledged
			}
		}
	}
	if l.ackPending {
		l.ackPending = false
		n := 0
		for n < len(l.unacked) && int8(l.unacked[n].Seq-l.ackSeq) < 0 {
			n++
		}
		l.unacked = l.unacked[n:]
		if l.resend >= 0 {
			l.resend = max(0, l.resend-n)
			if l.resend >= len(l.unacked) {
				l.resend = -1
			}
		}
	}
	if l.needNak {
		l.needNak = false
		return Frame{Type: TypeNak, Ack: l.recvNext}
	}
	if n := len(l.unacked); l.resend < 0 && n > 0 && int(l.unacked[n-1].Seq-l.unacked[0].Seq)+1 >= window {
		l.resend = 0 // window full: go back to the oldest unacknowledged frame
	}
	if l.resend >= 0 {
		f := l.withAck(*l.unacked[l.resend])
		l.resend++
		if l.resend >= len(l.unacked) {
			l.resend = -1
		}
		l.Retransmits++
		return f
	}
	if f, ok := l.payload(); ok {
		f.Seq = l.sendSeq
		l.sendSeq++
		kept := f
		l.unacked = append(l.unacked, &kept)
		return l.withAck(f)
	}
	if len(l.unacked) > 0 {
		// Nothing new: keep offering the oldest unacknowledged frame.
		return l.withAck(*l.unacked[0])
	}
	if l.needAck {
		l.needAck = false
		return Frame{Type: TypeAck, Ack: l.recvNext}
	}
	return Frame{Type: TypeIdle}
}

func (l *Link) withAck(f Frame) Frame {
	if l.needAck {
		f.Type, f.Ack = TypeDatAck, l.recvNext
		l.needAck = false
	} else {
		f.Type, f.Ack = TypeData, 0
	}
	return f
}

// payload builds the next data frame: a pending flow-control update first,
// then channel data, lowest channel first.
func (l *Link) payload() (Frame, bool) {
	if l.flowDirty {
		l.flowDirty = false
		d := make([]byte, NumChannels)
		for i, s := range l.localStop {
			if s {
				d[i] = 0xFF
			}
		}
		return Frame{Chan: ChanFlow, Data: d}, true
	}
	for ch := 0; ch < NumChannels; ch++ {
		if l.localStop[ch] && l.rx[ch].len() < rxResumeAt {
			l.localStop[ch], l.flowDirty = false, true
		}
		r := &l.tx[ch]
		n := min(r.len(), MaxData)
		if n == 0 || l.peerStop[ch] {
			continue
		}
		d := make([]byte, n)
		for i := range d {
			d[i] = r.get()
		}
		return Frame{Chan: byte(ch), Data: d}, true
	}
	return Frame{}, false
}

// Write queues bytes for the peer on channel ch, blocking while the channel's
// outgoing buffer is full.
func (l *Link) Write(ch int, p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, b := range p {
		for l.tx[ch].free() == 0 && l.err == nil {
			l.cond.Wait()
		}
		if l.err != nil {
			return i, l.err
		}
		l.tx[ch].put(b)
	}
	return len(p), nil
}

// Read returns at least one byte received on channel ch, blocking until data
// arrives or the link fails.
func (l *Link) Read(ch int, p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.rx[ch].len() == 0 && l.err == nil {
		l.cond.Wait()
	}
	if l.rx[ch].len() == 0 {
		return 0, l.err
	}
	n := 0
	for n < len(p) && l.rx[ch].len() > 0 {
		p[n] = l.rx[ch].get()
		n++
	}
	return n, nil
}

// Buffered returns how many received bytes are waiting on channel ch.
func (l *Link) Buffered(ch int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rx[ch].len()
}

// Drained reports whether everything written has been sent and acknowledged.
func (l *Link) Drained() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.tx {
		if r.len() > 0 {
			return false
		}
	}
	return len(l.unacked) == 0
}

// Channel adapts one channel to io.ReadWriter.
type Channel struct {
	L  *Link
	Ch int
}

func (c Channel) Read(p []byte) (int, error)  { return c.L.Read(c.Ch, p) }
func (c Channel) Write(p []byte) (int, error) { return c.L.Write(c.Ch, p) }

// ReadByte blocks for one byte.
func (c Channel) ReadByte() (byte, error) {
	var b [1]byte
	_, err := c.L.Read(c.Ch, b[:])
	return b[0], err
}
