// Command se2mid converts SE performances (.kb/.lp/.sp) to Standard MIDI Files.
//
//	se2mid [-o out.mid] [-marks] song[.kb] ...
//
// With several inputs, each is written next to its source as song.mid.
package main

import (
	"flag"
	"fmt"
	"os"

	"sepiano/se"
)

func main() {
	out := flag.String("o", "", "output file (single input only; default song.mid)")
	marks := flag.Bool("marks", false, "keep Zenph sync marks and other keys outside the piano range")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: se2mid [-o out.mid] [-marks] song[.kb] ...")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 || (*out != "" && flag.NArg() != 1) {
		flag.Usage()
		os.Exit(2)
	}
	status := 0
	for _, in := range flag.Args() {
		dst := *out
		if dst == "" {
			dst = se.TrimExt(in) + ".mid"
		}
		if err := convert(in, dst, !*marks); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", in, err)
			status = 1
			continue
		}
		fmt.Println(dst)
	}
	os.Exit(status)
}

func convert(in, dst string, skipMarks bool) error {
	p, err := se.Load(in)
	if err != nil {
		return err
	}
	b, err := p.ToMIDI(nil, skipMarks)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
