package nodeproto

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// WorkstationCap is one of the two capabilities of a workstation node
// (docs/NODES.md, "Workstation"): agent, which runs a Claude Code or Codex
// session in a granted folder, and terminal, an interactive shell started
// in one. Each is scoped to its own folders (Roots); a folder of a call
// must lie under one of them, as written and as its symlinks resolve. A
// node never offers its home folder, or a folder above it, unless its
// owner says so (AllowHome, which grants the home folder itself and never
// one above it).
type WorkstationCap struct {
	Roots []string `json:"roots"`
	// AllowHome lets a root be the node's home folder itself.
	AllowHome bool `json:"allow_home,omitempty"`
	// Providers are the agent CLIs the node found ("claude", "codex"),
	// and Sandbox how the node confines a session ("sandbox-exec",
	// "bwrap", or "none"): what the node reports, not what it is approved
	// for.
	Providers []string `json:"providers,omitempty"`
	Sandbox   string   `json:"sandbox,omitempty"`
}

// Agent providers and sandboxes a node reports.
const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"

	SandboxExec  = "sandbox-exec"
	SandboxBwrap = "bwrap"
	SandboxNone  = "none"
)

// Bounds of the workstation family.
const (
	maxWorkRoots = 32
	// MaxAgentPrompt bounds a prompt or a reply, bytes.
	MaxAgentPrompt = 64 << 10
	// MaxTerminalInput bounds one terminal.send, bytes.
	MaxTerminalInput = 64 << 10
	// MaxTerminalRead bounds what one terminal.read returns, bytes.
	MaxTerminalRead = 256 << 10
	// MaxTerminalWaitMS bounds how long a terminal.read waits for output.
	MaxTerminalWaitMS = 20000
	// MaxAgentEvents bounds the events one agent.status returns.
	MaxAgentEvents = 200
)

// Check reports what is wrong with the declaration; name is "agent" or
// "terminal".
func (w *WorkstationCap) Check(name string) error {
	if w == nil {
		return nil
	}
	if len(w.Roots) == 0 || len(w.Roots) > maxWorkRoots {
		return fmt.Errorf("%s: 1 to %d roots", name, maxWorkRoots)
	}
	for _, r := range w.Roots {
		if err := CheckAbsPath(r); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for _, p := range w.Providers {
		if p != ProviderClaude && p != ProviderCodex {
			return fmt.Errorf("%s: no provider %q", name, p)
		}
	}
	switch w.Sandbox {
	case "", SandboxExec, SandboxBwrap, SandboxNone:
	default:
		return fmt.Errorf("%s: no sandbox %q", name, w.Sandbox)
	}
	return nil
}

// coversWork reports whether approved covers declared: approved at all,
// every declared root approved, the home folder only if approved.
func coversWork(approved, declared *WorkstationCap) bool {
	if declared == nil {
		return true
	}
	if approved == nil || declared.AllowHome && !approved.AllowHome {
		return false
	}
	for _, r := range declared.Roots {
		if !slices.ContainsFunc(approved.Roots, func(a string) bool { return SamePath(a, r) }) {
			return false
		}
	}
	return true
}

// intersectWork is the part of declared that approved covers.
func intersectWork(declared, approved *WorkstationCap) *WorkstationCap {
	if declared == nil || approved == nil {
		return nil
	}
	out := &WorkstationCap{AllowHome: declared.AllowHome && approved.AllowHome, Providers: slices.Clone(declared.Providers), Sandbox: declared.Sandbox}
	for _, r := range declared.Roots {
		if slices.ContainsFunc(approved.Roots, func(a string) bool { return SamePath(a, r) }) {
			out.Roots = append(out.Roots, r)
		}
	}
	if len(out.Roots) == 0 {
		return nil
	}
	return out
}

// AllowsFolder reports whether folder lies under one of w's roots, as the
// core can tell from the path alone; the node checks it again, symlinks
// resolved.
func (w *WorkstationCap) AllowsFolder(folder string) bool {
	return w != nil && CheckAbsPath(folder) == nil && slices.ContainsFunc(w.Roots, func(r string) bool { return PathWithin(r, folder) })
}

func (w *WorkstationCap) summary(name string) string {
	if len(w.Roots) == 1 {
		return name + " (1 folder)"
	}
	return fmt.Sprintf("%s (%d folders)", name, len(w.Roots))
}

// The methods of the agent family.
const (
	// MethodAgentStart starts a session (AgentStartParams → AgentSession).
	MethodAgentStart = "agent.start"
	// MethodAgentStatus reads a session with its events after a sequence
	// number (AgentRef → AgentSession).
	MethodAgentStatus = "agent.status"
	// MethodAgentReply answers a session that waits (AgentReplyParams →
	// AgentSession).
	MethodAgentReply = "agent.reply"
	// MethodAgentStop stops a session (AgentRef → AgentSession).
	MethodAgentStop = "agent.stop"
	// MethodAgentList lists the sessions, without events (nothing →
	// AgentList).
	MethodAgentList = "agent.list"
)

// AgentMethods are the methods the agent capability grants.
var AgentMethods = []string{MethodAgentStart, MethodAgentStatus, MethodAgentReply, MethodAgentStop, MethodAgentList}

// The methods of the terminal family.
const (
	// MethodTerminalOpen opens a shell in a folder (TerminalOpenParams →
	// TerminalInfo).
	MethodTerminalOpen = "terminal.open"
	// MethodTerminalSend types into it (TerminalSendParams → TerminalInfo).
	MethodTerminalSend = "terminal.send"
	// MethodTerminalRead reads its output from an offset, waiting up to
	// WaitMS for some (TerminalReadParams → TerminalOutput).
	MethodTerminalRead = "terminal.read"
	// MethodTerminalResize sets its size (TerminalResizeParams →
	// TerminalInfo).
	MethodTerminalResize = "terminal.resize"
	// MethodTerminalClose ends it (TerminalRef → TerminalInfo).
	MethodTerminalClose = "terminal.close"
	// MethodTerminalList lists the terminals (nothing → TerminalList).
	MethodTerminalList = "terminal.list"
)

// TerminalMethods are the methods the terminal capability grants.
var TerminalMethods = []string{MethodTerminalOpen, MethodTerminalSend, MethodTerminalRead, MethodTerminalResize, MethodTerminalClose, MethodTerminalList}

// WorkstationStarts are the methods that start something or act inside a
// session: a lockdown refuses them, and stops what runs.
var WorkstationStarts = []string{MethodAgentStart, MethodAgentReply, MethodTerminalOpen, MethodTerminalSend}

// AgentStartParams starts a session in Folder with Prompt.
type AgentStartParams struct {
	Folder string `json:"folder"`
	Prompt string `json:"prompt"`
	// Provider is "claude" or "codex"; "" is the node's first.
	Provider string `json:"provider,omitempty"`
	// Model is the provider's model; "" is the node's own default.
	Model string `json:"model,omitempty"`
	// CoreToken is the core's Claude login token, sent only when the core
	// brain asked for it for this session (use_core_login); "" uses the
	// node's own login. The node never stores or logs it.
	CoreToken string `json:"core_token,omitempty"`
}

// Check reports what is wrong with p.
func (p AgentStartParams) Check() error {
	switch {
	case CheckAbsPath(p.Folder) != nil:
		return fmt.Errorf("folder %q must be an absolute path", p.Folder)
	case strings.TrimSpace(p.Prompt) == "":
		return errors.New("the prompt is empty")
	case len(p.Prompt) > MaxAgentPrompt || !utf8.ValidString(p.Prompt):
		return fmt.Errorf("the prompt must be UTF-8 and at most %d bytes", MaxAgentPrompt)
	case p.Provider != "" && p.Provider != ProviderClaude && p.Provider != ProviderCodex:
		return fmt.Errorf("no provider %q", p.Provider)
	case len(p.Model) > 128 || strings.ContainsAny(p.Model, " \t\r\n\x00") || strings.HasPrefix(p.Model, "-"):
		return fmt.Errorf("model %q", p.Model)
	case len(p.CoreToken) > 4096 || strings.ContainsAny(p.CoreToken, " \t\r\n\x00"):
		return errors.New("the core token is malformed")
	}
	return nil
}

// AgentRef names a session; After asks for the events after that
// sequence number.
type AgentRef struct {
	ID    string `json:"id"`
	After int    `json:"after,omitempty"`
}

// AgentReplyParams answers a session that waits: Text for its question,
// and for a permission or an irreversible step, Allow says yes.
type AgentReplyParams struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Allow bool   `json:"allow,omitempty"`
}

// Agent session states.
const (
	AgentRunning = "running"
	AgentWaiting = "waiting"
	AgentDone    = "done"
	AgentFailed  = "failed"
	AgentStopped = "stopped"
)

// What a waiting session asks.
const (
	// AskQuestion is a question for the owner.
	AskQuestion = "question"
	// AskPermission is a tool the session wants and the node's permission
	// rules refused: allow it, or not.
	AskPermission = "permission"
	// AskIrreversible is a step the session says cannot be undone (a push,
	// a deploy, a delete outside the folder): an approval poll.
	AskIrreversible = "irreversible"
)

// AgentEvent is one thing a session did.
type AgentEvent struct {
	Seq  int       `json:"seq"`
	Kind string    `json:"kind"` // text, tool, question, done, failed, stopped
	Text string    `json:"text"`
	TS   time.Time `json:"ts"`
}

// AgentSession is a session as the node has it.
type AgentSession struct {
	ID       string `json:"id"`
	Folder   string `json:"folder"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	State    string `json:"state"`
	// Sandbox is how the session is confined: sandbox-exec, bwrap, or
	// none (then the provider's own permission checks hold it).
	Sandbox string `json:"sandbox"`
	// CoreLogin says the session runs with the core's login token.
	CoreLogin bool `json:"core_login,omitempty"`
	// Ask and Question are what a waiting session asks.
	Ask      string `json:"ask,omitempty"`
	Question string `json:"question,omitempty"`
	// Summary is the session's last word once it is done.
	Summary string `json:"summary,omitempty"`
	// Changed are the files changed in the folder (git status in a
	// repository, else the files modified since the session started),
	// DiffStat git's diff --stat.
	Changed  []string   `json:"changed,omitempty"`
	DiffStat string     `json:"diff_stat,omitempty"`
	Error    string     `json:"error,omitempty"`
	Turns    int        `json:"turns"`
	Started  time.Time  `json:"started"`
	Ended    *time.Time `json:"ended,omitempty"`
	// Events are the events after the AgentRef's After; NextSeq is the
	// sequence number the next one gets.
	Events  []AgentEvent `json:"events,omitempty"`
	NextSeq int          `json:"next_seq"`
}

// Finished reports whether the session ended.
func (s AgentSession) Finished() bool {
	return s.State == AgentDone || s.State == AgentFailed || s.State == AgentStopped
}

// AgentList lists sessions.
type AgentList struct {
	Sessions []AgentSession `json:"sessions"`
}

// EventAgent is a session's news (an AgentSession without events, with
// the news in Ask or State): a question, or its end.
const EventAgent = "agent"

// TerminalOpenParams opens a shell in Folder, Cols × Rows (80 × 24 for 0).
type TerminalOpenParams struct {
	Folder string `json:"folder"`
	Cols   int    `json:"cols,omitempty"`
	Rows   int    `json:"rows,omitempty"`
}

// TerminalRef names a terminal.
type TerminalRef struct {
	ID string `json:"id"`
}

// TerminalSendParams types Data.
type TerminalSendParams struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

// TerminalResizeParams sets a terminal's size.
type TerminalResizeParams struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// TerminalReadParams reads output from Offset, waiting up to WaitMS for
// some when there is none yet.
type TerminalReadParams struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

// TerminalInfo is a terminal.
type TerminalInfo struct {
	ID       string    `json:"id"`
	Folder   string    `json:"folder"`
	Shell    string    `json:"shell"`
	Sandbox  string    `json:"sandbox"`
	Started  time.Time `json:"started"`
	Closed   bool      `json:"closed,omitempty"`
	ExitCode *int      `json:"exit_code,omitempty"`
	// Output is how many bytes it wrote so far.
	Output int64 `json:"output"`
}

// TerminalOutput is output from Offset to Next. Dropped says output
// before Offset was asked for that the node no longer keeps.
type TerminalOutput struct {
	ID       string `json:"id"`
	Data     []byte `json:"data"`
	Offset   int64  `json:"offset"`
	Next     int64  `json:"next"`
	Dropped  bool   `json:"dropped,omitempty"`
	Closed   bool   `json:"closed,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

// TerminalList lists terminals.
type TerminalList struct {
	Terminals []TerminalInfo `json:"terminals"`
}

// CheckSize reports whether cols × rows is a terminal size (0 for the
// default).
func CheckSize(cols, rows int) error {
	if cols < 0 || rows < 0 || cols > 1000 || rows > 1000 {
		return fmt.Errorf("terminal size %d×%d", cols, rows)
	}
	return nil
}
