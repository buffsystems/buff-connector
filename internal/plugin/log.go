package plugin

import (
	"bufio"
	"io"
	"strings"
	"sync"
	"time"
)

// Everything a plugin writes to stderr is kept here — the last N lines, per
// instance.
//
// Without it the only answer support can give is "it is failing", and the
// operator is asked to go and find logs they do not know the location of. The
// ring is small and in memory on purpose: it is a diagnostic, not an audit
// trail, and it must not grow unbounded on a customer's machine.

const logLines = 200

// LogRing holds the tail of an instance's output.
type LogRing struct {
	mu    sync.Mutex
	lines []LogLine
}

// LogLine is one line with the moment it was written.
type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Add records a line, dropping the oldest once full.
func (r *LogRing) Add(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, LogLine{At: time.Now().UTC(), Text: text})
	if len(r.lines) > logLines {
		r.lines = r.lines[len(r.lines)-logLines:]
	}
}

// Lines returns a copy, newest last.
func (r *LogRing) Lines() []LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LogLine(nil), r.lines...)
}

// Last is the most recent line, for the "last error" an operator sees first.
func (r *LogRing) Last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == 0 {
		return ""
	}
	return r.lines[len(r.lines)-1].Text
}

// drain copies a plugin's stderr into the ring until it closes.
func (r *LogRing) drain(rc io.Reader) {
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 8<<10), 64<<10)
	for sc.Scan() {
		if line := strings.TrimRight(sc.Text(), "\r"); line != "" {
			r.Add(line)
		}
	}
}
