package claudecli

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// ArgList is the argument list of a run of the Claude CLI, every
// run the bridge makes: the stream-json output it reads, extra, and a bare
// -p at the end, which reads the prompt from stdin (see Start). With
// isolate, the run gets only its folder's own Claude Code setup, with
// settings, unless "", as its --settings.
func ArgList(isolate bool, settings string, extra ...string) []string {
	args := []string{"--output-format", "stream-json", "--verbose"}
	if isolate {
		// Only the brain folder's own Claude Code setup. Your user settings
		// (with their hooks, plugins and permissions), skills, agents,
		// commands and the MCP servers in ~/.claude.json are yours, and a
		// brain answers whoever may message it; under
		// --dangerously-skip-permissions it would call those MCP tools
		// without asking.
		args = append(args, "--setting-sources", "project,local", "--strict-mcp-config")
		if settings != "" {
			args = append(args, "--settings", settings)
		}
	}
	args = append(args, extra...)
	return append(args, "-p")
}

// Spawn is a run of the Claude CLI for Start.
type Spawn struct {
	Binary    string   // the CLI, or the sandbox that wraps it
	Args      []string // ArgList's, wrapped likewise
	Dir       string
	Env       []string
	Prompt    string        // goes on stdin, for the -p the arguments end in
	Stdout    io.Writer     // gets the stream-json output
	WaitDelay time.Duration // copy-pipe deadline, supplied by the caller
}

// Process is a run of the CLI that Start started.
type Process struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
	// stopped closes after the supervisor signalled the group, or Wait
	// finished naturally. Force-exit waits for this, not for pipe copies.
	stopped  chan struct{}
	stopOnce sync.Once
}

// Start starts the CLI as r says, in a process group of its own,
// which is killed when ctx is done, with everything the CLI started that
// stayed in it (isolateProcessGroup).
func Start(ctx context.Context, r Spawn) (*Process, error) {
	cmd := exec.CommandContext(ctx, r.Binary, r.Args...)
	cmd.Dir = r.Dir
	cmd.Env = r.Env
	platform.IsolateProcessGroup(cmd)
	// The prompt goes on stdin, which -p reads to its end, rather than in
	// argv: argv is bounded by ARG_MAX, about 1 MiB on macOS with the
	// environment, and on Linux one argument by 128 KiB, which a long
	// prompt file and a few pasted texts reach; and every process listing
	// shows it. The CLI takes 10 MB on stdin. os/exec writes it, and Wait
	// stops waiting for that once the CLI has exited.
	cmd.Stdin = strings.NewReader(r.Prompt)
	cmd.Stdout = r.Stdout
	// Only the end of stderr is kept: the CLI, or something it started that
	// holds on to its stderr, could otherwise make the bridge hold any
	// amount of it, and only the last error is of use.
	p := &Process{cmd: cmd, stderr: newTailBuffer(stderrTailBytes), stopped: make(chan struct{})}
	cancel := cmd.Cancel
	cmd.Cancel = func() error {
		defer p.acknowledgeStop()
		return cancel()
	}
	cmd.Stderr = p.stderr
	cmd.WaitDelay = r.WaitDelay
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return p, nil
}

// pid is the CLI's process ID, which leads its process group.
func (p *Process) PID() int { return p.cmd.Process.Pid }

// wait waits for the CLI to exit and for what it wrote to be copied, for
// runWaitDelay at most once it exited, and then kills what is left of its
// process group, a command it left running in the background say, which
// nothing else stops.
func (p *Process) Wait() error {
	err := p.cmd.Wait()
	_ = platform.KillProcessGroup(p.PID())
	p.acknowledgeStop()
	return err
}

func (p *Process) acknowledgeStop() { p.stopOnce.Do(func() { close(p.stopped) }) }

// stderrTail is the end of what the CLI wrote to stderr, without the space
// around it.
func (p *Process) StderrTail() string { return strings.TrimSpace(p.stderr.String()) }

// Stopped closes after the process group was signalled or Wait finished.
// It does not wait for output hooks or delivery.
func (p *Process) Stopped() <-chan struct{} { return p.stopped }
