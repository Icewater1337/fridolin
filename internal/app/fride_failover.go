package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Failover (docs/NODES.md, "Failover"; #761): work on a node that has been
// offline longer than fride.failover_minutes leaves it for another node
// that can take it, and resumes there from its pushed branch. What it left
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
// it was last seen, or since this core started when that is later (a core
// that was down itself has not seen its nodes come back yet). A node the
// registry no longer has is offline since the core started.
func (f *frideRuntime) offlineFor(id string) (time.Duration, bool) {
	if f.b.nodes.Online(id) {
		return 0, false
	}
	since := f.born
	if n, err := f.b.nodes.Registry().Get(id); err == nil && n.LastSeen != nil && n.LastSeen.After(since) {
		since = *n.LastSeen
	}
	return time.Since(since), true
}

// failover moves the work of nodes offline longer than failoverAfter, and
// cleans up what earlier failovers left on nodes that are back. The queue
// loop runs it.
func (f *frideRuntime) failover() {
	if after := f.failoverAfter(); after > 0 {
		threads, err := f.b.store.FrideThreads(f.ctx, store.FrideSent)
		if err != nil {
			log.Printf("fride: reading the work on nodes: %v", err)
		}
		for _, t := range threads {
			if f.ctx.Err() != nil {
				return
			}
			if down, off := f.offlineFor(t.Node); t.Node == "" || !off || down < after {
				continue
			}
			release, err := f.claim(t.ID, "it is moving off its offline node")
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(f.ctx, 25*time.Minute)
			text, err := f.failoverThread(ctx, t.ID, "", frideByQueue)
			cancel()
			release()
			switch {
			case err != nil:
				log.Printf("fride: moving FrIDE #%d off its offline node: %v", t.ID, err)
			case text != "":
				f.say(t, text)
			}
		}
	}
	f.cleanOrphans()
}

// failoverThread moves thread id off its offline node: the thread is
// detached from it (what it had there is recorded as an orphan), and
// handed to another node (one that matches want, when given) or its
// instance elsewhere, with a brief that continues its branch. A thread no
// node takes now waits in the queue, which places it once one can. It
// returns what the chat is told; the caller holds the thread's claim.
func (f *frideRuntime) failoverThread(ctx context.Context, id int64, want, by string) (string, error) {
	unlock := f.lock(id)
	defer unlock()
	t, err := f.thread(ctx, id)
	if err != nil {
		return "", err
	}
	down, off := f.offlineFor(t.Node)
	if t.State != store.FrideSent || t.Node == "" || !off {
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
	log.Printf("fride: FrIDE #%d left %s, offline for %s; it resumes from branch %q", id, from, down.Round(time.Second), t.Branch)
	var placed bool
	var why string
	if t.Instance != "" {
		var s frideStarted
		placed, why, s = f.placeOnInstance(ctx, &t, by, by == frideByQueue)
		if placed {
			f.instanceNotice(t, s)
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
		return outboundmsg.FrideFailoverWaitsNotice(id, from, down, why, t.Branch), nil
	}
	return outboundmsg.FrideFailoverNotice(id, from, down, f.where(t.Node, t.Instance), t.FrideThread, t.Branch), nil
}

// detach closes thread t here while its node is offline: what it has
// there is recorded as an orphan, closed and archived once the node is
// back. The caller holds the thread's claim.
func (f *frideRuntime) detach(ctx context.Context, t store.FrideThread) (string, error) {
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
	text := fmt.Sprintf("FrIDE #%d is closed here while %s is offline.", cur.ID, from)
	if cur.Branch != "" {
		text += fmt.Sprintf(" Branch %s keeps what was pushed.", cur.Branch)
	}
	if cur.TaskID != "" || cur.FrideThread != 0 {
		text += fmt.Sprintf(" What it left on %s is closed, archived into the vault and removed there once it is back.", from)
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
