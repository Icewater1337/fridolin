package nodeproto

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"
)

// FrideCap is FrIDE, the coding IDE, running on the
// node in companion mode, paired with the node agent. The agent declares
// it only while FrIDE answers its companion API, so it comes and goes with
// `fride serve`; approving it once covers it every time it comes back.
type FrideCap struct {
	// URL is FrIDE's own address on the node, a loopback URL: what its UI
	// is opened at there.
	URL string `json:"url"`
	// Repos are the repositories FrIDE knows on the node.
	Repos []string `json:"repos,omitempty"`
	// NotLoggedIn says FrIDE's Claude was not logged in when a thread last
	// failed there: work goes to another FrIDE while one can take it.
	NotLoggedIn bool `json:"not_logged_in,omitempty"`
}

// FrideNotLoggedIn reports whether FrIDE's failure text says its Claude is
// not logged in ("Not logged in · Please run /login").
func FrideNotLoggedIn(text string) bool {
	return strings.Contains(strings.ToLower(text), "not logged in")
}

// FrideLoginFix is how the owner logs FrIDE's Claude in again.
const FrideLoginFix = "in the node's box run docker compose exec fride claude setup-token and set-claude-token, then docker compose restart fride (natively: log in to claude as FrIDE's user and restart fride serve)"

// Bounds of a fride declaration.
const (
	maxFrideRepos   = 256
	maxFrideRepoLen = 256
	maxURLLen       = 2048
)

// Check reports what is wrong with the declaration.
func (f *FrideCap) Check() error {
	if f == nil {
		return nil
	}
	if err := CheckLoopbackURL(f.URL); err != nil {
		return fmt.Errorf("fride: %w", err)
	}
	if len(f.Repos) > maxFrideRepos {
		return fmt.Errorf("fride: at most %d repositories", maxFrideRepos)
	}
	for _, r := range f.Repos {
		if r == "" || len(r) > maxFrideRepoLen || !utf8.ValidString(r) || strings.ContainsAny(r, "\x00\r\n") {
			return fmt.Errorf("fride: repository name %q", r)
		}
	}
	return nil
}

// CheckLoopbackURL reports whether u is an http or https URL of this
// machine's loopback interface, as FrIDE's API and its webhook must be.
func CheckLoopbackURL(u string) error {
	if u == "" || len(u) > maxURLLen {
		return errors.New("the URL is empty or too long")
	}
	p, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("the URL %q: %v", u, err)
	}
	if p.Scheme != "http" && p.Scheme != "https" {
		return fmt.Errorf("the URL %q must be http or https", u)
	}
	host := p.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("the URL %q must be on this machine (127.0.0.1, [::1] or localhost)", u)
		}
	}
	if p.User != nil {
		return fmt.Errorf("the URL %q must not hold a user name or password", u)
	}
	return nil
}

// The fride methods. The node passes each call to the companion API of
// the FrIDE its params' instance names (FrideOn), with that FrIDE's
// pairing token: "" is the node's own FrIDE (the fride capability),
// another name one of its FrIDE instances (fride_host).
const (
	// MethodFrideTask hands FrIDE a message (FrideTaskParams → FrideThread).
	MethodFrideTask = "fride.task"
	// MethodFrideStatus reads a thread, and its task when it has one
	// (FrideStatusParams → FrideStatus).
	MethodFrideStatus = "fride.status"
	// MethodFrideReply answers a thread (FrideReplyParams → FrideThread).
	MethodFrideReply = "fride.reply"
	// MethodFrideTaskInfo reads a task (FrideTaskRef → FrideTask).
	MethodFrideTaskInfo = "fride.task.info"
	// MethodFrideClose closes a task: rationale written, agents and stack
	// stopped, its slot freed (FrideTaskRef → FrideTask).
	MethodFrideClose = "fride.close"
	// MethodFrideMem is FrIDE's check whether a stack may start now
	// (FrideOn → FrideMem).
	MethodFrideMem = "fride.mem"
	// MethodFrideExport writes a task's bundle into a transfer on the node
	// (FrideExportParams → FrideTransfer); read it with fride.read.
	MethodFrideExport = "fride.export"
	// MethodFrideRead reads a chunk of a transfer (FrideChunkParams →
	// FrideChunk).
	MethodFrideRead = "fride.read"
	// MethodFrideWrite appends a chunk to a transfer, making it at offset 0
	// (FrideChunkParams → FrideTransfer).
	MethodFrideWrite = "fride.write"
	// MethodFrideImport imports a transfer's bundle as a task
	// (FrideImportParams → FrideTask).
	MethodFrideImport = "fride.import"
	// MethodFrideDrop removes a transfer (FrideChunkParams with the ID →
	// nothing).
	MethodFrideDrop = "fride.drop"
	// MethodFrideDelete removes a task's workspace (FrideDeleteParams →
	// nothing), with FrIDE's DELETE /api/tasks/<task>.
	MethodFrideDelete = "fride.delete"
	// MethodFrideRepoClone clones a repository into FrIDE's base folder on
	// the node and has FrIDE rescan it (FrideRepoParams with URL →
	// FrideRepo).
	MethodFrideRepoClone = "fride.repo_clone"
	// MethodFrideRepoNew makes a new repository in FrIDE's base folder on
	// the node, with an initial commit and, when asked, on GitHub with gh
	// (FrideRepoParams → FrideRepo).
	MethodFrideRepoNew = "fride.repo_new"
)

// FrideMethods lists the fride methods.
var FrideMethods = []string{
	MethodFrideTask, MethodFrideStatus, MethodFrideReply, MethodFrideTaskInfo, MethodFrideClose, MethodFrideMem,
	MethodFrideExport, MethodFrideRead, MethodFrideWrite, MethodFrideImport, MethodFrideDrop, MethodFrideDelete,
	MethodFrideRepoClone, MethodFrideRepoNew, MethodFrideIdentities,
}

// FrideChunkBytes is the most one fride.read returns and one fride.write
// takes: a bundle travels in chunks, each inside one message.
const FrideChunkBytes = 8 << 20

// FrideTaskParams hands FrIDE's inbox a message.
type FrideTaskParams struct {
	FrideOn
	Message string   `json:"message"`
	Repos   []string `json:"repos,omitempty"`
	// Ref is the sender's own id: the same ref again returns the thread it
	// started instead of starting the work twice.
	Ref    string `json:"ref,omitempty"`
	Source string `json:"source,omitempty"`
}

// FrideThread is an inbox thread, as FrIDE's companion API has it.
type FrideThread struct {
	ThreadID int    `json:"thread_id"`
	Status   string `json:"status"`
	TaskID   string `json:"task_id"`
	Question string `json:"question,omitempty"`
	Error    string `json:"error,omitempty"`
	Position int    `json:"position,omitempty"`
	Existing bool   `json:"existing,omitempty"`
	// Messages are the thread's trail.
	Messages []FrideMessage `json:"messages,omitempty"`
}

// FrideMessage is one line of a thread.
type FrideMessage struct {
	Role   string `json:"role"`
	Text   string `json:"text"`
	TaskID string `json:"task_id,omitempty"`
	TS     string `json:"ts,omitempty"`
}

// FrideStatusParams names a thread.
type FrideStatusParams struct {
	FrideOn
	ThreadID int `json:"thread_id"`
}

// FrideStatus is a thread with its task.
type FrideStatus struct {
	Thread FrideThread `json:"thread"`
	Task   *FrideTask  `json:"task,omitempty"`
}

// FrideReplyParams answers a thread.
type FrideReplyParams struct {
	FrideOn
	ThreadID int    `json:"thread_id"`
	Text     string `json:"text"`
}

// FrideTaskRef names a task.
type FrideTaskRef struct {
	FrideOn
	TaskID string `json:"task_id"`
}

// FrideDeleteParams removes a task's workspace. Force discards what is not
// committed: only once an archive holds it.
type FrideDeleteParams struct {
	FrideOn
	TaskID string `json:"task_id"`
	Force  bool   `json:"force,omitempty"`
}

// FrideTask is a task, as FrIDE's companion API has it.
type FrideTask struct {
	TaskID        string            `json:"task_id"`
	Exists        bool              `json:"exists"`
	Name          string            `json:"name,omitempty"`
	Status        string            `json:"status,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	Branches      map[string]string `json:"branches,omitempty"`
	AgentRunning  bool              `json:"agent_running,omitempty"`
	AgentWaiting  bool              `json:"agent_waiting,omitempty"`
	StackUp       bool              `json:"stack_up,omitempty"`
	EntryURL      string            `json:"entry_url,omitempty"`
	Workspace     string            `json:"workspace,omitempty"`
	RationalePath string            `json:"rationale_path,omitempty"`
	Rationale     string            `json:"rationale,omitempty"`
	SourceTaskID  string            `json:"source_task_id,omitempty"`
	Notes         []string          `json:"notes,omitempty"`
}

// FrideRepoParams adds a repository to FrIDE on the node: fride.repo_clone
// clones URL as Name, fride.repo_new makes Name with an initial commit and,
// with GitHub, creates it on GitHub with gh (private unless Public) and
// pushes it.
type FrideRepoParams struct {
	FrideOn
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	GitHub bool   `json:"github,omitempty"`
	Public bool   `json:"public,omitempty"`
}

// FrideRepo is a repository added to FrIDE on the node.
type FrideRepo struct {
	Name string `json:"name"`
	// Path is its folder on the node, in FrIDE's base folder.
	Path string `json:"path"`
	// GitHubURL is the repository gh created, for fride.repo_new.
	GitHubURL string `json:"github_url,omitempty"`
	// Warning says what did not work after the repository was made, such
	// as gh failing: the repository is there without it.
	Warning string `json:"warning,omitempty"`
	// Rescanned says FrIDE read its base folders again, and Repos are the
	// repositories it knows since.
	Rescanned bool     `json:"rescanned"`
	Repos     []string `json:"repos,omitempty"`
}

// FrideMem says whether the node's FrIDE may start a stack now.
type FrideMem struct {
	CanStart      bool   `json:"can_start"`
	Reason        string `json:"reason,omitempty"`
	Warn          string `json:"warn,omitempty"`
	RunningStacks int    `json:"running_stacks"`
	// HostFree is the free memory FrIDE sees, bytes.
	HostFree uint64 `json:"host_free,omitempty"`
}

// FrideExportParams exports a task; Stop stops its agents and stack first,
// so the conversation and the files are complete.
type FrideExportParams struct {
	FrideOn
	TaskID string `json:"task_id"`
	Stop   bool   `json:"stop,omitempty"`
}

// FrideTransfer is a bundle staged on the node.
type FrideTransfer struct {
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// FrideChunkParams reads from or writes to a transfer. A write at offset 0
// with no ID makes a new transfer.
type FrideChunkParams struct {
	FrideOn
	ID     string `json:"id,omitempty"`
	Offset int64  `json:"offset"`
	// Max bounds a read (0: FrideChunkBytes).
	Max  int    `json:"max,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// FrideChunk is a chunk read.
type FrideChunk struct {
	Data []byte `json:"data"`
	EOF  bool   `json:"eof"`
}

// FrideImportParams imports a transfer as a task: As names it when its id
// is taken here, StartAgent reopens its agent with its conversation.
type FrideImportParams struct {
	FrideOn
	ID         string `json:"id"`
	As         string `json:"as,omitempty"`
	StartAgent bool   `json:"start_agent,omitempty"`
}

// Fride webhook events (FrIDE's docs/COMPANION.md). An event this side
// does not know is taken as an update.
const (
	FrideStarted  = "started"
	FrideUpdate   = "update"
	FrideQuestion = "question"
	FrideDone     = "done"
	FrideFailed   = "failed"
)

// FrideEvent is thread news from FrIDE's webhook, verified by the node.
type FrideEvent struct {
	// Instance is the FrIDE instance the news is from, "" for the node's
	// own FrIDE.
	Instance      string `json:"instance,omitempty"`
	Event         string `json:"event"`
	ThreadID      int    `json:"thread_id"`
	TaskID        string `json:"task_id"`
	Text          string `json:"text"`
	Status        string `json:"status"`
	Branch        string `json:"branch,omitempty"`
	RationalePath string `json:"rationale_path,omitempty"`
	TS            string `json:"ts,omitempty"`
}

// Kind is the event as this side reads it: an unknown one is an update.
func (e FrideEvent) Kind() string {
	switch e.Event {
	case FrideStarted, FrideQuestion, FrideDone, FrideFailed:
		return e.Event
	}
	return FrideUpdate
}

// Event kinds a node reports (TypeEvent).
const (
	// EventFride is FrIDE thread news (a FrideEvent).
	EventFride = "fride"
)

// Event is news the node reports on its own, not as an answer.
type Event struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// Declare is what the node offers now, sent when it changes without a new
// connection: FrIDE came up or went away.
type Declare struct {
	Capabilities Capabilities `json:"capabilities"`
}
