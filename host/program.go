// Package host is a replacement for the PC side of the STAHNKE SE/PC
// programs (play.exe, record.exe, ...). Each of those Windows programs is a
// thin "communicator": it uploads a Z80 program to the instrument, then serves
// files and a console to it over the link. The Z80 program does the real work.
//
// The Z80 programs are Wayne Stahnke's copyrighted code, so this package does
// not include them: Program extracts one from the user's own copy of a suite
// executable at run time.
package host

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Program is a Z80 program image as uploaded on channel 0.
//
// The image starts with a 16-byte load header that the instrument's boot ROM
// interprets:
//
//	00 03 | start (LE16) | end (LE16) | entry (LE16) | 00 x 8
//
// followed by the bytes for addresses start..end inclusive (the header itself
// occupies start-16..start-1). The program text normally begins with a CR LF,
// a banner line and a 0x7F terminator.
type Program struct {
	Image             []byte
	Start, End, Entry uint16
	Banner            string
}

// ParseProgram validates a raw image.
func ParseProgram(img []byte) (*Program, error) {
	if len(img) < 17 || img[0] != 0 || img[1] != 3 || !bytes.Equal(img[8:16], make([]byte, 8)) {
		return nil, errors.New("host: not a Z80 load image")
	}
	p := &Program{
		Start: uint16(img[2]) | uint16(img[3])<<8,
		End:   uint16(img[4]) | uint16(img[5])<<8,
		Entry: uint16(img[6]) | uint16(img[7])<<8,
	}
	if p.End < p.Start || p.Entry < p.Start || p.Entry > p.End {
		return nil, errors.New("host: inconsistent load header")
	}
	n := int(p.End-p.Start) + 1 + 16
	if n > len(img) {
		return nil, errors.New("host: image shorter than its header says")
	}
	p.Image = img[:n]
	body := p.Image[16:]
	if i := bytes.IndexByte(body, 0x7F); i > 0 {
		p.Banner = strings.TrimSpace(string(body[:i]))
	}
	return p, nil
}

// ExtractProgram finds the embedded Z80 program in a suite executable
// (play.exe, record.exe, monitor.exe, ...).
func ExtractProgram(exe []byte) (*Program, error) {
	for off := 0; ; {
		i := bytes.Index(exe[off:], []byte{0x00, 0x03})
		if i < 0 {
			return nil, errors.New("host: no Z80 program found in executable")
		}
		off += i
		if p, err := ParseProgram(exe[off:]); err == nil && strings.Contains(p.Banner, "V") && len(p.Image) > 64 {
			return p, nil
		}
		off++
	}
}

// LoadProgram reads a suite executable, or a raw image saved from one.
func LoadProgram(path string) (*Program, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if p, err := ParseProgram(b); err == nil {
		return p, nil
	}
	p, err := ExtractProgram(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}
