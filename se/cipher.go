// Package se reads and writes Stahnke SE reproducing-piano performance files
// (.kb key events, .lp damper pedal, .sp soft pedal), as played by the
// "STAHNKE SE/PC PERFORMANCE PLAYER" (play.exe, Z80 program V50.45).
//
// The format was reverse engineered from the Z80 player code; see
// docs/SE-FILE-FORMAT.md for the full description.
package se

// Cipher is the byte-stream scrambler applied to every SE file after its
// 4-byte seed (Z80 routines 0x0BB9, 0x0E67, 0x0E7C).
//
// The keystream depends only on the seed, so the same operation both
// encrypts and decrypts. A seed of 00 00 00 00 yields an all-zero keystream,
// i.e. the file is stored in the clear; every Zenph file seen so far uses it.
type Cipher struct {
	s    [4]byte // Z80 0x293A..0x293D
	alt  bool    // Z80 0x293E != 0: alternate tap set
	zero bool
}

// NewCipher initialises the scrambler from the first four bytes of a file.
func NewCipher(seed [4]byte) *Cipher {
	c := &Cipher{s: [4]byte{seed[3], seed[2], seed[1], seed[0]}, alt: seed[3] == 0}
	c.zero = seed == [4]byte{}
	return c
}

// Byte transforms one byte of the stream and advances the keystream.
func (c *Cipher) Byte(b byte) byte {
	if c.zero {
		return b
	}
	out := b ^ c.s[3]
	s0, s1, s2, s3 := uint16(c.s[0]), uint16(c.s[1]), uint16(c.s[2]), uint16(c.s[3])
	hi := s3<<8 | s2
	lo := s1<<8 | s0
	a := s3
	if c.alt {
		a ^= (hi << 4) >> 8
		a ^= (lo << 1) >> 8
		a ^= (lo << 3) >> 8
	} else {
		a ^= (hi << 3) >> 8
		a ^= (lo << 1) >> 8
		a ^= s0
	}
	// shift the 32-bit register left one byte, feeding back A
	c.s = [4]byte{byte(a), c.s[0], c.s[1], c.s[2]}
	return out
}

// Crypt applies the scrambler in place to buf[4:], using buf[0:4] as the seed.
func Crypt(buf []byte) {
	if len(buf) < 4 {
		return
	}
	c := NewCipher([4]byte{buf[0], buf[1], buf[2], buf[3]})
	for i := 4; i < len(buf); i++ {
		buf[i] = c.Byte(buf[i])
	}
}
