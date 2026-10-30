package claudecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/creack/pty"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// liveDialogs are the questions an interactive Claude Code asks before it
// works, by what the screen shows (with its spaces and escape sequences
// taken out) and the keys that answer them. The development-channels
// warning is the one a live session always meets: its first choice, "I am
// using this for local development", is the one to take. Any other dialog
// is left alone, and the session then never answers its first turn.
var liveDialogs = []struct{ shows, keys string }{
	{"Iamusingthisforlocaldevelopment", "\r"},
}

var ansi = regexp.MustCompile(`\x1b(\[[0-9;?<>=]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[()][A-Z0-9])`)

// LiveProc is an interactive Claude Code in a terminal of its own.
type LiveProc struct {
	cmd  *exec.Cmd
	term *os.File
	done chan struct{}

	mu       sync.Mutex
	screen   []byte // the end of what it wrote, raw
	waitErr  error
	seen     string // the screen's text since the last dialog it answered
	answered int
}

// StartLive starts r in a pseudo-terminal, answering the dialogs it knows
// (liveDialogs). It ends with ctx, and with everything the CLI started
// that stayed in its process group.
func StartLive(ctx context.Context, r Spawn) (*LiveProc, error) {
	cmd := exec.Command(r.Binary, r.Args...)
	cmd.Dir, cmd.Env = r.Dir, r.Env
	term, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		return nil, err
	}
	p := &LiveProc{cmd: cmd, term: term, done: make(chan struct{})}
	go p.read()
	go func() {
		err := cmd.Wait()
		_ = platform.KillProcessGroup(cmd.Process.Pid)
		p.mu.Lock()
		p.waitErr = err
		p.mu.Unlock()
		term.Close()
		close(p.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			p.Kill()
		case <-p.done:
		}
	}()
	return p, nil
}

// read drains the terminal: the CLI blocks when nobody does.
func (p *LiveProc) read() {
	buf := make([]byte, 8192)
	for {
		n, err := p.term.Read(buf)
		if n > 0 {
			p.took(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

const liveScreenBytes = 16 << 10

func (p *LiveProc) took(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.screen = append(p.screen, b...)
	if len(p.screen) > liveScreenBytes {
		p.screen = bytes.Clone(p.screen[len(p.screen)-liveScreenBytes:])
	}
	p.seen += strings.Join(strings.Fields(string(ansi.ReplaceAll(b, nil))), "")
	for _, d := range liveDialogs {
		if i := strings.Index(p.seen, d.shows); i >= 0 {
			p.seen = p.seen[i+len(d.shows):]
			p.answered++
			_, _ = io.WriteString(p.term, d.keys)
		}
	}
	if len(p.seen) > liveScreenBytes {
		p.seen = p.seen[len(p.seen)-liveScreenBytes:]
	}
}

// PID is the CLI's process ID, which leads its process group.
func (p *LiveProc) PID() int { return p.cmd.Process.Pid }

// Done closes once the CLI has exited.
func (p *LiveProc) Done() <-chan struct{} { return p.done }

// Err is how the CLI ended, once Done closed.
func (p *LiveProc) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

// Kill stops the CLI and what it started.
func (p *LiveProc) Kill() { _ = platform.KillProcessGroup(p.cmd.Process.Pid) }

// Screen is the end of what the CLI wrote, without escape sequences: for
// the log when the session did not come up.
func (p *LiveProc) Screen() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(string(ansi.ReplaceAll(p.screen, nil)))
}

// Answered is how many dialogs the process asked and were answered.
func (p *LiveProc) Answered() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answered
}
