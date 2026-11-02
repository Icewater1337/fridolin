// Package audit is the bridge's append-only audit log: one JSON object a
// line in <data_dir>/audit/YYYY-MM.jsonl, a file a month. Every line has
// one schema (Entry), whatever wrote it: runs, the tools they called,
// approvals and votes, delegations, lockdowns. A line names its thread
// (the chat a run serves, which a task delegated from it shares), so the
// log reads per thread. No secret value is ever written: tool arguments
// stay out, and texts are shortened and scrubbed of what looks like a
// token (Scrub).
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The kinds of lines.
const (
	RunStart          = "run_start"
	RunEnd            = "run_end"
	RunRefused        = "run_refused"
	ToolCall          = "tool_call"
	ApprovalRequested = "approval_requested"
	ApprovalVote      = "approval_vote"
	ApprovalDecided   = "approval_decided"
	QuestionAsked     = "question_asked"
	QuestionAnswered  = "question_answered"
	Delegation        = "delegation"
	Lockdown          = "lockdown"
	Unlock            = "unlock"
	SkillApproved     = "skill_approved"
	// Tailnet is a request that changed something through the core's
	// tailnet listener (M1): By is the caller's tailnet login, ID its
	// device, Detail the method and path, Outcome the HTTP status.
	Tailnet = "tailnet"
	// Housekeeping is the housekeeping job: a run (Detail its summary) and
	// each thing it stopped, removed or asked about (ID the approval).
	Housekeeping = "housekeeping"
	// The owner's chat commands: a join code (!join), a dashboard sign-in
	// link (!dashboard), a node approved (!approve) or revoked (!revoke).
	// By is the sender, Chat where they asked, ID the node; Outcome is ok,
	// or why it was refused. The code or link itself is never written.
	NodeJoinCode  = "node_join_code"
	DashboardLink = "dashboard_link"
	NodeApproved  = "node_approved"
	NodeRevoked   = "node_revoked"
	// OwnerApproval is the owner's !approve or !deny of something that is
	// not a node, a vote in the poll that picks one, and the confirmation
	// an unexpected one asks for (owner_approvals.go).
	OwnerApproval = "owner_approval"
	// The core's move from the owner's chat (#187): !movecore asked
	// (ID the target, Outcome whether a poll went out), the owner's
	// "Move now" (ID the approval, Detail source and target), how a move
	// ended (ID the move, Outcome its state), and what the machine a move
	// left did with the host secrets (ID the node, Outcome kept or
	// forgotten).
	CoreMoveRequested = "core_move_requested"
	CoreMoveApproved  = "core_move_approved"
	CoreMoveEnded     = "core_move_ended"
	CoreSecrets       = "core_secrets"
	// NodeSession is a workstation session on a node (#159) starting or
	// ending: Detail says which (agent or terminal, the node, the folder),
	// Outcome how it ended.
	NodeSession = "node_session"
	// NodeCommand is a command line typed into a node's terminal, its
	// secrets masked (Scrub).
	NodeCommand = "node_command"
	// NodeExec is a command the core brain ran on a node with node_exec,
	// or asked the owner to approve: Brain and Run the run's, Detail the
	// node and the command line, its secrets masked (Scrub), and for a
	// command whose rule asks the owner, ID the approval and By who
	// approved it; Outcome the exit code ("exit 0"), "timed out",
	// "pending", "denied", or why it failed or was refused.
	NodeExec = "node_exec"
	// Errand is a step of an errand (docs/BRAINS.md, "Errands"), in
	// which Fridolin writes to someone who is not in allowed_numbers:
	// Brain the brain that asked for it, Run its run (or "errand:<id>"),
	// ID the errand, Chat the person's number masked, Detail the step
	// (asked, opened, sent, answered, ended, blocked, unblocked) and
	// Outcome how it went. The messages themselves stay in bridge.db's
	// errand_lines, never in the log.
	Errand = "errand"
	// FileView is a file, folder or wiki page read on the Files page, the
	// wiki or the site's /files and /wiki (docs/FILES.md): By is
	// "dashboard" or "site:<account>", Brain the brain whose folder it is,
	// Detail what was done (view, open, download, list, search, wiki,
	// wiki-search), the root and the path, Outcome ok or why it was
	// refused.
	FileView = "file_view"
	// FileChange is a change on the Files page, the wiki or the site's
	// /files and /wiki: By and Brain as for FileView, Detail the action
	// (edit, create, upload, move, delete, upload-rule), the root and the
	// path ("<path> -> <to>" for a move, and for a delete where it went in
	// the trash), Outcome ok (with "commit <hash>" where it was committed)
	// or why it was refused.
	FileChange = "file_change"
	// ScheduledCommand is a scheduled agent's program run on a node:
	// Brain and ID the agent's brain and run, Detail the agent, node and
	// program (not its arguments), Outcome how it ended.
	ScheduledCommand = "scheduled_command"
	// ConnectionCall is a call of Fridolin's connections to OpenAI and
	// ElevenLabs (docs/CONNECTIONS.md): By is "module:<id>" or
	// "brain:<id>" (Brain and Run then the run's), Detail the connection,
	// the operation and the model ("openai transcribe
	// gpt-4o-mini-transcribe"), Outcome ok or why it failed or was
	// refused. Never what was sent or answered.
	ConnectionCall = "connection_call"
	// ConnectionKey is a connection's key set, changed or removed on the
	// dashboard: By is "dashboard", Detail the connection and what
	// changed. Never the key.
	ConnectionKey = "connection_key"
	// FrideInstance is a FrIDE instance started or stopped on a node
	// (docs/NODES.md, "The core and FrIDE instances"): By is "core brain",
	// "dashboard" or "idle", ID the instance, Detail the node, the memory
	// and why, Outcome ok or why it failed. Never a token.
	FrideInstance = "fride_instance"
)

// Entry is one line. Fields are added, never renamed; a reader ignores
// the ones it does not know.
type Entry struct {
	TS   string `json:"ts"`
	Kind string `json:"kind"`
	// Thread is the chat a run serves; a delegated task's run is in the
	// thread of the core run that delegated it.
	Thread string `json:"thread,omitempty"`
	// Chat is where it happened: the run's own chat, or where a poll went.
	Chat  string `json:"chat,omitempty"`
	Brain string `json:"brain,omitempty"`
	Run   string `json:"run,omitempty"`
	// ID is the approval's, question's or delegation's id.
	ID string `json:"id,omitempty"`
	// Skill and Risk are a run's skill and risk budget, or an approval's.
	Skill string `json:"skill,omitempty"`
	Risk  string `json:"risk,omitempty"`
	// By is who acted: a voter's number, "dashboard", a brain.
	By         string `json:"by,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// DirName is the data dir's folder of audit logs.
const DirName = "audit"

// maxDetail bounds a line's detail.
const maxDetail = 300

// tokenRe matches what looks like a credential: a long run of letters,
// digits and -_/+= without spaces, or a key=value of a secret-sounding
// key.
var (
	tokenRe  = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{32,}`)
	secretRe = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|pin)\s*[:=]\s*\S+`)
	// prefixRe matches credentials by their known prefixes, shorter than
	// tokenRe's run (OpenAI's sk-…, ElevenLabs' sk_…), and secret flags
	// and headers on a command line.
	prefixRe = regexp.MustCompile(`(?i)\b(sk[-_][a-z0-9_-]{8,}|gh[pousr]_[a-z0-9]{8,}|github_pat_[a-z0-9_]{8,}|xox[abprs]-[a-z0-9-]{8,}|AKIA[0-9A-Z]{12,}|glpat-[a-z0-9_-]{8,})|(--?(password|passwd|token|secret|api-key|apikey|auth)[= ]\S+)|((bearer|basic)\s+\S+)`)
)

// Scrub shortens s for a line and masks what looks like a secret in it.
func Scrub(s string) string {
	s = secretRe.ReplaceAllString(s, "$1=[redacted]")
	s = prefixRe.ReplaceAllString(s, "[redacted]")
	s = tokenRe.ReplaceAllString(s, "[redacted]")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxDetail {
		n := maxDetail
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n] + "…"
	}
	return s
}

// Log appends to the files in Dir.
type Log struct {
	mu  sync.Mutex
	Dir string
	Now func() time.Time
}

func (l *Log) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Append writes e with its time, its texts scrubbed. A log that cannot be
// written is logged, not fatal: the work it records has happened.
func (l *Log) Append(e Entry) {
	if l == nil || l.Dir == "" {
		return
	}
	t := l.now()
	e.TS = t.UTC().Format(time.RFC3339Nano)
	e.Detail = Scrub(e.Detail)
	e.Outcome = Scrub(e.Outcome)
	line, err := json.Marshal(e)
	if err != nil {
		log.Printf("audit: %v", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		log.Printf("audit: %v", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, t.UTC().Format("2006-01")+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("audit: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("audit: %v", err)
	}
}

// Filter picks lines; an empty field picks every value.
type Filter struct {
	Thread, Kind, Brain, Run, ID string
	// Kinds, when set, picks lines of these kinds only.
	Kinds []string
	// Query is a case-insensitive substring of the detail, outcome or by.
	Query string
	// Months is how many monthly files to read, newest first (default 2).
	Months int
	// Limit caps the lines returned (default 500).
	Limit int
}

func (f Filter) keep(e Entry) bool {
	switch {
	case f.Thread != "" && e.Thread != f.Thread,
		f.Kind != "" && e.Kind != f.Kind,
		len(f.Kinds) > 0 && !slices.Contains(f.Kinds, e.Kind),
		f.Brain != "" && e.Brain != f.Brain,
		f.Run != "" && e.Run != f.Run,
		f.ID != "" && e.ID != f.ID:
		return false
	}
	if q := strings.ToLower(f.Query); q != "" {
		return strings.Contains(strings.ToLower(e.Detail+" "+e.Outcome+" "+e.By+" "+e.Chat), q)
	}
	return true
}

// maxLine is the longest line Read takes; longer ones are skipped.
const maxLine = 64 << 10

// Read returns the lines f picks, newest first, from the newest monthly
// files in dir. A missing folder is no lines.
func Read(dir string, f Filter) ([]Entry, error) {
	months := f.Months
	if months <= 0 {
		months = 2
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	names, err := files(dir)
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for i := len(names) - 1; i >= 0 && len(names)-i <= months; i-- {
		got, err := readFile(filepath.Join(dir, names[i]), f)
		if err != nil {
			return nil, err
		}
		// Newest first: the file's lines are oldest first.
		for j := len(got) - 1; j >= 0; j-- {
			out = append(out, got[j])
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// files lists dir's monthly files, oldest first.
func files(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(n, ".jsonl") {
			if _, err := time.Parse("2006-01", strings.TrimSuffix(n, ".jsonl")); err == nil {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names)
	return names, nil
}

func readFile(path string, f Filter) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	defer file.Close()
	var out []Entry
	r := bufio.NewReaderSize(file, 64<<10)
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// Too long for a line the bridge writes: skip the rest of it.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			continue
		}
		if len(line) > 0 && len(line) <= maxLine {
			var e Entry
			if json.Unmarshal(line, &e) == nil && e.Kind != "" && f.keep(e) {
				out = append(out, e)
			}
		}
		if err != nil {
			break
		}
	}
	return out, nil
}
