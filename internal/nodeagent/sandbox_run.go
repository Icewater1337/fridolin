package nodeagent

import (
	"os/exec"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// sandboxRun is one run in a brain's sandbox: its stdout, kept for
// sandbox.read, the tail of its stderr, and how it ended.
type sandboxRun struct {
	id, brain, name string

	mu       sync.Mutex
	changed  chan struct{} // closed and replaced at each change
	out      []byte
	stderr   []byte
	overflow bool
	done     bool
	exitCode int
	errMsg   string
	ended    time.Time
	lastRead time.Time
	cmd      *exec.Cmd
}

func newSandboxRun(id, brain string) *sandboxRun {
	return &sandboxRun{id: id, brain: brain, name: nodeproto.SandboxName(brain), changed: make(chan struct{}), lastRead: time.Now()}
}

// notify wakes the reads that wait. Call with mu held.
func (r *sandboxRun) notify() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// stdoutWriter keeps the run's stdout up to the buffer; past it, the
// run is stopped (overflowed says so) and the rest dropped.
type stdoutWriter struct {
	r          *sandboxRun
	overflowed func()
}

func (w stdoutWriter) Write(p []byte) (int, error) {
	r := w.r
	r.mu.Lock()
	keep := min(len(p), max(nodeproto.SandboxRunBuffer-len(r.out), 0))
	r.out = append(r.out, p[:keep]...)
	first := keep < len(p) && !r.overflow
	if keep < len(p) {
		r.overflow = true
	}
	if keep > 0 {
		r.notify()
	}
	r.mu.Unlock()
	if first && w.overflowed != nil {
		go w.overflowed()
	}
	return len(p), nil
}

// stderrWriter keeps the last MaxSandboxStderrTail bytes of stderr.
type stderrWriter struct{ r *sandboxRun }

func (w stderrWriter) Write(p []byte) (int, error) {
	r := w.r
	r.mu.Lock()
	r.stderr = append(r.stderr, p...)
	if n := len(r.stderr) - nodeproto.MaxSandboxStderrTail; n > 0 {
		r.stderr = slices.Clone(r.stderr[n:])
	}
	r.mu.Unlock()
	return len(p), nil
}

// finish records how the run ended.
func (r *sandboxRun) finish(code int, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overflow && msg == "" {
		msg = "the run's output went past the node's buffer, so it was stopped"
	}
	r.done, r.exitCode, r.errMsg, r.ended = true, code, msg, time.Now()
	r.notify()
}

// isDone reports whether the run ended.
func (r *sandboxRun) isDone() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

// readFrom is sandbox.read's answer from offset, now.
func (r *sandboxRun) readFrom(offset int64) (nodeproto.SandboxReadResult, chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastRead = time.Now()
	if offset > int64(len(r.out)) {
		return nodeproto.SandboxReadResult{}, nil, false
	}
	data := r.out[offset:]
	more := false
	if len(data) > nodeproto.MaxSandboxRead {
		data, more = data[:nodeproto.MaxSandboxRead], true
	}
	if more || !r.done {
		data = completeRunes(data)
	}
	res := nodeproto.SandboxReadResult{Data: string(data), Next: offset + int64(len(data))}
	if r.done && !more && res.Next == int64(len(r.out)) {
		res.Done, res.ExitCode, res.Error = true, r.exitCode, r.errMsg
		res.StderrTail = string(r.stderr)
	}
	return res, r.changed, true
}

// completeRunes is b without a character cut off at its end, which the
// next read returns whole.
func completeRunes(b []byte) []byte {
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		c := b[len(b)-i]
		if utf8.RuneStart(c) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}
