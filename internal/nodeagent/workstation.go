package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// The workstation mode (docs/NODES.md, "Workstation"): the capabilities
// agent (workstation_agent.go) and terminal (workstation_terminal.go),
// each in folders the owner granted (workstation_paths.go), inside the
// platform's sandbox where there is one (workstation_sandbox.go).

// Defaults of the workstation family.
const (
	// DefaultTerminalIdle closes a terminal nobody typed into or read
	// from for this long.
	DefaultTerminalIdle = 30 * time.Minute
	// DefaultAgentMaxRuntime stops a session that ran this long in all.
	DefaultAgentMaxRuntime = 2 * time.Hour
	// terminalBuffer is the output a terminal keeps for reading, bytes.
	terminalBuffer = 1 << 20
	// maxSessions bounds the sessions of each kind on a node.
	maxSessions = 8
	// keepEnded is how long an ended session stays to be read.
	keepEnded = time.Hour
	// workNewsQueue bounds the news waiting for the core.
	workNewsQueue = 64
)

// workstation is the agent's sessions.
type workstation struct {
	mu     sync.Mutex
	agents map[string]*agentSession
	terms  map[string]*terminal
	news   chan nodeproto.AgentSession
	once   sync.Once

	probe     sync.Once
	sandbox   string
	providers []string
}

func (w *workstation) init() {
	w.once.Do(func() {
		w.agents = map[string]*agentSession{}
		w.terms = map[string]*terminal{}
		w.news = make(chan nodeproto.AgentSession, workNewsQueue)
	})
}

// newSessionID is a session's ID: 12 hex digits.
func newSessionID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// lookAgent finds a provider's CLI; tests replace it.
var lookAgent = platform.LookTool

// probeWork finds, once, the sandbox and the agent CLIs of this machine.
func (a *Agent) probeWork() (string, []string) {
	a.work.probe.Do(func() {
		a.work.sandbox = a.WorkSandbox
		if a.work.sandbox == "" {
			a.work.sandbox = detectSandbox()
		}
		for _, p := range []string{nodeproto.ProviderClaude, nodeproto.ProviderCodex} {
			if _, err := a.agentPath(p); err == nil {
				a.work.providers = append(a.work.providers, p)
			}
		}
	})
	return a.work.sandbox, a.work.providers
}

// agentPath is the provider's CLI on this machine.
func (a *Agent) agentPath(provider string) (string, error) {
	if a.AgentPaths != nil {
		if p, ok := a.AgentPaths[provider]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	return lookAgent(provider)
}

// declareWork fills in what the node reports of its workstation
// capabilities: the sandbox, and for agent the CLIs it found.
func (a *Agent) declareWork(c *nodeproto.Capabilities) {
	if c.Agent == nil && c.Terminal == nil {
		return
	}
	sb, providers := a.probeWork()
	if c.Agent != nil {
		w := *c.Agent
		w.Roots, w.Sandbox, w.Providers = slices.Clone(w.Roots), sb, slices.Clone(providers)
		c.Agent = &w
	}
	if c.Terminal != nil {
		w := *c.Terminal
		w.Roots, w.Sandbox, w.Providers = slices.Clone(w.Roots), sb, nil
		c.Terminal = &w
	}
}

// workstationCall serves the agent and terminal methods.
func (a *Agent) workstationCall(ctx context.Context, eff nodeproto.Capabilities, method string, params json.RawMessage) (any, *nodeproto.Error) {
	a.work.init()
	decode := func(v any) *nodeproto.Error {
		if len(params) == 0 {
			return nil
		}
		if err := json.Unmarshal(params, v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return nil
	}
	switch method {
	case nodeproto.MethodAgentStart:
		var p nodeproto.AgentStartParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.agentStart(eff, p)
	case nodeproto.MethodAgentStatus, nodeproto.MethodAgentStop:
		var p nodeproto.AgentRef
		if e := decode(&p); e != nil {
			return nil, e
		}
		if method == nodeproto.MethodAgentStop {
			return a.agentStop(p.ID)
		}
		return a.agentStatus(p)
	case nodeproto.MethodAgentReply:
		var p nodeproto.AgentReplyParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.agentReply(p)
	case nodeproto.MethodAgentList:
		return a.agentList(), nil
	case nodeproto.MethodTerminalOpen:
		var p nodeproto.TerminalOpenParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.terminalOpen(eff, p)
	case nodeproto.MethodTerminalSend:
		var p nodeproto.TerminalSendParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.terminalSend(p)
	case nodeproto.MethodTerminalRead:
		var p nodeproto.TerminalReadParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.terminalRead(ctx, p)
	case nodeproto.MethodTerminalResize:
		var p nodeproto.TerminalResizeParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.terminalResize(p)
	case nodeproto.MethodTerminalClose:
		var p nodeproto.TerminalRef
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.terminalClose(p.ID)
	case nodeproto.MethodTerminalList:
		return a.terminalList(), nil
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

// stopUnapproved ends the sessions of a kind the node may no longer
// serve: a revocation, or an approval taken back. stopAll ends them all.
func (a *Agent) stopUnapproved(eff nodeproto.Capabilities, all bool) {
	a.work.init()
	a.work.mu.Lock()
	var agents []*agentSession
	var terms []*terminal
	for _, s := range a.work.agents {
		if all || eff.Agent == nil || !slices.ContainsFunc(eff.Agent.Roots, func(r string) bool { return inside(r, s.folder) || inside(r, s.requested) }) {
			agents = append(agents, s)
		}
	}
	for _, t := range a.work.terms {
		if all || eff.Terminal == nil || !slices.ContainsFunc(eff.Terminal.Roots, func(r string) bool { return inside(r, t.folder) || inside(r, t.requested) }) {
			terms = append(terms, t)
		}
	}
	a.work.mu.Unlock()
	for _, s := range agents {
		s.stop("no longer approved")
	}
	for _, t := range terms {
		t.end()
	}
}

// pushNews hands the core a session's news, dropping it when the queue
// is full: the core reads the session's state with agent.status anyway.
func (a *Agent) pushNews(s nodeproto.AgentSession) {
	a.work.init()
	s.Events = nil
	select {
	case a.work.news <- s:
	default:
		a.logf("node: agent session %s: news dropped, the queue to the core is full", s.ID)
	}
}
