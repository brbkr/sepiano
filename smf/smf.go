// Package smf is a minimal Standard MIDI File reader and writer: enough for
// piano performances (notes, controllers, tempo), with no dependencies.
package smf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// Message is a channel message at an absolute time.
type Message struct {
	Time   time.Duration
	Status byte // 0x80 note off, 0x90 note on, 0xB0 control change, ... (channel in low nibble)
	Data1  byte
	Data2  byte
}

// Kind returns the status with the channel stripped.
func (m Message) Kind() byte { return m.Status & 0xF0 }

// Read parses an SMF (format 0 or 1, PPQN or SMPTE timing) and returns all
// channel messages from all tracks, merged and sorted by time.
func Read(data []byte) ([]Message, error) {
	r := bytes.NewReader(data)
	id, body, err := chunk(r)
	if err != nil {
		return nil, err
	}
	if id != "MThd" || len(body) < 6 {
		return nil, errors.New("smf: missing MThd header")
	}
	ntrk := int(binary.BigEndian.Uint16(body[2:]))
	div := binary.BigEndian.Uint16(body[4:])

	type tempoChange struct {
		tick  uint64
		usPQN uint32
	}
	var tempos []tempoChange
	type raw struct {
		tick uint64
		seq  int
		m    Message
	}
	var msgs []raw
	seq := 0
	for t := 0; t < ntrk; t++ {
		id, trk, err := chunk(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if id != "MTrk" {
			continue
		}
		var tick uint64
		var running byte
		for p := 0; p < len(trk); {
			dt, n := vlq(trk[p:])
			if n == 0 {
				return nil, errors.New("smf: bad delta time")
			}
			p += n
			tick += uint64(dt)
			if p >= len(trk) {
				break
			}
			st := trk[p]
			switch {
			case st == 0xFF:
				if p+2 > len(trk) {
					return nil, errors.New("smf: truncated meta event")
				}
				typ := trk[p+1]
				l, n := vlq(trk[p+2:])
				start := p + 2 + n
				if start+int(l) > len(trk) {
					return nil, errors.New("smf: truncated meta event")
				}
				if typ == 0x51 && l == 3 {
					b := trk[start:]
					tempos = append(tempos, tempoChange{tick, uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])})
				}
				p = start + int(l)
				if typ == 0x2F {
					p = len(trk)
				}
			case st == 0xF0 || st == 0xF7:
				l, n := vlq(trk[p+1:])
				p += 1 + n + int(l)
			default:
				if st&0x80 != 0 {
					running = st
					p++
				} else if running == 0 {
					return nil, errors.New("smf: data byte without status")
				}
				m := Message{Status: running}
				need := 2
				if k := running & 0xF0; k == 0xC0 || k == 0xD0 {
					need = 1
				}
				if p+need > len(trk) {
					return nil, errors.New("smf: truncated channel message")
				}
				m.Data1 = trk[p]
				if need == 2 {
					m.Data2 = trk[p+1]
				}
				p += need
				msgs = append(msgs, raw{tick, seq, m})
				seq++
			}
		}
	}

	// Convert ticks to wall-clock time through the tempo map.
	var toTime func(uint64) time.Duration
	if div&0x8000 != 0 {
		fps := float64(-int8(div >> 8))
		if fps == 29 {
			fps = 29.97
		}
		perSec := fps * float64(div&0xFF)
		toTime = func(t uint64) time.Duration { return time.Duration(float64(t) / perSec * float64(time.Second)) }
	} else {
		if div == 0 {
			return nil, errors.New("smf: zero division")
		}
		sort.SliceStable(tempos, func(i, j int) bool { return tempos[i].tick < tempos[j].tick })
		toTime = func(t uint64) time.Duration {
			var us float64
			last, cur := uint64(0), 500000.0
			for _, tc := range tempos {
				if tc.tick >= t {
					break
				}
				us += float64(tc.tick-last) * cur / float64(div)
				last, cur = tc.tick, float64(tc.usPQN)
			}
			us += float64(t-last) * cur / float64(div)
			return time.Duration(us * float64(time.Microsecond))
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].tick != msgs[j].tick {
			return msgs[i].tick < msgs[j].tick
		}
		return msgs[i].seq < msgs[j].seq
	})
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		m.m.Time = toTime(m.tick)
		out[i] = m.m
	}
	return out, nil
}

func chunk(r *bytes.Reader) (string, []byte, error) {
	var h [8]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return "", nil, io.EOF
		}
		return "", nil, err
	}
	n := binary.BigEndian.Uint32(h[4:])
	if int64(n) > int64(r.Len()) {
		n = uint32(r.Len()) // tolerate a short final chunk
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", nil, err
	}
	return string(h[:4]), b, nil
}

func vlq(b []byte) (uint32, int) {
	var v uint32
	for i := 0; i < len(b) && i < 4; i++ {
		v = v<<7 | uint32(b[i]&0x7F)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

func putVLQ(buf *bytes.Buffer, v uint32) {
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(v & 0x7F)
	for v >>= 7; v > 0; v >>= 7 {
		i--
		tmp[i] = byte(v&0x7F) | 0x80
	}
	buf.Write(tmp[i:])
}

// Write produces a format-0 file with the given resolution in ticks per
// second (tempo is fixed at one quarter note per second, so this is also the
// PPQN). Messages are stably sorted by time first. A track name meta event
// is written when name is non-empty.
func Write(msgs []Message, ticksPerSecond uint16, name string) ([]byte, error) {
	if ticksPerSecond == 0 || ticksPerSecond > 0x7FFF {
		return nil, fmt.Errorf("smf: bad resolution %d", ticksPerSecond)
	}
	ms := append([]Message(nil), msgs...)
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Time < ms[j].Time })
	var trk bytes.Buffer
	if name != "" {
		trk.Write([]byte{0x00, 0xFF, 0x03})
		putVLQ(&trk, uint32(len(name)))
		trk.WriteString(name)
	}
	trk.Write([]byte{0x00, 0xFF, 0x51, 0x03, 0x0F, 0x42, 0x40}) // 1,000,000 us per quarter
	var last uint64
	for _, m := range ms {
		t := uint64((m.Time*time.Duration(ticksPerSecond) + time.Second/2) / time.Second)
		if t < last {
			t = last
		}
		putVLQ(&trk, uint32(t-last))
		last = t
		trk.WriteByte(m.Status)
		trk.WriteByte(m.Data1 & 0x7F)
		if k := m.Kind(); k != 0xC0 && k != 0xD0 {
			trk.WriteByte(m.Data2 & 0x7F)
		}
	}
	trk.Write([]byte{0x00, 0xFF, 0x2F, 0x00})
	var out bytes.Buffer
	out.WriteString("MThd")
	binary.Write(&out, binary.BigEndian, []uint32{6})
	binary.Write(&out, binary.BigEndian, []uint16{0, 1, ticksPerSecond})
	out.WriteString("MTrk")
	binary.Write(&out, binary.BigEndian, uint32(trk.Len()))
	out.Write(trk.Bytes())
	return out.Bytes(), nil
}
