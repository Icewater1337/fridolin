package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// The core's side of the workstation mode (docs/NODES.md, "Workstation";
// #159): agent sessions the core brain starts on nodes, and terminals the
// core brain or the dashboard opens. The core numbers them (Agent #2,
// terminal #3) and keeps, in memory, which chat and brain each belongs
// to: a session the core has no record of (a restart forgot it) is an
// orphan, which housekeeping stops on its node. Every start and end, and
// every command line typed into a terminal, is a line of the audit log;
// a lockdown refuses starting and stops what runs.

// workNotify is the Notify of the approvals a session's irreversible step
// asks for: the answer goes to the session (workRuntime.decided).
const workNotify = "fridolin:workstation"

// workRuntime is the core's record of node sessions.
type workRuntime struct {
	b *App

	mu     sync.Mutex
	next   int64
	agents map[int64]*workAgent
	terms  map[int64]*workTerm
}

// workAgent is an agent session the core started.
type workAgent struct {
	N                          int64
	Node, ID, Folder, Provider string
	Chat, Brain, Run           string
	Started                    time.Time
	// Approval is the poll pending for its irreversible step.
	Approval string
	Ended    bool
	// Seen is the sequence number of the last event node_agent_status
	// showed.
	Seen int
}

// workTerm is a terminal the core opened.
type workTerm struct {
	N                int64
	Node, ID, Folder string
	// By is the brain that opened it, or "dashboard".
	By      string
	Started time.Time
	// line is what was typed since the last Enter, for the audit log.
	line   []rune
	escape bool
	Ended  bool
	// ReadAt is where node_terminal_read goes on.
	ReadAt int64
}

func newWorkRuntime(b *App) *workRuntime {
	return &workRuntime{b: b, agents: map[int64]*workAgent{}, terms: map[int64]*workTerm{}}
}

// workNodes are the online nodes that offer kind ("agent", "terminal")
// and match want (an id, name or tag; "" for any).
func (w *workRuntime) workNodes(kind, want string) []nodes.View {
	views, err := w.b.nodes.Views()
	if err != nil {
		log.Printf("workstation: reading the node registry: %v", err)
	}
	var out []nodes.View
	for _, v := range views {
		if v.Device != nodeproto.DeviceApproved || workCapOf(v.Effective, kind) == nil {
			continue
		}
		if want != "" && v.ID != want && !strings.EqualFold(v.Name, want) && !slices.Contains(v.Tags, strings.ToLower(want)) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func workCapOf(c nodeproto.Capabilities, kind string) *nodeproto.WorkstationCap {
	if kind == "agent" {
		return c.Agent
	}
	return c.Terminal
}

// anyWorkNode reports whether some node has kind approved: the tools of
// kind exist only then.
func (w *workRuntime) anyWorkNode(kind string) bool {
	return len(w.workNodes(kind, "")) > 0
}

// place finds the node and the folder for a session of kind: the one
// node that matches want, and folder as given (absolute), relative to
// its first granted folder, or that folder for "".
func (w *workRuntime) place(kind, want, folder string) (nodes.View, string, error) {
	list := w.workNodes(kind, want)
	var online []nodes.View
	for _, v := range list {
		if v.Online {
			online = append(online, v)
		}
	}
	switch {
	case len(online) == 0 && len(list) > 0:
		return nodes.View{}, "", fmt.Errorf("%s is offline", list[0].Name)
	case len(online) == 0:
		return nodes.View{}, "", fmt.Errorf("no node offers %s%s; the owner turns it on with fridolin node workstation on and approves it", kind, forWant(want))
	case len(online) > 1:
		names := make([]string, len(online))
		for i, v := range online {
			names[i] = v.Name
		}
		return nodes.View{}, "", fmt.Errorf("several nodes offer %s: name one (%s)", kind, strings.Join(names, ", "))
	}
	v := online[0]
	wc := workCapOf(v.Effective, kind)
	folder = strings.TrimSpace(folder)
	switch {
	case folder == "":
		folder = wc.Roots[0]
	case nodeproto.CheckAbsPath(folder) != nil:
		sep := "/"
		if strings.Contains(wc.Roots[0], `\`) {
			sep = `\`
		}
		folder = strings.TrimSuffix(wc.Roots[0], sep) + sep + strings.TrimPrefix(folder, sep)
	}
	if !wc.AllowsFolder(folder) {
		return v, "", fmt.Errorf("%s is not in a folder %s may use for %s; granted: %s", folder, v.Name, kind, strings.Join(wc.Roots, ", "))
	}
	return v, folder, nil
}

func forWant(want string) string {
	if want == "" {
		return ""
	}
	return fmt.Sprintf(" that is or is tagged %q", want)
}

// nodeName is how the chat names node id.
func (w *workRuntime) nodeName(id string) string {
	if n, err := w.b.nodes.Registry().Get(id); err == nil && n.Name != "" {
		return n.Name
	}
	return id
}

func (w *workRuntime) auditSession(e audit.Entry) {
	e.Kind = audit.NodeSession
	w.b.audit.Append(e)
}

// startAgent starts a session for a core run, whose news go to chatJID
// (App.newsChat).
func (w *workRuntime) startAgent(ctx context.Context, c mcpCaller, chatJID, want, folder, prompt, provider, model string, coreLogin bool) (*workAgent, nodeproto.AgentSession, error) {
	v, folder, err := w.place("agent", want, folder)
	if err != nil {
		return nil, nodeproto.AgentSession{}, err
	}
	p := nodeproto.AgentStartParams{Folder: folder, Prompt: prompt, Provider: provider, Model: model}
	if coreLogin {
		// Only when the core brain asked for it for this one session.
		p.CoreToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
		if p.CoreToken == "" {
			return nil, nodeproto.AgentSession{}, errors.New("use_core_login: the core has no CLAUDE_CODE_OAUTH_TOKEN to lend; leave it out, so the node uses its own login")
		}
	}
	s, err := w.b.nodes.AgentStart(ctx, v.ID, p)
	detail := fmt.Sprintf("agent start on %s in %s", v.Name, folder)
	if err != nil {
		w.auditSession(audit.Entry{Brain: c.run.Brain, Run: c.run.ID, Chat: c.run.Chat, Thread: c.run.Chat, Detail: detail, Outcome: "failed: " + err.Error()})
		return nil, s, err
	}
	w.mu.Lock()
	w.next++
	a := &workAgent{N: w.next, Node: v.ID, ID: s.ID, Folder: s.Folder, Provider: s.Provider, Chat: chatJID, Brain: c.run.Brain, Run: c.run.ID, Started: time.Now()}
	w.agents[a.N] = a
	w.mu.Unlock()
	if coreLogin {
		detail += ", with the core's login"
	}
	w.auditSession(audit.Entry{Brain: c.run.Brain, Run: c.run.ID, Chat: c.run.Chat, Thread: c.run.Chat, ID: fmt.Sprintf("agent#%d", a.N), Detail: detail + ", sandbox " + s.Sandbox, Outcome: "started"})
	return a, s, nil
}

// agent is the record of Agent #n.
func (w *workRuntime) agent(n int64) (*workAgent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := w.agents[n]
	if a == nil {
		return nil, fmt.Errorf("there is no Agent #%d", n)
	}
	return a, nil
}

// agentByNode is the record of a node's session.
func (w *workRuntime) agentByNode(node, id string) *workAgent {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, a := range w.agents {
		if a.Node == node && a.ID == id {
			return a
		}
	}
	return nil
}

// onEvent takes a session's news (nodes.Options.OnEvent).
func (w *workRuntime) onEvent(node string, ev nodeproto.Event) {
	if ev.Kind != nodeproto.EventAgent {
		return
	}
	var s nodeproto.AgentSession
	if err := json.Unmarshal(ev.Data, &s); err != nil {
		log.Printf("workstation: unreadable news from node %s: %v", node, err)
		return
	}
	a := w.agentByNode(node, s.ID)
	if a == nil {
		log.Printf("workstation: news of agent session %s on node %s, which the core did not start (or forgot in a restart); housekeeping stops it", s.ID, node)
		return
	}
	go w.relay(a, s)
}

// relay takes a session's news to the chat that started it.
func (w *workRuntime) relay(a *workAgent, s nodeproto.AgentSession) {
	news := outboundmsg.NodeAgentNews{Session: a.N, Node: w.nodeName(a.Node), Folder: a.Folder}
	switch {
	case s.State == nodeproto.AgentWaiting && (s.Ask == nodeproto.AskIrreversible || s.Ask == nodeproto.AskPermission):
		news.Kind, news.Text = s.Ask, s.Question
		id, err := w.askApproval(a, s)
		if err != nil {
			log.Printf("workstation: asking about Agent #%d's step: %v", a.N, err)
			news.Kind, news.Text = "question", s.Question+"\n\n(The approval poll could not be sent: "+err.Error()+". Until it is, the step is not taken.)"
		} else {
			w.mu.Lock()
			a.Approval = id
			w.mu.Unlock()
		}
	case s.State == nodeproto.AgentWaiting:
		news.Kind, news.Text = "question", s.Question
	case s.Finished():
		news.Kind, news.Changed = s.State, s.Changed
		news.Text = s.Summary
		if s.State == nodeproto.AgentFailed {
			news.Text = s.Error
		}
		w.mu.Lock()
		was := a.Ended
		a.Ended = true
		w.mu.Unlock()
		if !was {
			w.auditSession(audit.Entry{Brain: a.Brain, Run: a.Run, Chat: a.Chat, Thread: a.Chat, ID: fmt.Sprintf("agent#%d", a.N),
				Detail: fmt.Sprintf("agent end on %s in %s, %d files changed", w.nodeName(a.Node), a.Folder, len(s.Changed)), Outcome: s.State,
				DurationMS: time.Since(a.Started).Milliseconds()})
		}
	default:
		return
	}
	w.say(a, outboundmsg.NodeAgentNewsMessage(news))
}

// say sends text to the session's chat and records it in its brain's
// history, so its next run sees what the session asked or did.
func (w *workRuntime) say(a *workAgent, text string) {
	if a.Chat == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.b.wa.SendChunked(ctx, a.Chat, text); err != nil {
		log.Printf("workstation: telling %s about Agent #%d: %v", a.Chat, a.N, err)
		return
	}
	if br := w.b.brainMgr.Get(a.Brain); br != nil {
		br.AppendEntry(history.Entry{From: "node-agent", Chat: a.Chat, Text: text, Type: history.Out, Via: "node-agent"})
	}
}

// askApproval puts a poll for the session's step into its chat (M5's
// approvals); the answer goes to decided.
func (w *workRuntime) askApproval(a *workAgent, s nodeproto.AgentSession) (string, error) {
	b := w.b
	target := a.Chat
	if target == "" || !strings.Contains(target, "@") {
		target = ownerChat(b.cfgs.Get())
	}
	if target == "" {
		return "", errors.New("allowed_numbers holds no number of the owner")
	}
	ctx := context.Background()
	now := time.Now()
	ap := store.Approval{Kind: approvalKind, ID: "a" + randomToken(9), Brain: a.Brain, Run: a.Run, Thread: a.Chat, Chat: target,
		Notify: workNotify, Risk: "irreversible",
		Action:    fmt.Sprintf("Agent #%d on %s (in %s): %s", a.N, w.nodeName(a.Node), a.Folder, s.Question),
		Details:   fmt.Sprintf("node %s, session %s, %s", a.Node, a.ID, a.Provider),
		CreatedAt: now, ExpiresAt: now.Add(b.approvalTimeout())}
	if err := b.store.AddApproval(ctx, ap); err != nil {
		return "", err
	}
	if err := b.outbox.Poll(ctx, target, outboundmsg.ApprovalQuestion(ap.Action), []string{outboundmsg.ApprovalYes, outboundmsg.ApprovalNo, outboundmsg.ApprovalDraft}, approvalRefPrefix+ap.ID); err != nil {
		_, _ = b.store.DecideApproval(ctx, ap.ID, store.ApprovalDenied, "", "bridge", now)
		return "", err
	}
	b.audit.Append(audit.Entry{Kind: audit.ApprovalRequested, Brain: a.Brain, Run: a.Run, Chat: target, Thread: a.Chat, ID: ap.ID, Risk: ap.Risk, Detail: ap.Action, Outcome: "pending"})
	return ap.ID, nil
}

// decided takes the owner's answer to a session's step to the session.
func (w *workRuntime) decided(ap store.Approval) {
	w.mu.Lock()
	var a *workAgent
	for _, x := range w.agents {
		if x.Approval == ap.ID {
			a = x
			x.Approval = ""
		}
	}
	w.mu.Unlock()
	if a == nil {
		log.Printf("workstation: approval %s is no session's", ap.ID)
		return
	}
	allow := ap.Status == store.ApprovalApproved
	if allow && w.b.locked() {
		allow = false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	text := ""
	if !allow && ap.Status == store.ApprovalApproved {
		text = "The bridge is in lockdown."
	}
	if _, err := w.b.nodes.AgentReply(ctx, a.Node, nodeproto.AgentReplyParams{ID: a.ID, Text: text, Allow: allow}); err != nil {
		log.Printf("workstation: passing the answer to Agent #%d on: %v", a.N, err)
	}
}

// agentList lists the sessions the core started, newest first.
func (w *workRuntime) agentList() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []map[string]any{}
	for n := w.next; n > 0; n-- {
		a := w.agents[n]
		if a == nil {
			continue
		}
		state := "running"
		if a.Ended {
			state = "ended"
		} else if a.Approval != "" {
			state = "waiting for the approval poll"
		}
		out = append(out, map[string]any{"session": a.N, "node": a.Node, "folder": a.Folder, "provider": a.Provider, "state": state, "started": a.Started})
	}
	return out
}
