package brain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Icewater1337/fridolin/internal/route"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/platform"

	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// ── Brain ────────────────────────────────────────────────────────────────

// Brain is one isolated Claude Code context: its own folder, conversation
// history and prompt file.
//
// A brain does not keep a Claude process alive between messages. Each message
// spawns a fresh `claude -p` run seeded with the recent chat history. Messages
// for a brain run one at a time from its queue (or, in interrupt mode, a newer
// request from the same person in the same chat stops the run in flight),
// each chat's in order and the chats in turn, so that one busy chat does not
// keep the others waiting behind all of its messages.
type Brain struct {
	// Fixed for the brain's lifetime. Changing a brain's folder replaces the
	// Brain; everything else is in settings and changes live.
	id      string
	workDir string
	// root is the brain's folder, opened once when the Brain is made and
	// closed when it is retired. The bridge reads and writes its files in
	// the folder through it (see openInFolder), and through this handle
	// rather than the path, which could lead elsewhere by then.
	root *os.Root

	settings   atomic.Pointer[Settings]
	runner     Runner
	input      InputRules
	claimMedia func(mediaDir, brainID string, paths []string) map[string]string
	sink       *brainSinkRef
	// slots are the run slots all brains share (max_concurrent_runs); nil
	// for no limit.
	slots *runSlots

	// historyMu serialises the bridge's writes to the history file: an
	// append must not land in a copy that a rewrite then drops (see
	// rewriteHistory).
	historyMu sync.Mutex

	mu      sync.Mutex
	current *brainRun
	// pending holds what waits, in the order it came in. Each chat's
	// messages in it are that chat's lane, which runs in order; which
	// lane goes next is nextIn's choice.
	pending []Inbound
	// turns is, by chat, when its last run started, counted by turn: the
	// chat whose turn is longest ago goes next. Chats with nothing waiting
	// are forgotten once that no longer decides anything (takeTurnLocked).
	turns   map[string]uint64
	turn    uint64
	working bool // a worker goroutine is draining pending
	// workers counts the worker goroutines until they have returned,
	// their last delivery done: a stop waits for them (shutdown).
	workers sync.WaitGroup
	retired bool // removed from the configuration, or the bridge stops
	last    *RunInfo
	// slotWait is set while the worker waits for a run slot.
	slotWait *slotWait

	// tmuxViewer is an optional Terminal window tailing this brain's
	// activity log. A run starts it in the background and a reload can close
	// it, so viewerMu guards it, with viewerStarting, set while a start is
	// under way, and viewerGen, which closeViewer moves on so that a start
	// that ends after it knows to close what it opened.
	viewerMu       sync.Mutex
	viewerStart    func(name, streamLog, cwd string, passthrough []string) (*platform.Viewer, error)
	tmuxViewer     *platform.Viewer
	viewerStarting bool
	viewerGen      int
}

// brainSettings is everything about a brain that can change while it runs.
// Each run takes one snapshot, so an edit never changes a run half-way.
type Settings struct {
	Name            string
	Manifest        manifest.Manifest
	ClaudePath      string
	Provider        string
	CodexPath       string
	Model           string
	Effort          string
	DisallowedTools []string
	PromptFile      string // relative to the folder; see BrainSpec
	Dangerously     bool
	InheritUser     bool // inherit_claude_user_config
	OTPGuard        bool
	OTPMarkers      []string
	Viewer          bool
	RunTimeout      time.Duration // 0 for none
	QueueMode       string
	MaxPending      int
	Observe         string // BrainManifest.Observe
	CredsDir        string // where per-brain credential files live
	MediaDir        string // the media folder, which holds the brain's inbox
	EnvPassthrough  []string
	ToolsDir        string // the bridge's Python helpers, or ""
	DataDir         string // the data dir, absolute
	DBPath          string // the bridge's DB, absolute
	PlaudPATH       string // the PATH to run with when brains may use Plaud's CLI
	Vault           bool   // the vault runs: the prompt explains .cold.json and the vault tools
	// ownPATH are the folders of the bridge's PATH inside this brain's
	// folder, which setBridgePATH took out of the bridge's: they go first
	// on the PATH of this brain's runs, and of no one else's.
	OwnPATH []string
}

// RunInfo summarises the last finished run, for the dashboard.
type RunInfo struct {
	Chat     string    `json:"chat"`
	Finished time.Time `json:"finished"`
	Seconds  float64   `json:"seconds"`
	Outcome  string    `json:"outcome"` // one of the outcome constants
	Error    string    `json:"error,omitempty"`
}

// The outcomes of a run, RunInfo.Outcome. The dashboard labels each one
// (outcomePill in web/assets/components/brain.js).
const (
	outcomeOK          = "ok"
	outcomeError       = "error"
	outcomeInterrupted = "interrupted"
	outcomeQuestion    = "question" // it asked a question and is waiting
)

type brainRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	// processMu serialises starting a CLI with the second signal's stop.
	// Once forceStopped is set, no process can start for this run.
	processMu    sync.Mutex
	process      interface{ Stopped() <-chan struct{} }
	forceStopped bool
	// pid is the CLI's while it runs, and 0 once Wait has reaped it: for
	// the dashboard, which shows it. Stopping a run takes no PID (see
	// interruptRun).
	pid     int
	msg     Inbound
	started time.Time
}

var (
	ErrDropped   = errors.New("dropped before it ran")
	ErrRetired   = errors.New("brain was removed")
	ErrQueueFull = errors.New("brain queue is full")

	// errSandboxUnavailable is what the chat sees. The cause goes to the log
	// only, because it can name private paths.
	ErrSandboxUnavailable = errors.New("the sandbox could not be set up, so this message was not run (details in the bridge log)")
)

// ID is the brain's stable identifier.
func (br *Brain) ID() string { return br.id }

// Name is the brain's display name.
func (br *Brain) Name() string { return br.settings.Load().Name }

// Dir is the brain's folder.
func (br *Brain) Dir() string { return br.workDir }

// Manifest returns the immutable manifest snapshot used by current runs.
// Callers must not mutate its slices or optional nested settings.
func (br *Brain) Manifest() manifest.Manifest { return br.settings.Load().Manifest }

// logInbound writes m to the brain's history: a line for each part of a
// merged message, with its ID, so that one deleted later is taken out
// alone, and with the time it was sent, not the time the debouncer let it
// through. It returns m with each part's line kept on it
// (Inbound.logged), so that its own run shows the message once, as the
// new message, after the chat's history before those lines.
func (br *Brain) logInbound(m Inbound) Inbound {
	// A message taken up again, by the brain in its brain's new folder
	// say, has its lines in that history already, where they stay:
	// written again, it would be there twice. A brain whose history does
	// not have them, one the message was routed to anew, writes its own.
	if br.holdsLines(m) {
		return m
	}
	parts := m.ownParts()
	for i, p := range parts {
		at := p.At
		if at.IsZero() {
			at = m.At
		}
		parts[i].logged = br.AppendEntry(history.Entry{TS: historyTime(at), Chat: m.JID, From: p.Sender, Text: chat.HistoryText(p.TextWithQuote(), p.Media), Type: history.In, ID: p.ID})
	}
	if len(m.parts) > 0 {
		m.parts = parts
	}
	m.logged = parts[0].logged
	return m
}

// holdsLines reports whether the brain's history has the lines a brain
// wrote for m (Inbound.logged), the first of them at least: it has when
// it wrote them, or when its folder is the one of the brain that did.
func (br *Brain) holdsLines(m Inbound) bool {
	if m.logged == (history.Entry{}) {
		return false
	}
	return m.loggedIn(history.ForChat(br.root, br.id, br.input.Chats(m.Inbound), promptHistoryBudget, m.enoughBefore(1))) >= 0
}

// historyTime is t as a history line has it, "" for none: the time the
// line is written.
func historyTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// Submit hands a message to the brain: it goes into the history and, unless
// it is untagged chatter that gets no run (see observeRuns), into the
// queue. It returns the message's position in the queue (1 = next, 0 for
// none) and whether it has to wait for other work: the brain's, or other
// brains' runs that take every run slot. A run it interrupts does not
// count.
func (br *Brain) Submit(m Inbound) (pos int, busy bool, err error) {
	return br.submit(&m)
}

// submit is Submit, and leaves in m (its Media, and its parts') where the
// message's files are now. A brain removed while it takes the message
// (errBrainRetired after the history) has moved them into its inbox by
// then, and BrainManager.Submit hands m on to the next brain with those
// paths, whose claimMedia moves the files on into its own inbox. With the
// paths they had before, the next brain got files that were no longer
// there, and they stayed in the removed brain's inbox.
func (br *Brain) submit(m *Inbound) (pos int, busy bool, err error) {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	s := br.settings.Load()
	// Untagged chatter needs nothing more than the history, unless the
	// brain observes it with runs: the next run in the chat reads it there.
	queues := !m.Silent || br.input.Observe(s.Observe, m.Inbound)

	// Turned away before anything is written: a message the brain does not
	// take is not in its history, where the sender's retry after "queue is
	// full", or the brain it is routed to instead of a retired one, would
	// put it a second time.
	br.mu.Lock()
	if br.retired {
		br.mu.Unlock()
		return 0, false, ErrRetired
	}
	if queues && !br.hasRoomLocked(*m, s) {
		if !m.Silent {
			br.mu.Unlock()
			return 0, true, ErrQueueFull
		}
		// Chatter that finds no place is kept in the history all the
		// same, without a run of its own: it was said in the chat, and
		// the next run there reads it.
		queues = false
	}
	br.mu.Unlock()

	// The files go into this brain's inbox, the only part of the media
	// folder its sandbox opens, before the history names where they are.
	*m = m.withMediaMoved(br.claimMedia(s.MediaDir, br.id, m.Media))

	// History first, so that later runs see the message even when it is
	// merged into a queue that is already moving.
	*m = br.logInbound(*m)
	if !queues {
		return 0, false, nil
	}

	br.mu.Lock()
	if br.retired {
		br.mu.Unlock()
		return 0, false, ErrRetired
	}

	// A newer message from the same chat supersedes queued untagged chatter:
	// it is already in the history the next run reads. In interrupt mode, a
	// tagged message also replaces its sender's own earlier request in that
	// chat, waiting or running. No other request is touched: other chats,
	// and other people's requests in a group, keep their place, and untagged
	// chatter never interrupts a run. Nor do ingested files interrupt a
	// request, or get replaced: each is a job of its own, not a correction of
	// the last one. Observe-only runs are another matter, see below.
	next, gone, interrupting, err := admit(br.pending, *m, s.QueueMode, s.MaxPending)
	br.pending = next
	defer Dropped(gone)
	if err != nil {
		br.mu.Unlock()
		return 0, true, err
	}
	_, replaces := supersedes(*m, s.QueueMode)
	// Work addressed to the brain does not wait for an observe-only run: it
	// stops the one in flight and goes ahead of those waiting. Their chatter
	// is in the history, where the next run in its chat reads it.
	var interrupt *brainRun
	if cur := br.current; cur != nil && (interrupting && replaces(cur.msg) || !m.Silent && cur.msg.Silent) {
		interrupt = cur
	}
	pos = slices.Index(br.queueOrderLocked(), len(br.pending)-1) + 1
	busy = pos > 1 || (br.current != nil && interrupt == nil) || br.slots.busy(!m.Silent)
	start := !br.working
	br.working = true
	if start {
		br.workers.Add(1)
	}
	// A worker that waits for a slot for chatter now waits for one for a
	// request, which goes first.
	var stopOther func()
	if br.slotWait != nil && !m.Silent {
		stopOther = br.slots.upgrade(br.slotWait)
	}
	br.mu.Unlock()

	if interrupt != nil {
		br.interruptRun(interrupt)
	}
	if stopOther != nil {
		stopOther()
	}
	if start {
		go br.worker()
	}
	return pos, busy, nil
}

// nextIn returns where in pending, waiting messages in the order they came
// in, the one to run next is. Work addressed to the brain goes before
// untagged chatter, then the chat whose last turn (see Brain.turns) is
// longest ago, one that never had one first, and among chats alike the one
// whose message came in first. A chat's own messages run in the order they
// came in: they share a turn, and the earliest of them wins the tie.
func nextIn(pending []Inbound, turns map[string]uint64) int {
	best := -1
	for i, p := range pending {
		if best < 0 {
			best = i
			continue
		}
		b := pending[best]
		if p.Silent != b.Silent {
			if !p.Silent {
				best = i
			}
			continue
		}
		if turns[p.JID] < turns[b.JID] {
			best = i
		}
	}
	return best
}

// takeTurnLocked records that a run for chat starts now. A chat with
// nothing waiting whose turn came before every waiting chat's is
// forgotten: it would go before all of them anyway, as one that never had
// a turn does, so the record is no longer needed. br.mu is held.
func (br *Brain) takeTurnLocked(chatJID string) {
	if br.turns == nil {
		br.turns = map[string]uint64{}
	}
	br.turn++
	br.turns[chatJID] = br.turn
	low := br.turn
	waiting := map[string]bool{}
	for _, p := range br.pending {
		waiting[p.JID] = true
		low = min(low, br.turns[p.JID])
	}
	for c, t := range br.turns {
		if !waiting[c] && t < low {
			delete(br.turns, c)
		}
	}
}

// queueOrderLocked returns the indexes of pending in the order the
// messages will run as things stand (see nextIn). Messages that come
// later can still go before some of them, in a chat whose turn comes
// first. br.mu is held.
func (br *Brain) queueOrderLocked() []int {
	idx := make([]int, len(br.pending))
	for i := range idx {
		idx[i] = i
	}
	rest := slices.Clone(br.pending)
	turns := maps.Clone(br.turns)
	if turns == nil {
		turns = map[string]uint64{}
	}
	turn := br.turn
	order := make([]int, 0, len(rest))
	for len(rest) > 0 {
		i := nextIn(rest, turns)
		order = append(order, idx[i])
		turn++
		turns[rest[i].JID] = turn
		rest = slices.Delete(rest, i, i+1)
		idx = slices.Delete(idx, i, i+1)
	}
	return order
}

// supersedes says what m replaces when it joins the queue (see Submit):
// whether it interrupts its sender's own run, which only a request does in
// interrupt mode, and which waiting or running messages it replaces.
func supersedes(m Inbound, mode string) (interrupting bool, replaces func(Inbound) bool) {
	interrupting = mode == QueueInterrupt && !m.Silent && !m.HandedIn()
	return interrupting, func(p Inbound) bool {
		return p.JID == m.JID && (p.Silent || (interrupting && p.Sender == m.Sender && !p.HandedIn()))
	}
}

// hasRoomLocked reports whether the queue takes m: it has a free place,
// or one m makes by what it replaces, or untagged chatter to drop. br.mu is
// held.
func (br *Brain) hasRoomLocked(m Inbound, s *Settings) bool {
	_, replaces := supersedes(m, s.QueueMode)
	n, silent := 0, false
	for _, p := range br.pending {
		if !replaces(p) {
			n++
			silent = silent || p.Silent
		}
	}
	return n < s.MaxPending || silent
}

// worker drains the queue one message at a time, each in a run slot (see
// runSlots).
func (br *Brain) worker() {
	defer br.workers.Done()
	for {
		br.mu.Lock()
		if br.retired || len(br.pending) == 0 {
			br.working = false
			br.mu.Unlock()
			return
		}
		slot, ok := br.waitForSlotLocked()
		if !ok || br.retired || len(br.pending) == 0 {
			// Retired, or what waited was dropped meanwhile.
			br.mu.Unlock()
			if ok {
				slot.release()
			}
			continue
		}
		i := nextIn(br.pending, br.turns)
		m := br.pending[i]
		br.pending = slices.Delete(br.pending, i, i+1)
		if slot != nil && !slot.running(!m.Silent) {
			// A request in another brain took this slot's place before
			// the run began: the chatter is in the history, as for an
			// observe-only run it stops.
			br.mu.Unlock()
			slot.release()
			Dropped([]Inbound{m})
			continue
		}
		br.takeTurnLocked(m.JID)
		s := br.settings.Load()
		ctx, cancel := runContext(s.RunTimeout)
		run := &brainRun{cancel: cancel, done: make(chan struct{}), msg: m, started: time.Now()}
		br.current = run
		br.mu.Unlock()

		if m.Silent {
			log.Printf("brain %s: running silently for untagged message from %s", br.id, m.Sender)
		}
		res := br.invokeGuarded(run, ctx, s, m)
		res.Duration = time.Since(run.started)
		// The answer goes into the history once it was delivered (see
		// Outbox), not here: the next run must not read as said what
		// never reached the chat.
		br.finishRun(run)
		if slot != nil {
			slot.release()
		}
		br.recordLast(m, res)
		br.deliverGuarded(m, res)
	}
}

// waitForSlotLocked waits for a run slot for what runs next, with br.mu
// held on entry and again on return, though not while it waits: messages
// keep coming in, and a request among them makes the wait one for a
// request (see Submit). It returns a nil slot without a limit, and false
// when the brain was retired while it waited.
func (br *Brain) waitForSlotLocked() (*runSlot, bool) {
	if br.slots == nil {
		return nil, true
	}
	request := !br.pending[nextIn(br.pending, br.turns)].Silent
	w, stopOther := br.slots.wait(request, br.stopObservation)
	br.slotWait = w
	br.mu.Unlock()
	if stopOther != nil {
		stopOther()
	}
	select {
	case <-w.ready:
	default:
		log.Printf("brain %s: waiting for a run slot: other brains' runs take all of max_concurrent_runs", br.id)
		<-w.ready
	}
	slot, ok := br.slots.take(w)
	br.mu.Lock()
	br.slotWait = nil
	return slot, ok
}

// stopObservation stops the brain's observe-only run, for a request in
// another brain that waits for its run slot.
func (br *Brain) stopObservation() {
	br.mu.Lock()
	cur := br.current
	br.mu.Unlock()
	if cur != nil && cur.msg.Silent {
		log.Printf("brain %s: stopping the observe-only run in %s: a request waits for its run slot", br.id, cur.msg.JID)
		br.interruptRun(cur)
	}
}

// invokeGuarded is invoke with a panic in it turned into the run's error,
// so that the run is finished and recorded like any failed one and the
// worker goes on with the next message. Unrecovered, the panic would end
// the whole bridge and leave every brain's running claude behind.
func (br *Brain) invokeGuarded(run *brainRun, ctx context.Context, s *Settings, m Inbound) (res claudecli.Result) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("brain %s: the run for %s panicked: %v\n%s", br.id, m.JID, p, debug.Stack())
			res = claudecli.Result{Err: claudecli.ErrPanicked}
		}
	}()
	return br.invoke(run, ctx, s, m)
}

// deliverGuarded hands a finished run to the bridge and then to whoever
// waits for the message, each with a panic logged rather than ending the
// worker, and the one waiting told even when the delivery panicked.
func (br *Brain) deliverGuarded(m Inbound, res claudecli.Result) {
	guard := func(what string, fn func()) {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("brain %s: %s the run for %s panicked: %v\n%s", br.id, what, m.JID, p, debug.Stack())
			}
		}()
		fn()
	}
	guard("delivering", func() { br.sink.Finished(br, m, res) })
	if m.Done != nil {
		guard("finishing", func() { m.Done(res.Err) })
	}
}

// runContext is a run's context, which a stop cancels and which ends after
// timeout too, unless that is 0.
func runContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(context.Background(), timeout)
	}
	return context.WithCancel(context.Background())
}

// dropped tells the messages in ms that will not run, outside br.mu.
func Dropped(ms []Inbound) {
	for _, m := range ms {
		if m.Done != nil {
			m.Done(ErrDropped)
		}
	}
}

// WaitIdle waits until the brain has nothing running or queued, for at
// most max, and reports whether it got there. A backup asks before it
// copies the brain's folder (see backupBrain). Between two queued runs no
// run is current for a moment, so it waits for the worker to stop.
func (br *Brain) WaitIdle(ctx context.Context, max time.Duration) bool {
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	for {
		br.mu.Lock()
		run, working := br.current, br.working
		br.mu.Unlock()
		if !working {
			return true
		}
		var done <-chan struct{}
		var next <-chan time.Time
		if run != nil {
			done = run.done
		} else {
			next = time.After(50 * time.Millisecond)
		}
		select {
		case <-done:
		case <-next:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (br *Brain) recordLast(m Inbound, res claudecli.Result) {
	info := &RunInfo{Chat: m.JID, Finished: time.Now(), Seconds: res.Duration.Seconds(), Outcome: outcomeOK}
	switch {
	case errors.Is(res.Err, claudecli.ErrInterrupted):
		info.Outcome = outcomeInterrupted
	case res.Err != nil:
		info.Outcome, info.Error = outcomeError, res.Err.Error()
	case res.Asked:
		info.Outcome = outcomeQuestion
	}
	br.mu.Lock()
	br.last = info
	br.mu.Unlock()
}

// StopCurrent kills the run in flight, whichever chat it serves, and, with
// clearQueue, drops everything waiting. It is for the dashboard and shutdown;
// a chat's own !stop uses StopChat. It reports whether a run was stopped.
func (br *Brain) StopCurrent(clearQueue bool) bool {
	br.mu.Lock()
	var gone []Inbound
	if clearQueue {
		gone, br.pending = br.pending, nil
	}
	cur := br.current
	br.mu.Unlock()
	Dropped(gone)
	return br.stopRun(cur)
}

// StopChat stops what the brain does for one chat: the run in flight if it
// is that chat's, and the chat's waiting messages. One brain serves several
// chats, so nothing another chat asked for is touched. It reports whether a
// run was stopped and how many waiting messages were dropped.
func (br *Brain) StopChat(jid string) (stopped bool, n int) {
	br.mu.Lock()
	var gone []Inbound
	kept := br.pending[:0]
	for _, p := range br.pending {
		if p.JID == jid {
			gone = append(gone, p)
			continue
		}
		kept = append(kept, p)
	}
	br.pending = kept
	cur := br.current
	if cur != nil && cur.msg.JID != jid {
		cur = nil
	}
	br.mu.Unlock()
	Dropped(gone)
	return br.stopRun(cur), len(gone)
}

// ResetChat starts the chat that goes by chats (see chatJIDs) afresh for
// the brain: a reset marker in its history under each of the chat's JIDs,
// which the next run's prompt stops reading the chat's messages at. What
// the chat had running or waiting is for the caller to stop (StopChat);
// the history itself keeps every line, for `fridolin history`. by is who
// sent !reset.
func (br *Brain) ResetChat(chats []string, by string) {
	for _, jid := range chats {
		br.AppendEntry(history.Entry{Chat: jid, From: by, Type: history.Reset})
	}
}

// StopHandOffs stops the messages handed to this brain (/ingest, Plaud) that
// answer in jid: the run in flight if it is one, and those waiting. The rest
// of the brain's work goes on: jid may well be another brain's chat. It
// reports whether a run was stopped and how many waiting messages were
// dropped.
func (br *Brain) StopHandOffs(jid string) (stopped bool, n int) {
	handOff := func(m Inbound) bool { return m.Brain != "" && m.JID == jid }
	br.mu.Lock()
	var gone []Inbound
	kept := br.pending[:0]
	for _, p := range br.pending {
		if handOff(p) {
			gone = append(gone, p)
			continue
		}
		kept = append(kept, p)
	}
	br.pending = kept
	cur := br.current
	if cur != nil && !handOff(cur.msg) {
		cur = nil
	}
	br.mu.Unlock()
	Dropped(gone)
	return br.stopRun(cur), len(gone)
}

// stopRunWait is how long a stop waits for a run it killed to end.
const StopRunWait = 10 * time.Second

// stopRun kills run, if there is one, and waits for it to end.
func (br *Brain) stopRun(run *brainRun) bool {
	if run == nil {
		return false
	}
	br.interruptRun(run)
	select {
	case <-run.done:
	case <-time.After(StopRunWait):
		log.Printf("brain %s: run did not stop within %v", br.id, StopRunWait)
	}
	return true
}

// shutdown stops the brain for good as the bridge stops. It takes no new
// message and starts no new run, and hands what was waiting, which never
// ran, to the Waiting hook at once. The run in flight gets grace to finish
// and be delivered, and is interrupted after that, which the Interrupted
// hook hears of. shutdown returns once the worker has ended, its last
// delivery done. A second signal can abandon a stuck shutdown. The activity viewer
// stays, for the next start to replace.
func (br *Brain) shutdown(grace time.Duration) {
	waiting := br.closeQueue()
	br.sink.Waiting(br, waiting)
	idle := make(chan struct{})
	go func() {
		br.workers.Wait()
		close(idle)
	}()
	graceOver := time.NewTimer(grace)
	defer graceOver.Stop()
	var cut *brainRun
	select {
	case <-idle:
	case <-graceOver.C:
		br.mu.Lock()
		cur := br.current
		br.mu.Unlock()
		if cur != nil {
			log.Printf("brain %s: the run for %s is not done %v into the stop; stopping it", br.id, cur.msg.JID, grace)
			br.interruptRun(cur)
		}
		select {
		case <-idle:
		case <-time.After(StopRunWait):
			log.Printf("brain %s: worker still finishing %v after the interrupt; waiting for its delivery", br.id, StopRunWait)
			// Closing the store now would lose an answer the worker is
			// still handing to the outbox. The grace bounds the run, not
			// the hand-off of its result.
			<-idle
		}
		// The run may have answered as it was stopped, and then it was
		// delivered like any other.
		br.mu.Lock()
		if cur != nil && br.last != nil && br.last.Outcome == outcomeInterrupted {
			cut = cur
		}
		br.mu.Unlock()
	}
	if cut != nil {
		br.sink.Interrupted(br, cut.msg)
	}
	// Done with the folder after every worker has released it.
	br.root.Close()
}

// closeQueue stops the brain taking messages and starting runs, and
// returns what it had waiting, which never ran. A worker that waits for a
// run slot gives up its place.
func (br *Brain) closeQueue() []Inbound {
	br.mu.Lock()
	defer br.mu.Unlock()
	br.freezeQueueLocked()
	gone := br.pending
	br.pending = nil
	return gone
}

// freezeQueueLocked stops admission and further turns, retaining pending
// messages for shutdown persistence. br.mu is held.
func (br *Brain) freezeQueueLocked() {
	br.retired = true
	if br.slotWait != nil {
		br.slots.giveUp(br.slotWait)
	}
}

// retire stops the brain for good after a reload took it out, removed
// from the configuration or moved to another folder: it takes nothing
// more, drops what is still waiting (the reload took that out first, see
// closeQueue) and stops the run in flight. With closeViewer it also ends
// the activity viewer: not when the brain moved, where the brain that runs
// in the new folder replaces the tmux session of that name.
func (br *Brain) retire(closeViewer bool) {
	Dropped(br.closeQueue())
	br.StopCurrent(false)
	// run.done precedes delivery. Retirement must finish that hand-off
	// before closing the folder, requeuing work in its replacement, or
	// allowing Bridge.Stop to close the store below the old worker.
	br.workers.Wait()
	if closeViewer {
		br.closeViewer()
	}
	// Every worker has finished with the folder.
	br.root.Close()
}

// holdsFolder reports whether the folder the brain opened is still the one
// at dir. When you move it aside and put another folder there, a restored
// copy say, the next reload makes the brain anew in that one, as for a new
// path. A brain cannot do this to its own folder: the sandbox pins it.
func (br *Brain) holdsFolder(dir string) bool {
	held, err := br.root.Stat(".")
	if err != nil {
		return false
	}
	at, err := os.Stat(dir)
	return err == nil && os.SameFile(held, at)
}

// readFile reads the file name in the brain's folder (see openInFolder),
// at most maxBrainFile of it.
func (br *Brain) readFile(name string) ([]byte, error) {
	f, err := fsx.OpenInFolder(br.root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return fsx.ReadAtMost(f)
}

// BrainRuntime is a brain's live state, for the dashboard.
type Runtime struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Dir      string            `json:"dir"`
	Routing  manifest.Routing  `json:"routing"`
	Model    string            `json:"model"`
	Effort   string            `json:"effort"`
	Running  *RunView          `json:"running"`
	Queue    []QueuedView      `json:"queue"`
	Last     *RunInfo          `json:"last"`
	Backup   manifest.Backup   `json:"backup"`
	Manifest manifest.Manifest `json:"manifest"`
}

// RunView describes the run in flight.
type RunView struct {
	Chat    string    `json:"chat"`
	Sender  string    `json:"sender"`
	Preview string    `json:"preview"`
	Silent  bool      `json:"silent"`
	Started time.Time `json:"started"`
	PID     int       `json:"pid"`
}

// QueuedView describes a waiting message.
type QueuedView struct {
	Chat    string    `json:"chat"`
	Sender  string    `json:"sender"`
	Preview string    `json:"preview"`
	Silent  bool      `json:"silent"`
	Queued  time.Time `json:"queued"`
}

// Snapshot returns the brain's live state, the queue in the order it will
// run.
func (br *Brain) Snapshot() Runtime {
	s := br.settings.Load()
	rt := Runtime{
		ID: br.id, Name: s.Name, Dir: br.workDir,
		Routing: s.Manifest.Routing, Model: s.Model, Effort: s.Effort,
		Backup: s.Manifest.Backup, Manifest: s.Manifest, Queue: []QueuedView{},
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if cur := br.current; cur != nil {
		rt.Running = &RunView{Chat: cur.msg.JID, Sender: cur.msg.Sender, Preview: textx.Truncate(cur.msg.TextWithQuote(), 160),
			Silent: cur.msg.Silent, Started: cur.started, PID: cur.pid}
	}
	for _, i := range br.queueOrderLocked() {
		p := br.pending[i]
		rt.Queue = append(rt.Queue, QueuedView{Chat: p.JID, Sender: p.Sender, Preview: textx.Truncate(p.TextWithQuote(), 160), Silent: p.Silent, Queued: p.At})
	}
	if br.last != nil {
		l := *br.last
		rt.Last = &l
	}
	return rt
}

// OpenMedia lists the files of the messages the brain is working on or has
// waiting: the vault keeps them hot (they are an open thread's files).
func (br *Brain) OpenMedia() []string {
	br.mu.Lock()
	defer br.mu.Unlock()
	var out []string
	if cur := br.current; cur != nil {
		out = append(out, cur.msg.Media...)
	}
	for _, p := range br.pending {
		out = append(out, p.Media...)
	}
	return out
}

// ensureViewer starts the tmux activity viewer if enabled and not running.
// It does so in the background, one start at a time: tmux and osascript,
// which can wait on an Automation consent prompt under launchd, must not
// hold up the run.
func (br *Brain) ensureViewer(s *Settings) {
	if !s.Viewer {
		return
	}
	br.viewerMu.Lock()
	if br.viewerStarting {
		br.viewerMu.Unlock()
		return
	}
	br.viewerStarting = true
	tv, gen := br.tmuxViewer, br.viewerGen
	br.viewerMu.Unlock()
	go func() {
		started := br.startViewerUnlessAlive(tv, s)
		br.viewerMu.Lock()
		br.viewerStarting = false
		closed := gen != br.viewerGen // the setting went off, or the brain
		if started != nil && !closed {
			br.tmuxViewer = started
		}
		br.viewerMu.Unlock()
		if started != nil && closed {
			started.Close()
		}
	}()
}

// startViewerUnlessAlive starts a viewer unless tv still runs. It returns
// the new viewer, or nil when tv runs or none could start.
func (br *Brain) startViewerUnlessAlive(tv *platform.Viewer, s *Settings) *platform.Viewer {
	if tv != nil && tv.IsAlive() {
		return nil
	}
	// tail fails on a log that does not exist yet, so create it first,
	// through the folder as a run opens it: touch would have followed a
	// dangling symlink there and created the file it points to.
	f, err := fsx.OpenInFolder(br.root, StreamLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("brain %s: no tmux viewer: could not open stream log: %v", br.id, err)
		return nil
	}
	f.Close()
	nv, err := br.viewerStart("claude-"+br.id, br.StreamLogPath(), br.workDir, s.EnvPassthrough)
	if err != nil {
		log.Printf("brain %s: could not start tmux viewer: %v", br.id, err)
		return nil
	}
	return nv
}

// closeViewer ends the activity viewer's tmux session, if it has one, which
// closes its Terminal window. A start under way closes what it opens.
func (br *Brain) closeViewer() {
	br.viewerMu.Lock()
	tv := br.tmuxViewer
	br.tmuxViewer = nil
	br.viewerGen++
	br.viewerMu.Unlock()
	if tv != nil {
		tv.Close()
	}
}

// streamLogName is the brain's activity log, in its folder.
const StreamLogName = "claude-stream.log"

func (br *Brain) StreamLogPath() string {
	return filepath.Join(br.workDir, StreamLogName)
}

// invoke runs the selected CLI once for m.
func (br *Brain) invoke(run *brainRun, ctx context.Context, s *Settings, m Inbound) claudecli.Result {
	// The message's files come back from the vault's cold tier first, so
	// that the prompt, built next with what the memory index recalls,
	// names files the run can read.
	if br.input.Hydrate != nil && len(m.Media) > 0 {
		br.input.Hydrate(ctx, br.id, append([]string(nil), m.Media...))
	}
	return br.runner.Run(ctx, RunSpec{
		ID: br.id, Dir: br.workDir, Settings: s, Prompt: br.buildPrompt(s, m), Message: m.Inbound,
		BeforeStart: func() { br.ensureViewer(s) },
	}, claudecli.Callbacks{
		Start: run.startProcess,
		Started: func(pid int) func() {
			br.setRunPID(run, pid)
			return nil
		},
		Reaped: func() { br.setRunPID(run, 0) },
		OpenStream: func(pid int, result func(claudecli.Result)) (claudecli.StreamSink, func()) {
			return br.openRunStream(m, runHeading(m, s, pid), result)
		},
		ReadPanic: func(p any, stack []byte) {
			log.Printf("brain %s: reading the output of the run for %s panicked: %v\n%s", br.id, m.JID, p, stack)
		},
		Notice: func() {
			if !m.Silent {
				br.sink.Working(m.JID)
			}
		},
		SlowResponse: func(elapsed time.Duration) {
			log.Printf("brain %s: response took %v for %s (slow response)", br.id, elapsed, m.JID)
		},
		Stderr: func(pid int, text string) { br.logStderr(m, pid, text) },
	})
}

// logStderr adds what the CLI wrote to stderr to the brain's activity log,
// the end of it when there is much, where a failed run can be looked into
// even when the run succeeded or its error kept only a little of it. The
// block names the run, the run for m with process pid, as its heading
// does (runHeading): runs can end out of the order they started in.
func (br *Brain) logStderr(m Inbound, pid int, stderr string) {
	if stderr == "" {
		return
	}
	f, err := fsx.OpenInFolder(br.root, StreamLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("brain %s: could not open stream log: %v", br.id, err)
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "[%s] stderr of the run for %s from %s, pid %d:\n%s\n",
		time.Now().Format("15:04:05"), m.JID, m.Sender, pid, textx.Tail(stderr, stderrLogBytes))
}

// stderrLogBytes is how much of a run's stderr goes to its activity log.
const stderrLogBytes = 4000

// promptHistoryBudget is how far back from the end of the history a run's
// prompt looks for its chat's messages: far more than a window of them
// takes in a busy brain, so that a chat quiet while others talked still
// gets its last messages, without reading years of history on every run.
var promptHistoryBudget int64 = 16 << 20

// historyBefore returns what m's run is shown of its chat, the one that
// goes by br.input.Chats(m.Inbound), oldest first and at most limit entries in all.
// before is the chat's history before m's own lines (Inbound.logged).
// since is what went out to the chat after them, while m waited in the
// queue, and what waits in the outbox to go out ahead of anything m's
// run says (see Brain.undelivered): the brain's replies and questions,
// and what came through /send. Those answer earlier messages (the run
// before m's, or the question it asked), which m's sender had not seen
// when they wrote, and m's run must know what they said. The messages
// others sent after m are left out: each has a run of its own, or is for
// the next one to see. Without m's lines in the history, before is the
// chat's last entries, with those in the outbox among them, and since is
// empty; when the history cannot be read (see openInFolder), only the
// outbox's are left.
func (br *Brain) historyBefore(m Inbound, limit int) (before, since []history.Entry) {
	chats := br.input.Chats(m.Inbound)
	// The answers on their way first, then the history: one delivered in
	// between is in both, and withUndelivered takes it once.
	pending := br.undelivered(chats)
	entries := history.ForChat(br.root, br.id, chats, promptHistoryBudget, m.enoughBefore(limit))
	at := m.loggedIn(entries)
	if at < 0 {
		before = withUndelivered(entries, pending)
	} else {
		before = entries[:at]
		for _, e := range entries[at+1:] {
			if e.Type == history.Out {
				since = append(since, e)
			}
		}
		since = withUndelivered(since, pending)
	}
	// The newest count most: what was sent since, then what came before.
	since = since[max(0, len(since)-limit):]
	before = before[max(0, len(before)-(limit-len(since))):]
	return before, since
}

// enoughBefore tells chatHistory, which reads the chat's history back from
// its end, when it has what m's run needs: every line of m's (see
// Inbound.logged) and limit entries before the first of them, or for a
// message without lines, limit entries. A line deleted since is not
// found, and the reading goes on as far as it may.
func (m Inbound) enoughBefore(limit int) func(e history.Entry) bool {
	var lines []history.Entry
	for _, p := range m.ownParts() {
		if p.logged != (history.Entry{}) {
			lines = append(lines, p.logged)
		}
	}
	older := 0
	return func(e history.Entry) bool {
		if len(lines) > 0 {
			// Newest first: the first of m's lines is the last found.
			if i := slices.IndexFunc(lines, func(l history.Entry) bool { return l.SameLine(e) }); i >= 0 {
				lines = slices.Delete(lines, i, i+1)
			}
			return false
		}
		older++
		return older >= limit
	}
}

// loggedIn returns where m's own lines begin in entries, the chat's
// history: at the last line that is the line of the first of m's parts
// still there (see Inbound.logged), or -1 for none. A part deleted since
// took its line along; one edited while it waited has its line rewritten,
// with the new text.
func (m Inbound) loggedIn(entries []history.Entry) int {
	for _, p := range m.ownParts() {
		if p.logged == (history.Entry{}) {
			continue
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if p.logged.SameLine(entries[i]) {
				return i
			}
		}
	}
	return -1
}

// identities reads the brain's identity map, through the folder: a map
// swapped for a symlink to another brain's would put that brain's contacts
// in this one's prompt.
func (br *Brain) identities() map[string]string {
	idMap, err := br.readFile(prompttext.IdentityMapPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("brain %s: could not read identity map %s: %v", br.id, prompttext.IdentityMapPath, err)
	}
	return prompttext.ParseIdentityMap(idMap)
}

// buildPrompt gathers what m's run is told, through the brain's folder: its
// prompt file, the chat's recent history and the identity map. renderPrompt
// puts it into words.
func (br *Brain) buildPrompt(s *Settings, m Inbound) string {
	in := prompttext.Input{Now: time.Now(), BrainID: br.id, Message: m.Inbound, BuildChat: br.input.BuildChat != nil && br.input.BuildChat(m.JID), ObserveOnly: m.Silent, Vault: s.Vault}
	if name := s.PromptFile; name != "" {
		if data, err := br.readFile(name); err != nil {
			log.Printf("brain %s: could not read prompt file %s: %v", br.id, name, err)
		} else {
			in.Instructions = strings.TrimSpace(string(data))
		}
	}
	limit := prompttext.HistoryLimit(m.IsGroup())
	in.History, in.Since = br.historyBefore(m, limit)
	in.Names = br.identities()
	if br.input.Recall != nil && !m.Silent {
		in.Recall = br.input.Recall(RecallRequest{ID: br.id, Dir: br.workDir, Manifest: s.Manifest, Message: m.Inbound, Shown: slices.Concat(in.History, in.Since)})
	}
	return prompttext.Build(in)
}

// runHeading starts a run's part of the activity log: what it answers and
// with what, so that it can be told from the others and matched to the
// run the dashboard shows.
func runHeading(m Inbound, s *Settings, pid int) string {
	model := s.Model
	if model == "" {
		model = "the CLI's default"
	}
	return fmt.Sprintf("── Run for %s from %s, pid %d, model %s ──", m.JID, m.Sender, pid, model)
}

// runHeadingRE matches a heading runHeading wrote, after the time
// readStream puts in front of every line, and takes its pid. The pid is
// the last one in it: a chat or a sender cannot end the line the way the
// model does.
var runHeadingRE = regexp.MustCompile(`^\[\d\d:\d\d:\d\d\] ── Run for .*, pid (\d+), model .* ──$`)

// RunHeadingPID returns the pid of the run whose heading line is, and
// false for any other line of the activity log.
func RunHeadingPID(line string) (int, bool) {
	m := runHeadingRE.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	pid, err := strconv.Atoi(m[1])
	return pid, err == nil
}

// streamLogMax is the size at which the activity log is rotated when a run
// starts: it becomes streamLogOld, replacing the one before, so the two
// hold about twice this at most. Named *.log, the old one is left out of
// backups too.
const (
	streamLogMax = 5 << 20
	streamLogOld = "claude-stream.1.log"
)

// rotateStreamLog moves a full activity log aside, through the folder: a
// rename replaces a symlink at streamLogOld itself, never what it points to.
func (br *Brain) rotateStreamLog() {
	fi, err := br.root.Lstat(StreamLogName)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < streamLogMax {
		return
	}
	if err := br.root.Rename(StreamLogName, streamLogOld); err != nil {
		log.Printf("brain %s: could not rotate its stream log: %v", br.id, err)
	}
}

// runSink is the StreamSink of m's run in br: the brain's activity log, and
// the chat for the questions the run asks.
type runSink struct {
	br       *Brain
	m        Inbound
	log      *os.File // nil when it could not be opened
	onResult func(claudecli.Result)
}

func (s *runSink) Log(line string) {
	if s.log != nil {
		_, _ = fmt.Fprintf(s.log, "[%s] %s\n", time.Now().Format("15:04:05"), line)
	}
}

// Question sends text to the run's chat. Untagged (silent) messages never
// produce a visible reply, so they must not ask anything either.
func (s *runSink) Question(text string) bool {
	return !s.m.Silent && s.br.sink.Question(s.br, s.m.JID, text)
}

func (s *runSink) Result(res claudecli.Result) {
	if s.onResult != nil {
		s.onResult(res)
	}
}

// readStream is parseStream for m's run, with heading as the run's first
// line in the activity log, calling onResult, when set, as soon as the
// result is in, while the stream may still go on.
func (br *Brain) readStream(m Inbound, reader io.Reader, heading string, onResult func(claudecli.Result)) claudecli.Result {
	return br.readAgentStream(m, reader, heading, onResult, "claude")
}

func (br *Brain) readAgentStream(m Inbound, reader io.Reader, heading string, onResult func(claudecli.Result), provider string) claudecli.Result {
	sink, closeStream := br.openRunStream(m, heading, onResult)
	defer closeStream()
	return claudecli.ParseProvider(reader, sink, provider)
}

func (br *Brain) openRunStream(m Inbound, heading string, onResult func(claudecli.Result)) (claudecli.StreamSink, func()) {
	br.rotateStreamLog()
	// Through the folder, like the history: a brain can swap the log for a
	// symlink to a file its sandbox denies it writing.
	streamLog, err := fsx.OpenInFolder(br.root, StreamLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("brain %s: could not open stream log: %v", br.id, err)
	}
	closeStream := func() {
		if streamLog != nil {
			_ = streamLog.Close()
		}
	}
	ready := false
	defer func() {
		if !ready {
			closeStream()
		}
	}()
	sink := &runSink{br: br, m: m, log: streamLog, onResult: onResult}
	sink.Log(heading)
	ready = true
	return sink, closeStream
}

func (br *Brain) finishRun(run *brainRun) {
	run.cancel()

	br.mu.Lock()
	if br.current == run {
		br.current = nil
	}
	br.mu.Unlock()

	close(run.done)
}

func (br *Brain) setRunPID(run *brainRun, pid int) {
	br.mu.Lock()
	run.pid = pid
	br.mu.Unlock()
}

// startProcess publishes a started process before a force-stop can return.
// Ordinary stops still use only context cancellation through os/exec.
func (run *brainRun) startProcess(ctx context.Context, spec claudecli.Spawn) (*claudecli.Process, error) {
	run.processMu.Lock()
	defer run.processMu.Unlock()
	if run.forceStopped {
		return nil, context.Canceled
	}
	proc, err := claudecli.Start(ctx, spec)
	if err == nil {
		run.process = proc
	}
	return proc, err
}

// awaitProcessStop prevents a later start, then waits only for the supervisor
// to acknowledge killing the process group (or finishing Wait). It does not
// wait for output readers, hooks or reply delivery, which may be blocked.
func (run *brainRun) awaitProcessStop() {
	run.processMu.Lock()
	run.forceStopped = true
	proc := run.process
	run.processMu.Unlock()
	if proc != nil {
		<-proc.Stopped()
	}
}

// interruptRun stops a run by cancelling its context, on which cmd.Cancel
// kills the run's process group (see invoke): the one way a run is
// stopped. It signals no process itself, so a stop that comes once the
// CLI was reaped cannot hit another process that got its PID meanwhile.
func (br *Brain) interruptRun(run *brainRun) {
	if run != nil {
		run.cancel()
	}
}

// AppendHistory appends one JSONL entry to the brain's history file.
func (br *Brain) AppendHistory(chatJID, from, text, typ string) {
	br.appendHistoryVia(chatJID, from, text, typ, "")
}

// appendHistoryVia is AppendHistory for a line marked with how it came in
// (see historyEntry.Via).
func (br *Brain) appendHistoryVia(chatJID, from, text, typ, via string) {
	br.AppendEntry(history.Entry{From: from, Chat: chatJID, Text: text, Type: typ, Via: via})
}

// AppendEntry appends entry to the brain's history file, stamped with the
// time now unless it has a time. It returns the line as it reads back, or
// none when the file could not be opened.
func (br *Brain) AppendEntry(entry history.Entry) history.Entry {
	br.historyMu.Lock()
	entry, err := history.Append(br.root, entry)
	br.historyMu.Unlock()
	br.entryWritten(entry, err)
	if err != nil {
		return history.Entry{}
	}
	return entry
}

// RecordDelivered writes the brain's answer text to chat into its
// history once it reached WhatsApp, after remove took it out of the
// outbox; when remove fails, it writes nothing and returns the error, and
// the outbox tries again later, so the answer is recorded once. Both
// happen under historyMu, which undelivered reads the outbox under: a run
// finds the answer in the outbox or in the history, never in neither.
func (br *Brain) RecordDelivered(chatJID, text string, remove func() error) error {
	br.historyMu.Lock()
	if err := remove(); err != nil {
		br.historyMu.Unlock()
		return err
	}
	entry, err := history.Append(br.root, history.Entry{From: br.id, Chat: chatJID, Text: text, Type: history.Out})
	br.historyMu.Unlock()
	br.entryWritten(entry, err)
	return nil
}

// entryWritten logs an entry that could not be written, or hands one that
// was to OnHistory, outside historyMu.
func (br *Brain) entryWritten(entry history.Entry, err error) {
	if err != nil {
		log.Printf("brain %s: failed to open history file %s: %v", br.id, filepath.Join(br.workDir, history.FileName), err)
		return
	}
	br.sink.History(br.id, entry)
}

// undelivered returns the brain's answers in the chat that goes by chats
// which are still on their way (brainSink.Undelivered). It reads them
// under historyMu, which RecordDelivered moves an answer from the outbox
// into the history under: the history, read after, holds every answer
// this did not return.
func (br *Brain) undelivered(chats []string) []history.Entry {
	if br.sink == nil || br.sink.target == nil {
		return nil
	}
	br.historyMu.Lock()
	all := br.sink.Undelivered(br.id)
	br.historyMu.Unlock()
	var out []history.Entry
	for _, e := range all {
		if slices.Contains(chats, e.Chat) {
			out = append(out, e)
		}
	}
	return out
}

// withUndelivered puts the answers still on their way among the history's
// entries, each after the last entry no later than it. One the history
// holds already, delivered after pending was read, is left out: the same
// answer, in the same chat, recorded no earlier than it was written.
func withUndelivered(entries, pending []history.Entry) []history.Entry {
	at := func(e history.Entry) time.Time {
		t, _ := time.Parse(time.RFC3339, e.TS)
		return t
	}
	for _, p := range pending {
		pt := at(p)
		if slices.ContainsFunc(entries, func(e history.Entry) bool {
			return e.Type == history.Out && e.Chat == p.Chat && e.Text == p.Text && !at(e).Before(pt)
		}) {
			continue
		}
		i := len(entries)
		for i > 0 && at(entries[i-1]).After(pt) {
			i--
		}
		entries = slices.Insert(entries, i, p)
	}
	return entries
}

// ── brainManager ─────────────────────────────────────────────────────────

// brainManager owns the brains and routes messages to them. Its brain set can
// be replaced at any time with Apply: unchanged brains keep running untouched,
// removed brains finish being stopped in the background.
type Manager struct {
	reloadMu sync.Mutex // serialises Apply
	// stopped is set by Stop, under reloadMu: the brains are stopped for
	// good, and a reload would start new ones.
	stopped bool
	// retiring are the brains reloads took out, while they stop and hand
	// on what they had waiting (see requeue); Stop waits for them.
	retiring sync.WaitGroup
	// forced stops admission and publication without waiting for reloadMu,
	// which may be held by filesystem work during a reload.
	forced atomic.Bool

	mu      sync.RWMutex // guards the fields below; never held while calling into a Brain
	brains  map[string]*Brain
	retired map[*Brain]struct{} // removed from order, until retirement finishes
	order   []*Brain
	routes  *route.Table

	options Options
	sink    *brainSinkRef
	// slots bound the runs of every brain together, across reloads.
	slots *runSlots
}

// brainManagerOptions supplies runtime dependencies without configuration or
// host policy. The same Runner and Sink serve live and retired brains.
type Options struct {
	Runner      Runner
	Sink        Sink
	Input       InputRules
	ViewerStart func(name, streamLog, cwd string, passthrough []string) (*platform.Viewer, error)
	ClaimMedia  func(mediaDir, brainID string, paths []string) map[string]string
	// ShutdownGrace is read when Stop reaches each live brain, at the old
	// command policy lookup point. Nil uses the service's 15-second default.
	ShutdownGrace func() time.Duration
}

// brainApplyOptions is one reload's command projection. SetDirs runs under
// reloadMu, outside mu, before publishing the new brain set. Its closure
// captures the configuration used for this Apply, rather than reading a
// newer live configuration while updating sandbox profiles.
type ApplyOptions struct {
	Held    []route.Held
	OwnPATH map[string][]string
	SetDirs func(map[string][]string)
}

// brainOpenFailure reports a root which became unavailable after resolution.
// It is converted into a registry diagnostic by command composition only.
type OpenFailure struct {
	Spec manifest.Spec
	Err  error
}

func NewManager(options Options) *Manager {
	return &Manager{
		brains: map[string]*Brain{}, options: options,
		sink: &brainSinkRef{target: options.Sink}, slots: newRunSlots(0),
	}
}

// PauseRuns holds new run-slot grants until the returned function is called.
// Startup replay uses it to apply later persisted edits before a queued run
// can start. Releasing the pause is idempotent.
func (bm *Manager) PauseRuns() func() { return bm.slots.pause() }

// SetSink wires runtime delivery before the first message. Every current and
// future brain shares this reference, like the former hooks pointer.
func (bm *Manager) SetSink(s Sink) { bm.sink.target = s }

// apply keeps unchanged brains running and retires removed ones in the
// background. Resolution and process policy are supplied by the caller.
func (bm *Manager) Apply(specs []manifest.Spec, globals Globals, options ApplyOptions) ([]OpenFailure, error) {
	bm.reloadMu.Lock()
	defer bm.reloadMu.Unlock()

	if bm.stopped || bm.forced.Load() {
		return nil, ErrStopped
	}
	if err := route.Validate(specs); err != nil {
		return nil, err
	}

	bm.mu.RLock()
	old := bm.brains
	bm.mu.RUnlock()

	next := make(map[string]*Brain, len(specs))
	order := make([]*Brain, 0, len(specs))
	active := make([]manifest.Spec, 0, len(specs))
	held := append([]route.Held(nil), options.Held...)
	var failures []OpenFailure
	workDirs := map[string][]string{}

	// The folders opened for new brains, closed again unless they run.
	var created []*Brain
	applied := false
	defer func() {
		if !applied {
			for _, br := range created {
				br.root.Close()
			}
		}
	}()

	for _, sp := range specs {
		m := sp.Manifest
		// Even a brain left out below keeps its folder denied to the others.
		// The profiles get the folder as configured too, see BrainSpec.Path.
		workDirs[m.ID] = []string{sp.Dir}
		if sp.Path != "" && sp.Path != sp.Dir {
			workDirs[m.ID] = []string{sp.Path, sp.Dir}
		}
		s := MergeSettings(globals, sp)
		s.OwnPATH = append([]string(nil), options.OwnPATH[m.ID]...)
		br, ok := old[m.ID]
		if !ok || br.workDir != sp.Dir || !br.holdsFolder(sp.Dir) {
			root, err := os.OpenRoot(sp.Dir)
			if err != nil {
				log.Printf("brain %s: leaving it out: %v", m.ID, err)
				failures = append(failures, OpenFailure{Spec: sp, Err: err})
				held = append(held, route.Held{ID: m.ID, Routing: m.Routing})
				continue
			}
			br = &Brain{
				id:          m.ID,
				workDir:     sp.Dir,
				root:        root,
				runner:      bm.options.Runner,
				viewerStart: bm.options.ViewerStart,
				input:       bm.options.Input,
				claimMedia:  bm.options.ClaimMedia,
				sink:        bm.sink,
				slots:       bm.slots,
			}
			created = append(created, br)
		}
		br.settings.Store(s)
		if !s.Viewer {
			br.closeViewer() // the viewer setting was turned off
		}

		next[m.ID] = br
		order = append(order, br)
		active = append(active, sp)
	}
	// The complete set was validated before opening roots. Removing a spec
	// cannot introduce a clash; BuildWithHeld also validates direct callers.
	routes, err := route.BuildWithHeld(active, held)
	if err != nil {
		return nil, err
	}

	var removed []*Brain
	for id, br := range old {
		if nb, ok := next[id]; !ok || nb != br {
			removed = append(removed, br)
		}
	}

	if options.SetDirs != nil {
		options.SetDirs(workDirs)
	}
	bm.slots.setLimit(globals.RunLimit)

	bm.mu.Lock()
	// KillRuns publishes its latch under this same lock, so its snapshot
	// cannot miss a newly published brain or one about to retire.
	if bm.forced.Load() {
		bm.mu.Unlock()
		return nil, ErrStopped
	}
	if bm.retired == nil {
		bm.retired = make(map[*Brain]struct{})
	}
	for _, br := range removed {
		bm.retired[br] = struct{}{}
	}
	bm.brains, bm.order, bm.routes = next, order, routes
	bm.mu.Unlock()
	applied = true

	for _, br := range removed {
		if nb, ok := next[br.id]; ok {
			log.Printf("brain %s: now in the folder at %s; stopping it in the old one", br.id, nb.workDir)
		} else {
			log.Printf("brain %s: removed from configuration; stopping it", br.id)
		}
		_, moved := next[br.id]
		// In the background, since the run in flight may take a while to
		// stop. What waits goes on once it has: not to a brain in the same
		// folder while the old run still works there.
		waiting := br.closeQueue()
		bm.retiring.Go(func() {
			defer func() {
				bm.mu.Lock()
				delete(bm.retired, br)
				bm.mu.Unlock()
			}()
			br.retire(!moved)
			bm.requeue(br, waiting)
		})
	}
	return failures, nil
}

// errBrainsStopped is a reload after Stop.
var ErrStopped = errors.New("the brains are stopped")

// requeue hands what from had waiting when a reload took it out, which
// never ran, to the brains that take it now: the brain in its new folder,
// or the one its chat goes to. A message keeps its history lines where
// that brain's history has them (see Brain.logInbound). One no brain takes
// is dropped.
func (bm *Manager) requeue(from *Brain, ms []Inbound) {
	if bm.keepRetiredWaiting(from, ms) {
		return
	}
	for _, m := range ms {
		to, _, _, err := bm.Submit(m)
		switch {
		case err != nil:
			// Stop may have closed the destination after the check above.
			if bm.keepRetiredWaiting(from, []Inbound{m}) {
				continue
			}
			log.Printf("brain %s: dropping a message for %s that waited for it: %v", from.id, m.JID, err)
			Dropped([]Inbound{m})
		case to.id != from.id:
			log.Printf("brain %s: handed a message for %s that waited for it to brain %s", from.id, m.JID, to.id)
		}
	}
}

// keepRetiredWaiting sends a retired queue to shutdown persistence once
// Stop has begun, instead of starting new work in the replacement brain.
func (bm *Manager) keepRetiredWaiting(from *Brain, ms []Inbound) bool {
	bm.reloadMu.Lock()
	stopped := bm.stopped
	bm.reloadMu.Unlock()
	if !stopped {
		return false
	}
	bm.sink.Waiting(from, ms)
	return true
}

// BrainFor returns the brain that handles a chat, or nil when the route is
// reserved for an unavailable brain (see HeldFor).
func (bm *Manager) BrainFor(jid string, isGroup bool) *Brain {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.brains[bm.routes.For(jid, isGroup)]
}

// HeldFor returns the unavailable brain that reserves the chat, or empty.
func (bm *Manager) HeldFor(jid string, isGroup bool) string {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.routes.HeldFor(jid, isGroup)
}

// DMBrain returns the brain that gets direct messages: the one with
// handles_dms, else the fallback brain, or nil.
func (bm *Manager) DMBrain() *Brain { return bm.BrainFor("", false) }

// Fallback returns the brain BrainFor falls back to, for the allowed groups
// no brain claims and, while no brain has handles_dms, for direct
// messages: the one marked default, else the first brain; nil when there
// is none.
func (bm *Manager) Fallback() *Brain {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.brains[bm.routes.Default()]
}

// Get returns a brain by id.
func (bm *Manager) Get(id string) *Brain {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.brains[id]
}

// Brains returns the brains in configuration order.
func (bm *Manager) Brains() []*Brain {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return append([]*Brain(nil), bm.order...)
}

// Route returns the brain that handles m: the brain it names, else the one
// its chat routes to. A message that names a brain that no longer exists gets
// nil rather than whichever brain owns its chat.
func (bm *Manager) Route(m Inbound) *Brain {
	if m.Brain != "" {
		return bm.Get(m.Brain)
	}
	return bm.BrainFor(m.JID, m.IsGroup())
}

// Submit routes a message to its brain. A message that lands on a brain being
// removed at that moment is routed again once, with its files where that
// brain left them (see Brain.submit).
func (bm *Manager) Submit(m Inbound) (br *Brain, pos int, busy bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		if bm.forced.Load() {
			return nil, 0, false, ErrStopped
		}
		br = bm.Route(m)
		if br == nil && m.Brain != "" {
			return nil, 0, false, fmt.Errorf("brain %s is not configured", m.Brain)
		}
		if br == nil {
			return nil, 0, false, fmt.Errorf("no brain configured for %s", m.JID)
		}
		pos, busy, err = br.submit(&m)
		if !errors.Is(err, ErrRetired) {
			return br, pos, busy, err
		}
	}
	return br, pos, busy, err
}

// StopHandOffs stops, in every brain, the hand-offs that answer in jid (see
// Brain.StopHandOffs). A brain picked for files or recordings answers in its
// reply chat, often a chat another brain owns, so !stop there must reach it.
// It reports whether a run was stopped and how many waiting messages were
// dropped.
func (bm *Manager) StopHandOffs(jid string) (stopped bool, Dropped int) {
	for _, br := range bm.Brains() {
		s, d := br.StopHandOffs(jid)
		stopped, Dropped = stopped || s, Dropped+d
	}
	return stopped, Dropped
}

// KillRuns cancels every brain's run in flight at once, for a second signal.
// Before os.Exit it waits for each process supervisor to acknowledge the
// kill, never for the runs' output or delivery. Cancellation alone could
// return before os/exec's watcher had a chance to signal the process group.
func (bm *Manager) KillRuns() {
	bm.mu.Lock()
	bm.forced.Store(true)
	brains := append([]*Brain(nil), bm.order...)
	for br := range bm.retired {
		brains = append(brains, br)
	}
	bm.mu.Unlock()

	var runs []*brainRun
	for _, br := range brains {
		br.mu.Lock()
		// Freeze admission and the worker's next turn before capturing
		// current. Keep pending intact for concurrent graceful shutdown.
		br.freezeQueueLocked()
		cur := br.current
		br.mu.Unlock()
		if cur != nil {
			br.interruptRun(cur)
			runs = append(runs, cur)
		}
	}
	for _, run := range runs {
		run.awaitProcessStop()
	}
}

// Stop stops every brain for good as the bridge stops (see
// Brain.shutdown), all at once, after the brains reloads took out have
// kept what they had waiting. Each run in flight gets shutdownGrace
// to finish and be delivered.
func (bm *Manager) Stop() {
	bm.reloadMu.Lock()
	bm.stopped = true
	brains := bm.Brains()
	waiting := make([][]Inbound, len(brains))
	for i, br := range brains {
		waiting[i] = br.closeQueue()
	}
	bm.reloadMu.Unlock()
	// Freeze and keep live queues before waiting for retired workers.
	// Otherwise those workers could requeue a message into a live brain
	// and start it while the rest of shutdown waited for retirement.
	for i, br := range brains {
		bm.sink.Waiting(br, waiting[i])
	}
	bm.retiring.Wait()
	var wg sync.WaitGroup
	for _, br := range brains {
		wg.Go(func() {
			grace := 15 * time.Second
			if bm.options.ShutdownGrace != nil {
				grace = bm.options.ShutdownGrace()
			}
			br.shutdown(grace)
		})
	}
	wg.Wait()
}
