package main

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// claudeArgList is the argument list of a run of the Claude CLI, every
// run the bridge makes: the stream-json output it reads, extra, and a bare
// -p at the end, which reads the prompt from stdin (see startClaude). With
// isolate, the run gets only its folder's own Claude Code setup, with
// settings, unless "", as its --settings.
func claudeArgList(isolate bool, settings string, extra ...string) []string {
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

// claudeRun is a run of the Claude CLI for startClaude.
type claudeRun struct {
	binary    string   // the CLI, or the sandbox that wraps it
	args      []string // claudeArgList's, wrapped likewise
	dir       string
	env       []string
	prompt    string        // goes on stdin, for the -p the arguments end in
	stdout    io.Writer     // gets the stream-json output
	waitDelay time.Duration // copy-pipe deadline, supplied by the caller
}

// claudeProcess is a run of the CLI that startClaude started.
type claudeProcess struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
	// stopped closes after the supervisor signalled the group, or Wait
	// finished naturally. Force-exit waits for this, not for pipe copies.
	stopped  chan struct{}
	stopOnce sync.Once
}

// startClaude starts the CLI as r says, in a process group of its own,
// which is killed when ctx is done, with everything the CLI started that
// stayed in it (isolateProcessGroup).
func startClaude(ctx context.Context, r claudeRun) (*claudeProcess, error) {
	cmd := exec.CommandContext(ctx, r.binary, r.args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	platform.IsolateProcessGroup(cmd)
	// The prompt goes on stdin, which -p reads to its end, rather than in
	// argv: argv is bounded by ARG_MAX, about 1 MiB on macOS with the
	// environment, and on Linux one argument by 128 KiB, which a long
	// prompt file and a few pasted texts reach; and every process listing
	// shows it. The CLI takes 10 MB on stdin. os/exec writes it, and Wait
	// stops waiting for that once the CLI has exited.
	cmd.Stdin = strings.NewReader(r.prompt)
	cmd.Stdout = r.stdout
	// Only the end of stderr is kept: the CLI, or something it started that
	// holds on to its stderr, could otherwise make the bridge hold any
	// amount of it, and only the last error is of use.
	p := &claudeProcess{cmd: cmd, stderr: newTailBuffer(stderrTailBytes), stopped: make(chan struct{})}
	cancel := cmd.Cancel
	cmd.Cancel = func() error {
		defer p.acknowledgeStop()
		return cancel()
	}
	cmd.Stderr = p.stderr
	cmd.WaitDelay = r.waitDelay
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return p, nil
}

// pid is the CLI's process ID, which leads its process group.
func (p *claudeProcess) pid() int { return p.cmd.Process.Pid }

// wait waits for the CLI to exit and for what it wrote to be copied, for
// runWaitDelay at most once it exited, and then kills what is left of its
// process group, a command it left running in the background say, which
// nothing else stops.
func (p *claudeProcess) wait() error {
	err := p.cmd.Wait()
	_ = platform.KillProcessGroup(p.pid())
	p.acknowledgeStop()
	return err
}

func (p *claudeProcess) acknowledgeStop() { p.stopOnce.Do(func() { close(p.stopped) }) }

// stderrTail is the end of what the CLI wrote to stderr, without the space
// around it.
func (p *claudeProcess) stderrTail() string { return strings.TrimSpace(p.stderr.String()) }

// Stopped closes after the process group was signalled or Wait finished.
// It does not wait for output hooks or delivery.
func (p *claudeProcess) Stopped() <-chan struct{} { return p.stopped }
