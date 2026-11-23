package se

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Performance is a complete recording: the three files that share a base name.
type Performance struct {
	KB     *KB
	Damper *Pedal // .lp, sampled every DamperPeriod ticks
	Soft   *Pedal // .sp, sampled every SoftPeriod ticks
}

// TrimExt removes a trailing .kb/.lp/.sp so either "song" or "song.kb" can be given.
func TrimExt(path string) string {
	for _, ext := range []string{".kb", ".lp", ".sp", ".KB", ".LP", ".SP"} {
		if strings.HasSuffix(path, ext) {
			return path[:len(path)-len(ext)]
		}
	}
	return path
}

// Load reads base.kb, base.lp and base.sp.
func Load(base string) (*Performance, error) {
	base = TrimExt(base)
	read := func(ext string) ([]byte, error) { return os.ReadFile(base + ext) }
	raw, err := read(".kb")
	if err != nil {
		return nil, err
	}
	kb, err := ParseKB(raw)
	if err != nil {
		return nil, fmt.Errorf("%s.kb: %w", base, err)
	}
	p := &Performance{KB: kb}
	for _, f := range []struct {
		ext string
		dst **Pedal
	}{{".lp", &p.Damper}, {".sp", &p.Soft}} {
		raw, err := read(f.ext)
		if err != nil {
			return nil, err
		}
		if *f.dst, err = ParsePedal(raw); err != nil {
			return nil, fmt.Errorf("%s%s: %w", base, f.ext, err)
		}
	}
	return p, nil
}

// Save writes base.kb, base.lp and base.sp. The player aborts if a pedal file
// runs out before the key stream ends, so short pedal streams are extended by
// holding their last value to cover End plus a one-second margin.
func (p *Performance) Save(base string) error {
	base = TrimExt(base)
	kb, lp, sp, err := p.Encode()
	if err != nil {
		return err
	}
	for _, f := range []struct {
		ext string
		b   []byte
	}{{".kb", kb}, {".lp", lp}, {".sp", sp}} {
		if err := os.WriteFile(base+f.ext, f.b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Encode returns the contents of the .kb, .lp and .sp files that Save
// writes, with the same pedal extension.
func (p *Performance) Encode() (kb, lp, sp []byte, err error) {
	end := p.KB.End
	if n := len(p.KB.Events); n > 0 && p.KB.Events[n-1].Tick > end {
		end = p.KB.Events[n-1].Tick
	}
	need := func(period uint32) int { return int((end+TicksPerSecond)/period) + 1 }
	if kb, err = p.KB.Encode(true); err != nil {
		return nil, nil, nil, err
	}
	for _, f := range []struct {
		dst    *[]byte
		ped    *Pedal
		period uint32
	}{{&lp, p.Damper, DamperPeriod}, {&sp, p.Soft, SoftPeriod}} {
		ped := &Pedal{}
		if f.ped != nil {
			ped.Seed, ped.Samples = f.ped.Seed, append([]byte(nil), f.ped.Samples...)
		}
		var last byte
		if len(ped.Samples) > 0 {
			last = ped.Samples[len(ped.Samples)-1]
		}
		for len(ped.Samples) < need(f.period) {
			ped.Samples = append(ped.Samples, last)
		}
		if *f.dst, err = ped.Encode(); err != nil {
			return nil, nil, nil, err
		}
	}
	return kb, lp, sp, nil
}

// Duration is the length of the key stream.
func (p *Performance) Duration() time.Duration { return TickDuration(p.KB.End) }

// TickDuration converts ticks to wall-clock time.
func TickDuration(t uint32) time.Duration {
	return time.Duration(t) * time.Second / TicksPerSecond
}
