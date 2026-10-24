package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// The programs of approved command agents, added to their nodes'
// allowlists (docs/NODES.md, "Programs from the core"). Whenever the
// approved agents change, a node connects, or the scheduler ticks, the
// core works out each node's list, the programs its approved command
// agents run there that node.json does not allow already, and sends a
// node whose list changed the whole of it (nodes.Service.SetPrograms).
// A program no approved agent runs any more drops out of the list, and
// with it out of the node's allowlist. Nothing is sent in a lockdown.

// programsRetry is how long a list that did not reach a node waits before
// it is sent again.
const programsRetry = 10 * time.Minute

// nodePrograms keeps the nodes' lists in step with the approved agents.
type nodePrograms struct {
	b *App
	// timeout bounds one node's call.
	timeout time.Duration

	mu      sync.Mutex
	sent    map[string]string    // node id: the list it took, as JSON
	failed  map[string]time.Time // node id: when its list last failed
	running bool
	again   bool
	// idle closes when no sync runs, for the tests.
	idle chan struct{}
}

func newNodePrograms(b *App) *nodePrograms {
	return &nodePrograms{b: b, timeout: time.Minute, sent: map[string]string{}, failed: map[string]time.Time{}}
}

// commandProgram is a program an approved command agent runs on a node.
type commandProgram struct {
	Node  string // as the agent names it, id or name
	Argv  []string
	Agent string // brain/name
}

// approvedCommands are the programs of the approved command agents of
// every brain in brainDirs.
func approvedCommands(brainDirs []config.BrainRef) []commandProgram {
	var out []commandProgram
	for _, ref := range brainDirs {
		m := ref.Approved
		if m == nil {
			continue
		}
		for _, s := range m.Schedules {
			if c := s.Command; c != nil && len(c.Argv) > 0 {
				out = append(out, commandProgram{Node: c.Node, Argv: c.Argv, Agent: m.ID + "/" + s.Name})
			}
		}
	}
	return out
}

// nodeNamed is the node ref names, by id or by name; nil for none.
func nodeNamed(views []nodes.View, ref string) *nodes.View {
	for i := range views {
		if views[i].ID == ref {
			return &views[i]
		}
	}
	for i := range views {
		if strings.EqualFold(views[i].Name, ref) {
			return &views[i]
		}
	}
	return nil
}

// ownRules are the exec rules of n that node.json allows and the owner
// approved: those the core did not add.
func ownRules(eff nodeproto.Capabilities) nodeproto.Capabilities {
	if eff.Exec == nil {
		return nodeproto.Capabilities{}
	}
	e := *eff.Exec
	e.Allow = slices.DeleteFunc(slices.Clone(e.Allow), func(r nodeproto.ExecRule) bool { return r.Core })
	return nodeproto.Capabilities{Exec: &e}
}

// wantedPrograms is each node's list: by node id, for the nodes that
// accept programs from the core and whose owner approved that they do.
func wantedPrograms(views []nodes.View, cmds []commandProgram) map[string][]nodeproto.ExecProgram {
	out := map[string][]nodeproto.ExecProgram{}
	for _, v := range views {
		if v.Device == nodeproto.DeviceApproved && v.Effective.Allows(nodeproto.MethodExecPrograms) {
			out[v.ID] = []nodeproto.ExecProgram{}
		}
	}
	for _, c := range cmds {
		v := nodeNamed(views, c.Node)
		if v == nil {
			continue
		}
		list, ok := out[v.ID]
		if !ok || ownRules(v.Effective).PermitsExec(c.Argv) == nil {
			continue
		}
		p := nodeproto.ExecProgram{Name: c.Argv[0], Args: slices.Clone(c.Argv[1:])}
		i := slices.IndexFunc(list, func(x nodeproto.ExecProgram) bool { return x.Name == p.Name && slices.Equal(x.Args, p.Args) })
		if i < 0 {
			list = append(list, p)
			i = len(list) - 1
		}
		if !slices.Contains(list[i].Agents, c.Agent) {
			list[i].Agents = append(list[i].Agents, c.Agent)
		}
		out[v.ID] = list
	}
	for id, list := range out {
		for i := range list {
			slices.Sort(list[i].Agents)
		}
		slices.SortFunc(list, func(x, y nodeproto.ExecProgram) int {
			return strings.Compare(x.Name+"\x00"+strings.Join(x.Args, "\x00"), y.Name+"\x00"+strings.Join(y.Args, "\x00"))
		})
		out[id] = list
	}
	return out
}

// connected sends node id its list again: it may have missed a change
// while it was away, or come back with its own file lost.
func (p *nodePrograms) connected(id string) {
	p.mu.Lock()
	delete(p.sent, id)
	delete(p.failed, id)
	p.mu.Unlock()
	p.sync()
}

// changed sends every node whose list changed now, after the owner
// approved, edited or removed an agent.
func (p *nodePrograms) changed() {
	p.mu.Lock()
	clear(p.failed)
	p.mu.Unlock()
	p.sync()
}

// sync sends the lists that changed, in the background; one sync runs at
// a time, and a call during one runs another after it.
func (p *nodePrograms) sync() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		p.again = true
		return
	}
	p.running = true
	p.idle = make(chan struct{})
	go p.loop()
}

func (p *nodePrograms) loop() {
	for {
		p.once()
		p.mu.Lock()
		if !p.again {
			p.running = false
			close(p.idle)
			p.mu.Unlock()
			return
		}
		p.again = false
		p.mu.Unlock()
	}
}

// wait returns once no sync runs, for the tests.
func (p *nodePrograms) wait() {
	p.mu.Lock()
	idle := p.idle
	p.mu.Unlock()
	if idle != nil {
		<-idle
	}
}

func (p *nodePrograms) once() {
	b := p.b
	if b.nodes == nil || b.cfgs == nil || b.locked() {
		return
	}
	views, err := b.nodes.Views()
	if err != nil {
		log.Printf("nodes: the programs from the core: %v", err)
		return
	}
	wanted := wantedPrograms(views, approvedCommands(b.cfgs.Get().BrainDirs))
	for id, list := range wanted {
		if !b.nodes.Online(id) {
			continue
		}
		data, _ := json.Marshal(list)
		key := string(data)
		p.mu.Lock()
		skip := p.sent[id] == key || time.Since(p.failed[id]) < programsRetry
		p.mu.Unlock()
		if skip {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
		_, err := b.nodes.SetPrograms(ctx, id, list)
		cancel()
		p.mu.Lock()
		if err != nil {
			p.failed[id] = time.Now()
			delete(p.sent, id)
		} else {
			p.sent[id] = key
			delete(p.failed, id)
		}
		p.mu.Unlock()
		if err != nil {
			log.Printf("nodes: sending node %s the programs of its command agents: %v", id, err)
		}
	}
}

// The states of a command agent's program on its node (programView.State).
const (
	// ProgramAllowed: the node runs it, as node.json allows it or as the
	// core added it.
	ProgramAllowed = "allowed"
	// ProgramWaiting: the node accepts it, once the agent is approved, or
	// once the node connects and takes the core's list.
	ProgramWaiting = "waiting"
	// ProgramRefused: the node would not take it, and says why.
	ProgramRefused = "refused"
	// ProgramNotAccepted: the node accepts no programs from the core, or
	// the owner did not approve that it does.
	ProgramNotAccepted = "not-accepted"
	// ProgramNoNode: there is no such node.
	ProgramNoNode = "no-node"
)

// programView is how a command agent's program stands on its node, for
// the Schedules page and the agent wizard.
type programView struct {
	Node     string `json:"node,omitempty"`
	NodeName string `json:"node_name,omitempty"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
	// By says who allows it: "node" (node.json), "core" (added for the
	// approved agents); "" when it is not allowed.
	By string `json:"by,omitempty"`
}

// programOn is how argv stands on the node ref names, approved saying
// whether its agent is approved.
func programOn(views []nodes.View, ref string, argv []string, approved bool) programView {
	v := nodeNamed(views, ref)
	if v == nil {
		return programView{State: ProgramNoNode, Detail: fmt.Sprintf("there is no node %q (the Nodes page lists them)", ref)}
	}
	out := programView{Node: v.ID, NodeName: v.Name}
	name := v.Name
	if len(argv) == 0 {
		out.State, out.Detail = ProgramRefused, "the agent names no program"
		return out
	}
	if ownRules(v.Effective).PermitsExec(argv) == nil {
		out.State, out.By, out.Detail = ProgramAllowed, "node", fmt.Sprintf("node %s's node.json allows %s", name, argv[0])
		return out
	}
	if approved && v.Effective.PermitsExec(argv) == nil {
		out.State, out.By, out.Detail = ProgramAllowed, "core", fmt.Sprintf("added to node %s's allowlist for its approved agents", name)
		return out
	}
	if st := v.Programs; st != nil && approved {
		for _, r := range st.Refused {
			if r.Name == argv[0] && slices.Equal(r.Args, argv[1:]) {
				out.State, out.Detail = ProgramRefused, fmt.Sprintf("node %s refused it: %s", name, r.Why)
				return out
			}
		}
	}
	var declared, approvedFolders []string
	if v.Declared.Exec != nil {
		declared = v.Declared.Exec.AcceptFromCore
	}
	if v.Effective.Exec != nil {
		approvedFolders = v.Effective.Exec.AcceptFromCore
	}
	switch {
	case len(declared) == 0:
		out.State, out.Detail = ProgramNotAccepted, fmt.Sprintf("node %s accepts no programs from the core: add the program's folder to exec.accept_from_core in its node.json, or allow it there (docs/NODES.md, Programs from the core)", name)
	case len(approvedFolders) == 0:
		out.State, out.Detail = ProgramNotAccepted, fmt.Sprintf("node %s accepts programs from %s, which waits for your approval of its commands on the Nodes page", name, strings.Join(declared, ", "))
	case !slices.ContainsFunc(approvedFolders, func(f string) bool { return under(f, argv[0]) }):
		out.State, out.Detail = ProgramNotAccepted, fmt.Sprintf("%s is in none of the folders node %s accepts programs from (%s)", argv[0], name, strings.Join(approvedFolders, ", "))
	case !approved:
		out.State, out.Detail = ProgramWaiting, fmt.Sprintf("approving the agent adds it to node %s's allowlist, with exactly these arguments", name)
	case !v.Online:
		out.State, out.Detail = ProgramWaiting, fmt.Sprintf("added to node %s's allowlist when it connects", name)
	default:
		out.State, out.Detail = ProgramWaiting, fmt.Sprintf("being added to node %s's allowlist", name)
	}
	return out
}

// under reports whether path lies in folder, as written, in either OS's
// spelling: the node checks it again with its symlinks resolved.
func under(folder, path string) bool {
	norm := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, `\`, "/"), "/") }
	f, p := norm(folder), norm(path)
	return f != "" && strings.HasPrefix(p, f+"/")
}
