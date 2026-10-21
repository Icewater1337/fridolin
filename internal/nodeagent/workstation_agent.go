package nodeagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// agentSession is a Claude Code or Codex session in a granted folder.
// Each turn is one run of the CLI (`claude -p`, `codex exec`), the next
// turn resumes the provider's own session with the owner's answer. A turn
// that ends with a question (AskUserQuestion, or a last line "QUESTION:"),
// with a step it says cannot be undone ("IRREVERSIBLE:"), or, without a
// sandbox, with tools Claude Code's permission rules refused, leaves the
// session waiting for agent.reply; any other ends it, and the node lists
// what changed in the folder.
type agentSession struct {
	a                               *Agent
	id, folder, requested, provider string
	model, sandbox, cli             string
	coreToken                       string
	coreLogin                       bool
	started                         time.Time
	tmp                             string
	git                             bool
	baseline                        map[string]time.Time
	ctx                             context.Context
	cancelAll                       context.CancelFunc

	mu           sync.Mutex
	state        string
	ask          string
	question     string
	summary      string
	errMsg       string
	changed      []string
	diffStat     string
	events       []nodeproto.AgentEvent
	nextSeq      int
	turns        int
	ended        time.Time
	resume       string   // the provider's own session or thread
	allowed      []string // tools the owner allowed (no sandbox)
	pendingAllow []string // tools the last turn was refused
	cancelTurn   context.CancelFunc
	turnDone     chan struct{}
}

// maxEvents bounds the events a session keeps.
const maxEvents = 1000

func (a *Agent) agentMaxRuntime() time.Duration {
	if a.AgentMaxRuntime > 0 {
		return a.AgentMaxRuntime
	}
	return DefaultAgentMaxRuntime
}

func (a *Agent) agentStart(eff nodeproto.Capabilities, p nodeproto.AgentStartParams) (any, *nodeproto.Error) {
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	folder, perr := a.confinedWorkFolder(eff, "agent", p.Folder)
	if perr != nil {
		return nil, perr
	}
	sb, providers := a.probeWork()
	provider := p.Provider
	if provider == "" {
		if len(providers) == 0 {
			return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "neither claude nor codex is installed on this node")
		}
		provider = providers[0]
	}
	cli, err := a.agentPath(provider)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "%s is not installed on this node", provider)
	}
	if p.CoreToken != "" && provider != nodeproto.ProviderClaude {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the core's login works for claude only")
	}
	a.work.mu.Lock()
	running := 0
	for _, s := range a.work.agents {
		if s.endedAt().IsZero() {
			running++
		}
	}
	a.work.mu.Unlock()
	if running >= maxSessions {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%d agent sessions run on this node already", running)
	}
	tmp, err := os.MkdirTemp("", "fridolin-agent-")
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "a temp folder: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.agentMaxRuntime())
	s := &agentSession{a: a, id: newSessionID(), folder: folder, requested: filepath.Clean(p.Folder), provider: provider, model: p.Model,
		sandbox: sb, cli: cli, coreToken: p.CoreToken, coreLogin: p.CoreToken != "", started: time.Now(), tmp: tmp, ctx: ctx, cancelAll: cancel, state: nodeproto.AgentRunning}
	s.git = s.isGitRepo()
	if !s.git {
		s.baseline = snapshotTree(folder)
	}
	a.work.mu.Lock()
	a.work.agents[s.id] = s
	a.pruneWorkLocked(time.Now())
	a.work.mu.Unlock()
	go func() {
		<-ctx.Done()
		if s.endedAt().IsZero() {
			s.stop(fmt.Sprintf("it ran longer than %v", a.agentMaxRuntime()))
		}
	}()
	a.logf("node: started agent session %s (%s) in %s, sandbox %s, core login %v", s.id, provider, folder, sb, p.CoreToken != "")
	s.startTurn(sessionRules(folder, provider) + "\n\n" + p.Prompt)
	return s.snapshot(0), nil
}

// sessionRules is what every session is told first.
func sessionRules(folder, provider string) string {
	return "You work for the owner of this machine through fridolin, in the folder " + folder + ". Stay in this folder. " +
		"Nobody watches this session live: if you need the owner's decision or information, end your turn with a last line \"QUESTION: <your question>\" and stop; the answer comes as the next message. " +
		"Never take a step that cannot be undone (git push, a deploy, publishing, sending a message, deleting or changing anything outside this folder) without the owner's yes: end your turn with a last line \"IRREVERSIBLE: <the step>\" and stop; the next message says whether you may. " +
		"When you are done, end with a short summary of what you did."
}

// addEventLocked records an event.
func (s *agentSession) addEventLocked(kind, text string) {
	if r := []rune(text); len(r) > 2000 {
		text = string(r[:2000]) + "…"
	}
	s.events = append(s.events, nodeproto.AgentEvent{Seq: s.nextSeq, Kind: kind, Text: text, TS: time.Now().UTC()})
	s.nextSeq++
	if len(s.events) > maxEvents {
		s.events = slices.Clone(s.events[len(s.events)-maxEvents:])
	}
}

func (s *agentSession) endedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// snapshot is the session with its events after seq.
func (s *agentSession) snapshot(after int) nodeproto.AgentSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := nodeproto.AgentSession{ID: s.id, Folder: s.folder, Provider: s.provider, Model: s.model, State: s.state, Sandbox: s.sandbox,
		CoreLogin: s.coreLogin, Ask: s.ask, Question: s.question, Summary: s.summary, Changed: slices.Clone(s.changed),
		DiffStat: s.diffStat, Error: s.errMsg, Turns: s.turns, Started: s.started.UTC(), NextSeq: s.nextSeq}
	if !s.ended.IsZero() {
		e := s.ended.UTC()
		out.Ended = &e
	}
	for _, e := range s.events {
		if e.Seq >= after && len(out.Events) < nodeproto.MaxAgentEvents {
			out.Events = append(out.Events, e)
		}
	}
	return out
}

func (a *Agent) agentByID(id string) (*agentSession, *nodeproto.Error) {
	a.work.mu.Lock()
	defer a.work.mu.Unlock()
	s := a.work.agents[id]
	if s == nil {
		return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no agent session %q", id)
	}
	return s, nil
}

func (a *Agent) agentStatus(p nodeproto.AgentRef) (any, *nodeproto.Error) {
	s, perr := a.agentByID(p.ID)
	if perr != nil {
		return nil, perr
	}
	return s.snapshot(p.After), nil
}

func (a *Agent) agentList() nodeproto.AgentList {
	a.work.mu.Lock()
	a.pruneWorkLocked(time.Now())
	list := make([]*agentSession, 0, len(a.work.agents))
	for _, s := range a.work.agents {
		list = append(list, s)
	}
	a.work.mu.Unlock()
	out := nodeproto.AgentList{Sessions: []nodeproto.AgentSession{}}
	for _, s := range list {
		snap := s.snapshot(0)
		snap.Events = nil
		out.Sessions = append(out.Sessions, snap)
	}
	slices.SortFunc(out.Sessions, func(x, y nodeproto.AgentSession) int { return x.Started.Compare(y.Started) })
	return out
}

func (a *Agent) agentReply(p nodeproto.AgentReplyParams) (any, *nodeproto.Error) {
	s, perr := a.agentByID(p.ID)
	if perr != nil {
		return nil, perr
	}
	if len(p.Text) > nodeproto.MaxAgentPrompt {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "at most %d bytes", nodeproto.MaxAgentPrompt)
	}
	s.mu.Lock()
	if s.state != nodeproto.AgentWaiting {
		state := s.state
		s.mu.Unlock()
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "agent session %s is %s, not waiting for an answer", p.ID, state)
	}
	text := strings.TrimSpace(p.Text)
	switch s.ask {
	case nodeproto.AskPermission, nodeproto.AskIrreversible:
		if s.ask == nodeproto.AskPermission && p.Allow {
			s.allowed = append(s.allowed, s.pendingAllow...)
		}
		verdict := "The owner did not allow it: do not take that step; finish without it, or ask."
		if p.Allow {
			verdict = "The owner allowed it: go ahead."
		}
		text = strings.TrimSpace(verdict + "\n\n" + text)
	default:
		if text == "" {
			s.mu.Unlock()
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the answer is empty")
		}
	}
	s.pendingAllow = nil
	s.ask, s.question, s.state = "", "", nodeproto.AgentRunning
	s.addEventLocked("reply", text)
	s.mu.Unlock()
	s.startTurn(text)
	return s.snapshot(0), nil
}

func (a *Agent) agentStop(id string) (any, *nodeproto.Error) {
	s, perr := a.agentByID(id)
	if perr != nil {
		return nil, perr
	}
	s.stop("stopped by the core")
	return s.snapshot(0), nil
}

// stop ends the session: its turn is killed, what changed is listed.
func (s *agentSession) stop(why string) {
	s.mu.Lock()
	if !s.ended.IsZero() {
		s.mu.Unlock()
		return
	}
	cancel, done := s.cancelTurn, s.turnDone
	s.state = nodeproto.AgentStopped
	s.ask, s.question = "", ""
	s.ended = time.Now()
	s.addEventLocked("stopped", why)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	s.finish()
	s.a.logf("node: agent session %s stopped: %s", s.id, why)
	s.a.pushNews(s.snapshot(0))
}

// finish lists what changed, forgets the core's token and removes the
// session's temp folder.
func (s *agentSession) finish() {
	changed, stat := s.changes()
	s.mu.Lock()
	s.changed, s.diffStat = changed, stat
	s.coreToken = ""
	s.mu.Unlock()
	s.cancelAll()
	_ = os.RemoveAll(s.tmp)
}
