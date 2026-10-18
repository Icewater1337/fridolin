package logx

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// Ring keeps the most recent log lines in memory for the dashboard. It is
// an io.Writer installed next to stderr with log.SetOutput.
type Ring struct {
	mu      sync.Mutex
	lines   []Line
	max     int
	nextSeq int64
	partial string
}

// Line is one line of log output.
type Line struct {
	Seq  int64     `json:"seq"`
	TS   time.Time `json:"ts"`
	Text string    `json:"text"`
}

// NewRing keeps up to max lines.
func NewRing(max int) *Ring { return &Ring{max: max, nextSeq: 1} }

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	text := r.partial + string(p)
	parts := strings.Split(text, "\n")
	r.partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		r.lines = append(r.lines, Line{Seq: r.nextSeq, TS: time.Now(), Text: line})
		r.nextSeq++
	}
	if over := len(r.lines) - r.max; over > 0 {
		r.lines = append([]Line(nil), r.lines[over:]...)
	}
	return len(p), nil
}

// Since returns up to limit lines with Seq > after, oldest first, and the seq
// to ask for next time.
func (r *Ring) Since(after int64, limit int) ([]Line, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Line
	for _, l := range r.lines {
		if l.Seq > after {
			out = append(out, l)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	next := after
	if len(out) > 0 {
		next = out[len(out)-1].Seq
	}
	return out, next
}

// stdWaLogger routes whatsmeow's logging through the standard logger, so it
// reaches the dashboard's log view as well as stderr.
type stdWaLogger struct {
	module string
	level  int
}

var waLevels = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

// NewWaLogger routes messages at level or above through the process logger.
// Unknown levels use INFO, and child loggers keep their parent's threshold.
func NewWaLogger(module, level string) waLog.Logger {
	lv, ok := waLevels[strings.ToUpper(level)]
	if !ok {
		lv = 1
	}
	return &stdWaLogger{module: module, level: lv}
}

func (l *stdWaLogger) logf(lv int, tag, msg string, args ...interface{}) {
	if lv < l.level {
		return
	}
	log.Printf("[%s %s] %s", l.module, tag, fmt.Sprintf(msg, args...))
}

func (l *stdWaLogger) Debugf(msg string, args ...interface{}) { l.logf(0, "DEBUG", msg, args...) }
func (l *stdWaLogger) Infof(msg string, args ...interface{})  { l.logf(1, "INFO", msg, args...) }
func (l *stdWaLogger) Warnf(msg string, args ...interface{})  { l.logf(2, "WARN", msg, args...) }
func (l *stdWaLogger) Errorf(msg string, args ...interface{}) { l.logf(3, "ERROR", msg, args...) }
func (l *stdWaLogger) Sub(module string) waLog.Logger {
	return &stdWaLogger{module: l.module + "/" + module, level: l.level}
}
