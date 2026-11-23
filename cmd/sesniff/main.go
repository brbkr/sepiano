// Command sesniff decodes SE/PC serial traffic, to check the protocol against
// the original Windows host.
//
// Offline, from captures (e.g. sehost -capture, or any logger's raw files):
//
//	sesniff -pc pc_to_piano.bin -piano piano_to_pc.bin
//
// Live, from a passive tap: two USB serial adapters whose RX pins listen to
// the PC's TXD and the instrument's TXD respectively (and grounds joined):
//
//	sesniff -tap-pc /dev/ttyUSB1 -tap-piano /dev/ttyUSB2
//
// Without -baud, the rate (57600 or 28800) is found by cycling the tap until
// frames with valid CRCs appear, on the instrument's line when it is tapped.
// Start sesniff before the PC program so it can lock on early.
//
// Prints link frames (idle frames and bare acks hidden unless -all), the
// console text, and decoded file-service requests. -dump DIR also saves each
// direction's channel payload streams (pc-ch0.bin is the uploaded Z80 program).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"go.bug.st/serial"

	"sepiano/host"
	"sepiano/link"
)

type side struct {
	name    string
	dec     link.Decoder
	rpc     host.RPCDecoder
	lastSeq int
	streams [link.NumChannels][]byte
}

var (
	all      = flag.Bool("all", false, "show idle frames and bare acknowledgements")
	dump     = flag.String("dump", "", "directory to write per-channel payload streams")
	mu       sync.Mutex
	t0       = time.Now()
	offline  bool
	detected int
)

func main() {
	pcFile := flag.String("pc", "", "capture of PC -> instrument bytes")
	pianoFile := flag.String("piano", "", "capture of instrument -> PC bytes")
	tapPC := flag.String("tap-pc", "", "serial port listening to the PC's transmit line")
	tapPiano := flag.String("tap-piano", "", "serial port listening to the instrument's transmit line")
	baud := flag.Int("baud", 0, "baud rate for live taps (0 detects 57600 or 28800)")
	flag.Parse()

	pc := &side{name: "PC   ", lastSeq: -1, rpc: host.RPCDecoder{FromZ80: false}}
	piano := &side{name: "PIANO", lastSeq: -1, rpc: host.RPCDecoder{FromZ80: true}}
	switch {
	case *pcFile != "" || *pianoFile != "":
		// Both lines run continuously at the same rate, so interleaving the
		// captures by byte offset approximates their real time order.
		offline = true
		var a, b []byte
		var err error
		if *pcFile != "" {
			if a, err = os.ReadFile(*pcFile); err != nil {
				fail(err)
			}
		}
		if *pianoFile != "" {
			if b, err = os.ReadFile(*pianoFile); err != nil {
				fail(err)
			}
		}
		const step = 16
		for i := 0; i < max(len(a), len(b)); i += step {
			if i < len(a) {
				feed(pc, a[i:min(i+step, len(a))])
			}
			if i < len(b) {
				feed(piano, b[i:min(i+step, len(b))])
			}
		}
	case *tapPC != "" || *tapPiano != "":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		rate := *baud
		if rate == 0 {
			rate = host.Bauds[0]
		}
		type tap struct {
			s    *side
			port serial.Port
		}
		var taps []tap
		for _, x := range []struct {
			s    *side
			path string
		}{{pc, *tapPC}, {piano, *tapPiano}} {
			if x.path == "" {
				continue
			}
			p, err := serial.Open(x.path, &serial.Mode{BaudRate: rate})
			if err != nil {
				fail(err)
			}
			defer p.Close()
			p.SetReadTimeout(20 * time.Millisecond)
			taps = append(taps, tap{x.s, p})
		}
		if *baud == 0 {
			// The instrument only ever talks at its own rate. The PC line is
			// less reliable for this: while play.exe scans ports it sends
			// handshake frames at 57600 even if the instrument uses 28800.
			probe, accept, which := taps[0], afterHandshake, "PC"
			if len(taps) == 2 || taps[0].s == piano {
				probe, accept, which = taps[len(taps)-1], anyFrames, "instrument"
			}
			fmt.Fprintf(os.Stderr, "detecting baud rate on the %s line (%v)...\n", which, host.Bauds)
			r, seen, err := detectBaud(ctx, probe.port, host.Bauds, 300*time.Millisecond, accept,
				func(msg string) { fmt.Fprintln(os.Stderr, msg) })
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				fail(err)
			}
			rate = r
			fmt.Fprintf(os.Stderr, "%s line: %d baud (pass -baud %d to skip detection next time)\n", which, rate, rate)
			for _, t := range taps {
				if t != probe {
					t.port.SetMode(&serial.Mode{BaudRate: rate})
					t.port.ResetInputBuffer()
				}
			}
			feed(probe.s, seen)
		}
		detected = rate
		var wg sync.WaitGroup
		for _, t := range taps {
			wg.Add(1)
			go func(s *side, p serial.Port) {
				defer wg.Done()
				buf := make([]byte, 1024)
				for ctx.Err() == nil {
					n, err := p.Read(buf)
					if err != nil {
						fmt.Fprintln(os.Stderr, err)
						return
					}
					feed(s, buf[:n])
				}
			}(t.s, t.port)
		}
		wg.Wait()
	default:
		flag.Usage()
		os.Exit(2)
	}
	if detected != 0 {
		fmt.Printf("baud rate: %d\n", detected)
	}
	for _, s := range []*side{pc, piano} {
		fmt.Printf("%s: %d frames dropped (bad CRC/length), %d stray bytes\n", s.name, s.dec.Bad, s.dec.Skipped)
		if *dump != "" {
			os.MkdirAll(*dump, 0o755)
			for ch, b := range s.streams {
				if len(b) > 0 {
					name := filepath.Join(*dump, fmt.Sprintf("%s-ch%d.bin", map[*side]string{pc: "pc", piano: "piano"}[s], ch))
					os.WriteFile(name, b, 0o644)
					fmt.Printf("  wrote %s (%d bytes)\n", name, len(b))
				}
			}
		}
	}
}

func feed(s *side, b []byte) {
	mu.Lock()
	defer mu.Unlock()
	for _, f := range s.dec.Feed(b) {
		show(s, f)
	}
}

func show(s *side, f link.Frame) {
	ts := ""
	if !offline {
		ts = fmt.Sprintf("%8.3f ", time.Since(t0).Seconds())
	}
	data := f.Type == link.TypeData || f.Type == link.TypeDatAck
	if !data {
		if *all || (f.Type != link.TypeIdle && f.Type != link.TypeAck) {
			fmt.Printf("%s%s %v\n", ts, s.name, f)
		}
		return
	}
	fresh := s.lastSeq < 0 || int8(f.Seq-byte(s.lastSeq)) > 0
	if !fresh {
		// Resends happen constantly: the sender re-offers its oldest
		// unacknowledged frame whenever it has nothing new.
		if *all {
			fmt.Printf("%s%s %v (resend)\n", ts, s.name, f)
		}
		return
	}
	s.lastSeq = int(f.Seq)
	if f.Chan != link.ChanBoot || *all { // the program upload is long; hide it
		fmt.Printf("%s%s %v\n", ts, s.name, f)
	}
	if f.Chan >= link.NumChannels {
		return
	}
	s.streams[f.Chan] = append(s.streams[f.Chan], f.Data...)
	switch {
	case f.Chan == link.ChanFile:
		for _, m := range s.rpc.Feed(f.Data) {
			fmt.Printf("%s%s   file: %s\n", ts, s.name, m)
		}
	case f.Chan == link.ChanConsole:
		fmt.Printf("%s%s   console: %q\n", ts, s.name, f.Data)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
