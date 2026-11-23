package host

import (
	"fmt"
	"strconv"
)

var reqNames = map[byte]string{
	ReqSetName: "set-name", ReqSetExt: "set-ext", ReqGetName: "get-name", ReqGetExt: "get-ext",
	ReqGetArg: "get-arg", ReqOpenPerf: "open-perf", ReqReadPerf: "read-perf", ReqCreatePerf: "create-perf",
	ReqWritePerf: "write-perf", ReqClosePerf: "close-perf", ReqCommitPerf: "commit-perf", ReqAbortPerf: "abort-perf",
	ReqOpenFile: "open-file", ReqReadFile: "read-file", ReqCreateFile: "create-file", ReqWriteFile: "write-file",
	ReqCloseFile: "close-file", ReqCommitFile: "commit-file", ReqAbortFile: "abort-file",
}

// RPCDecoder turns one direction of the channel-2 byte stream into readable
// request (from the instrument) or reply (from the PC) summaries.
type RPCDecoder struct {
	FromZ80 bool
	buf     []byte
}

// Feed adds bytes and returns the messages completed by them.
func (d *RPCDecoder) Feed(p []byte) []string {
	d.buf = append(d.buf, p...)
	var out []string
	for len(d.buf) > 0 {
		s, n := d.one()
		if n == 0 {
			break
		}
		out = append(out, s)
		d.buf = d.buf[n:]
	}
	return out
}

// one decodes a message at the start of buf, returning its length or 0 if incomplete.
func (d *RPCDecoder) one() (string, int) {
	b := d.buf
	op := b[0]
	name := reqNames[op]
	if name == "" {
		return fmt.Sprintf("?? %02x", op), 1
	}
	text := func(from int) (string, int) {
		for i := from; i < len(b); i++ {
			if b[i] == 0x7F {
				return strconv.Quote(string(b[from:i])), i + 1
			}
		}
		return "", 0
	}
	u16 := func(i int) int { return int(b[i]) | int(b[i+1])<<8 }
	if d.FromZ80 {
		switch op {
		case ReqSetName, ReqSetExt:
			if s, n := text(1); n > 0 {
				return name + " " + s, n
			}
			return "", 0
		case ReqReadPerf:
			if len(b) < 4 {
				return "", 0
			}
			return fmt.Sprintf("%s file=%d len=%d", name, b[1], u16(2)), 4
		case ReqWritePerf:
			if len(b) < 4 || len(b) < 4+u16(2) {
				return "", 0
			}
			return fmt.Sprintf("%s file=%d len=%d", name, b[1], u16(2)), 4 + u16(2)
		case ReqReadFile:
			if len(b) < 3 {
				return "", 0
			}
			return fmt.Sprintf("%s len=%d", name, u16(1)), 3
		case ReqWriteFile:
			if len(b) < 3 || len(b) < 3+u16(1) {
				return "", 0
			}
			return fmt.Sprintf("%s len=%d", name, u16(1)), 3 + u16(1)
		}
		return name, 1
	}
	switch op {
	case ReqGetName, ReqGetExt, ReqGetArg:
		if s, n := text(1); n > 0 {
			return name + " -> " + s, n
		}
		return "", 0
	case ReqReadPerf:
		if len(b) < 4 || len(b) < 4+u16(2) {
			return "", 0
		}
		return fmt.Sprintf("%s -> file=%d %d bytes", name, b[1], u16(2)), 4 + u16(2)
	case ReqWritePerf:
		if len(b) < 4 {
			return "", 0
		}
		return fmt.Sprintf("%s -> file=%d wrote %d", name, b[1], u16(2)), 4
	case ReqReadFile:
		if len(b) < 3 || len(b) < 3+u16(1) {
			return "", 0
		}
		return fmt.Sprintf("%s -> %d bytes", name, u16(1)), 3 + u16(1)
	case ReqWriteFile:
		if len(b) < 3 {
			return "", 0
		}
		return fmt.Sprintf("%s -> wrote %d", name, u16(1)), 3
	}
	return name + " -> ok", 1
}
