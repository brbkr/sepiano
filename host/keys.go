package host

import (
	"io"
	"time"
)

// Special keys reach the Z80 program as 0x00 followed by the PC BIOS scan
// code, exactly as the Windows host sends them; printable keys, Enter (0x0D),
// Esc (0x1B) and Space are sent as ASCII.
var (
	KeyUp    = []byte{0x00, 0x48}
	KeyDown  = []byte{0x00, 0x50}
	KeyLeft  = []byte{0x00, 0x4B}
	KeyRight = []byte{0x00, 0x4D}
	KeyHome  = []byte{0x00, 0x47}
	KeyEnd   = []byte{0x00, 0x4F}
	KeyPgUp  = []byte{0x00, 0x49}
	KeyPgDn  = []byte{0x00, 0x51}
	KeyIns   = []byte{0x00, 0x52}
	KeyDel   = []byte{0x00, 0x53}
)

// KeyF returns the code for function key F1..F10.
func KeyF(n int) []byte { return []byte{0x00, byte(0x3A + n)} }

var csiTilde = map[string][]byte{
	"1": KeyHome, "7": KeyHome, "2": KeyIns, "3": KeyDel, "4": KeyEnd, "8": KeyEnd,
	"5": KeyPgUp, "6": KeyPgDn,
	"11": KeyF(1), "12": KeyF(2), "13": KeyF(3), "14": KeyF(4), "15": KeyF(5),
	"17": KeyF(6), "18": KeyF(7), "19": KeyF(8), "20": KeyF(9), "21": KeyF(10),
}

var csiFinal = map[byte][]byte{'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft, 'H': KeyHome, 'F': KeyEnd}
var ss3Final = map[byte][]byte{'P': KeyF(1), 'Q': KeyF(2), 'R': KeyF(3), 'S': KeyF(4), 'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft, 'H': KeyHome, 'F': KeyEnd}

// ReadKeys translates raw terminal input (ANSI escape sequences) into PC
// console codes and sends one slice per key. A lone Esc is recognised after a
// short pause. interrupt is called, and reading stops, on Ctrl-C.
func ReadKeys(r io.Reader, out chan<- []byte, interrupt func()) {
	raw := make(chan byte, 64)
	go func() {
		defer close(raw)
		b := make([]byte, 64)
		for {
			n, err := r.Read(b)
			for _, c := range b[:n] {
				raw <- c
			}
			if err != nil {
				return
			}
		}
	}()
	next := func(wait time.Duration) (byte, bool) {
		select {
		case c, ok := <-raw:
			return c, ok
		case <-time.After(wait):
			return 0, false
		}
	}
	defer close(out)
	for c := range raw {
		switch c {
		case 0x03:
			if interrupt != nil {
				interrupt()
			}
			return
		case '\n':
			out <- []byte{0x0D}
		case 0x7F:
			out <- []byte{0x08}
		case 0x1B:
			c2, ok := next(40 * time.Millisecond)
			if !ok {
				out <- []byte{0x1B}
				continue
			}
			switch c2 {
			case '[':
				var params []byte
				for {
					c3, ok := next(40 * time.Millisecond)
					if !ok {
						break
					}
					if c3 >= 0x40 && c3 <= 0x7E {
						if c3 == '~' {
							if k := csiTilde[string(params)]; k != nil {
								out <- k
							}
						} else if k := csiFinal[c3]; k != nil {
							out <- k
						}
						break
					}
					params = append(params, c3)
				}
			case 'O':
				if c3, ok := next(40 * time.Millisecond); ok {
					if k := ss3Final[c3]; k != nil {
						out <- k
					}
				}
			default:
				out <- []byte{0x1B}
				out <- []byte{c2}
			}
		default:
			out <- []byte{c}
		}
	}
}
