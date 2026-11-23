// Package library stores the piano's performances.
//
// Files are the source of truth. Each piece is a set of files in one folder,
// named by an 8-character id (the longest name the Z80 programs accept, should
// one ask for it):
//
//	<id>.kb  <id>.lp  <id>.sp   the performance, byte for byte as uploaded
//	<id>.json                   metadata (written last)
//	<id>.mid                    the original MIDI file, for converted pieces
//
// The index is kept in memory and rebuilt from the folder at start, so there
// is no database to corrupt. Every write goes to a temporary file that is
// synced and renamed into place, because the Pi's power may be cut at any
// moment.
package library

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"sepiano/se"
)

// Origins of a piece.
const (
	OriginImport   = "import"
	OriginUpload   = "upload"
	OriginMIDI     = "midi"
	OriginRecorded = "recorded"
)

// DefaultInstrument is the rendering this piano is.
const DefaultInstrument = "Steinway_SED133291"

// ErrNotFound means no piece has that id.
var ErrNotFound = errors.New("no such piece")

// Piece is one performance in the library.
type Piece struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Composer      string    `json:"composer"`
	Performer     string    `json:"performer"`
	RecordedOn    string    `json:"recorded_on"`
	Instrument    string    `json:"instrument"`
	PerfKey       string    `json:"perf_key"`
	DurationMs    int64     `json:"duration_ms"`
	Notes         string    `json:"notes"`
	Origin        string    `json:"origin"`
	SourceName    string    `json:"source_name"`
	HeaderText    string    `json:"header_text"`
	KBSHA256      string    `json:"kb_sha256"`
	HasMIDISource bool      `json:"has_midi_source"`
	AddedAt       time.Time `json:"added_at"`
}

// Patch changes metadata; nil fields are left alone.
type Patch struct {
	Title      *string `json:"title"`
	Composer   *string `json:"composer"`
	Performer  *string `json:"performer"`
	RecordedOn *string `json:"recorded_on"`
	Instrument *string `json:"instrument"`
	Notes      *string `json:"notes"`
}

// Settings are the service's preferences.
type Settings struct {
	PreferredInstrument string `json:"preferred_instrument"`
	Level               int    `json:"level"`
}

// Filter selects pieces. Empty fields match everything.
type Filter struct {
	Q, Composer, Performer, Instrument string
}

// Library is the store. It is safe for concurrent use.
type Library struct {
	dir      string // the pieces
	settings string // settings file

	mu     sync.RWMutex
	pieces map[string]*Piece
	bySHA  map[string]string
	set    Settings
}

// Open opens (creating if needed) a library under dataDir: pieces go in
// dataDir/library, settings in dataDir/settings.json.
func Open(dataDir string) (*Library, error) {
	l := &Library{dir: filepath.Join(dataDir, "library"), settings: filepath.Join(dataDir, "settings.json")}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return nil, err
	}
	l.set = Settings{PreferredInstrument: DefaultInstrument}
	if b, err := os.ReadFile(l.settings); err == nil {
		if err := json.Unmarshal(b, &l.set); err != nil {
			return nil, fmt.Errorf("%s: %w", l.settings, err)
		}
	}
	if _, err := l.Rescan(); err != nil {
		return nil, err
	}
	return l, nil
}

// Dir is the folder holding the pieces' files.
func (l *Library) Dir() string { return l.dir }

// Rescan rebuilds the index from the folder. Performance files without
// metadata (copied in by hand, or left by a power cut before the metadata was
// written) get metadata derived from their contents.
func (l *Library) Rescan() (int, error) {
	pieces := map[string]*Piece{}
	bySHA := map[string]string{}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		id, ext := strings.TrimSuffix(name, filepath.Ext(name)), strings.ToLower(filepath.Ext(name))
		switch {
		case ext == ".json":
			if !have[id+".kb"] {
				continue // metadata left over from a delete cut short
			}
			b, err := os.ReadFile(filepath.Join(l.dir, name))
			if err != nil {
				return 0, err
			}
			var p Piece
			if err := json.Unmarshal(b, &p); err != nil || p.ID != id {
				p = Piece{} // damaged: rebuilt below
				if built, err := l.describe(id, id, OriginImport); err == nil {
					p = *built
					l.writeMeta(&p)
				} else {
					continue
				}
			}
			pieces[id] = &p
		case ext == ".kb" && !have[id+".json"]:
			if !have[id+".lp"] || !have[id+".sp"] {
				continue
			}
			source := id
			if !isID(id) { // copied in under its own name: give it an id
				nid := newID(func(s string) bool { return pieces[s] != nil || have[s+".kb"] })
				for _, x := range []string{".kb", ".lp", ".sp", ".mid"} {
					if have[id+x] {
						if err := os.Rename(filepath.Join(l.dir, id+x), filepath.Join(l.dir, nid+x)); err != nil {
							return 0, err
						}
					}
				}
				have[nid+".kb"] = true
				id = nid
			}
			p, err := l.describe(id, source, OriginImport)
			if err != nil {
				continue
			}
			if err := l.writeMeta(p); err != nil {
				return 0, err
			}
			pieces[id] = p
		}
	}
	for id, p := range pieces {
		bySHA[p.KBSHA256] = id
	}
	l.mu.Lock()
	l.pieces, l.bySHA = pieces, bySHA
	l.mu.Unlock()
	return len(pieces), nil
}

// describe builds metadata for the files dir/id.* named after sourceName.
func (l *Library) describe(id, sourceName, origin string) (*Piece, error) {
	kb, err := os.ReadFile(filepath.Join(l.dir, id+".kb"))
	if err != nil {
		return nil, err
	}
	k, err := se.ParseKB(kb)
	if err != nil {
		return nil, err
	}
	p := newPiece(id, sourceName, origin, kb, k)
	if fi, err := os.Stat(filepath.Join(l.dir, id+".kb")); err == nil {
		p.AddedAt = fi.ModTime()
	}
	_, err = os.Stat(filepath.Join(l.dir, id+".mid"))
	p.HasMIDISource = err == nil
	return p, nil
}

func newPiece(id, sourceName, origin string, kb []byte, k *se.KB) *Piece {
	m := ParseName(sourceName)
	sum := sha256.Sum256(kb)
	return &Piece{
		ID: id, Title: m.Title, Composer: m.Composer, Performer: m.Performer,
		RecordedOn: m.RecordedOn, Instrument: m.Instrument, PerfKey: m.PerfKey,
		DurationMs: se.TickDuration(k.End).Milliseconds(), Origin: origin,
		SourceName: sourceName, HeaderText: strings.TrimSpace(k.Text),
		KBSHA256: hex.EncodeToString(sum[:]), AddedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func (l *Library) writeMeta(p *Piece) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(l.dir, p.ID+".json"), append(b, '\n'))
}

// writeAtomic replaces path with data so that a power cut leaves either the
// old file or the new one.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil { // make the rename itself durable
		d.Sync()
		d.Close()
	}
	return nil
}

const idAlphabet = "abcdefghijkmnpqrstuvwxyz23456789" // no l, o, 0, 1

func isID(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune(idAlphabet, c) {
			return false
		}
	}
	return true
}

func newID(taken func(string) bool) string {
	const alphabet = idAlphabet
	for {
		b := make([]byte, 8)
		rand.Read(b)
		for i := range b {
			b[i] = alphabet[int(b[i])%len(alphabet)]
		}
		if id := string(b); !taken(id) {
			return id
		}
	}
}

// List returns the pieces matching f, sorted by composer, title and instrument.
func (l *Library) List(f Filter) []Piece {
	words := strings.Fields(strings.ToLower(f.Q))
	l.mu.RLock()
	out := make([]Piece, 0, len(l.pieces))
	for _, p := range l.pieces {
		if f.Composer != "" && !strings.EqualFold(p.Composer, f.Composer) ||
			f.Performer != "" && !strings.EqualFold(p.Performer, f.Performer) ||
			f.Instrument != "" && !strings.EqualFold(p.Instrument, f.Instrument) {
			continue
		}
		text := strings.ToLower(strings.Join([]string{p.Title, p.Composer, p.Performer, p.Notes, p.SourceName, p.Instrument, p.RecordedOn}, " "))
		match := true
		for _, w := range words {
			if !strings.Contains(text, w) {
				match = false
				break
			}
		}
		if match {
			out = append(out, *p)
		}
	}
	l.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if c := strings.Compare(strings.ToLower(a.Composer), strings.ToLower(b.Composer)); c != 0 {
			return c < 0
		}
		if c := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
			return c < 0
		}
		if a.Instrument != b.Instrument {
			return a.Instrument < b.Instrument
		}
		return a.ID < b.ID
	})
	return out
}

// Get returns one piece.
func (l *Library) Get(id string) (Piece, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	p, ok := l.pieces[id]
	if !ok {
		return Piece{}, ErrNotFound
	}
	return *p, nil
}

// Files returns the folder and base name of a piece's performance files.
func (l *Library) Files(id string) (dir, base string, err error) {
	if _, err := l.Get(id); err != nil {
		return "", "", err
	}
	return l.dir, id, nil
}

// Update changes a piece's metadata.
func (l *Library) Update(id string, patch Patch) (Piece, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.pieces[id]
	if !ok {
		return Piece{}, ErrNotFound
	}
	q := *p
	for _, f := range []struct {
		src *string
		dst *string
	}{{patch.Title, &q.Title}, {patch.Composer, &q.Composer}, {patch.Performer, &q.Performer},
		{patch.RecordedOn, &q.RecordedOn}, {patch.Instrument, &q.Instrument}, {patch.Notes, &q.Notes}} {
		if f.src != nil {
			*f.dst = strings.TrimSpace(*f.src)
		}
	}
	if q.Title == "" {
		return Piece{}, errors.New("the title cannot be empty")
	}
	if err := l.writeMeta(&q); err != nil {
		return Piece{}, err
	}
	*p = q
	return q, nil
}

// Delete removes a piece and its files.
func (l *Library) Delete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.pieces[id]
	if !ok {
		return ErrNotFound
	}
	// The performance first: without its .kb a piece no longer exists, even
	// if the power fails half way.
	for _, ext := range []string{".kb", ".lp", ".sp", ".mid", ".json"} {
		if err := os.Remove(filepath.Join(l.dir, id+ext)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	delete(l.bySHA, p.KBSHA256)
	delete(l.pieces, id)
	return nil
}

// Settings returns the preferences.
func (l *Library) Settings() Settings {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.set
}

// SaveSettings stores the preferences.
func (l *Library) SaveSettings(s Settings) error {
	if s.Level > 0 {
		return errors.New("the level cannot be above 0")
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := writeAtomic(l.settings, append(b, '\n')); err != nil {
		return err
	}
	l.set = s
	return nil
}
