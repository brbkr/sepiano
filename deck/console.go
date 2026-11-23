package deck

import (
	"regexp"
	"strconv"
	"strings"
)

// What a console line from the player or recorder means to the deck.
type consoleEvent int

const (
	evNone consoleEvent = iota
	evStart
	evPause
	evResume
	evQuit
	evRestart
	evEnd
	evLevel
	evArmed
	evRecording
	evInterlock
)

var levelRE = regexp.MustCompile(`^LEVEL=\s*(-?\d+)`)

// parseLine classifies one console line. "START PLAYING", "END OF
// PERFORMANCE", "QUIT PLAYING" and "LEVEL=0 (NORMAL)" / "LEVEL=-n" (down to
// -16) were printed by the real player on the piano; the pause, resume,
// restart and recorder strings come from the program text and still need
// checking there. Lines are matched by prefix, because the key help
// ("DEPRESS SPACE BAR TO STOP/RESUME PLAYING") mentions the same words.
func parseLine(line string) (consoleEvent, int) {
	s := strings.TrimSpace(strings.ToUpper(line))
	switch {
	case strings.HasPrefix(s, "START PLAYING"):
		return evStart, 0
	case strings.HasPrefix(s, "STOP PLAYING"):
		return evPause, 0
	case strings.HasPrefix(s, "RESUME PLAYING"):
		return evResume, 0
	case strings.HasPrefix(s, "QUIT PLAYING"):
		return evQuit, 0
	case strings.HasPrefix(s, "RESTART PERFORMANCE"):
		return evRestart, 0
	case strings.HasPrefix(s, "END OF PERFORMANCE"):
		return evEnd, 0
	case strings.Contains(s, "INTERLOCK FAILURE"):
		return evInterlock, 0
	case strings.Contains(s, "SPACE BAR TO START RECORDING"):
		return evArmed, 0
	case strings.Contains(s, "ENTER TO END RECORDING"):
		return evRecording, 0
	}
	if m := levelRE.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			return evLevel, n
		}
	}
	return evNone, 0
}

// lineWriter splits the console byte stream into lines for the deck.
type lineWriter struct {
	buf  []byte
	emit func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if strings.TrimSpace(line) != "" {
			w.emit(line)
		}
	}
	if len(w.buf) > 4096 { // a runaway line without an end
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}
