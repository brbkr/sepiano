package se

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpus returns the directory holding the original recordings (the parent
// of the sepiano module by default, or $SE_CORPUS).
func corpus(t *testing.T) string {
	dir := os.Getenv("SE_CORPUS")
	if dir == "" {
		dir = "../.."
	}
	if _, err := os.Stat(filepath.Join(dir, "Boes", "10Sec-C9-400.kb")); err != nil {
		t.Skip("recording corpus not found; set SE_CORPUS")
	}
	return dir
}

func walk(t *testing.T, ext string) []string {
	var out []string
	filepath.Walk(corpus(t), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ext) && !strings.Contains(p, "sepiano") {
			out = append(out, p)
		}
		return nil
	})
	if len(out) == 0 {
		t.Fatalf("no %s files", ext)
	}
	return out
}

func TestTenSecondTestFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(corpus(t), "Boes", "10Sec-C9-400.kb"))
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParseKB(raw)
	if err != nil {
		t.Fatal(err)
	}
	notes := k.Notes()
	want := []Event{
		{808, 108, true, 400}, {848, 108, false, 0},
		{8008, 108, true, 400}, {8048, 108, false, 0},
		{8808, 109, true, 400}, {9048, 109, false, 0},
	}
	if len(notes) != len(want) {
		t.Fatalf("got %d notes: %+v", len(notes), notes)
	}
	for i := range want {
		if notes[i] != want[i] {
			t.Errorf("note %d = %+v, want %+v", i, notes[i], want[i])
		}
	}
	if k.Format != 0x0F || k.Model != 0x11 || !strings.HasPrefix(k.Text, "Copyright (c) Zenph") {
		t.Errorf("header = %02x %02x %q", k.Format, k.Model, k.Text)
	}
}

func TestKBRoundTrip(t *testing.T) {
	for _, f := range walk(t, ".kb") {
		raw, _ := os.ReadFile(f)
		k, err := ParseKB(raw)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		pad := bytes.HasSuffix(raw, bytes.Repeat([]byte{0xFF}, 20))
		enc, err := k.Encode(pad)
		if err != nil {
			t.Errorf("%s: encode: %v", f, err)
			continue
		}
		if !bytes.Equal(enc, raw) {
			t.Errorf("%s: re-encoding differs (%d vs %d bytes)", f, len(enc), len(raw))
		}
	}
}

func TestPedalRoundTrip(t *testing.T) {
	files := append(walk(t, ".lp"), walk(t, ".sp")...)
	same := 0
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		p, err := ParsePedal(raw)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		enc, _ := p.Encode()
		if bytes.Equal(enc, raw) {
			same++
			continue
		}
		// Must at least decode to the same samples.
		q, err := ParsePedal(enc)
		if err != nil || !bytes.Equal(q.Samples, p.Samples) {
			t.Errorf("%s: re-encoded samples differ", f)
		}
	}
	t.Logf("%d/%d pedal files re-encode byte-identically", same, len(files))
	if same != len(files) {
		t.Errorf("only %d/%d byte-identical", same, len(files))
	}
}

func TestEncryptedFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(corpus(t), "Hamb", "banneret.kb"))
	if err != nil {
		t.Skip("banneret.kb not present")
	}
	k, err := ParseKB(raw)
	if err != nil {
		t.Fatalf("encrypted file does not decode: %v", err)
	}
	if k.Seed == [4]byte{} || len(k.Notes()) < 100 {
		t.Fatalf("seed %x, %d notes", k.Seed, len(k.Notes()))
	}
	enc, _ := k.Encode(false)
	if !bytes.Equal(enc, raw) {
		t.Error("encrypted file does not re-encode identically")
	}
	for _, ext := range []string{".lp", ".sp"} {
		raw, _ := os.ReadFile(filepath.Join(corpus(t), "Hamb", "banneret"+ext))
		if _, err := ParsePedal(raw); err != nil {
			t.Errorf("banneret%s: %v", ext, err)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	p := &Performance{
		KB: &KB{Format: 0x0F, Model: 0x11, Text: "test\r\n", Events: []Event{
			{Tick: 1000, Key: 60, On: true, Vel: 120},
			{Tick: 1000, Key: 64, On: true, Vel: 600},
			{Tick: 1800, Key: 60}, {Tick: 1801, Key: 64},
		}, End: 2000},
		Damper: &Pedal{Samples: []byte{0, 0, 10, 200, 255, 255, 255}},
	}
	base := filepath.Join(t.TempDir(), "x")
	if err := p.Save(base); err != nil {
		t.Fatal(err)
	}
	q, err := Load(base + ".kb")
	if err != nil {
		t.Fatal(err)
	}
	got := q.KB.Notes()
	if len(got) != 4 || got[1].Vel != 600 || got[3].Tick != 1801 {
		t.Fatalf("notes = %+v", got)
	}
	if n := len(q.Soft.Samples); n*SoftPeriod < int(q.KB.End)+TicksPerSecond {
		t.Errorf("soft pedal too short: %d samples", n)
	}
	if q.Damper.At(4*DamperPeriod, DamperPeriod) != 255 || q.Damper.At(1e6, DamperPeriod) != 255 {
		t.Error("damper samples not preserved/extended")
	}
}
