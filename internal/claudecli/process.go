package claudecli

import (
	"context"
	"io"
	"os/exec"
	"strconv"
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
	// Launch starts the run somewhere other than a process here, in a
	// Docker Sandbox on a node say; nil starts Binary on this machine.
	// It gets the Spawn with Stdout set, and ends the run when ctx ends.
	Launch func(ctx context.Context, s Spawn) (Process, error)
}

// Process is a run of the CLI that Start started: a process on this
// machine, or a run elsewhere that Spawn.Launch started.
type Process interface {
	// PID is the CLI's process ID on this machine, which leads its
	// process group, or 0 for a run that is no process here.
	PID() int
	// ID names the run in the brain's activity log: "pid 1234" for a
	// process here.
	ID() string
	// Wait waits for the run to end and for what it wrote to be copied.
	Wait() error
	// StderrTail is the end of what the run wrote to stderr, without the
	// space around it.
	StderrTail() string
	// Stopped closes once the run was told to stop, or Wait finished. It
	// does not wait for output hooks or delivery.
	Stopped() <-chan struct{}
}

// localProcess is a run of the CLI as a process on this machine.
type localProcess struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
	// stopped closes after the supervisor signalled the group, or Wait
	// finished naturally. Force-exit waits for this, not for pipe copies.
	stopped  chan struct{}
	stopOnce sync.Once
}

// Start starts the CLI as r says, in a process group of its own,
// which is killed when ctx is done, with everything the CLI started that
// stayed in it (isolateProcessGroup). With r.Launch, it starts the run
// that way instead.
func Start(ctx context.Context, r Spawn) (Process, error) {
	if r.Launch != nil {
		return r.Launch(ctx, r)
	}
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
	p := &localProcess{cmd: cmd, stderr: newTailBuffer(stderrTailBytes), stopped: make(chan struct{})}
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

// PID is the CLI's process ID, which leads its process group.
func (p *localProcess) PID() int { return p.cmd.Process.Pid }

// ID is "pid" and the PID.
func (p *localProcess) ID() string { return "pid " + strconv.Itoa(p.PID()) }

// Wait waits for the CLI to exit and for what it wrote to be copied, for
// runWaitDelay at most once it exited, and then kills what is left of its
// process group, a command it left running in the background say, which
// nothing else stops.
func (p *localProcess) Wait() error {
	err := p.cmd.Wait()
	_ = platform.KillProcessGroup(p.PID())
	p.acknowledgeStop()
	return err
}

func (p *localProcess) acknowledgeStop() { p.stopOnce.Do(func() { close(p.stopped) }) }

// StderrTail is the end of what the CLI wrote to stderr, without the space
// around it.
func (p *localProcess) StderrTail() string { return strings.TrimSpace(p.stderr.String()) }

// Stopped closes after the process group was signalled or Wait finished.
// It does not wait for output hooks or delivery.
func (p *localProcess) Stopped() <-chan struct{} { return p.stopped }
