// Package link implements the serial link protocol spoken between the
// STAHNKE SE/PC host programs and the SE instrument's Z80 controller.
//
// Wire format of one frame (see docs/SE-SERIAL-PROTOCOL.md):
//
//	FF FF FF 7F | type | ack | seq | chan | count | data[count] | ts_lo ts_hi | crc32 (LE)
//
// count is at most 32; the CRC is CRC-32/IEEE over type..ts. Between frames
// the sender emits 0x00 filler when it has nothing queued.
package link

import (
	"fmt"
	"hash/crc32"
)

// Frame types.
const (
	TypeIdle   = 0x00 // keep-alive, no payload
	TypeSync1  = 0x01 // handshake: receiver moves to state 0
	TypeSync2  = 0x02 // handshake: receiver moves to state 1 (sent when unsynchronised)
	TypeSync3  = 0x03 // handshake: receiver moves to state 2 (connected)
	TypePurge  = 0x04 // channel purge acknowledgement
	TypeNak    = 0x08 // reject: resend starting at Ack
	TypeAck    = 0x09 // acknowledge everything before Ack
	TypeDatAck = 0x0A // data frame carrying an acknowledgement
	TypeData   = 0x0B // data frame without acknowledgement
)

// ChanFlow marks a flow-control frame: Data holds one stop flag per channel.
const ChanFlow = 0xFF

// Channels.
const (
	ChanBoot    = 0 // host->Z80: program image; Z80->host: ESC ends the session
	ChanConsole = 1 // host->Z80: keystrokes; Z80->host: text, ESC ends the session
	ChanFile    = 2 // file service requests from the Z80 and replies
	NumChannels = 3
)

// MaxData is the largest payload of one frame.
const MaxData = 32

// Sync is the frame preamble.
var Sync = [4]byte{0xFF, 0xFF, 0xFF, 0x7F}

// Frame is one link-layer frame.
type Frame struct {
	Type byte
	Ack  byte // acknowledged/rejected sequence number (types 8, 9, 0x0A)
	Seq  byte // sequence number of a data frame (0x0A, 0x0B)
	Chan byte
	Data []byte
	TS   uint16 // timestamp; the PC host always sends 0
}

func (f Frame) String() string {
	name := map[byte]string{0: "IDLE", 1: "SYNC1", 2: "SYNC2", 3: "SYNC3", 4: "PURGE", 8: "NAK", 9: "ACK", 0x0A: "DATA+ACK", 0x0B: "DATA"}[f.Type]
	if name == "" {
		name = fmt.Sprintf("TYPE%02X", f.Type)
	}
	s := name
	switch f.Type {
	case TypeNak, TypeAck:
		s += fmt.Sprintf(" ack=%d", f.Ack)
	case TypeDatAck:
		s += fmt.Sprintf(" ack=%d seq=%d", f.Ack, f.Seq)
	case TypeData:
		s += fmt.Sprintf(" seq=%d", f.Seq)
	case TypePurge:
		s += fmt.Sprintf(" chan=%d", f.Chan)
	}
	if f.Type == TypeDatAck || f.Type == TypeData {
		if f.Chan == ChanFlow {
			s += fmt.Sprintf(" FLOW stop=% x", f.Data)
		} else {
			s += fmt.Sprintf(" ch%d %q", f.Chan, f.Data)
		}
	}
	if f.TS != 0 {
		s += fmt.Sprintf(" ts=%d", f.TS)
	}
	return s
}

// Encode returns the frame's wire bytes.
func (f Frame) Encode() []byte {
	if len(f.Data) > MaxData {
		panic("link: frame payload too long")
	}
	b := make([]byte, 0, 4+5+len(f.Data)+2+4)
	b = append(b, Sync[:]...)
	b = append(b, f.Type, f.Ack, f.Seq, f.Chan, byte(len(f.Data)))
	b = append(b, f.Data...)
	b = append(b, byte(f.TS), byte(f.TS>>8))
	c := crc32.ChecksumIEEE(b[4:])
	return append(b, byte(c), byte(c>>8), byte(c>>16), byte(c>>24))
}

// Decoder reassembles frames from a byte stream. Corrupt frames (bad CRC or
// count) are dropped and counted; the decoder then hunts for the next preamble.
type Decoder struct {
	buf     []byte
	state   int // 0..3 preamble, 4 header, 5 body
	need    int
	Bad     int // frames dropped for CRC or length errors
	Skipped int // bytes discarded while hunting for a preamble (excluding 0x00 filler)
}

// Feed consumes bytes and returns any complete, valid frames.
func (d *Decoder) Feed(p []byte) []Frame {
	var out []Frame
	for _, c := range p {
		switch {
		case d.state < 4:
			if c == Sync[d.state] {
				d.state++
			} else if c == Sync[0] {
				// FF FF FF FF 7F: stay aligned on the most recent FFs.
				if d.state != 3 {
					d.state = 1
				}
			} else {
				if c != 0 {
					d.Skipped += d.state + 1
				}
				d.state = 0
			}
			if d.state == 4 {
				d.buf = d.buf[:0]
				d.need = 5
			}
		case d.state == 4:
			d.buf = append(d.buf, c)
			if len(d.buf) == 5 {
				n := int(c)
				if n > MaxData {
					d.Bad++
					d.state = 0
					continue
				}
				d.state = 5
				d.need = 5 + n + 2 + 4
			}
		default:
			d.buf = append(d.buf, c)
			if len(d.buf) == d.need {
				d.state = 0
				body := d.buf[:d.need-4]
				crc := d.buf[d.need-4:]
				want := crc32.ChecksumIEEE(body)
				got := uint32(crc[0]) | uint32(crc[1])<<8 | uint32(crc[2])<<16 | uint32(crc[3])<<24
				if got != want {
					d.Bad++
					continue
				}
				n := int(body[4])
				out = append(out, Frame{
					Type: body[0], Ack: body[1], Seq: body[2], Chan: body[3],
					Data: append([]byte(nil), body[5:5+n]...),
					TS:   uint16(body[5+n]) | uint16(body[6+n])<<8,
				})
			}
		}
	}
	return out
}
