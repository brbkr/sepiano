// Command sedump prints the contents of an SE performance (.kb/.lp/.sp) as text.
//
//	sedump [-pedals] [-fillers] song[.kb]
package main

import (
	"flag"
	"fmt"
	"os"

	"sepiano/se"
)

var names = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

func noteName(k uint8) string { return fmt.Sprintf("%s%d", names[k%12], int(k)/12-1) }

func main() {
	pedals := flag.Bool("pedals", false, "also list pedal position changes")
	fillers := flag.Bool("fillers", false, "include time-filler markers")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: sedump [-pedals] [-fillers] song[.kb]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	p, err := se.Load(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	k := p.KB
	fmt.Printf("seed %x  format %02x  model %02x  extra %d bytes\n", k.Seed, k.Format, k.Model, len(k.Extra))
	fmt.Printf("text %q\n", k.Text)
	on := 0
	for _, e := range k.Notes() {
		if e.On {
			on++
		}
	}
	fmt.Printf("%d notes, length %v, damper %d samples, soft %d samples\n\n",
		on, p.Duration(), len(p.Damper.Samples), len(p.Soft.Samples))
	type line struct {
		t   uint32
		txt string
	}
	var lines []line
	for _, e := range k.Events {
		if e.Filler() && !*fillers {
			continue
		}
		switch {
		case e.Filler():
			lines = append(lines, line{e.Tick, fmt.Sprintf("marker   key=%d", e.Key)})
		case e.On:
			lines = append(lines, line{e.Tick, fmt.Sprintf("note on  key=%3d %-4s vel=%4d", e.Key, noteName(e.Key), e.Vel)})
		default:
			lines = append(lines, line{e.Tick, fmt.Sprintf("note off key=%3d %s", e.Key, noteName(e.Key))})
		}
	}
	if *pedals {
		for _, ped := range []struct {
			name   string
			p      *se.Pedal
			period uint32
		}{{"damper", p.Damper, se.DamperPeriod}, {"soft  ", p.Soft, se.SoftPeriod}} {
			last := -1
			for i, s := range ped.p.Samples {
				if int(s) != last {
					lines = append(lines, line{uint32(i) * ped.period, fmt.Sprintf("%s   pos=%3d", ped.name, s)})
					last = int(s)
				}
			}
		}
	}
	// merge by time (stable: notes before pedals at the same tick)
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j].t < lines[j-1].t; j-- {
			lines[j], lines[j-1] = lines[j-1], lines[j]
		}
	}
	for _, l := range lines {
		fmt.Printf("%9.4fs %8d  %s\n", float64(l.t)/se.TicksPerSecond, l.t, l.txt)
	}
	fmt.Printf("%9.4fs %8d  end of performance\n", float64(k.End)/se.TicksPerSecond, k.End)
}
