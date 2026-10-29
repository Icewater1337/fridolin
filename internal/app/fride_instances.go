package app

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// FrIDE instances (docs/NODES.md, "The core and FrIDE instances";
// docs/VISION.md, "FrIDE instances"): each a whole FrIDE in a Docker
// Sandbox of its own on a node that offers fride_host, for one trust
// domain that fride.instances names. The core starts one when work needs
// it, with the memory placement (fride_instance_place.go) decides, hands
// it its git identities with their tokens, keeps them current, and stops
// it once it idled for its idle_stop_minutes. Its threads stay on it.

// frideInstanceBackoff is how long the queue waits after a start that
// failed before it starts the instance again on its own.
const frideInstanceBackoff = 5 * time.Minute

// Who starts and stops are by, in the audit, besides "core brain" and
// "dashboard": the queue's own starts, the stops that make room for
// another instance, and the idle stop.
const (
	frideByQueue = "queue"
	frideByRoom  = "core"
	frideByIdle  = "idle"
)

// frideInstanceRefreshEvery is how often running instances are given
// their identities, repositories and egress again where they changed: a
// GitHub token is renewed before it runs out, the config was edited.
const frideInstanceRefreshEvery = 5 * time.Minute

// frideInstances is the core's side of the instances.
type frideInstances struct {
	f *frideRuntime
	// mu makes starts, stops and refreshes one at a time, so that two
	// starts never count the same free memory.
	mu sync.Mutex

	st sync.Mutex
	// active is when each instance last had work or news, or when the
	// core first saw it run; failed is its last start that failed; sent
	// what each running instance was last given (a fingerprint, tokens
	// included, that stays in memory).
	active map[string]time.Time
	failed map[string]frideFailure
	sent   map[string]string
	// toldNoLogin is, by node, the reason its instances' agents cannot
	// log in to Claude that the owner was told of.
	toldNoLogin map[string]string
	// refresh asks the loop to give running instances what changed (true:
	// even what did not).
	refresh chan bool
}

// frideFailure is a start that did not happen: node says a node's start
// failed, which the queue does not try again within
// frideInstanceBackoff; else no node could take it, which it tries again
// once one might.
type frideFailure struct {
	at   time.Time
	why  string
	node bool
}

func newFrideInstances(f *frideRuntime) *frideInstances {
	return &frideInstances{f: f, active: map[string]time.Time{}, failed: map[string]frideFailure{}, sent: map[string]string{}, toldNoLogin: map[string]string{}, refresh: make(chan bool, 1)}
}

// configured are the instances fride.instances names.
func (i *frideInstances) configured() map[string]config.FrideInstanceConfig {
	return i.f.b.cfgs.Get().Fride.Instances
}

// touch records work or news on instance now.
func (i *frideInstances) touch(name string) {
	if name == "" {
		return
	}
	i.st.Lock()
	i.active[name] = time.Now()
	i.st.Unlock()
}

// lastActive is when instance last had work or news; zero when the core
// has not seen it yet.
func (i *frideInstances) lastActive(name string) time.Time {
	i.st.Lock()
	defer i.st.Unlock()
	return i.active[name]
}

// changed asks for running instances to be given their identities again
// where they changed (gitIdentityService.changed).
func (i *frideInstances) changed() { i.kick(false) }

func (i *frideInstances) kick(again bool) {
	if !i.f.b.nodesEnabled || len(i.configured()) == 0 {
		return
	}
	i.f.start()
	select {
	case i.refresh <- again:
	default:
		if again {
			// One waits already; make it an again.
			select {
			case <-i.refresh:
			default:
			}
			select {
			case i.refresh <- true:
			default:
			}
		}
	}
}

// onEvent hears a node with instances ask for its identities again.
func (i *frideInstances) onEvent(node string, ev nodeproto.Event) {
	if ev.Kind != nodeproto.EventFrideIdentities {
		return
	}
	if n, err := i.f.b.nodes.Registry().Get(node); err == nil && n.Declared.FrideHost != nil {
		i.kick(true)
	}
}

// frideWhere is where an instance is: the node and what it says of it.
type frideWhere struct {
	v  nodes.View
	in nodeproto.FrideInstance
}

// running reports whether the instance runs on a node the core reaches.
func (w frideWhere) running() bool {
	return w.v.Node != nil && w.v.Online && w.in.State == nodeproto.FrideInstanceRunning
}

func frideStateRank(state string) int {
	switch state {
	case nodeproto.FrideInstanceRunning:
		return 0
	case nodeproto.FrideInstanceStarting:
		return 1
	case nodeproto.FrideInstanceFailed:
		return 2
	}
	return 3
}

// locate finds instance name on the nodes: where it runs, else where it
// starts, failed or stopped, an online node first.
func locate(name string, views []nodes.View) (frideWhere, bool) {
	var out []frideWhere
	for _, v := range views {
		if v.Device != nodeproto.DeviceApproved {
			continue
		}
		if in := v.Effective.FrideHost.Instance(name); in != nil {
			out = append(out, frideWhere{v: v, in: *in})
		}
	}
	if len(out) == 0 {
		return frideWhere{}, false
	}
	slices.SortStableFunc(out, func(a, b frideWhere) int {
		online := func(w frideWhere) int {
			if w.v.Online {
				return 0
			}
			return 1
		}
		return cmp.Or(cmp.Compare(online(a), online(b)), cmp.Compare(frideStateRank(a.in.State), frideStateRank(b.in.State)), cmp.Compare(a.v.ID, b.v.ID))
	})
	return out[0], true
}

// views reads the nodes.
func (i *frideInstances) views() []nodes.View {
	views, err := i.f.b.nodes.Views()
	if err != nil {
		log.Printf("fride: reading the node registry: %v", err)
	}
	return views
}

// openThreads are the threads of each instance that are not closed.
func (i *frideInstances) openThreads(ctx context.Context) (map[string][]store.FrideThread, error) {
	all, err := i.f.b.store.FrideThreads(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[string][]store.FrideThread{}
	for _, t := range all {
		if t.Instance != "" && t.State != store.FrideClosed {
			out[t.Instance] = append(out[t.Instance], t)
		}
	}
	return out, nil
}

// threadList names threads as the chat does: #1, #3.
func threadList(ts []store.FrideThread) string {
	ids := make([]string, len(ts))
	for k, t := range ts {
		ids[k] = fmt.Sprintf("#%d", t.ID)
	}
	return strings.Join(ids, ", ")
}

// allowedOn reports whether the instance's config lets it run on v.
func allowedOn(ic config.FrideInstanceConfig, v nodes.View) bool {
	if len(ic.Nodes) == 0 {
		return true
	}
	return slices.ContainsFunc(ic.Nodes, func(ref string) bool {
		ref = strings.TrimSpace(ref)
		return ref == v.ID || strings.EqualFold(ref, v.Name)
	})
}

// hostReady reports whether v can start instances now: tpl is the
// template the core sends ("" for none), which a node that built none
// needs; lent says the core lends the instance its Claude token, so that
// the node needs none of its own.
func hostReady(v nodes.View, tpl string, lent bool) bool {
	h := v.Effective.FrideHost
	return v.Device == nodeproto.DeviceApproved && v.Online && h != nil && h.Ready && (tpl != "" || !h.NeedsTemplate) && (h.NoLogin == "" || lent)
}

// coreToken is the core's Claude token, which instances whose config
// says core_login get with each start: "" for none.
func (i *frideInstances) coreToken() string {
	return strings.TrimSpace(getenv(i.f.b.env, "CLAUDE_CODE_OAUTH_TOKEN"))
}

// lends reports whether the core lends instance config ic its Claude
// token.
func (i *frideInstances) lends(ic config.FrideInstanceConfig) bool {
	return ic.CoreLogin && i.coreToken() != ""
}

// noLoginOn are the nodes instance config ic may run on that are ready
// but whose instances' agents cannot log in to Claude, by id, with why;
// none when the core lends its token (lent).
func noLoginOn(ic config.FrideInstanceConfig, views []nodes.View, lent bool) map[string]frideNoLogin {
	out := map[string]frideNoLogin{}
	if lent {
		return out
	}
	for _, v := range views {
		h := v.Effective.FrideHost
		if v.Device == nodeproto.DeviceApproved && v.Online && h != nil && h.Ready && h.NoLogin != "" && allowedOn(ic, v) {
			out[v.ID] = frideNoLogin{node: v.Name, why: h.NoLogin}
		}
	}
	return out
}

// frideNoLogin is a node whose instances' agents cannot log in to Claude.
type frideNoLogin struct{ node, why string }

// tellNoLogin tells the owner, once per node and reason while the core
// runs, that work wanted a FrIDE instance where its agents cannot log in
// to Claude, and how to fix it.
func (i *frideInstances) tellNoLogin(on map[string]frideNoLogin) {
	for _, id := range slices.Sorted(maps.Keys(on)) {
		n := on[id]
		i.st.Lock()
		told := i.toldNoLogin[id] == n.why
		i.toldNoLogin[id] = n.why
		i.st.Unlock()
		if !told {
			i.f.b.tellOwner(outboundmsg.FrideHostNoLoginNotice(n.node, n.why))
		}
	}
}

// noTemplate says why a node that built no template cannot start
// instances of this core.
const noTemplate = "it built no FrIDE template, and this core does not know where its images are published (FRIDOLIN_BOX_REGISTRY): run fridolin node fride host setup with Docker there"

// frideSandboxTemplate is the published FrIDE template a node that built
// none makes its instances from: <registry>/fridolin-fride-sandbox:<tag>,
// with the tag the core's image names (FRIDOLIN_FRIDE_SANDBOX_TAG, which
// changes only with what goes into the template, so that a new core does
// not have every instance made anew), else the core's version. "" when
// the core does not know its registry (a local build).
func frideSandboxTemplate(getenv func(string) string, version string) string {
	reg := strings.TrimRight(strings.TrimSpace(getenv("FRIDOLIN_BOX_REGISTRY")), "/")
	tag := strings.TrimSpace(getenv("FRIDOLIN_FRIDE_SANDBOX_TAG"))
	if tag == "" {
		tag = version
	}
	if reg == "" || tag == "" {
		return ""
	}
	ref := reg + "/" + nodeproto.FrideSandboxImage + ":" + tag
	if nodeproto.CheckTemplateRef(ref) != nil {
		return ""
	}
	return ref
}

// template is the FrIDE template the core sends with each start.
func (i *frideInstances) template() string {
	b := i.f.b
	return frideSandboxTemplate(func(name string) string { return getenv(b.env, name) }, b.nodes.Version())
}

// hosts are the nodes instance name may start on (only pin, when set),
// as placement sees them.
func (i *frideInstances) hosts(name string, ic config.FrideInstanceConfig, views []nodes.View, pin string, open map[string][]store.FrideThread) []frideHost {
	var out []frideHost
	tpl, lent := i.template(), i.lends(ic)
	for _, v := range views {
		if !hostReady(v, tpl, lent) || !allowedOn(ic, v) || pin != "" && v.ID != pin {
			continue
		}
		fh := v.Effective.FrideHost
		h := frideHost{ID: v.ID, Name: v.Name, FreeMB: fh.AvailableMemoryMB - fh.ReserveMB}
		for _, in := range fh.Instances {
			up := in.State == nodeproto.FrideInstanceRunning || in.State == nodeproto.FrideInstanceStarting
			if in.Name == name {
				h.Has = true
				if up {
					h.FreeMB += in.MemoryMB
				}
				continue
			}
			if up {
				active := i.lastActive(in.Name)
				h.Running = append(h.Running, frideGuest{Name: in.Name, MemoryMB: in.MemoryMB, Open: len(open[in.Name]), Active: active})
			}
		}
		out = append(out, h)
	}
	return out
}

// noHost says why no node can start instance name; tpl is the template
// the core sends, lent whether it lends its Claude token.
func noHost(name string, ic config.FrideInstanceConfig, views []nodes.View, pin, tpl string, lent bool) string {
	if pin != "" {
		why := "is not in the registry"
		if i := slices.IndexFunc(views, func(v nodes.View) bool { return v.ID == pin }); i >= 0 {
			v := views[i]
			switch {
			case !v.Online:
				why = "is offline"
			case v.Effective.FrideHost == nil:
				why = "does not offer FrIDE instances now"
			case v.Effective.FrideHost.Ready && v.Effective.FrideHost.NeedsTemplate && tpl == "":
				why = "cannot start instances of this core: " + noTemplate
			case v.Effective.FrideHost.Ready && v.Effective.FrideHost.NoLogin != "" && !lent:
				why = "cannot log in FrIDE's agents to Claude: " + v.Effective.FrideHost.NoLogin
			default:
				why = "cannot start instances now: " + orDefault(v.Effective.FrideHost.Problem, "its sandboxes are not ready")
			}
			return fmt.Sprintf("the FrIDE instance %s has open threads on %s, which %s", name, v.Name, why)
		}
		return fmt.Sprintf("the FrIDE instance %s has open threads on node %s, which %s", name, pin, why)
	}
	var problems []string
	for _, v := range views {
		if v.Device != nodeproto.DeviceApproved || v.Effective.FrideHost == nil || !allowedOn(ic, v) {
			continue
		}
		switch {
		case !v.Online:
			problems = append(problems, v.Name+" is offline")
		case !v.Effective.FrideHost.Ready:
			problems = append(problems, v.Name+": "+orDefault(v.Effective.FrideHost.Problem, "its sandboxes are not ready"))
		case v.Effective.FrideHost.NeedsTemplate && tpl == "":
			problems = append(problems, v.Name+": "+noTemplate)
		case v.Effective.FrideHost.NoLogin != "" && !lent:
			problems = append(problems, v.Name+": its instances' agents cannot log in to Claude: "+v.Effective.FrideHost.NoLogin)
		}
	}
	why := "no approved node offers FrIDE instances (fride_host)"
	if len(ic.Nodes) > 0 {
		why += " among " + strings.Join(ic.Nodes, ", ")
	}
	if len(problems) > 0 {
		why = "no node can start FrIDE instances now (" + strings.Join(problems, "; ") + ")"
	}
	return fmt.Sprintf("the FrIDE instance %s cannot start: %s", name, why)
}

// frideStarted is what a start did.
type frideStarted struct {
	Name, Node, NodeID string
	MemoryMB           int
	// Wanted is what it asked for first; Already says it ran already.
	Wanted  int
	Already bool
	// Stopped are the idle instances stopped to make room.
	Stopped []string
	// Missing are the identities its config names that the core does
	// not hold.
	Missing []string
}

// less reports whether it got less memory than it asked for.
func (s frideStarted) less() bool { return !s.Already && s.Wanted > 0 && s.MemoryMB < s.Wanted }

// Text says what happened, for a tool's answer.
func (s frideStarted) Text() string {
	var b strings.Builder
	if s.Already {
		fmt.Fprintf(&b, "The FrIDE instance %s runs on %s with %d MB.", s.Name, s.Node, s.MemoryMB)
	} else {
		fmt.Fprintf(&b, "Started the FrIDE instance %s on %s with %d MB.", s.Name, s.Node, s.MemoryMB)
	}
	if s.less() {
		fmt.Fprintf(&b, " That is less than the %d MB asked: no node had more room. A heavy stack may not fit.", s.Wanted)
	}
	if len(s.Stopped) > 0 {
		fmt.Fprintf(&b, " To make room, the idle instance %s stopped.", strings.Join(s.Stopped, ", "))
	}
	if len(s.Missing) > 0 {
		fmt.Fprintf(&b, " It has no %s identity: the core holds none (Integrations on the dashboard).", strings.Join(s.Missing, " or "))
	}
	return b.String()
}

// errUnknownInstance says name is no configured instance.
func (i *frideInstances) errUnknown(name string) error {
	names := slices.Sorted(maps.Keys(i.configured()))
	if len(names) == 0 {
		return apperr.New(apperr.NotFound, fmt.Sprintf("there is no FrIDE instance %q: fride.instances names none", name))
	}
	return apperr.New(apperr.NotFound, fmt.Sprintf("there is no FrIDE instance %q: the instances are %s", name, strings.Join(names, ", ")))
}

// ensure makes instance name run, starting it at size ("" for medium)
// when it does not; auto is the queue's own try, which waits
// frideInstanceBackoff after a start that failed, and makes none in a
// lockdown.
func (i *frideInstances) ensure(ctx context.Context, name, size, by, why string, auto bool) (frideStarted, error) {
	if w, ok := locate(name, i.views()); ok && w.running() && w.in.NoLogin == "" {
		i.touch(name)
		return frideStarted{Name: name, Node: w.v.Name, NodeID: w.v.ID, MemoryMB: w.in.MemoryMB, Already: true}, nil
	}
	if auto {
		if i.f.b.locked() {
			return frideStarted{}, errors.New("the bridge is in lockdown, which starts no FrIDE instance")
		}
		i.st.Lock()
		last, failed := i.failed[name]
		i.st.Unlock()
		if failed && last.node && time.Since(last.at) < frideInstanceBackoff {
			return frideStarted{}, fmt.Errorf("its last start failed (%s); the next try is after %s", last.why, last.at.Add(frideInstanceBackoff).Format("15:04"))
		}
	}
	return i.start(ctx, name, size, by, why)
}

// start starts instance name at size ("" for medium; a running one is
// remade at it when it is another, and has no open thread). by and why go
// into the audit.
func (i *frideInstances) start(ctx context.Context, name, size, by, why string) (frideStarted, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	ic, ok := i.configured()[name]
	if !ok {
		return frideStarted{}, i.errUnknown(name)
	}
	if !validFrideSize(size) {
		return frideStarted{}, apperr.New(apperr.Invalid, fmt.Sprintf("size %q: small, medium or large", size))
	}
	open, err := i.openThreads(ctx)
	if err != nil {
		return frideStarted{}, err
	}
	views := i.views()
	tries := frideTries(ic.MemoryMB, size)
	pin := ""
	// stale is where it runs with agents that did not log in to Claude:
	// started again there once that node has the token, which logs them
	// in; else stopped there and started on a node that has it, unless
	// open threads keep it where it is.
	var stale *frideWhere
	if w, ok := locate(name, views); ok && w.running() && w.in.NoLogin != "" {
		stale = &w
		if hostReady(w.v, i.template(), i.lends(ic)) {
			pin = w.v.ID
		}
	} else if ok && w.running() {
		if size == "" || ic.MemoryMB > 0 || w.in.MemoryMB == tries[0] {
			i.touch(name)
			return frideStarted{Name: name, Node: w.v.Name, NodeID: w.v.ID, MemoryMB: w.in.MemoryMB, Already: true}, nil
		}
		if ts := open[name]; len(ts) > 0 {
			return frideStarted{}, apperr.New(apperr.Conflict, fmt.Sprintf("the FrIDE instance %s runs on %s with %d MB and has open threads (%s): it is made %s once they are closed", name, w.v.Name, w.in.MemoryMB, threadList(ts), size))
		}
		// Remade at the new size where it runs: its workspaces are there.
		pin = w.v.ID
	} else if ok && w.v.Online && w.in.State == nodeproto.FrideInstanceStarting {
		// Never a second one elsewhere while it comes up.
		pin = w.v.ID
	}
	for _, t := range open[name] {
		if t.Node != "" {
			// Its threads stay where they are.
			pin = t.Node
		}
	}
	hosts := i.hosts(name, ic, views, pin, open)
	i.tellNoLogin(noLoginOn(ic, views, i.lends(ic)))
	if len(hosts) == 0 {
		return frideStarted{}, i.fail(name, by, why, apperr.New(apperr.Unavailable, noHost(name, ic, views, pin, i.template(), i.lends(ic))), false)
	}
	spot, ok := placeFrideInstance(name, tries, hosts)
	if !ok {
		return frideStarted{}, i.fail(name, by, why, apperr.New(apperr.Unavailable, frideNoRoom(name, tries, hosts)), false)
	}
	node := hosts[slices.IndexFunc(hosts, func(h frideHost) bool { return h.ID == spot.Node })]
	out := frideStarted{Name: name, Node: node.Name, NodeID: node.ID, MemoryMB: spot.MemoryMB, Wanted: tries[0]}
	if stale != nil && stale.v.ID != node.ID {
		if err := i.stopOn(ctx, stale.v.ID, stale.v.Name, name, by, "its agents cannot log in to Claude there; it starts on "+node.Name); err != nil {
			return frideStarted{}, i.fail(name, by, why, fmt.Errorf("stopping it on %s, where its agents cannot log in to Claude: %w", stale.v.Name, err), true)
		}
	}
	for _, other := range spot.Stop {
		if err := i.stopOn(ctx, node.ID, node.Name, other, frideByRoom, "to make room for "+name); err != nil {
			return frideStarted{}, i.fail(name, by, why, fmt.Errorf("making room on %s: %w", node.Name, err), true)
		}
		out.Stopped = append(out.Stopped, other)
	}
	p, missing := i.params(ctx, name, ic, spot.MemoryMB)
	out.Missing = missing
	in, err := i.f.b.nodes.FrideInstanceStart(ctx, node.ID, p)
	if err == nil && in.State != nodeproto.FrideInstanceRunning {
		err = fmt.Errorf("it is %s: %s", orDefault(in.State, "not running"), orDefault(in.Problem, "the node gave no reason"))
	}
	if err != nil {
		return frideStarted{}, i.fail(name, by, why, fmt.Errorf("starting the FrIDE instance %s on %s: %w", name, node.Name, err), true)
	}
	if in.MemoryMB > 0 {
		out.MemoryMB = in.MemoryMB
	}
	i.st.Lock()
	i.active[name], i.sent[name] = time.Now(), startFingerprint(p)
	delete(i.failed, name)
	i.st.Unlock()
	detail := fmt.Sprintf("started on %s (%s) with %d MB", node.Name, node.ID, out.MemoryMB)
	if out.less() {
		detail += fmt.Sprintf(", less than the %d MB asked", out.Wanted)
	}
	if why != "" {
		detail += ": " + why
	}
	log.Printf("fride: the FrIDE instance %s %s", name, detail)
	i.f.b.audit.Append(audit.Entry{Kind: audit.FrideInstance, By: by, ID: name, Detail: detail, Outcome: "ok"})
	return out, nil
}

// fail records that a start of name did not happen (node: a node's start
// failed), and returns err. The queue's own tries that found no node are
// neither logged nor audited: it tries every few seconds.
func (i *frideInstances) fail(name, by, why string, err error, node bool) error {
	i.st.Lock()
	i.failed[name] = frideFailure{at: time.Now(), why: err.Error(), node: node}
	i.st.Unlock()
	if by == frideByQueue && !node {
		return err
	}
	detail := "start"
	if why != "" {
		detail += ": " + why
	}
	log.Printf("fride: the FrIDE instance %s did not start: %v", name, err)
	i.f.b.audit.Append(audit.Entry{Kind: audit.FrideInstance, By: by, ID: name, Detail: detail, Outcome: "failed: " + err.Error()})
	return err
}

// stopOn stops instance name on node id.
func (i *frideInstances) stopOn(ctx context.Context, id, nodeName, name, by, why string) error {
	_, err := i.f.b.nodes.FrideInstanceStop(ctx, id, name)
	outcome := "ok"
	if err != nil {
		outcome = "failed: " + err.Error()
	}
	detail := fmt.Sprintf("stopped on %s (%s)", nodeName, id)
	if why != "" {
		detail += ": " + why
	}
	i.f.b.audit.Append(audit.Entry{Kind: audit.FrideInstance, By: by, ID: name, Detail: detail, Outcome: outcome})
	if err != nil {
		log.Printf("fride: stopping the FrIDE instance %s on %s: %v", name, nodeName, err)
		return fmt.Errorf("stopping the FrIDE instance %s: %w", name, err)
	}
	log.Printf("fride: the FrIDE instance %s %s", name, detail)
	i.st.Lock()
	delete(i.sent, name)
	delete(i.active, name)
	i.st.Unlock()
	return nil
}

// stop stops instance name wherever it runs. One with open threads only
// with force. It returns what to say.
func (i *frideInstances) stop(ctx context.Context, name, by, why string, force bool) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	views := i.views()
	_, configured := i.configured()[name]
	var where []frideWhere
	for _, v := range views {
		if v.Device != nodeproto.DeviceApproved || !v.Online {
			continue
		}
		if in := v.Effective.FrideHost.Instance(name); in != nil && (in.State == nodeproto.FrideInstanceRunning || in.State == nodeproto.FrideInstanceStarting) {
			where = append(where, frideWhere{v: v, in: *in})
		}
	}
	if _, known := locate(name, views); !configured && !known {
		return "", i.errUnknown(name)
	}
	if len(where) == 0 {
		return fmt.Sprintf("The FrIDE instance %s does not run.", name), nil
	}
	open, err := i.openThreads(ctx)
	if err != nil {
		return "", err
	}
	if ts := open[name]; len(ts) > 0 && !force {
		return "", apperr.New(apperr.Conflict, fmt.Sprintf("the FrIDE instance %s has open threads (%s): close them first (fride_close), or stop it anyway, which interrupts their work", name, threadList(ts)))
	}
	var on []string
	for _, w := range where {
		if err := i.stopOn(ctx, w.v.ID, w.v.Name, name, by, why); err != nil {
			return "", err
		}
		on = append(on, w.v.Name)
	}
	return fmt.Sprintf("Stopped the FrIDE instance %s on %s; its disk stays, and it starts again when work needs it.", name, strings.Join(on, ", ")), nil
}

// params are what fride.instance.start gets for instance name at mb:
// its repositories and egress, and the identities its config names with
// their tokens, from the GitHub and the GitLab connection. missing are
// those the core does not hold.
func (i *frideInstances) params(ctx context.Context, name string, ic config.FrideInstanceConfig, mb int) (nodeproto.FrideInstanceStartParams, []string) {
	p := nodeproto.FrideInstanceStartParams{Name: name, MemoryMB: mb, Repos: slices.Clone(ic.Repos), Egress: slices.Clone(ic.Egress),
		Template: i.template()}
	if ic.CoreLogin {
		p.CoreToken = i.coreToken()
	}
	if len(ic.Identities) == 0 || i.f.b.gitIDs == nil {
		return p, slices.Clone(ic.Identities)
	}
	// GitHub may be asked for a new token: not for ever.
	bctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	have := map[string]nodeproto.GitIdentity{}
	for _, g := range i.f.b.gitIDs.build(bctx) {
		have[g.ID] = g
	}
	var missing []string
	for _, id := range ic.Identities {
		g, ok := have[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		p.Identities = append(p.Identities, nodeproto.FrideIdentity{ID: g.ID, Kind: g.Kind, Host: g.Host, Name: g.Name, Email: g.Email, Token: g.Token})
		if p.Git == nil && g.Name != "" && g.Email != "" {
			p.Git = &nodeproto.FrideGitUser{Name: g.Name, Email: g.Email}
		}
	}
	return p, missing
}

// startFingerprint tells apart what starts give a running instance:
// identities with their tokens, the core's Claude token, repositories,
// egress and git user, not its memory. It stays in memory.
func startFingerprint(p nodeproto.FrideInstanceStartParams) string {
	p.MemoryMB = 0
	data, _ := json.Marshal(p)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// refreshAll gives each running instance what changed since it was last
// given it (again: all of it), by starting it again at its memory, which
// a node takes as an update. Not in a lockdown.
func (i *frideInstances) refreshAll(ctx context.Context, again bool) {
	if i.f.b.locked() {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	views := i.views()
	for _, name := range slices.Sorted(maps.Keys(i.configured())) {
		w, ok := locate(name, views)
		if !ok || !w.running() || !hostReady(w.v, i.template(), i.lends(i.configured()[name])) {
			continue
		}
		p, _ := i.params(ctx, name, i.configured()[name], w.in.MemoryMB)
		fp := startFingerprint(p)
		i.st.Lock()
		same := i.sent[name] == fp
		i.st.Unlock()
		if same && !again && w.in.NoLogin == "" {
			// Its agents logged in; one that did not is given the
			// token now that its node has one.
			continue
		}
		// Only an update: an instance stopped since the declaration the
		// core holds (an idle stop a moment ago) stays stopped.
		p.UpdateOnly = true
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		got, err := i.f.b.nodes.FrideInstanceStart(cctx, w.v.ID, p)
		cancel()
		if err != nil {
			log.Printf("fride: giving the FrIDE instance %s on %s its identities, repositories and egress: %v", name, w.v.Name, err)
			continue
		}
		if got.State != nodeproto.FrideInstanceRunning {
			continue
		}
		i.st.Lock()
		i.sent[name] = fp
		i.st.Unlock()
		log.Printf("fride: the FrIDE instance %s on %s has its current identities, repositories and egress", name, w.v.Name)
	}
}

// idleStop stops each running instance that has had no open thread and no
// news for its idle_stop_minutes (one not in fride.instances any more,
// the default's), and starts counting for one it sees run first.
func (i *frideInstances) idleStop(ctx context.Context) {
	views := i.views()
	open, err := i.openThreads(ctx)
	if err != nil {
		log.Printf("fride: reading the FrIDE threads for the idle stop: %v", err)
		return
	}
	cfg := i.configured()
	now := time.Now()
	seen := map[string]bool{}
	for _, v := range views {
		if !v.Online || v.Effective.FrideHost == nil {
			continue
		}
		for _, in := range v.Effective.FrideHost.Instances {
			if in.State != nodeproto.FrideInstanceRunning || seen[in.Name] {
				continue
			}
			seen[in.Name] = true
			minutes := config.FrideIdleStopDefault
			if ic, ok := cfg[in.Name]; ok {
				minutes = ic.IdleStop()
			}
			if minutes == 0 {
				continue
			}
			if len(open[in.Name]) > 0 {
				i.touch(in.Name)
				continue
			}
			last := i.lastActive(in.Name)
			if last.IsZero() {
				i.touch(in.Name)
				continue
			}
			idle := time.Duration(minutes) * i.f.b.waits.frideIdleUnit
			if now.Sub(last) < idle {
				continue
			}
			why := fmt.Sprintf("idle for %d minutes", minutes)
			if _, err := i.stop(ctx, in.Name, frideByIdle, why, false); err != nil {
				log.Printf("fride: the idle stop of the FrIDE instance %s: %v", in.Name, err)
			}
		}
	}
}

// loop stops idle instances, gives running ones what changed, and kicks
// the queue when an instance came up, until the runtime stops.
func (i *frideInstances) loop() {
	idle := time.NewTicker(i.f.b.waits.frideIdleCheck)
	defer idle.Stop()
	refresh := time.NewTicker(frideInstanceRefreshEvery)
	defer refresh.Stop()
	ctx := i.f.ctx
	i.refreshAll(ctx, false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			i.idleStop(ctx)
		case <-refresh.C:
			i.refreshAll(ctx, false)
		case again := <-i.refresh:
			i.refreshAll(ctx, again)
		}
	}
}
