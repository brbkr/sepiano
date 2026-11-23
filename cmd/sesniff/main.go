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
//	sesniff -baud 57600 -tap-pc /dev/ttyUSB1 -tap-piano /dev/ttyUSB2
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
	all     = flag.Bool("all", false, "show idle frames and bare acknowledgements")
	dump    = flag.String("dump", "", "directory to write per-channel payload streams")
	mu      sync.Mutex
	t0      = time.Now()
	offline bool
)

func main() {
	pcFile := flag.String("pc", "", "capture of PC -> instrument bytes")
	pianoFile := flag.String("piano", "", "capture of instrument -> PC bytes")
	tapPC := flag.String("tap-pc", "", "serial port listening to the PC's transmit line")
	tapPiano := flag.String("tap-piano", "", "serial port listening to the instrument's transmit line")
	baud := flag.Int("baud", 57600, "baud rate for live taps")
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
		var wg sync.WaitGroup
		for _, x := range []struct {
			s    *side
			port string
		}{{pc, *tapPC}, {piano, *tapPiano}} {
			if x.port == "" {
				continue
			}
			p, err := serial.Open(x.port, &serial.Mode{BaudRate: *baud})
			if err != nil {
				fail(err)
			}
			p.SetReadTimeout(20 * time.Millisecond)
			wg.Add(1)
			go func(s *side, p serial.Port) {
				defer wg.Done()
				defer p.Close()
				buf := make([]byte, 1024)
				for ctx.Err() == nil {
					n, err := p.Read(buf)
					if err != nil {
						fmt.Fprintln(os.Stderr, err)
						return
					}
					feed(s, buf[:n])
				}
			}(x.s, p)
		}
		wg.Wait()
	default:
		flag.Usage()
		os.Exit(2)
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
