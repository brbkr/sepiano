// Command sehost runs a STAHNKE SE/PC Z80 program on an SE instrument,
// replacing the Windows host (play.exe, record.exe, ...). It uploads the Z80
// program taken from your copy of the original executable, then serves the
// console and files to it, exactly as the original does.
//
//	sehost -exe play.exe -port /dev/ttyUSB0 song[.kb]
//	sehost -list
//
// Keys while playing (handled by the Z80 player itself): Space stop/resume,
// Enter quit, Esc restart, Up/Down subdue/restore. Ctrl-C aborts at once.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.bug.st/serial"
	"golang.org/x/term"

	"sepiano/host"
	"sepiano/link"
	"sepiano/se"
)

func main() { os.Exit(run()) }

func run() int {
	exe := flag.String("exe", "play.exe", "suite executable (or raw image) holding the Z80 program")
	port := flag.String("port", "", "serial port, e.g. /dev/ttyUSB0, /dev/ttyAMA0 or COM1")
	baud := flag.Int("baud", 0, "baud rate (0 tries 57600 then 28800, like the original)")
	wait := flag.Duration("wait", 250*time.Millisecond, "time allowed for the link handshake")
	list := flag.Bool("list", false, "list serial ports and exit")
	trace := flag.Bool("trace", false, "log every non-idle frame to stderr")
	capture := flag.String("capture", "", "write raw traffic to PREFIX.tx and PREFIX.rx (for sesniff)")
	saveImg := flag.String("save-program", "", "write the extracted Z80 image to this file and exit")
	probe := flag.Bool("probe", false, "only test the link handshake (nothing is uploaded), then exit")
	lines := flag.Bool("lines", false, "show the port's modem status lines (CTS, DSR, DCD, RI), then exit")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: sehost -exe play.exe -port PORT [flags] song[.kb] [arg2]\n       sehost -probe -port PORT\n       sehost -list")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *list {
		ports, err := serial.GetPortsList()
		if err != nil {
			return fail(err)
		}
		for _, p := range ports {
			fmt.Println(p)
		}
		return 0
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var fileLog func(string, ...any)
	opts := host.ConnectOptions{Baud: *baud, Wait: *wait}
	if *trace {
		opts.OnFrame = func(out bool, f link.Frame) {
			if f.Type == link.TypeIdle {
				return
			}
			dir := "<-"
			if out {
				dir = "->"
			}
			fmt.Fprintf(os.Stderr, "%s %s %v\r\n", time.Now().Format("15:04:05.000"), dir, f)
		}
		fileLog = func(format string, args ...any) { fmt.Fprintf(os.Stderr, "file: "+format+"\r\n", args...) }
	}
	if *capture != "" {
		tx, err1 := os.Create(*capture + ".tx")
		rx, err2 := os.Create(*capture + ".rx")
		if err := errors.Join(err1, err2); err != nil {
			return fail(err)
		}
		defer tx.Close()
		defer rx.Close()
		opts.RawTx, opts.RawRx = tx, rx
	}

	if *lines {
		if *port == "" {
			flag.Usage()
			return 2
		}
		return showLines(*port)
	}
	if *probe {
		if *port == "" {
			flag.Usage()
			return 2
		}
		conn, err := host.Connect(ctx, *port, opts)
		if err != nil {
			return fail(err)
		}
		// Keep the link up briefly so a trace shows the instrument settling.
		time.Sleep(400 * time.Millisecond)
		fmt.Printf("handshake OK on %s at %d baud (received %d bytes, %d bad frames)\n",
			*port, conn.Baud, conn.Pump.RxBytes, conn.Pump.Decoder.Bad)
		conn.Close()
		return 0
	}

	prog, err := host.LoadProgram(*exe)
	if err != nil {
		return fail(err)
	}
	if *saveImg != "" {
		if err := os.WriteFile(*saveImg, prog.Image, 0o644); err != nil {
			return fail(err)
		}
		fmt.Printf("%s: %d bytes, load %#04x-%#04x, entry %#04x\n", prog.Banner, len(prog.Image), prog.Start, prog.End, prog.Entry)
		return 0
	}
	if flag.NArg() < 1 || flag.NArg() > 2 || *port == "" {
		flag.Usage()
		return 2
	}
	song := flag.Arg(0)
	if filepath.Ext(song) == "" {
		song += ".kb"
	}
	if _, err := os.Stat(song); err != nil && filepath.Ext(song) == ".kb" {
		return fail(fmt.Errorf("recording does not exist: %s", se.TrimExt(song)))
	}
	files := host.NewFileService(song)
	files.Log = fileLog
	if flag.NArg() == 2 {
		files.Arg = flag.Arg(1)
	}

	fmt.Fprintf(os.Stderr, "%s\n", prog.Banner)
	conn, err := host.Connect(ctx, *port, opts)
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
	fmt.Fprintf(os.Stderr, "connected on %s at %d baud\n", *port, conn.Baud)

	var keys chan []byte
	if term.IsTerminal(int(os.Stdin.Fd())) {
		old, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer term.Restore(int(os.Stdin.Fd()), old)
		}
		keys = make(chan []byte, 16)
		go host.ReadKeys(os.Stdin, keys, cancel)
	}
	s := &host.Session{Program: prog, Files: files, Console: os.Stdout, Keys: keys}
	err = s.Run(ctx, conn.Link, conn.Exited())
	switch {
	case errors.Is(err, host.ErrQuit):
		err = nil
	case errors.Is(err, context.Canceled):
		fmt.Fprint(os.Stderr, "\r\naborted\r\n")
		err = nil
	}
	if err != nil {
		var f host.Fatal
		if !errors.As(err, &f) {
			err = fmt.Errorf("communicator error: %w", err)
		}
		fmt.Fprintf(os.Stderr, "\r\n%v\r\n", err)
		return 1
	}
	return 0
}

// showLines reports the modem status inputs with DTR and RTS raised, as the
// original host leaves them. A powered device on a working cable usually
// drives some of them; a dead or unplugged line leaves them all off.
func showLines(path string) int {
	p, err := serial.Open(path, &serial.Mode{BaudRate: 57600})
	if err != nil {
		return fail(err)
	}
	defer p.Close()
	show := func(label string) {
		st, err := p.GetModemStatusBits()
		if err != nil {
			fmt.Println(label, err)
			return
		}
		fmt.Printf("%-14s CTS=%v DSR=%v DCD=%v RI=%v\n", label, st.CTS, st.DSR, st.DCD, st.RI)
	}
	for _, s := range []struct {
		label    string
		dtr, rts bool
	}{{"DTR+RTS on:", true, true}, {"DTR+RTS off:", false, false}, {"DTR on only:", true, false}, {"RTS on only:", false, true}} {
		p.SetDTR(s.dtr)
		p.SetRTS(s.rts)
		time.Sleep(150 * time.Millisecond)
		show(s.label)
	}
	return 0
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, err)
	return 1
}
