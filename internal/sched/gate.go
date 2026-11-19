package sched

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// A gate is a cheap program that runs before a scheduled agent's brain
// run and decides whether the brain is woken at all (manifest.ScheduleGate,
// docs/BRAINS.md "Gates"). The last line it writes to stdout is JSON:
//
//	{"wake": false}                   the run ends there, as StatusGated
//	{"wake": true, "data": {...}}     the brain run starts, data in its task
//
// A gate that times out, writes more than MaxGateOutput, exits non-zero or
// ends without that line fails the run; it never skips it.

// Bounds of a gate's output.
const (
	// MaxGateOutput is what a gate may write, stdout and stderr together.
	MaxGateOutput = 1 << 20
	// MaxGateData is the most its data may take in the run's task.
	MaxGateData = 64 << 10
)

// Gate is a gate's decision.
type Gate struct {
	Wake bool
	// Data is what it hands the brain run, as JSON; nil for none.
	Data json.RawMessage
}

// ParseGate reads a gate's decision from its stdout: its last line that
// is not blank.
func ParseGate(stdout string) (Gate, error) {
	text := strings.TrimRight(stdout, " \t\r\n")
	if text == "" {
		return Gate{}, errors.New(`it wrote nothing; its last line must be JSON, {"wake": true} or {"wake": false}`)
	}
	last := text
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		last = text[i+1:]
	}
	last = strings.TrimSpace(last)
	var out struct {
		Wake *bool           `json:"wake"`
		Data json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(strings.NewReader(last))
	if err := dec.Decode(&out); err != nil || dec.More() {
		return Gate{}, fmt.Errorf(`its last line is no JSON object: %q`, truncate(last, 200))
	}
	if out.Wake == nil {
		return Gate{}, fmt.Errorf(`its last line has no "wake": %q`, truncate(last, 200))
	}
	g := Gate{Wake: *out.Wake}
	if *out.Wake && len(out.Data) > 0 && !bytes.Equal(out.Data, []byte("null")) {
		if len(out.Data) > MaxGateData {
			return Gate{}, fmt.Errorf("its data is %d KiB, more than the %d KiB a run's task takes", len(out.Data)>>10, MaxGateData>>10)
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, out.Data, "", "  "); err != nil {
			return Gate{}, fmt.Errorf("its data: %w", err)
		}
		g.Data = buf.Bytes()
	}
	return g, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// GateOutput keeps what a gate writes, stdout and stderr each, up to
// MaxGateOutput together; once it would hold more, it calls Over, once,
// and keeps nothing more. Both writers may be used at once.
type GateOutput struct {
	// Over is called once the output passes MaxGateOutput: the caller
	// stops the program.
	Over func()

	mu             sync.Mutex
	stdout, stderr bytes.Buffer
	total          int
	over           bool
}

// Stdout and Stderr are the gate's two streams.
func (o *GateOutput) Stdout() *GateStream { return &GateStream{o: o, err: false} }
func (o *GateOutput) Stderr() *GateStream { return &GateStream{o: o, err: true} }

// Exceeded reports whether the output passed MaxGateOutput.
func (o *GateOutput) Exceeded() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.over
}

// Text returns what stdout and stderr held.
func (o *GateOutput) Text() (stdout, stderr string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stdout.String(), o.stderr.String()
}

// GateStream is one of a gate's streams (an io.Writer).
type GateStream struct {
	o   *GateOutput
	err bool
}

func (w *GateStream) Write(p []byte) (int, error) {
	o := w.o
	o.mu.Lock()
	if o.over {
		o.mu.Unlock()
		return len(p), nil
	}
	if o.total+len(p) > MaxGateOutput {
		o.over = true
		o.mu.Unlock()
		if o.Over != nil {
			o.Over()
		}
		return len(p), nil
	}
	o.total += len(p)
	if w.err {
		o.stderr.Write(p)
	} else {
		o.stdout.Write(p)
	}
	o.mu.Unlock()
	return len(p), nil
}
