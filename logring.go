package main

import (
	"strings"
	"sync"
)

// logRing is a thread-safe, fixed-capacity ring buffer of recent log lines. It is
// installed as a tee on the standard logger (io.MultiWriter) so the inspect API's
// /log endpoint can serve the tail of the system log read-only, without touching
// the ledger or fighting the DB lock.
type logRing struct {
	mu    sync.Mutex
	lines []string
	cap   int
}

func newLogRing(capacity int) *logRing {
	if capacity <= 0 {
		capacity = 500
	}
	return &logRing{cap: capacity}
}

// Write implements io.Writer: each newline-delimited record becomes one entry.
func (r *logRing) Write(p []byte) (int, error) {
	n := len(p)
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		r.mu.Lock()
		r.lines = append(r.lines, line)
		if len(r.lines) > r.cap {
			r.lines = r.lines[len(r.lines)-r.cap:]
		}
		r.mu.Unlock()
	}
	return n, nil
}

// Lines returns a snapshot of the buffered log lines, oldest first.
func (r *logRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}
