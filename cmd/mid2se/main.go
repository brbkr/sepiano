// Command mid2se converts a Standard MIDI File into an SE performance
// (song.kb, song.lp, song.sp) that the SE player can play.
//
//	mid2se [-o song] [-lead 1s] [-text "..."] in.mid
//
// Note-on velocities go through the default MIDI-to-SE curve, CC64 becomes the
// damper pedal and CC67 the soft pedal. Filenames for the SE instrument are
// limited to 8.3 characters, so keep the base name short if the Z80 program
// will display or request it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sepiano/se"
)

func main() {
	out := flag.String("o", "", "output base name (default: input without .mid)")
	lead := flag.Duration("lead", time.Second, "silence added before the first note")
	text := flag.String("text", "", "header text shown by the player (default: source file name)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mid2se [-o song] [-lead 1s] [-text ...] in.mid")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	in := flag.Arg(0)
	base := *out
	if base == "" {
		base = strings.TrimSuffix(strings.TrimSuffix(in, ".mid"), ".MID")
	}
	t := *text
	if t == "" {
		t = "Converted from " + filepath.Base(in)
	}
	data, err := os.ReadFile(in)
	if err == nil {
		var p *se.Performance
		if p, err = se.FromMIDI(data, nil, *lead, t+"\r\n"); err == nil {
			err = p.Save(base)
			if err == nil {
				fmt.Printf("%s.{kb,lp,sp}: %d events, %v\n", base, len(p.KB.Events), p.Duration().Round(time.Millisecond))
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
