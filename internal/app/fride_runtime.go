package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// FrIDE through nodes (docs/NODES.md, "FrIDE"; PLAN.md, Workstream E):
// the core brain hands coding work to FrIDE on a node with the fride_*
// tools, fridolin keeps which chat each piece of work came from
// (store.FrideThread), relays FrIDE's news there, moves work between nodes
// through the vault, and archives a closed task's workspace into the vault.

// frideRuntime is the core's side of FrIDE. Its goroutines start with the
// first news or the first work, and stop with the bridge.
type frideRuntime struct {
	b *App

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	news   chan frideNews
	kick   chan struct{}

	mu sync.Mutex
	// locks guard a thread's row between reading and writing it; busy
	// are the threads a move or a close works on.
	locks map[int64]*sync.Mutex
	busy  map[int64]string
	// lastErr is the last failure each thread's chat was told of, which
	// a quiet chat is not told again; sent is when the relay last sent a
	// node's text to a chat (frideSentKey), which it does not send again
	// within waits.frideRepeat. The relay alone uses both.
	lastErr map[int64]string
	sent    map[string]time.Time
	// stalled is the last stall notice of each thread the relay took
	// (FrideStallKey), which it drops again within waits.frideStallRepeat.
	stalled map[int64]frideStall
}

type frideStall struct {
	key string
	at  time.Time
}

type frideNews struct {
	node string
	ev   nodeproto.FrideEvent
}

func newFrideRuntime(b *App) *frideRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &frideRuntime{b: b, ctx: ctx, cancel: cancel, news: make(chan frideNews, 256), kick: make(chan struct{}, 1),
		locks: map[int64]*sync.Mutex{}, busy: map[int64]string{}, lastErr: map[int64]string{}, sent: map[string]time.Time{}, stalled: map[int64]frideStall{}}
}

// start runs the relay and the queue once.
func (f *frideRuntime) start() {
	f.once.Do(func() {
		f.wg.Go(f.relayLoop)
		f.wg.Go(f.queueLoop)
	})
}

// Stop stops both and waits for them: before the store closes.
func (f *frideRuntime) Stop() {
	f.cancel()
	f.wg.Wait()
}

// onEvent takes a node's news (nodes.Options.OnEvent): FrIDE's, in order.
func (f *frideRuntime) onEvent(node string, ev nodeproto.Event) {
	if ev.Kind != nodeproto.EventFride {
		return
	}
	var fe nodeproto.FrideEvent
	if err := json.Unmarshal(ev.Data, &fe); err != nil {
		log.Printf("fride: unreadable news from node %s: %v", node, err)
		return
	}
	if f.ctx.Err() != nil {
		return
	}
	f.start()
	select {
	case f.news <- frideNews{node: node, ev: fe}:
	default:
		log.Printf("fride: dropped %s of thread %d from node %s: too much news waiting", fe.Event, fe.ThreadID, node)
	}
}

// onDeclare hears that a node offers something new: waiting work may go
// there now.
func (f *frideRuntime) onDeclare(n *nodes.Node) {
	if n.Declared.Fride == nil || f.ctx.Err() != nil {
		return
	}
	f.start()
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// relayLoop relays FrIDE's news in batches: what arrives within
// waits.frideGather of the first goes together, so that one text a node
// sends for several threads at once is sent once.
func (f *frideRuntime) relayLoop() {
	f.adoptAgentThreads(agentThreadsBefore)
	for {
		var batch []frideNews
		select {
		case <-f.ctx.Done():
			return
		case n := <-f.news:
			batch = append(batch, n)
		}
		gather := time.NewTimer(f.b.waits.frideGather)
	collect:
		for {
			select {
			case <-f.ctx.Done():
				gather.Stop()
				return
			case n := <-f.news:
				batch = append(batch, n)
			case <-gather.C:
				break collect
			}
		}
		var posts []fridePost
		for _, n := range batch {
			if p, ok := f.relay(n.node, n.ev); ok {
				posts = append(posts, p)
			}
		}
		f.post(posts)
	}
}

func (f *frideRuntime) queueLoop() {
	t := time.NewTicker(f.b.waits.frideRetry)
	defer t.Stop()
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-t.C:
		case <-f.kick:
		}
		f.retryQueued()
	}
}

// lock locks thread id's row; the returned func unlocks it.
func (f *frideRuntime) lock(id int64) func() {
	f.mu.Lock()
	l := f.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		f.locks[id] = l
	}
	f.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// claim marks thread id busy with what, unless something else works on it.
func (f *frideRuntime) claim(id int64, what string) (release func(), err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if other := f.busy[id]; other != "" {
		return nil, fmt.Errorf("FrIDE #%d is busy: %s", id, other)
	}
	f.busy[id] = what
	return func() {
		f.mu.Lock()
		delete(f.busy, id)
		f.mu.Unlock()
	}, nil
}

// nodeName is how the chat names node id.
func (f *frideRuntime) nodeName(id string) string {
	if n, err := f.b.nodes.Registry().Get(id); err == nil && n.Name != "" {
		return n.Name
	}
	return id
}

// say sends text to the thread's chat and records it in the history of the
// brain that started the work, as FrIDE's: the core brain sees FrIDE's
// question there when the answer comes. A scheduled agent's thread only
// records it: its chat is no WhatsApp chat.
func (f *frideRuntime) say(t store.FrideThread, text string) {
	if !chat.IsInternalChat(t.Chat) {
		ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
		defer cancel()
		if err := f.b.wa.SendChunked(ctx, t.Chat, text); err != nil {
			log.Printf("fride: telling %s about FrIDE #%d: %v", t.Chat, t.ID, err)
			return
		}
	}
	if br := f.b.brainMgr.Get(t.Brain); br != nil {
		br.AppendEntry(history.Entry{From: "fride", Chat: t.Chat, Text: text, Type: history.Out, Via: "fride"})
	}
}

// fridePost is a piece of FrIDE's news that relay decided a chat gets.
type fridePost struct {
	t    store.FrideThread
	node string // its id
	kind string
	news outboundmsg.FrideNews
}

// relay takes FrIDE's news about one of its threads: it records it, and
// reports whether, and as what, the chat that started the work gets it
// (post sends it). A scheduled agent's work goes to a run of that agent
// instead (frideNews).
func (f *frideRuntime) relay(node string, ev nodeproto.FrideEvent) (fridePost, bool) {
	ctx := f.ctx
	t, err := f.b.store.FrideThreadOn(ctx, node, ev.ThreadID)
	if err != nil {
		if !errors.Is(err, store.ErrNoFrideThread) {
			log.Printf("fride: finding the thread of FrIDE's news: %v", err)
		} else {
			log.Printf("fride: news of thread %d on node %s, which no chat started here (moved away, or started elsewhere); dropped", ev.ThreadID, node)
		}
		return fridePost{}, false
	}
	unlock := f.lock(t.ID)
	t, err = f.b.store.GetFrideThread(ctx, t.ID)
	if err != nil || t.Node != node || t.FrideThread != ev.ThreadID {
		unlock()
		return fridePost{}, false
	}
	if ev.Status != "" {
		t.FrideStatus = ev.Status
	}
	if ev.TaskID != "" {
		t.TaskID = ev.TaskID
	}
	if ev.Branch != "" {
		t.Branch = ev.Branch
	}
	if err := f.b.store.SaveFrideThread(ctx, t); err != nil {
		log.Printf("fride: recording FrIDE #%d: %v", t.ID, err)
	}
	kind := ev.Kind()
	if kind == nodeproto.FrideQuestion && !outboundmsg.FrideAsks(ev.Text) {
		// Progress FrIDE marked as needing a reply (fride#16): news.
		kind = nodeproto.FrideUpdate
	}
	if outboundmsg.FrideStalled(ev.Text) && (kind == nodeproto.FrideQuestion || kind == nodeproto.FrideUpdate) {
		// "agent … has not moved for 2h", every half hour (#306): progress,
		// and the same again within hours is not news at all.
		kind = nodeproto.FrideUpdate
		key, now := outboundmsg.FrideStallKey(ev.Text), time.Now()
		if last, ok := f.stalled[t.ID]; ok && last.key == key && now.Sub(last.at) < f.b.waits.frideStallRepeat {
			unlock()
			return fridePost{}, false
		}
		f.stalled[t.ID] = frideStall{key: key, at: now}
	}
	if err := f.b.store.AddFrideNews(ctx, store.FrideNewsItem{Thread: t.ID, Kind: kind, Text: ev.Text}); err != nil {
		log.Printf("fride: keeping FrIDE #%d's news: %v", t.ID, err)
	}
	quiet := f.b.cfgs.Get().Live.Fride.News != config.FrideNewsAll
	if kind == nodeproto.FrideFailed && nodeproto.FrideNotLoggedIn(ev.Text) {
		// FrIDE there cannot think (#288): the work goes to another
		// FrIDE if one takes it, and stays here for a retry otherwise.
		moved := t
		moved.State, moved.Node, moved.FrideThread, moved.FrideStatus, moved.TaskID, moved.Branch = store.FrideQueued, "", 0, "", "", ""
		pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		placed, _ := f.place(pctx, &moved, node)
		cancel()
		unlock()
		to := ""
		if placed {
			t, to = moved, f.nodeName(moved.Node)
		}
		notice := outboundmsg.FrideNotLoggedInNotice(t.ID, f.nodeName(node), to, nodeproto.FrideLoginFix)
		if quiet && !placed && f.lastErr[t.ID] == notice {
			return fridePost{}, false
		}
		f.lastErr[t.ID] = notice
		f.say(t, notice)
		return fridePost{}, false
	}
	unlock()
	news := outboundmsg.FrideNews{
		Thread: t.ID, Node: f.nodeName(node), Kind: kind, Text: ev.Text,
		Task: t.TaskID, Branch: t.Branch, Rationale: orDefault(ev.RationalePath, t.Rationale),
	}
	if chat.IsScheduleChat(t.Chat) {
		f.toAgent(t, news)
		return fridePost{}, false
	}
	if quiet {
		switch kind {
		case nodeproto.FrideDone, nodeproto.FrideQuestion:
		case nodeproto.FrideFailed:
			if f.lastErr[t.ID] == ev.Text {
				return fridePost{}, false
			}
			f.lastErr[t.ID] = ev.Text
		default:
			// Progress and bookkeeping stay on the FrIDE page; a merge
			// does not, whatever FrIDE calls it.
			if outboundmsg.FrideBookkeeping(ev.Text) || !outboundmsg.FrideMerged(ev.Text) {
				return fridePost{}, false
			}
		}
	}
	return fridePost{t: t, node: node, kind: kind, news: news}, true
}

// agentThreadsBefore is when #300 shipped. From then on, a thread a
// scheduled agent starts reports to the agent's chat from its start, so
// only older threads may need adopting. A newer thread in a person's chat
// is that person's, even when it began while a run of an agent went on
// (#337).
var agentThreadsBefore = time.Date(2026, 10, 25, 19, 55, 29, 0, time.UTC)

// adoptAgentThreads gives the open threads a scheduled agent started
// before their news stayed with it (#300) to that agent's chat, so that
// their news follows the agent's routing (#306). A thread is the agent's
// when it began before cutoff, and while a run of it went on or with its
// message's first line in the agent's task, and that holds for no other
// agent of its brain. The relay passes agentThreadsBefore.
func (f *frideRuntime) adoptAgentThreads(cutoff time.Time) {
	threads, err := f.b.store.FrideThreads(f.ctx, "")
	if err != nil {
		log.Printf("fride: listing the threads scheduled agents may have started: %v", err)
		return
	}
	for _, t := range threads {
		if t.State == store.FrideClosed || chat.IsInternalChat(t.Chat) || !t.CreatedAt.Before(cutoff) {
			continue
		}
		br := f.b.brainMgr.Get(t.Brain)
		if br == nil {
			continue
		}
		var by []string
		for _, s := range br.Manifest().EffectiveSchedules() {
			if s.Command == nil && f.b.agents.startedFride(br, s, t) {
				by = append(by, s.Name)
			}
		}
		if len(by) != 1 {
			continue
		}
		to := chat.ScheduleChatPrefix + by[0]
		if err := f.b.store.SetFrideThreadChat(f.ctx, t.ID, to); err != nil {
			log.Printf("fride: giving FrIDE #%d to scheduled agent %s: %v", t.ID, by[0], err)
			continue
		}
		log.Printf("fride: FrIDE #%d, which scheduled agent %s of brain %s started, now reports to it, not to %s", t.ID, by[0], t.Brain, t.Chat)
	}
}

// toAgent hands news of a scheduled agent's thread to that agent: it is
// recorded in the agent's chat, and a question, or work done or failed,
// starts a run of the agent with it.
func (f *frideRuntime) toAgent(t store.FrideThread, n outboundmsg.FrideNews) {
	text := outboundmsg.FrideAgentNews(n, t.Message)
	if br := f.b.brainMgr.Get(t.Brain); br != nil {
		br.AppendEntry(history.Entry{From: "fride", Chat: t.Chat, Text: text, Type: history.Out, Via: "fride"})
	}
	switch n.Kind {
	case nodeproto.FrideQuestion, nodeproto.FrideDone, nodeproto.FrideFailed:
		f.b.agents.frideNews(t.Brain, strings.TrimPrefix(t.Chat, chat.ScheduleChatPrefix), text)
	}
}

// frideSentKey names a text node sent, as chat got it.
func frideSentKey(node, chat, kind, text string) string {
	return node + "\x00" + chat + "\x00" + kind + "\x00" + outboundmsg.FrideBodyKey(text)
}

// post sends the news relay let through: one text a node sent for
// several threads goes to each chat once, naming its threads, and not
// again within waits.frideRepeat.
func (f *frideRuntime) post(posts []fridePost) {
	now := time.Now()
	for k, at := range f.sent {
		if now.Sub(at) > f.b.waits.frideRepeat {
			delete(f.sent, k)
		}
	}
	var order []string
	groups := map[string][]fridePost{}
	for _, p := range posts {
		k := frideSentKey(p.node, p.t.Chat, p.kind, p.news.Text)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], p)
	}
	for _, k := range order {
		g := groups[k]
		if _, ok := f.sent[k]; ok {
			log.Printf("fride: news of FrIDE #%d from %s went to %s a moment ago; not sent again", g[0].t.ID, g[0].news.Node, g[0].t.Chat)
			continue
		}
		f.sent[k] = now
		if len(g) == 1 {
			f.say(g[0].t, outboundmsg.FrideNewsMessage(g[0].news))
			continue
		}
		var ids []int64
		for _, p := range g {
			if !slices.Contains(ids, p.t.ID) {
				ids = append(ids, p.t.ID)
			}
		}
		f.say(g[0].t, outboundmsg.FrideFanoutMessage(g[0].news.Node, g[0].kind, ids, g[0].news.Text))
	}
}

// frideNodes are the online nodes whose FrIDE may serve the core now and
// matches want (a node's id, name or one of its tags; "" for any) and has
// repos, in the registry's order.
func (f *frideRuntime) frideNodes(want string, repos []string) []nodes.View {
	views, err := f.b.nodes.Views()
	if err != nil {
		log.Printf("fride: reading the node registry: %v", err)
	}
	var out []nodes.View
	for _, v := range views {
		fc := v.Effective.Fride
		if !v.Online || fc == nil {
			continue
		}
		if want != "" && v.ID != want && !strings.EqualFold(v.Name, want) && !slices.Contains(v.Tags, strings.ToLower(want)) {
			continue
		}
		if len(fc.Repos) > 0 && slices.ContainsFunc(repos, func(r string) bool { return !slices.Contains(fc.Repos, r) }) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// noFrideNode says why no node can take work for want and repos.
func noFrideNode(want string, repos []string) string {
	why := "no online node offers FrIDE"
	if want != "" {
		why += fmt.Sprintf(" that is or is tagged %q", want)
	}
	if len(repos) > 0 {
		why += " with " + strings.Join(repos, ", ")
	}
	return why
}

// place hands thread t to the FrIDE of a node other than skip that may
// take it: one that matches, whose memory check says a stack may start,
// the one with the most memory free first, and one whose Claude is not
// logged in only when no other takes it. It reports why none took it.
func (f *frideRuntime) place(ctx context.Context, t *store.FrideThread, skip string) (placed bool, why string) {
	cands := slices.DeleteFunc(f.frideNodes(t.WantNode, t.Repos), func(v nodes.View) bool { return v.ID == skip })
	if len(cands) == 0 {
		return false, noFrideNode(t.WantNode, t.Repos)
	}
	type fit struct {
		v   nodes.View
		mem nodeproto.FrideMem
	}
	var fits []fit
	var reasons []string
	for _, v := range cands {
		mem, err := f.b.nodes.FrideMem(ctx, v.ID)
		switch {
		case err != nil:
			reasons = append(reasons, v.Name+": "+err.Error())
		case !mem.CanStart:
			reasons = append(reasons, v.Name+": "+orDefault(mem.Reason, "FrIDE says no stack may start now"))
		default:
			fits = append(fits, fit{v, mem})
		}
	}
	slices.SortStableFunc(fits, func(a, b fit) int {
		switch al, bl := a.v.Effective.Fride.NotLoggedIn, b.v.Effective.Fride.NotLoggedIn; {
		case al != bl && bl:
			return -1
		case al != bl:
			return 1
		case a.mem.HostFree > b.mem.HostFree:
			return -1
		case a.mem.HostFree < b.mem.HostFree:
			return 1
		}
		return 0
	})
	for _, c := range fits {
		th, err := f.b.nodes.FrideTask(ctx, c.v.ID, nodeproto.FrideTaskParams{Message: t.Message, Repos: t.Repos, Ref: "fridolin-" + strconv.FormatInt(t.ID, 10), Source: "fridolin"})
		if err != nil {
			reasons = append(reasons, c.v.Name+": "+err.Error())
			continue
		}
		t.Node, t.FrideThread, t.State, t.FrideStatus, t.TaskID = c.v.ID, th.ThreadID, store.FrideSent, th.Status, th.TaskID
		if err := f.b.store.SaveFrideThread(ctx, *t); err != nil {
			log.Printf("fride: recording where FrIDE #%d went: %v", t.ID, err)
		}
		log.Printf("fride: FrIDE #%d went to %s (%s) as its thread %d", t.ID, c.v.Name, c.v.ID, th.ThreadID)
		return true, ""
	}
	return false, strings.Join(reasons, "; ")
}

// addRepo adds a repository to the FrIDE of a node that matches a.Node:
// the first, in the registry's order, unless one has it already.
func (f *frideRuntime) addRepo(ctx context.Context, a repoAddArgs) (string, error) {
	cands := f.frideNodes(a.Node, nil)
	if len(cands) == 0 {
		return "", errors.New(noFrideNode(a.Node, nil))
	}
	for _, v := range cands {
		if slices.Contains(v.Effective.Fride.Repos, a.Name) {
			return fmt.Sprintf("FrIDE on %s has %s already: fride_task can work in it.", v.Name, a.Name), nil
		}
	}
	v := cands[0]
	p := nodeproto.FrideRepoParams{Name: a.Name, URL: a.URL}
	var repo nodeproto.FrideRepo
	var err error
	if a.URL != "" {
		repo, err = f.b.nodes.FrideRepoClone(ctx, v.ID, p)
	} else {
		p.GitHub = a.GitHub == nil || *a.GitHub
		p.Public = a.Private != nil && !*a.Private
		repo, err = f.b.nodes.FrideRepoNew(ctx, v.ID, p)
	}
	if err != nil {
		return "", fmt.Errorf("adding %s to FrIDE on %s: %w", a.Name, v.Name, err)
	}
	log.Printf("fride: added the repository %s to FrIDE on %s (%s) at %s", a.Name, v.Name, v.ID, repo.Path)
	var sb strings.Builder
	if a.URL != "" {
		fmt.Fprintf(&sb, "Cloned %s into %s on %s.", a.URL, repo.Path, v.Name)
	} else {
		fmt.Fprintf(&sb, "Made the repository %s in %s on %s, with an initial commit.", a.Name, repo.Path, v.Name)
		if repo.GitHubURL != "" {
			fmt.Fprintf(&sb, " On GitHub: %s.", repo.GitHubURL)
		}
	}
	if repo.Warning != "" {
		sb.WriteString(" " + repo.Warning + ".")
	}
	switch {
	case repo.Rescanned && slices.Contains(repo.Repos, a.Name):
		fmt.Fprintf(&sb, " FrIDE knows it now: fride_task can work in %s.", a.Name)
	case repo.Rescanned:
		fmt.Fprintf(&sb, " FrIDE read its folders again but does not list %s.", a.Name)
	default:
		sb.WriteString(" FrIDE could not be asked to read its folders again; it knows the repository after its next re-check or restart.")
	}
	return sb.String(), nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// retryQueued offers waiting work to the nodes again, and tells each chat
// whose work found one.
func (f *frideRuntime) retryQueued() {
	queued, err := f.b.store.FrideThreads(f.ctx, store.FrideQueued)
	if err != nil {
		log.Printf("fride: reading the waiting work: %v", err)
		return
	}
	for _, q := range queued {
		if f.ctx.Err() != nil {
			return
		}
		unlock := f.lock(q.ID)
		t, err := f.b.store.GetFrideThread(f.ctx, q.ID)
		if err != nil || t.State != store.FrideQueued {
			unlock()
			continue
		}
		ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
		placed, _ := f.place(ctx, &t, "")
		cancel()
		unlock()
		if placed {
			f.say(t, outboundmsg.FridePlacedNotice(t.ID, f.nodeName(t.Node)))
		}
	}
}

// newWork records work the core brain hands to FrIDE for chat and tries to
// place it at once. A thread no node can take waits, and its chat is told.
func (f *frideRuntime) newWork(ctx context.Context, chat, brain, message string, repos []string, want string) (store.FrideThread, bool, string, error) {
	f.start()
	t := store.FrideThread{Chat: chat, Brain: brain, Message: message, Repos: repos, WantNode: want, State: store.FrideQueued}
	id, err := f.b.store.AddFrideThread(ctx, t)
	if err != nil {
		return t, false, "", err
	}
	t.ID = id
	unlock := f.lock(id)
	placed, why := f.place(ctx, &t, "")
	unlock()
	if !placed {
		f.say(t, outboundmsg.FrideQueuedNotice(t.ID, why))
	}
	return t, placed, why, nil
}

// thread reads thread id for a tool or a command.
func (f *frideRuntime) thread(ctx context.Context, id int64) (store.FrideThread, error) {
	t, err := f.b.store.GetFrideThread(ctx, id)
	if errors.Is(err, store.ErrNoFrideThread) {
		return t, fmt.Errorf("there is no FrIDE #%d", id)
	}
	return t, err
}

// online reports why node id cannot serve FrIDE now, "" when it can.
func (f *frideRuntime) unreachable(id string) string {
	n, err := f.b.nodes.Registry().Get(id)
	switch {
	case err != nil:
		return fmt.Sprintf("node %s is not in the registry", id)
	case !f.b.nodes.Online(id):
		return n.Name + " is offline"
	case n.Effective().Fride == nil:
		return "FrIDE on " + n.Name + " does not answer, or is not approved there"
	}
	return ""
}

// status is FrIDE's word on thread t, with what fridolin knows of it.
func (f *frideRuntime) status(ctx context.Context, t store.FrideThread) map[string]any {
	out := map[string]any{"thread": t.ID, "state": t.State, "message": t.Message, "chat": t.Chat}
	if t.Node != "" {
		out["node"] = f.nodeName(t.Node)
	}
	for k, v := range map[string]string{"task": t.TaskID, "branch": t.Branch, "fride_status": t.FrideStatus, "archive": t.Archive, "wants_node": t.WantNode} {
		if v != "" {
			out[k] = v
		}
	}
	if t.State == store.FrideQueued || t.State == store.FrideClosed || t.Node == "" {
		return out
	}
	if why := f.unreachable(t.Node); why != "" {
		out["live"] = "not available: " + why
		return out
	}
	if t.FrideThread == 0 {
		if t.TaskID != "" {
			if task, err := f.b.nodes.FrideTaskInfo(ctx, t.Node, t.TaskID); err == nil {
				out["fride_task"] = task
			} else {
				out["live"] = "not available: " + err.Error()
			}
		}
		return out
	}
	st, err := f.b.nodes.FrideStatus(ctx, t.Node, t.FrideThread)
	if err != nil {
		out["live"] = "not available: " + err.Error()
		return out
	}
	if len(st.Thread.Messages) > 12 {
		st.Thread.Messages = st.Thread.Messages[len(st.Thread.Messages)-12:]
	}
	out["fride_thread"] = st.Thread
	if st.Task != nil {
		out["fride_task"] = st.Task
	}
	unlock := f.lock(t.ID)
	if cur, err := f.b.store.GetFrideThread(ctx, t.ID); err == nil && cur.Node == t.Node && cur.FrideThread == t.FrideThread {
		cur.FrideStatus = st.Thread.Status
		if st.Thread.TaskID != "" {
			cur.TaskID = st.Thread.TaskID
		}
		if st.Task != nil && st.Task.Branch != "" {
			cur.Branch = st.Task.Branch
		}
		_ = f.b.store.SaveFrideThread(ctx, cur)
	}
	unlock()
	return out
}

// reply passes text to thread id's FrIDE thread. Work that moved has no
// thread on its node yet: the reply starts one there, naming the task.
func (f *frideRuntime) reply(ctx context.Context, id int64, text string) (string, error) {
	unlock := f.lock(id)
	defer unlock()
	t, err := f.thread(ctx, id)
	if err != nil {
		return "", err
	}
	switch {
	case t.State == store.FrideQueued:
		return "", fmt.Errorf("FrIDE #%d still waits for a node; nothing has read it yet", id)
	case t.State == store.FrideClosed:
		return "", fmt.Errorf("FrIDE #%d is closed", id)
	}
	if why := f.unreachable(t.Node); why != "" {
		return "", errors.New(why)
	}
	name := f.nodeName(t.Node)
	if t.FrideThread == 0 {
		msg := fmt.Sprintf("%s\n\n(This continues task %s, which was moved here.)", text, t.TaskID)
		th, err := f.b.nodes.FrideTask(ctx, t.Node, nodeproto.FrideTaskParams{Message: msg, Ref: fmt.Sprintf("fridolin-%d-%d", t.ID, time.Now().UnixNano()), Source: "fridolin"})
		if err != nil {
			return "", err
		}
		t.FrideThread, t.FrideStatus = th.ThreadID, th.Status
		if err := f.b.store.SaveFrideThread(ctx, t); err != nil {
			return "", err
		}
		return fmt.Sprintf("Passed on to FrIDE on %s, as a new thread there for task %s (it was moved).", name, t.TaskID), nil
	}
	th, err := f.b.nodes.FrideReply(ctx, t.Node, t.FrideThread, text)
	if err != nil {
		return "", err
	}
	t.FrideStatus = th.Status
	_ = f.b.store.SaveFrideThread(ctx, t)
	return fmt.Sprintf("Passed on to FrIDE #%d on %s (FrIDE's status: %s).", id, name, orDefault(th.Status, "unknown")), nil
}

// vaultWorkspaces is the vault's workspaces folder, made when missing.
func (f *frideRuntime) vaultWorkspaces(sub ...string) (string, error) {
	return config.EnsureDir(filepath.Join(append([]string{f.b.cfgs.Boot().Layout().VaultWorkspaces()}, sub...)...))
}

// fetch exports task from node into the vault folder dir as name, through a
// temporary file, and checks it is a whole gzip'd tar. It returns the
// path and the size.
func (f *frideRuntime) fetch(ctx context.Context, node, task string, stop bool, dir, name string) (string, int64, error) {
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name())
	n, _, err := f.b.nodes.FrideExport(ctx, node, nodeproto.FrideExportParams{TaskID: task, Stop: stop}, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("exporting %s: %w", task, err)
	}
	if err := verifyBundle(tmp.Name()); err != nil {
		return "", 0, fmt.Errorf("the export of %s does not read: %w", task, err)
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		path = filepath.Join(dir, strings.TrimSuffix(name, bundleSuffix(name))+"-"+time.Now().UTC().Format("20060102T150405Z")+bundleSuffix(name))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", 0, err
	}
	return path, n, nil
}

func bundleSuffix(name string) string {
	for _, s := range []string{".fride.tar.gz", ".bundle"} {
		if strings.HasSuffix(name, s) {
			return s
		}
	}
	return filepath.Ext(name)
}

// verifyBundle checks that path is a gzip'd tar that reads to its end, as
// FrIDE's export is: a cut-off download is not.
func verifyBundle(path string) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	if st, err := fh.Stat(); err != nil || st.Size() == 0 {
		return errors.New("it is empty")
	}
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	entries := 0
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
		entries++
	}
	if entries == 0 {
		return errors.New("it holds no files")
	}
	return gz.Close()
}

// vaultRel is path as the vault names it, as the chat is told.
func (f *frideRuntime) vaultRel(path string) string {
	if f.b.vault != nil {
		// The vault's own path (vault/workspaces/...), which vault_get,
		// fridolin vault get and the Vault page take.
		if rel, ok := f.b.vault.currentLayout().rel(path); ok && rel != "" {
			return rel
		}
	}
	if rel, err := filepath.Rel(f.b.cfgs.Boot().Layout().Vault(), path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

// move moves thread id's task to node want: exported with its agents
// stopped from the node it is on into the vault (workspaces/moves/), imported
// on the target, removed from the source. It returns what the chat is
// told.
func (f *frideRuntime) move(ctx context.Context, id int64, want string) (string, error) {
	release, err := f.claim(id, "it is being moved")
	if err != nil {
		return "", err
	}
	defer release()
	unlock := f.lock(id)
	t, err := f.thread(ctx, id)
	unlock()
	if err != nil {
		return "", err
	}
	if t.State != store.FrideSent {
		return "", fmt.Errorf("FrIDE #%d is %s; only work FrIDE has can move", id, t.State)
	}
	if t.TaskID == "" {
		st := f.status(ctx, t)
		if task, ok := st["task"].(string); ok {
			t.TaskID = task
		}
		if t, err = f.thread(ctx, id); err != nil {
			return "", err
		}
		if t.TaskID == "" {
			return "", fmt.Errorf("FrIDE has not started a task for #%d yet, so there is nothing to move", id)
		}
	}
	targets := f.frideNodes(want, nil)
	if len(targets) == 0 {
		return "", errors.New(noFrideNode(want, nil))
	}
	target := targets[0]
	if target.ID == t.Node {
		return "", fmt.Errorf("FrIDE #%d is on %s already", id, target.Name)
	}
	if why := f.unreachable(t.Node); why != "" {
		return "", fmt.Errorf("its node cannot hand it over: %s", why)
	}
	from := f.nodeName(t.Node)
	info, err := f.b.nodes.FrideTaskInfo(ctx, t.Node, t.TaskID)
	if err != nil {
		return "", fmt.Errorf("reading task %s on %s: %w", t.TaskID, from, err)
	}
	dir, err := f.vaultWorkspaces("moves")
	if err != nil {
		return "", err
	}
	path, size, err := f.fetch(ctx, t.Node, t.TaskID, true, dir, t.TaskID+".bundle")
	if err != nil {
		return "", err
	}
	f.b.vault.track(ctx, path)
	log.Printf("fride: FrIDE #%d: task %s from %s is in the vault as %s (%d bytes)", id, t.TaskID, from, f.vaultRel(path), size)
	imported, err := f.importTo(ctx, target.ID, path, t.TaskID, info.AgentRunning)
	if err != nil {
		return "", fmt.Errorf("importing on %s: %w (the task stays on %s; its bundle is in the vault as %s)", target.Name, err, from, f.vaultRel(path))
	}
	if err := f.b.nodes.FrideDelete(ctx, t.Node, t.TaskID, true); err != nil {
		log.Printf("fride: FrIDE #%d moved, but removing task %s from %s failed: %v", id, t.TaskID, from, err)
	}
	unlock = f.lock(id)
	defer unlock()
	if t, err = f.thread(ctx, id); err != nil {
		return "", err
	}
	t.Node, t.FrideThread, t.TaskID, t.FrideStatus = target.ID, 0, imported.TaskID, imported.Status
	if imported.Branch != "" {
		t.Branch = imported.Branch
	}
	if err := f.b.store.SaveFrideThread(ctx, t); err != nil {
		return "", err
	}
	return outboundmsg.FrideMovedNotice(id, from, target.Name, imported.TaskID), nil
}

// importTo imports the bundle at path on node, under task's own id, or
// task-moved when that id is taken there.
func (f *frideRuntime) importTo(ctx context.Context, node, path, task string, startAgent bool) (nodeproto.FrideTask, error) {
	try := func(as string) (nodeproto.FrideTask, error) {
		fh, err := os.Open(path)
		if err != nil {
			return nodeproto.FrideTask{}, err
		}
		defer fh.Close()
		return f.b.nodes.FrideImport(ctx, node, fh, as, startAgent)
	}
	out, err := try("")
	if pe, ok := errors.AsType[*nodeproto.Error](err); ok && strings.Contains(pe.Message, "409") {
		out, err = try(task + "-moved")
	}
	return out, err
}

// close closes thread id's task in FrIDE, archives its workspace into the
// vault (workspaces/<task>.fride.tar.gz), which keeps it cold, and removes
// the workspace from the node once the archive reads whole. Work still
// waiting for a node is only dropped.
func (f *frideRuntime) close(ctx context.Context, id int64) (string, error) {
	release, err := f.claim(id, "it is being closed")
	if err != nil {
		return "", err
	}
	defer release()
	unlock := f.lock(id)
	t, err := f.thread(ctx, id)
	if err == nil && t.State == store.FrideQueued {
		t.State = store.FrideClosed
		err = f.b.store.SaveFrideThread(ctx, t)
		unlock()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("FrIDE #%d no longer waits for a node: dropped.", id), nil
	}
	unlock()
	if err != nil {
		return "", err
	}
	if t.State == store.FrideClosed {
		return fmt.Sprintf("FrIDE #%d is closed already; its archive is %s.", id, orDefault(t.Archive, "not in the vault")), nil
	}
	if why := f.unreachable(t.Node); why != "" {
		return "", errors.New(why)
	}
	if t.TaskID == "" {
		f.status(ctx, t)
		if t, err = f.thread(ctx, id); err != nil {
			return "", err
		}
		if t.TaskID == "" {
			return "", fmt.Errorf("FrIDE has not started a task for #%d, so there is nothing to close; it may still be asking (fride_status)", id)
		}
	}
	name := f.nodeName(t.Node)
	closed, err := f.b.nodes.FrideClose(ctx, t.Node, t.TaskID)
	if err != nil {
		return "", fmt.Errorf("closing task %s on %s: %w", t.TaskID, name, err)
	}
	// Recorded now: FrIDE's done comes at its next look at the task,
	// which may be after the workspace, and the document in it, is gone.
	unlock = f.lock(id)
	if cur, err := f.thread(ctx, id); err == nil {
		cur.Rationale = closed.RationalePath
		if closed.Branch != "" {
			cur.Branch = closed.Branch
		}
		_ = f.b.store.SaveFrideThread(ctx, cur)
	}
	unlock()
	dir, err := f.vaultWorkspaces()
	if err != nil {
		return "", err
	}
	path, size, err := f.fetch(ctx, t.Node, t.TaskID, false, dir, t.TaskID+".fride.tar.gz")
	if err != nil {
		return "", fmt.Errorf("task %s is closed on %s, but its workspace stays there: %w", t.TaskID, name, err)
	}
	// Into the vault now: uploaded to the cold tier, where the policy
	// (archived-workspace) lets it go cold at once.
	f.b.vault.track(ctx, path)
	removed := fmt.Sprintf("and removed from %s", name)
	if err := f.b.nodes.FrideDelete(ctx, t.Node, t.TaskID, true); err != nil {
		log.Printf("fride: FrIDE #%d: removing task %s from %s: %v", id, t.TaskID, name, err)
		removed = fmt.Sprintf("but removing it from %s failed (%v)", name, err)
	}
	unlock = f.lock(id)
	defer unlock()
	if cur, err := f.thread(ctx, id); err == nil {
		t = cur
	}
	t.State, t.Archive, t.FrideStatus = store.FrideClosed, f.vaultRel(path), closed.Status
	if closed.Branch != "" {
		t.Branch = closed.Branch
	}
	if err := f.b.store.SaveFrideThread(ctx, t); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Closed task %s on %s", t.TaskID, name)
	if t.Branch != "" {
		fmt.Fprintf(&b, " (branch %s)", t.Branch)
	}
	b.WriteString(".")
	if closed.RationalePath != "" {
		fmt.Fprintf(&b, " Rationale: %s.", closed.RationalePath)
	}
	fmt.Fprintf(&b, " Its workspace is archived in the vault as %s (%d bytes), %s.", t.Archive, size, removed)
	return b.String(), nil
}

// moveCommand runs !move: it says the move began, and how it ended.
func (f *frideRuntime) moveCommand(_ context.Context, jid, thread, node string) {
	notice := func(text string) {
		ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
		defer cancel()
		_ = f.b.wa.SendMessage(ctx, jid, text)
	}
	id, err := strconv.ParseInt(thread, 10, 64)
	if err != nil || id <= 0 {
		notice(outboundmsg.FrideMoveUsage)
		return
	}
	// A move changes work on two nodes: not in a lockdown (M5), as
	// fride_move is not.
	if f.b.locked() {
		notice(outboundmsg.LockdownRefusedNotice)
		return
	}
	f.start()
	notice(outboundmsg.FrideMovingNotice(id, node))
	f.wg.Go(func() {
		ctx, cancel := context.WithTimeout(f.ctx, 30*time.Minute)
		defer cancel()
		text, err := f.move(ctx, id, node)
		if err != nil {
			text = outboundmsg.FrideMoveFailedNotice(id, err.Error())
		}
		notice(text)
	})
}
