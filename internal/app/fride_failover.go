package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Failover (docs/NODES.md, "Failover"; #761): work on a node that has been
// offline longer than fride.failover_minutes, or an instance's work on a
// node that has not been able to run the instance that long, leaves it for
// another node that can take it, and resumes there from its pushed branch. What it left
// behind on the offline node is recorded (store.FrideOrphan) and closed,
// archived and removed there once that node answers again.

// frideCommitRule goes into every brief: a node can go away mid-task, and
// the work then resumes elsewhere from what the branch has on its remote.
const frideCommitRule = "Commit each small, self-contained step and push it to the task branch as you go, so that if this machine goes away the work can continue elsewhere from the pushed branch, losing minutes, not hours."

// frideBrief is what FrIDE gets for thread t: its message, a note to
// continue the branch an earlier node pushed when it has one, and the
// commit rule.
func frideBrief(t store.FrideThread) string {
	b := t.Message
	if t.Branch != "" {
		b += fmt.Sprintf("\n\n(This continues FrIDE #%d, whose earlier node went away. Continue on the existing branch %s in the same repositories, from its last pushed commit: read what is done there, then carry on; do not start over.)", t.ID, t.Branch)
	}
	return b + "\n\n" + frideCommitRule
}

// frideRef is the ref thread t's brief goes to FrIDE with: the same ref
// again returns the thread FrIDE started, so work that resumes after a
// failover gets one of its own.
func frideRef(t store.FrideThread) string {
	if t.Branch != "" {
		return fmt.Sprintf("fridolin-%d-resume-%d", t.ID, time.Now().UnixNano())
	}
	return "fridolin-" + strconv.FormatInt(t.ID, 10)
}

// failoverAfter is how long a node is offline before its work moves; 0
// for never.
func (f *frideRuntime) failoverAfter() time.Duration {
	return time.Duration(f.b.cfgs.Get().Live.Fride.Failover()) * f.b.waits.frideFailoverUnit
}

// offlineFor reports whether node id is offline, and for how long: since
// it was last heard from (the registry keeps that across restarts), or
// since this core started for a node it has never heard from. A node the
// registry no longer has is offline since the core started.
func (f *frideRuntime) offlineFor(id string) (time.Duration, bool) {
	if f.b.nodes.Online(id) {
		return 0, false
	}
	since := f.born
	if n, err := f.b.nodes.Registry().Get(id); err == nil && n.LastSeen != nil {
		since = *n.LastSeen
	}
	return time.Since(since), true
}

// frideStranding is why a thread's node cannot serve its work now: the
// node is offline, or, for an instance's work, it is online but cannot
// run the instance (its sandboxes are not ready, say), which keeps the
// instance's new work waiting as much as an offline node does.
type frideStranding struct {
	node, instance string
	offline        bool
	// why is what keeps the instance from running there, as "which …"
	// goes on (hostProblem).
	why string
	// down is how long it has been so: since the node was last heard
	// from, or since this core first saw the instance unable to run there.
	down time.Duration
}

// gone says why the work left, for a failover's notice.
func (s frideStranding) gone() string {
	if s.offline {
		return outboundmsg.FrideOffline(s.node, s.down)
	}
	return outboundmsg.FrideCannotRun(s.node, s.instance, s.down, s.why)
}

// now says what is wrong now, for a close while it is so.
func (s frideStranding) now() string {
	if s.offline {
		return s.node + " is offline"
	}
	return outboundmsg.FrideCannotRun(s.node, s.instance, 0, s.why)
}

// stranded reports whether thread t's node cannot serve it now, and why
// (frideStranding). It keeps when an instance was first seen unable to
// run on its node, which the failover counts from, and forgets it once
// the instance can run there again.
func (f *frideRuntime) stranded(t store.FrideThread) (frideStranding, bool) {
	if t.Node == "" {
		return frideStranding{}, false
	}
	s := frideStranding{node: f.nodeName(t.Node), instance: t.Instance}
	if down, off := f.offlineFor(t.Node); off {
		s.offline, s.down = true, down
		return s, true
	}
	if t.Instance == "" {
		return s, false
	}
	key := t.Node + "/" + t.Instance
	why := f.cannotRun(t.Node, t.Instance)
	f.stuckMu.Lock()
	defer f.stuckMu.Unlock()
	if why == "" {
		delete(f.stuck, key)
		return s, false
	}
	since, ok := f.stuck[key]
	if !ok {
		since = time.Now()
		f.stuck[key] = since
	}
	s.why, s.down = why, time.Since(since)
	return s, true
}

// cannotRun says why FrIDE instance name cannot run on node id, which is
// online, as "which …" goes on; "" when it runs or starts there, or the
// node can start it.
func (f *frideRuntime) cannotRun(id, name string) string {
	ic, ok := f.inst.configured()[name]
	if !ok {
		// No longer configured: nothing starts it anywhere.
		return ""
	}
	views := f.inst.views()
	k := slices.IndexFunc(views, func(v nodes.View) bool { return v.ID == id })
	if k < 0 {
		return "is not in the registry"
	}
	v := views[k]
	var in *nodeproto.FrideInstance
	if h := v.Effective.FrideHost; h != nil {
		in = h.Instance(name)
	}
	if in != nil && (in.State == nodeproto.FrideInstanceRunning || in.State == nodeproto.FrideInstanceStarting) {
		return ""
	}
	if !hostReady(v, f.inst.template(), f.inst.lends(ic)) {
		return hostProblem(v, f.inst.template(), f.inst.lends(ic))
	}
	if in != nil && in.State == nodeproto.FrideInstanceFailed {
		return "cannot start it: " + orDefault(in.Problem, "its last start there failed")
	}
	return ""
}

// failover moves the work of nodes that cannot serve it (offline, or
// unable to run its instance) longer than failoverAfter, and cleans up
// what earlier failovers left on nodes that are back. A core that just
// started first gives its nodes frideFailoverGrace to connect again. The
// queue loop runs it.
func (f *frideRuntime) failover() {
	if after := f.failoverAfter(); after > 0 {
		threads, err := f.b.store.FrideThreads(f.ctx, store.FrideSent)
		if err != nil {
			log.Printf("fride: reading the work on nodes: %v", err)
		}
		settled := time.Since(f.born) >= min(after, f.b.waits.frideFailoverGrace)
		for _, t := range threads {
			if f.ctx.Err() != nil {
				return
			}
			if s, ok := f.stranded(t); !ok || s.down < after || s.offline && !settled {
				continue
			}
			release, err := f.claim(t.ID, "it is moving off its node")
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(f.ctx, 25*time.Minute)
			text, err := f.failoverThread(ctx, t.ID, "", frideByQueue)
			cancel()
			release()
			switch {
			case err != nil:
				log.Printf("fride: moving FrIDE #%d off its node: %v", t.ID, err)
			case text != "":
				f.say(t, text)
			}
		}
	}
	f.cleanOrphans()
}

// failoverThread moves thread id off a node that cannot serve it
// (stranded): the thread is detached from it (what it had there is
// recorded as an orphan), and handed to another node (one that matches
// want, when given) or its instance elsewhere, with a brief that
// continues its branch. A thread no node takes now waits in the queue,
// which places it once one can. It returns what the chat is told; the
// caller holds the thread's claim.
func (f *frideRuntime) failoverThread(ctx context.Context, id int64, want, by string) (string, error) {
	unlock := f.lock(id)
	defer unlock()
	t, err := f.thread(ctx, id)
	if err != nil {
		return "", err
	}
	s, ok := f.stranded(t)
	if t.State != store.FrideSent || !ok {
		return "", nil
	}
	from := f.where(t.Node, t.Instance)
	if t.TaskID != "" || t.FrideThread != 0 {
		if err := f.b.store.AddFrideOrphan(ctx, store.FrideOrphan{Thread: t.ID, Node: t.Node, Instance: t.Instance, FrideThread: t.FrideThread, TaskID: t.TaskID}); err != nil {
			return "", err
		}
	}
	old := t.Node
	// Queued and on no node: the instance is no longer held on old, and
	// the queue takes the thread should the placement below fail.
	t.State, t.Node, t.FrideThread, t.TaskID, t.FrideStatus = store.FrideQueued, "", 0, "", ""
	if err := f.b.store.SaveFrideThread(ctx, t); err != nil {
		return "", err
	}
	log.Printf("fride: FrIDE #%d left %s (%s); it resumes from branch %q", id, from, s.gone(), t.Branch)
	var placed bool
	var why string
	if t.Instance != "" {
		var st frideStarted
		placed, why, st = f.placeOnInstance(ctx, &t, by, by == frideByQueue)
		if placed {
			f.instanceNotice(t, st)
		}
	} else {
		pick := t
		if want != "" {
			pick.WantNode = want
		}
		placed, why = f.place(ctx, &pick, old)
		t = pick
	}
	if !placed {
		return outboundmsg.FrideFailoverWaitsNotice(id, from, s.gone(), why, t.Branch), nil
	}
	return outboundmsg.FrideFailoverNotice(id, from, s.gone(), f.where(t.Node, t.Instance), t.FrideThread, t.Branch), nil
}

// detach closes thread t here while its node cannot serve it (s): what it
// has there is recorded as an orphan, closed and archived once it can be
// reached again. The caller holds the thread's claim.
func (f *frideRuntime) detach(ctx context.Context, t store.FrideThread, s frideStranding) (string, error) {
	unlock := f.lock(t.ID)
	defer unlock()
	cur, err := f.thread(ctx, t.ID)
	if err != nil {
		return "", err
	}
	from := f.where(cur.Node, cur.Instance)
	if cur.TaskID != "" || cur.FrideThread != 0 {
		if err := f.b.store.AddFrideOrphan(ctx, store.FrideOrphan{Thread: cur.ID, Node: cur.Node, Instance: cur.Instance, FrideThread: cur.FrideThread, TaskID: cur.TaskID}); err != nil {
			return "", err
		}
	}
	cur.State = store.FrideClosed
	if err := f.b.store.SaveFrideThread(ctx, cur); err != nil {
		return "", err
	}
	text := fmt.Sprintf("FrIDE #%d is closed here while %s.", cur.ID, s.now())
	if cur.Branch != "" {
		text += fmt.Sprintf(" Branch %s keeps what was pushed.", cur.Branch)
	}
	if cur.TaskID != "" || cur.FrideThread != 0 {
		text += fmt.Sprintf(" What it left on %s is closed, archived into the vault and removed there once it can be reached again.", from)
	}
	return text, nil
}

// cleanOrphans closes, archives and removes the work failovers left on
// nodes that answer again. One that does not answer yet stays recorded.
func (f *frideRuntime) cleanOrphans() {
	orphans, err := f.b.store.FrideOrphans(f.ctx)
	if err != nil {
		log.Printf("fride: reading the work left on offline nodes: %v", err)
		return
	}
	for _, o := range orphans {
		if f.ctx.Err() != nil {
			return
		}
		if f.unreachableOn(o.Node, o.Instance) != "" {
			continue
		}
		ctx, cancel := context.WithTimeout(f.ctx, 10*time.Minute)
		done, err := f.cleanOrphan(ctx, o)
		cancel()
		if err != nil {
			log.Printf("fride: cleaning up what FrIDE #%d left on %s: %v", o.Thread, f.where(o.Node, o.Instance), err)
		}
		if done {
			if err := f.b.store.DeleteFrideOrphan(f.ctx, o.ID); err != nil {
				log.Printf("fride: forgetting what FrIDE #%d left on %s: %v", o.Thread, f.where(o.Node, o.Instance), err)
			}
		}
	}
}

// cleanOrphan closes what o left on its node, archives it into the vault
// (workspaces/<task>.fride.tar.gz) and removes it there. It reports
// whether o is done with: a FrIDE thread that never became a task and no
// longer can is; one still in FrIDE's queue is tried again later.
func (f *frideRuntime) cleanOrphan(ctx context.Context, o store.FrideOrphan) (bool, error) {
	where := f.where(o.Node, o.Instance)
	task := o.TaskID
	if task == "" {
		st, err := f.b.nodes.FrideStatusOn(ctx, o.Node, o.Instance, o.FrideThread)
		if pe, ok := errors.AsType[*nodeproto.Error](err); ok && (pe.Code == nodeproto.CodeNotFound || strings.Contains(pe.Message, "404")) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		task = st.Thread.TaskID
		if task == "" {
			return droppable(st.Thread.Status) && st.Thread.Status != "queued", nil
		}
	}
	if _, err := f.b.nodes.FrideCloseOn(ctx, o.Node, o.Instance, task); err != nil {
		return false, fmt.Errorf("closing task %s: %w", task, err)
	}
	dir, err := f.vaultWorkspaces()
	if err != nil {
		return false, err
	}
	path, size, err := f.fetch(ctx, o.Node, o.Instance, task, false, dir, task+".fride.tar.gz")
	if err != nil {
		return false, err
	}
	f.b.vault.track(ctx, path)
	if err := f.b.nodes.FrideDeleteOn(ctx, o.Node, o.Instance, task, true); err != nil {
		return false, fmt.Errorf("task %s is archived as %s, but removing it failed: %w", task, f.vaultRel(path), err)
	}
	log.Printf("fride: what FrIDE #%d left on %s, task %s, is archived in the vault as %s (%d bytes) and removed there", o.Thread, where, task, f.vaultRel(path), size)
	return true, nil
}
