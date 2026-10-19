package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// testedClaudeVersion is the oldest Claude Code release the bridge was
// tested with. Brains rely on its flags (--effort, --setting-sources,
// --strict-mcp-config, --disallowed-tools), on the claudeMdExcludes
// setting passed with --settings, and on the shape of its stream-json
// events; brain.go notes what was checked against it. The CLI updates
// itself, so an older one usually means auto-updates are off.
const testedClaudeVersion = "2.1.302"

type claudeCacheEntry struct {
	file string // the binary, symlinks resolved
	mod  time.Time
	size int64
	st   claudeStatus
}

var (
	claudeVersionMu    sync.Mutex
	claudeVersionCache = map[string]claudeCacheEntry{}
)

// claudeInfo finds the CLI and its version. The version is cached until the
// binary changes: `claude` on the PATH is a symlink the CLI moves to each
// new release it installs, and the dashboard must not show the old one for
// as long as the bridge runs. A CLI that is not found is looked up again
// every time, so installing it shows at once. It runs the CLI with a
// brain's environment: a claude_path wrapper may run what a brain can
// change.
func claudeInfo(path string, passthrough []string) claudeStatus {
	return cliInfo(path, passthrough, claudeChild)
}

func codexInfo(path string, passthrough []string) claudeStatus {
	return cliInfo(path, passthrough, codexChild)
}

func cliInfo(path string, passthrough []string, scope childScope) claudeStatus {
	p, err := exec.LookPath(path)
	if err != nil {
		return claudeStatus{Path: path}
	}
	file, err := filepath.EvalSymlinks(p)
	if err != nil {
		file = p
	}
	var mod time.Time
	var size int64
	if fi, err := os.Stat(file); err == nil {
		mod, size = fi.ModTime(), fi.Size()
	}

	claudeVersionMu.Lock()
	defer claudeVersionMu.Unlock()
	if c, ok := claudeVersionCache[path]; ok && c.file == file && c.mod.Equal(mod) && c.size == size {
		return c.st
	}
	st := claudeStatus{Path: p, Found: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cmd := exec.CommandContext(ctx, p, "--version")
	cmd.Env = childEnv(scope, passthrough)
	out, err := cmd.Output()
	cancel()
	if err == nil {
		st.Version = strings.TrimSpace(string(out))
	}
	claudeVersionCache[path] = claudeCacheEntry{file: file, mod: mod, size: size, st: st}
	return st
}

// parseClaudeVersion reads the release from `claude --version` output such
// as "2.1.302 (Claude Code)".
func parseClaudeVersion(s string) ([3]int, bool) {
	var v [3]int
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return v, false
	}
	num := strings.TrimPrefix(fields[0], "v")
	if i := strings.IndexAny(num, "-+"); i >= 0 {
		num = num[:i]
	}
	parts := strings.Split(num, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// claudeVersionWarning says when the CLI is older than testedClaudeVersion,
// or "" when it is not, or when its version cannot be told.
func claudeVersionWarning(st claudeStatus) string {
	have, ok := parseClaudeVersion(st.Version)
	if !st.Found || !ok {
		return ""
	}
	tested, _ := parseClaudeVersion(testedClaudeVersion)
	for i := range have {
		if have[i] != tested[i] {
			if have[i] > tested[i] {
				return ""
			}
			return fmt.Sprintf("The Claude Code CLI at %s is version %d.%d.%d, older than %s, the oldest release Fridolin was tested with: brains may fail, or run without some of their settings. Update it (claude update).",
				st.Path, have[0], have[1], have[2], testedClaudeVersion)
		}
	}
	return ""
}

// logClaudeCLI says at startup which CLI brains run and warns when it is
// missing or too old, so a log excerpt shows it next to the bridge's own
// version.
func logClaudeCLI(path string, passthrough []string) {
	st := claudeInfo(path, passthrough)
	switch {
	case !st.Found:
		log.Printf("WARNING: the Claude Code CLI (%s) was not found, so brains cannot run. Set its path under Settings → Claude.", path)
	case st.Version == "":
		log.Printf("Claude Code CLI: %s (`--version` gave no answer)", st.Path)
	default:
		log.Printf("Claude Code CLI: %s, %s", st.Path, st.Version)
	}
	if w := claudeVersionWarning(st); w != "" {
		log.Printf("WARNING: %s", w)
	}
}

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
	binary string   // the CLI, or the sandbox that wraps it
	args   []string // claudeArgList's, wrapped likewise
	dir    string
	env    []string
	prompt string    // goes on stdin, for the -p the arguments end in
	stdout io.Writer // gets the stream-json output
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

// runWaitDelay is how long Wait waits, once the CLI has exited, for the
// copies of its input and output to end, a variable so tests need not
// wait for it.
var runWaitDelay = 5 * time.Second

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
	cmd.WaitDelay = runWaitDelay
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
