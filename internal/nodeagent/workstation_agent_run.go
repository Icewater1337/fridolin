package nodeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// startTurn runs one turn of the session with text in the background.
func (s *agentSession) startTurn(text string) {
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.cancelTurn, s.turnDone = cancel, done
	s.turns++
	s.mu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		s.runTurn(ctx, text)
	}()
}

// turnArgs is the CLI's argv for a turn; the prompt goes in on stdin.
func (s *agentSession) turnArgs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider == nodeproto.ProviderCodex {
		args := []string{s.cli, "exec", "--json", "--skip-git-repo-check", "-C", s.folder}
		if s.model != "" {
			args = append(args, "-m", s.model)
		}
		if s.sandbox != nodeproto.SandboxNone {
			// Inside the node's sandbox, which Codex's own cannot nest in.
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		} else {
			args = append(args, "--sandbox", "workspace-write")
		}
		if s.resume != "" {
			args = append(args, "resume", s.resume)
		}
		return append(args, "-")
	}
	args := []string{s.cli, "-p", "--output-format", "stream-json", "--verbose"}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.resume != "" {
		args = append(args, "--resume", s.resume)
	}
	if s.sandbox != nodeproto.SandboxNone {
		// The sandbox keeps the session's writes in its folder.
		args = append(args, "--dangerously-skip-permissions")
	} else {
		// No sandbox (Windows): Claude Code's own permission checks, edits
		// in the folder allowed, and what they refuse goes to the owner.
		args = append(args, "--permission-mode", "acceptEdits")
		if len(s.allowed) > 0 {
			args = append(args, "--allowedTools", strings.Join(s.allowed, ","))
		}
	}
	return args
}

// turnResult is what a turn's output said.
type turnResult struct {
	resume   string
	text     string // the last text, or the result
	asked    string // an AskUserQuestion
	denied   []string
	errMsg   string
	finished bool // a result arrived
}

func (s *agentSession) runTurn(ctx context.Context, text string) {
	plan := newSandboxPlan(s.folder, s.tmp, s.a.Dir, s.provider)
	argv, err := plan.wrap(s.sandbox, s.turnArgs())
	if err != nil {
		s.endTurn(turnResult{errMsg: err.Error()})
		return
	}
	// Running the provider's CLI is this capability's purpose; its folder
	// was confined (confinedWorkFolder) and the sandbox, where there is
	// one, keeps its writes there.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = s.folder
	env := append(filteredEnv(), "TMPDIR="+s.tmp, "CLAUDE_CODE_TMPDIR="+s.tmp, "FRIDOLIN_NODE_AGENT=1")
	s.mu.Lock()
	if s.coreToken != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+s.coreToken)
	}
	s.mu.Unlock()
	cmd.Env = env
	cmd.Stdin = strings.NewReader(text)
	stderr := &cappedBuffer{max: 16 << 10}
	cmd.Stderr = stderr
	platform.IsolateProcessGroup(cmd)
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		s.endTurn(turnResult{errMsg: fmt.Sprintf("starting %s: %v", s.provider, err)})
		return
	}
	res := s.parse(out)
	werr := cmd.Wait()
	if res.errMsg == "" && werr != nil && !res.finished {
		msg := strings.TrimSpace(stderr.buf.String())
		if r := []rune(msg); len(r) > 500 {
			msg = string(r[len(r)-500:])
		}
		res.errMsg = fmt.Sprintf("%s ended: %v %s", s.provider, werr, msg)
	}
	if ctx.Err() != nil && s.endedAt().IsZero() && res.errMsg == "" && !res.finished {
		res.errMsg = "the turn was cut off"
	}
	s.endTurn(res)
}

// streamLine is the part of a stream event of either CLI that is read.
type streamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	ThreadID  string `json:"thread_id"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	Message   *struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	PermissionDenials []struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	} `json:"permission_denials"`
	Item *struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
	} `json:"item"`
	Error json.RawMessage `json:"error"`
}

// parse reads a turn's stream: Claude Code's stream-json or Codex's JSONL.
func (s *agentSession) parse(r interface{ Read([]byte) (int, error) }) turnResult {
	var res turnResult
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev streamLine
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		s.event(ev, &res)
	}
	return res
}

func (s *agentSession) event(ev streamLine, res *turnResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.SessionID != "" {
		res.resume = ev.SessionID
	}
	switch ev.Type {
	case "assistant":
		if ev.Message == nil {
			return
		}
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if strings.TrimSpace(c.Text) != "" {
					res.text = c.Text
					s.addEventLocked("text", c.Text)
				}
			case "tool_use":
				s.addEventLocked("tool", toolSummary(c.Name, c.Input))
				if c.Name == "AskUserQuestion" {
					res.asked = askText(c.Input)
				}
			}
		}
	case "result":
		res.finished = true
		if ev.IsError {
			res.errMsg = orDefault(ev.Result, "the session ended with an error")
			return
		}
		if strings.TrimSpace(ev.Result) != "" {
			res.text = ev.Result
		}
		for _, d := range ev.PermissionDenials {
			res.denied = append(res.denied, toolRule(d.ToolName, d.ToolInput))
		}
	// Codex.
	case "thread.started":
		res.resume = ev.ThreadID
	case "item.started", "item.completed":
		if ev.Item == nil {
			return
		}
		switch {
		case ev.Item.Type == "agent_message" && ev.Type == "item.completed":
			res.text = ev.Item.Text
			s.addEventLocked("text", ev.Item.Text)
		case ev.Item.Type == "command_execution" && ev.Type == "item.started":
			s.addEventLocked("tool", "Bash: "+ev.Item.Command)
		case ev.Item.Type == "file_change" && ev.Type == "item.completed":
			s.addEventLocked("tool", "Edit")
		}
	case "turn.completed":
		res.finished = true
	case "turn.failed", "error":
		res.finished = true
		res.errMsg = codexError(ev)
	}
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func codexError(ev streamLine) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(ev.Error, &e) == nil && e.Message != "" {
		return e.Message
	}
	var str string
	if json.Unmarshal(ev.Error, &str) == nil && str != "" {
		return str
	}
	return "codex failed"
}

// toolSummary is a tool use in a line: its name and what it works on.
func toolSummary(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "description"} {
		if v, ok := in[k].(string); ok && v != "" {
			if r := []rune(v); len(r) > 200 {
				v = string(r[:200]) + "…"
			}
			return name + ": " + v
		}
	}
	return name
}

// toolRule is Claude Code's --allowedTools rule for a refused tool use:
// Bash with its command, else the tool.
func toolRule(name string, input json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	if name == "Bash" && json.Unmarshal(input, &in) == nil && in.Command != "" && !strings.ContainsAny(in.Command, "()\n") {
		return "Bash(" + in.Command + ")"
	}
	return name
}

// askText is an AskUserQuestion's questions, in a few lines.
func askText(input json.RawMessage) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var parts []string
	for _, q := range in.Questions {
		t := strings.TrimSpace(q.Question)
		for i, o := range q.Options {
			t += fmt.Sprintf("\n%d. %s", i+1, o.Label)
		}
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// irreversibleRe are commands the node takes for steps that cannot be
// undone when Claude Code's permission checks refused them.
var irreversibleRe = regexp.MustCompile(`(?i)\b(git\s+push|git\s+reset\s+--hard|rm\s+-[a-z]*r|deploy|publish|npm\s+publish|terraform\s+apply|kubectl\s+(apply|delete)|gh\s+(release|pr\s+merge|repo\s+delete))\b`)

// lastMarker finds a last line "QUESTION: …" or "IRREVERSIBLE: …".
func lastMarker(text string) (ask, what string) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		l := strings.TrimSpace(strings.Trim(lines[i], "*_`> "))
		if q, ok := cutPrefixFold(l, "QUESTION:"); ok && q != "" {
			return nodeproto.AskQuestion, q
		}
		if q, ok := cutPrefixFold(l, "IRREVERSIBLE:"); ok && q != "" {
			return nodeproto.AskIrreversible, q
		}
	}
	return "", ""
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return strings.TrimSpace(s[len(prefix):]), true
	}
	return "", false
}

// endTurn decides what the turn's end means: a question, a permission or
// an irreversible step to ask about, or the session's end.
func (s *agentSession) endTurn(res turnResult) {
	s.mu.Lock()
	if !s.ended.IsZero() {
		s.mu.Unlock()
		return
	}
	if res.resume != "" {
		s.resume = res.resume
	}
	ask, what := lastMarker(res.text)
	switch {
	case res.errMsg != "":
		s.state, s.errMsg = nodeproto.AgentFailed, res.errMsg
		s.addEventLocked("failed", res.errMsg)
	case len(res.denied) > 0:
		s.state, s.ask, s.pendingAllow = nodeproto.AgentWaiting, nodeproto.AskPermission, slices.Compact(res.denied)
		s.question = "The session wants to use " + strings.Join(s.pendingAllow, ", ") + ", which its permission rules refused. Allow it?"
		if irreversibleRe.MatchString(strings.Join(s.pendingAllow, " ")) {
			s.ask = nodeproto.AskIrreversible
		}
		s.addEventLocked("question", s.question)
	case res.asked != "":
		s.state, s.ask, s.question = nodeproto.AgentWaiting, nodeproto.AskQuestion, res.asked
		s.addEventLocked("question", res.asked)
	case ask != "":
		s.state, s.ask, s.question = nodeproto.AgentWaiting, ask, what
		s.addEventLocked("question", what)
	default:
		s.state, s.summary = nodeproto.AgentDone, strings.TrimSpace(res.text)
		s.addEventLocked("done", s.summary)
	}
	ended := s.state != nodeproto.AgentWaiting
	if ended {
		s.ended = time.Now()
	}
	s.mu.Unlock()
	if ended {
		s.finish()
	}
	s.a.logf("node: agent session %s: %s", s.id, s.snapshot(0).State)
	s.a.pushNews(s.snapshot(0))
}

// isGitRepo reports whether the folder is in a git work tree.
func (s *agentSession) isGitRepo() bool {
	out, err := s.git1("rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// git1 runs git in the folder, inside the session's sandbox: the folder's
// git config is the session's to write, and a filter or hook it names
// must not run outside.
func (s *agentSession) git1(args ...string) (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	full := append([]string{git, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + nullDevice(), "--no-pager"}, args...)
	argv, err := newSandboxPlan(s.folder, s.tmp, s.a.Dir, "").wrap(s.sandbox, full)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = s.folder
	cmd.Env = append(filteredEnv(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out := &cappedBuffer{max: 64 << 10}
	cmd.Stdout = out
	err = cmd.Run()
	return out.buf.String(), err
}

func nullDevice() string {
	if filepath.Separator == '\\' {
		return "NUL"
	}
	return "/dev/null"
}

// changes are the files the session changed: git's status in a
// repository, else the files new or modified since it started.
func (s *agentSession) changes() ([]string, string) {
	var out []string
	if s.git {
		status, err := s.git1("status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return nil, ""
		}
		for _, l := range strings.Split(status, "\n") {
			if len(l) > 3 {
				out = append(out, strings.TrimSpace(l[:2])+" "+l[3:])
			}
		}
		stat, _ := s.git1("diff", "--no-ext-diff", "--no-textconv", "--stat", "HEAD")
		if r := []rune(stat); len(r) > 4000 {
			stat = string(r[:4000]) + "…"
		}
		return capList(out), strings.TrimSpace(stat)
	}
	now := snapshotTree(s.folder)
	for p, t := range now {
		if old, ok := s.baseline[p]; !ok {
			out = append(out, "A "+p)
		} else if !t.Equal(old) {
			out = append(out, "M "+p)
		}
	}
	for p := range s.baseline {
		if _, ok := now[p]; !ok {
			out = append(out, "D "+p)
		}
	}
	slices.Sort(out)
	return capList(out), ""
}

func capList(l []string) []string {
	if len(l) > 200 {
		return append(l[:200], fmt.Sprintf("… and %d more", len(l)-200))
	}
	return l
}

// snapshotTree is the folder's files with their modification times, at
// most 20000, without .git and node_modules.
func snapshotTree(dir string) map[string]time.Time {
	out := map[string]time.Time{}
	errStop := errors.New("enough")
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if len(out) >= 20000 {
			return errStop
		}
		if info, err := d.Info(); err == nil {
			if rel, err := filepath.Rel(dir, p); err == nil {
				out[filepath.ToSlash(rel)] = info.ModTime()
			}
		}
		return nil
	})
	return out
}
