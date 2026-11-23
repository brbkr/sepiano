package library

import (
	"regexp"
	"strings"
	"unicode"
)

// Meta is what a file name says about a performance.
type Meta struct {
	Title      string
	Composer   string
	Performer  string
	RecordedOn string // YYYY-MM-DD
	Instrument string
	PerfKey    string // the same for every rendering of one performance
}

// Zenph names look like
//
//	02-Rachmaninoff19280404-Rachmaninoff_PreludeOp3_2-Steinway_SED133291
//
// track - performer and recording date - composer_work - instrument.
var zenphRE = regexp.MustCompile(`^(?:\d+-)?([A-Za-z][A-Za-z.']*?)(\d{8})-([^-]+)-([A-Za-z]+_[A-Za-z0-9]+)$`)

// A rendering instrument at the end of another kind of name.
var instrumentRE = regexp.MustCompile(`-((?:Steinway|Yamaha|Bosendorfer|Boesendorfer|Mason|Baldwin|Kawai|Fazioli|Bechstein)_[A-Za-z0-9]+)$`)

var opusRE = regexp.MustCompile(`Op(\d+)_(\d+)`)

// ParseName reads the metadata out of a performance's base file name.
func ParseName(name string) Meta {
	if m := zenphRE.FindStringSubmatch(name); m != nil {
		composer, work, _ := strings.Cut(m[3], "_")
		if work == "" {
			work, composer = composer, ""
		}
		d := m[2]
		return Meta{
			Title:      pretty(work),
			Composer:   pretty(composer),
			Performer:  pretty(m[1]),
			RecordedOn: d[0:4] + "-" + d[4:6] + "-" + d[6:8],
			Instrument: m[4],
			PerfKey:    strings.ToLower(m[1] + m[2] + "-" + m[3]),
		}
	}
	meta := Meta{Title: name}
	if m := instrumentRE.FindStringSubmatch(name); m != nil {
		meta.Instrument = m[1]
		name = strings.TrimSuffix(name, "-"+m[1])
	}
	meta.Title = pretty(name)
	meta.PerfKey = strings.ToLower(name)
	return meta
}

// pretty turns "PreludeOp3_2" into "Prelude Op. 3 No. 2" and
// "CarnegieShortSnippet" into "Carnegie Short Snippet".
func pretty(s string) string {
	s = opusRE.ReplaceAllString(s, "Op. $1 No. $2")
	s = strings.ReplaceAll(s, "_", " ")
	var b strings.Builder
	r := []rune(s)
	for i, c := range r {
		if i > 0 {
			p := r[i-1]
			next := rune(0)
			if i+1 < len(r) {
				next = r[i+1]
			}
			switch {
			case unicode.IsUpper(c) && unicode.IsLower(p):
				b.WriteRune(' ') // camelCase boundary
			case unicode.IsUpper(c) && unicode.IsUpper(p) && unicode.IsLower(next):
				b.WriteRune(' ') // "JSBach" -> "JS Bach"
			}
		}
		b.WriteRune(c)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
