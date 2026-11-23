package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sepiano/link"
	"sepiano/se"
)

// testProgram builds a small load image in the same layout as the suite's.
func testProgram(t *testing.T) *Program {
	body := []byte("\r\nTEST PROGRAM, V1.0\r\n\x7f")
	body = append(body, bytes.Repeat([]byte{0xC9}, 3000)...) // RET padding
	start, end := uint16(0x100), uint16(0x100+len(body)-1)
	img := []byte{0, 3, byte(start), byte(start >> 8), byte(end), byte(end >> 8), 0x10, 0x01, 0, 0, 0, 0, 0, 0, 0, 0}
	p, err := ParseProgram(append(img, body...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseProgram(t *testing.T) {
	p := testProgram(t)
	if p.Banner != "TEST PROGRAM, V1.0" || p.Entry != 0x110 || len(p.Image) != 16+3023 {
		t.Fatalf("%+v", p)
	}
	// Found inside a larger file too.
	q, err := ExtractProgram(append(append(bytes.Repeat([]byte{0, 3, 0xFF}, 50), p.Image...), 1, 2, 3))
	if err != nil || !bytes.Equal(q.Image, p.Image) {
		t.Fatalf("extract: %v", err)
	}
}

// If the real suite is available (SE_SUITE=dir with play.exe), check extraction.
func TestExtractFromPlayExe(t *testing.T) {
	dir := os.Getenv("SE_SUITE")
	if dir == "" {
		t.Skip("set SE_SUITE to a directory containing play.exe")
	}
	p, err := LoadProgram(filepath.Join(dir, "play.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Banner, "STAHNKE SE/PC PERFORMANCE PLAYER, V50.45") || len(p.Image) != 8131 || p.Entry != 0x153 {
		t.Fatalf("banner %q len %d entry %#x", p.Banner, len(p.Image), p.Entry)
	}
}

// fakeZ80 stands in for the instrument running a player program.
func fakeZ80(l *link.Link, prog *Program, want [3][]byte, gotKey chan<- byte) error {
	boot := link.Channel{L: l, Ch: link.ChanBoot}
	con := link.Channel{L: l, Ch: link.ChanConsole}
	file := link.Channel{L: l, Ch: link.ChanFile}
	img := make([]byte, len(prog.Image))
	if _, err := io.ReadFull(boot, img); err != nil {
		return err
	}
	if !bytes.Equal(img, prog.Image) {
		return errors.New("uploaded image differs")
	}
	con.Write([]byte("\r\n" + prog.Banner + "\r\n"))
	expect := func(b ...byte) error {
		got := make([]byte, len(b))
		if _, err := io.ReadFull(file, got); err != nil {
			return err
		}
		if !bytes.Equal(got, b) {
			return fmt.Errorf("file service replied % x, want % x", got, b)
		}
		return nil
	}
	file.Write([]byte{ReqOpenPerf})
	if err := expect(ReqOpenPerf); err != nil {
		return err
	}
	for fn := 0; fn < 3; fn++ {
		var got []byte
		for {
			file.Write([]byte{ReqReadPerf, byte(fn), 0x00, 0x04}) // 1 KB, as the player asks
			hdr := make([]byte, 4)
			if _, err := io.ReadFull(file, hdr); err != nil {
				return err
			}
			if hdr[0] != ReqReadPerf || hdr[1] != byte(fn) {
				return fmt.Errorf("bad read reply % x", hdr)
			}
			n := int(hdr[2]) | int(hdr[3])<<8
			buf := make([]byte, n)
			io.ReadFull(file, buf)
			got = append(got, buf...)
			if n < 0x400 {
				break
			}
		}
		if !bytes.Equal(got, want[fn]) {
			return fmt.Errorf("file %d: got %d bytes, want %d", fn, len(got), len(want[fn]))
		}
	}
	con.Write([]byte("DEPRESS SPACE BAR TO STOP/RESUME PLAYING\r\n"))
	k, err := con.ReadByte()
	if err != nil {
		return err
	}
	gotKey <- k
	con.Write([]byte("END OF PERFORMANCE\r\n\x1b"))
	return nil
}

func TestSessionAgainstSimulatedInstrument(t *testing.T) {
	// a small performance written with the se package
	dir := t.TempDir()
	perf := &se.Performance{KB: &se.KB{Format: 0x0F, Model: 0x11, Text: "sim\r\n", End: 4000}}
	for i := uint32(0); i < 300; i++ {
		perf.KB.Events = append(perf.KB.Events,
			se.Event{Tick: 800 + i*10, Key: byte(40 + i%40), On: true, Vel: uint16(60 + i)},
			se.Event{Tick: 805 + i*10, Key: byte(40 + i%40)})
	}
	perf.KB.End = 800 + 300*10 + 400
	base := filepath.Join(dir, "SIM")
	if err := perf.Save(base); err != nil {
		t.Fatal(err)
	}
	var want [3][]byte
	for i, ext := range perfExt {
		want[i], _ = os.ReadFile(base + ext)
	}

	pc, piano := link.NewPipe(1e-4, 1e-4) // a slightly noisy cable
	hostL, z80L := link.New(), link.New()
	z80L.Strict = true
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exited := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); (&link.Pump{L: z80L, Port: piano, Baud: 57600}).Run(ctx) }()
	var capTx, capRx bytes.Buffer
	var capMu sync.Mutex
	locked := func(b *bytes.Buffer) writerFunc {
		return func(p []byte) (int, error) { capMu.Lock(); defer capMu.Unlock(); return b.Write(p) }
	}
	go func() {
		(&link.Pump{L: hostL, Port: pc, Baud: 57600, RawTx: locked(&capTx), RawRx: locked(&capRx)}).Run(ctx)
		close(exited)
	}()
	for !hostL.Up() {
		time.Sleep(time.Millisecond)
	}

	prog := testProgram(t)
	keys := make(chan []byte, 1)
	keys <- []byte{' '}
	var console bytes.Buffer
	var mu sync.Mutex
	s := &Session{Program: prog, Files: NewFileService(base + ".kb"), Console: writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return console.Write(p)
	}), Keys: keys}

	gotKey := make(chan byte, 1)
	z80err := make(chan error, 1)
	go func() { z80err <- fakeZ80(z80L, prog, want, gotKey) }()
	err := s.Run(ctx, hostL, exited)
	if !errors.Is(err, ErrQuit) {
		t.Fatalf("session ended with %v", err)
	}
	if e := <-z80err; e != nil {
		t.Fatalf("instrument side: %v", e)
	}
	if k := <-gotKey; k != ' ' {
		t.Errorf("key %q", k)
	}
	mu.Lock()
	out := console.String()
	mu.Unlock()
	if !strings.Contains(out, "TEST PROGRAM, V1.0") || !strings.HasSuffix(out, "END OF PERFORMANCE\r\n") {
		t.Errorf("console: %q", out)
	}
	t.Logf("host retransmits %d, instrument retransmits %d", hostL.Retransmits, z80L.Retransmits)
	cancel()
	wg.Wait()

	// The captured traffic must decode into the expected file-service dialogue.
	capMu.Lock()
	defer capMu.Unlock()
	var msgs []string
	for _, dir := range []struct {
		raw     []byte
		fromZ80 bool
	}{{capRx.Bytes(), true}, {capTx.Bytes(), false}} {
		var d link.Decoder
		r := RPCDecoder{FromZ80: dir.fromZ80}
		last := -1
		for _, f := range d.Feed(dir.raw) {
			if (f.Type == link.TypeData || f.Type == link.TypeDatAck) && f.Chan == link.ChanFile &&
				(last < 0 || int8(f.Seq-byte(last)) > 0) {
				last = int(f.Seq)
				msgs = append(msgs, r.Feed(f.Data)...)
			} else if f.Type == link.TypeData || f.Type == link.TypeDatAck {
				if last < 0 || int8(f.Seq-byte(last)) > 0 {
					last = int(f.Seq)
				}
			}
		}
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"open-perf", "read-perf file=2 len=1024", "open-perf -> ok", "read-perf -> file=0 1024 bytes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("capture lacks %q:\n%s", want, joined)
		}
	}
	os.WriteFile(filepath.Join(os.TempDir(), "sehost-sim.tx"), capTx.Bytes(), 0o644)
	os.WriteFile(filepath.Join(os.TempDir(), "sehost-sim.rx"), capRx.Bytes(), 0o644)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestReadKeys(t *testing.T) {
	in := strings.NewReader("a\x1b[A\x1b[B\x1bOP\x1b[5~\n ")
	out := make(chan []byte, 16)
	ReadKeys(in, out, nil)
	var got [][]byte
	for k := range out {
		got = append(got, k)
	}
	want := [][]byte{{'a'}, KeyUp, KeyDown, KeyF(1), KeyPgUp, {0x0D}, {' '}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
