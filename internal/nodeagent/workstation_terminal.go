package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// terminal is a shell on a pseudo-terminal, started in a granted folder.
// Its output is kept in a buffer of terminalBuffer bytes, read by offset
// (terminal.read); it ends when its shell exits, when it is closed, or
// after TerminalIdle without a call.
type terminal struct {
	id, folder, requested, shell, sandbox string
	started                               time.Time
	proc                                  ptyProc
	tmp                                   string

	mu      sync.Mutex
	buf     []byte
	total   int64
	closed  bool
	exit    *int
	lastUse time.Time
	wake    chan struct{}
}

// defaultShell is the shell a terminal runs: the user's on macOS and
// Linux, PowerShell (else cmd) on Windows.
func defaultShell() []string {
	if runtime.GOOS == "windows" {
		if p, err := platform.LookTool("powershell.exe"); err == nil {
			return []string{p, "-NoLogo"}
		}
		if c := envValue("COMSPEC"); c != "" && filepath.IsAbs(c) {
			return []string{c}
		}
		win := envValue("SystemRoot")
		if win == "" {
			win = `C:\Windows`
		}
		return []string{filepath.Join(win, "System32", "cmd.exe")}
	}
	if sh := envValue("SHELL"); sh != "" && filepath.IsAbs(sh) {
		return []string{sh, "-i"}
	}
	for _, sh := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return []string{sh, "-i"}
		}
	}
	return []string{"/bin/sh", "-i"}
}

func (a *Agent) terminalOpen(eff nodeproto.Capabilities, p nodeproto.TerminalOpenParams) (any, *nodeproto.Error) {
	if err := nodeproto.CheckSize(p.Cols, p.Rows); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	folder, perr := a.confinedWorkFolder(eff, "terminal", p.Folder)
	if perr != nil {
		return nil, perr
	}
	cols, rows := p.Cols, p.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	a.work.mu.Lock()
	open := 0
	for _, t := range a.work.terms {
		if !t.isClosed() {
			open++
		}
	}
	a.work.mu.Unlock()
	if open >= maxSessions {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%d terminals are open on this node already", open)
	}
	tmp, err := os.MkdirTemp("", "fridolin-term-")
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "a temp folder: %v", err)
	}
	shell := a.TerminalShell
	if len(shell) == 0 {
		shell = defaultShell()
	}
	sb, _ := a.probeWork()
	plan := newSandboxPlan(folder, tmp, a.Dir, "")
	// The shell comes from the node's own settings or its user's login
	// shell, never from the request; what is typed reaches it only
	// through the PTY once it runs (terminal.send).
	prog, args, err := plan.wrap(sb, shell[0], shell[1:])
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	// The shell is the owner's, in the granted folder: what this
	// capability is for. The sandbox around it, where there is one, keeps
	// its writes in that folder and its temp folder.
	cmd := exec.Command(prog, args...)
	cmd.Dir = folder
	cmd.Env = append(filteredEnv(), "TERM=xterm-256color", "TMPDIR="+tmp, "HISTFILE="+filepath.Join(tmp, "history"), "FRIDOLIN_NODE_TERMINAL=1")
	proc, err := startPTY(cmd, cols, rows)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "starting %s: %v", shell[0], err)
	}
	now := time.Now()
	t := &terminal{id: newSessionID(), folder: folder, requested: filepath.Clean(p.Folder), shell: filepath.Base(shell[0]), sandbox: sb,
		started: now, proc: proc, tmp: tmp, lastUse: now, wake: make(chan struct{})}
	a.work.mu.Lock()
	a.work.terms[t.id] = t
	a.pruneWorkLocked(now)
	a.work.mu.Unlock()
	go t.readLoop()
	go t.waitLoop()
	go t.idleLoop(a.terminalIdle())
	a.logf("node: opened terminal %s (%s) in %s, sandbox %s", t.id, t.shell, folder, sb)
	return t.info(), nil
}

func (a *Agent) terminalIdle() time.Duration {
	if a.TerminalIdle > 0 {
		return a.TerminalIdle
	}
	return DefaultTerminalIdle
}

// filteredEnv is the agent's environment without what a session must not
// inherit: the core's login token, the agent's own settings.
func filteredEnv() []string {
	return slices.DeleteFunc(platform.EnvWith(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		k = strings.ToUpper(k)
		return k == "CLAUDE_CODE_OAUTH_TOKEN" || k == "ANTHROPIC_API_KEY" || strings.HasPrefix(k, "FRIDOLIN_")
	})
}

func (t *terminal) readLoop() {
	b := make([]byte, 32<<10)
	for {
		n, err := t.proc.Read(b)
		if n > 0 {
			t.mu.Lock()
			t.buf = append(t.buf, b[:n]...)
			if over := len(t.buf) - terminalBuffer; over > 0 {
				t.buf = slices.Clone(t.buf[over:])
			}
			t.total += int64(n)
			t.wakeLocked()
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (t *terminal) waitLoop() {
	code := t.proc.Wait()
	t.mu.Lock()
	t.closed, t.exit = true, &code
	t.wakeLocked()
	t.mu.Unlock()
	_ = os.RemoveAll(t.tmp)
}

func (t *terminal) idleLoop(idle time.Duration) {
	tick := time.NewTicker(min(idle/4, 30*time.Second) + time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		t.mu.Lock()
		closed, quiet := t.closed, time.Since(t.lastUse)
		t.mu.Unlock()
		if closed {
			return
		}
		if quiet >= idle {
			t.end()
			return
		}
	}
}

// wakeLocked wakes the reads waiting for output.
func (t *terminal) wakeLocked() {
	close(t.wake)
	t.wake = make(chan struct{})
}

func (t *terminal) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// end kills the shell; waitLoop records it.
func (t *terminal) end() {
	if !t.isClosed() {
		t.proc.Kill()
	}
}

func (t *terminal) info() nodeproto.TerminalInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return nodeproto.TerminalInfo{ID: t.id, Folder: t.folder, Shell: t.shell, Sandbox: t.sandbox, Started: t.started, Closed: t.closed, ExitCode: t.exit, Output: t.total}
}

func (a *Agent) terminalByID(id string) (*terminal, *nodeproto.Error) {
	a.work.mu.Lock()
	defer a.work.mu.Unlock()
	t := a.work.terms[id]
	if t == nil {
		return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no terminal %q", id)
	}
	return t, nil
}

func (a *Agent) terminalSend(p nodeproto.TerminalSendParams) (any, *nodeproto.Error) {
	t, perr := a.terminalByID(p.ID)
	if perr != nil {
		return nil, perr
	}
	if len(p.Data) > nodeproto.MaxTerminalInput {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "at most %d bytes at once", nodeproto.MaxTerminalInput)
	}
	t.mu.Lock()
	closed := t.closed
	t.lastUse = time.Now()
	t.mu.Unlock()
	if closed {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "terminal %s has ended", p.ID)
	}
	if _, err := t.proc.Write([]byte(p.Data)); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "typing into terminal %s: %v", p.ID, err)
	}
	return t.info(), nil
}

func (a *Agent) terminalRead(ctx context.Context, p nodeproto.TerminalReadParams) (any, *nodeproto.Error) {
	t, perr := a.terminalByID(p.ID)
	if perr != nil {
		return nil, perr
	}
	wait := time.Duration(min(max(p.WaitMS, 0), nodeproto.MaxTerminalWaitMS)) * time.Millisecond
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		t.mu.Lock()
		t.lastUse = time.Now()
		if p.Offset < t.total || t.closed || wait == 0 {
			out := t.outputLocked(p.Offset)
			t.mu.Unlock()
			return out, nil
		}
		wake := t.wake
		t.mu.Unlock()
		select {
		case <-wake:
		case <-deadline.C:
			wait = 0
		case <-ctx.Done():
			return nil, nodeproto.Errorf(nodeproto.CodeTimeout, "the read was cancelled")
		}
	}
}

// outputLocked is the output from offset on, as much as one read carries.
func (t *terminal) outputLocked(offset int64) nodeproto.TerminalOutput {
	start := t.total - int64(len(t.buf))
	out := nodeproto.TerminalOutput{ID: t.id, Closed: t.closed, ExitCode: t.exit}
	if offset > t.total || offset < 0 {
		offset = t.total
	}
	if offset < start {
		out.Dropped, offset = true, start
	}
	data := t.buf[offset-start:]
	if len(data) > nodeproto.MaxTerminalRead {
		data = data[:nodeproto.MaxTerminalRead]
	}
	out.Data, out.Offset, out.Next = slices.Clone(data), offset, offset+int64(len(data))
	if out.Next < t.total {
		// More waits: the shell has not ended as far as this read goes.
		out.Closed, out.ExitCode = false, nil
	}
	return out
}

func (a *Agent) terminalResize(p nodeproto.TerminalResizeParams) (any, *nodeproto.Error) {
	t, perr := a.terminalByID(p.ID)
	if perr != nil {
		return nil, perr
	}
	if err := nodeproto.CheckSize(p.Cols, p.Rows); err != nil || p.Cols == 0 || p.Rows == 0 {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "terminal size %d×%d", p.Cols, p.Rows)
	}
	if !t.isClosed() {
		if err := t.proc.Resize(p.Cols, p.Rows); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "resizing: %v", err)
		}
	}
	return t.info(), nil
}

func (a *Agent) terminalClose(id string) (any, *nodeproto.Error) {
	t, perr := a.terminalByID(id)
	if perr != nil {
		return nil, perr
	}
	t.end()
	deadline := time.Now().Add(5 * time.Second)
	for !t.isClosed() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	a.logf("node: closed terminal %s", id)
	return t.info(), nil
}

func (a *Agent) terminalList() nodeproto.TerminalList {
	a.work.mu.Lock()
	defer a.work.mu.Unlock()
	a.pruneWorkLocked(time.Now())
	out := nodeproto.TerminalList{Terminals: []nodeproto.TerminalInfo{}}
	for _, t := range a.work.terms {
		out.Terminals = append(out.Terminals, t.info())
	}
	slices.SortFunc(out.Terminals, func(x, y nodeproto.TerminalInfo) int { return x.Started.Compare(y.Started) })
	return out
}

// pruneWorkLocked forgets sessions that ended more than keepEnded ago.
func (a *Agent) pruneWorkLocked(now time.Time) {
	for id, t := range a.work.terms {
		t.mu.Lock()
		old := t.closed && now.Sub(t.lastUse) > keepEnded
		t.mu.Unlock()
		if old {
			delete(a.work.terms, id)
		}
	}
	for id, s := range a.work.agents {
		if e := s.endedAt(); !e.IsZero() && now.Sub(e) > keepEnded {
			delete(a.work.agents, id)
		}
	}
}

// envValue is the agent's environment variable name ("" when unset), as
// platform.EnvWith has it; Windows names ignore case.
func envValue(name string) string {
	for _, kv := range platform.EnvWith() {
		k, v, _ := strings.Cut(kv, "=")
		if k == name || runtime.GOOS == "windows" && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
