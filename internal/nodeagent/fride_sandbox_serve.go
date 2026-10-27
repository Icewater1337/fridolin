package nodeagent

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// The agent keeps the sandboxed FrIDE running while fride-sandbox.json
// says run: it holds the serve script's exec open (the VM stops 30 s
// after its last exec ends), starts it again when it ends, waiting
// longer after each quick end, and ends FrIDE when run is cleared. When
// the agent stops, FrIDE runs on until the VM stops, unless the agent's
// next start holds it again first.
const (
	// DefaultFrideSandboxPoll is how often the agent reads the state.
	DefaultFrideSandboxPoll = 5 * time.Second
	frideServeMinBackoff    = 2 * time.Second
	frideServeMaxBackoff    = 5 * time.Minute
	// frideServeSteady is how long a run must last for the wait before
	// the next start to go back to the shortest.
	frideServeSteady = 2 * time.Minute
	// frideServeStopWait bounds ending FrIDE.
	frideServeStopWait = 30 * time.Second
	// frideServeLogLines bounds the lines one serve logs.
	frideServeLogLines = 20
)

// frideServe is the held exec of the serve script.
type frideServe struct {
	name    string
	cmd     *exec.Cmd
	done    chan struct{}
	started time.Time
	err     error
}

// frideSandboxSupervise starts the loop that keeps the sandboxed FrIDE
// running, on a node with sbx and a state dir.
func (a *Agent) frideSandboxSupervise(ctx context.Context, wg *sync.WaitGroup) {
	if a.Dir == "" || a.sbx() == nil || !a.sandboxOffered() {
		return
	}
	wg.Go(func() { a.frideSandboxLoop(ctx) })
}

func (a *Agent) frideSandboxLoop(ctx context.Context) {
	poll := a.FrideSandboxPoll
	if poll <= 0 {
		poll = DefaultFrideSandboxPoll
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	var cur *frideServe
	backoff, next := frideServeMinBackoff, time.Time{}
	// What was logged last, so that a lasting failure is logged once.
	loadErr, startErr := "", ""
	for {
		st, err := LoadFrideSandbox(a.Dir)
		if msg := errText(err); msg != loadErr {
			loadErr = msg
			if msg != "" {
				a.logf("node: the sandboxed FrIDE: %s", msg)
			}
		}
		want := err == nil && st != nil && st.Run
		switch {
		case want && cur == nil && !time.Now().Before(next):
			cur, err = a.frideServeStart(st.Name)
			if msg := errText(err); msg != startErr && msg != "" {
				a.logf("node: starting the sandboxed FrIDE: %s", msg)
			}
			startErr = errText(err)
			if err != nil {
				next, backoff = time.Now().Add(backoff), min(2*backoff, frideServeMaxBackoff)
			}
		case !want && cur != nil:
			a.frideServeStop(cur)
			cur = nil
			a.logf("node: the sandboxed FrIDE stopped")
		}
		var done chan struct{}
		if cur != nil {
			done = cur.done
		}
		select {
		case <-ctx.Done():
			if cur != nil {
				_ = cur.cmd.Process.Kill()
				<-cur.done
			}
			return
		case <-done:
			if time.Since(cur.started) >= frideServeSteady {
				backoff = frideServeMinBackoff
			}
			a.logf("node: the sandboxed FrIDE's exec ended (%v); starting it again in %s if it should run", cur.err, backoff)
			cur, next, backoff = nil, time.Now().Add(backoff), min(2*backoff, frideServeMaxBackoff)
		case <-t.C:
		}
	}
}

// frideServeStart starts the serve script's exec and returns it held.
func (a *Agent) frideServeStart(name string) (*frideServe, error) {
	w := &lineLogger{logf: a.logf, prefix: "node: the sandboxed FrIDE: ", left: frideServeLogLines}
	args := append([]string{"exec", name}, frideSandboxScript(frideSandboxServe,
		strconv.Itoa(FrideSandboxPort), strconv.Itoa(FrideSandboxWebhookPort))...)
	cmd, err := a.sbx().start(nil, w, w, nil, args...)
	if err != nil {
		return nil, err
	}
	s := &frideServe{name: name, cmd: cmd, done: make(chan struct{}), started: time.Now()}
	go func() {
		s.err = cmd.Wait()
		close(s.done)
	}()
	return s, nil
}

// frideServeStop ends FrIDE in the sandbox, then the held exec.
func (a *Agent) frideServeStop(s *frideServe) {
	ctx, cancel := context.WithTimeout(context.Background(), frideServeStopWait)
	defer cancel()
	args := append([]string{"exec", s.name}, frideSandboxScript(frideSandboxStop)...)
	if _, stderr, err := a.sbx().run(ctx, nil, nil, args...); err != nil {
		a.logf("node: ending the sandboxed FrIDE: %v", sbxError(err, stderr))
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

// errText is err's text, "" for none.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// lineLogger logs what it is written line by line, up to left lines.
type lineLogger struct {
	mu     sync.Mutex
	logf   func(string, ...any)
	prefix string
	buf    []byte
	left   int
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			if len(l.buf) > 4096 {
				l.buf = l.buf[:0]
			}
			return len(p), nil
		}
		if line := bytes.TrimSpace(l.buf[:i]); len(line) > 0 && l.left > 0 {
			l.left--
			l.logf("%s%s", l.prefix, firstLine(line))
		}
		l.buf = l.buf[i+1:]
	}
}
