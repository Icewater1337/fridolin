package main

import (
	"sync"
	"unicode/utf8"
)

// stderrTailBytes is how much of a run's stderr the bridge keeps: plenty
// for the last error the CLI printed, and a bound on what the CLI, or a
// background process it left holding its stderr, can make the bridge
// hold in memory (twice this at most, see tailBuffer).
const stderrTailBytes = 32 << 10

// tailBuffer keeps the last limit bytes written to it and drops what came
// before, so a process that floods its output cannot fill the bridge's
// memory while the end, where the error usually is, survives. Writes come
// from the goroutine os/exec copies the output on, so it locks.
type tailBuffer struct {
	mu sync.Mutex
	// buf ends with the last limit bytes, and holds up to twice that:
	// cutting it back to limit at every write once it is full would move
	// limit bytes each time, which a flood of tiny writes makes costly.
	buf     []byte
	limit   int
	dropped bool // something was written before what buf holds
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) > t.limit {
		p = p[len(p)-t.limit:]
		t.dropped = true
	}
	if len(t.buf)+len(p) > 2*t.limit {
		// Cut back so that buf and p together are limit bytes.
		keep := t.limit - len(p)
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-keep:]...)
		t.dropped = true
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

// String returns the last limit bytes, marked with a leading "…" when
// earlier output was dropped. A rune cut in two at the start is dropped
// too, so the result is never invalid UTF-8 because of the cut.
func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, dropped := t.buf, t.dropped
	if len(b) > t.limit {
		b, dropped = b[len(b)-t.limit:], true
	}
	if !dropped {
		return string(b)
	}
	i := 0
	for i < len(b) && i < 4 && !utf8.RuneStart(b[i]) {
		i++
	}
	return "…" + string(b[i:])
}
