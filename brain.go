package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
)

// historyEntry is one line in a brain's conversation-history.jsonl file.
// Brains' own scripts read the file too, so docs/BRAINS.md and
// docs/schemas/history-line.schema.json describe the line: fields are
// added, never renamed or removed.
type historyEntry struct {
	TS   string `json:"ts"`
	From string `json:"from"`
	Chat string `json:"chat"` // JID of the chat (DM participant or group)
	Text string `json:"text"`
	Type string `json:"type"` // historyIn, historyOut, historyEvent or historyReset
	// Via marks an "out" line that came in through /send: "brain" (From is
	// the brain that sent it), "script" (the scripts token) or "api" (no
	// token). A brain's own replies have none.
	Via string `json:"via,omitempty"`
	// ID is the WhatsApp message's ID for a message from WhatsApp, which an
	// edit or a deletion of it names (see recordChange).
	ID string `json:"id,omitempty"`
	// OTPRequest is the id /send answered a request for a one-time code
	// with, on the "out" line of that request (see otpRequest).
	OTPRequest string `json:"otp_request,omitempty"`
}

// The history entry types. They are written to every brain's history file,
// which `fridolin history` and a brain's own scripts read, so their values
// must not change.
const (
	historyIn  = "in"  // a message to the brain
	historyOut = "out" // a message the brain sent, or one sent on its behalf
	// historyEvent is something that happened in a chat without a message,
	// such as a poll vote. Its From is who did it, and its text says what,
	// after their name.
	historyEvent = "event"
	// historyReset marks where !reset started a chat afresh. Its From is
	// who sent it, and it has no text. A run's prompt shows only the
	// chat's lines after the newest one; it is no message, so neither the
	// dashboard's feed nor `fridolin history` shows it.
	historyReset = "reset"
)

// normalizeJID strips the WhatsApp server suffix and a leading + from a JID,
// e.g. "15551234567@s.whatsapp.net" and "+15551234567" both become "15551234567".
// Spaces around the number and further +s in front of it go too, so that
// normalising the result again changes nothing: a number saved normalised
// still equals the normalised JID of its sender.
func normalizeJID(jid string) string {
	if idx := strings.Index(jid, "@"); idx != -1 {
		jid = jid[:idx]
	}
	jid = strings.TrimLeftFunc(jid, func(r rune) bool { return r == '+' || unicode.IsSpace(r) })
	return strings.TrimRightFunc(jid, unicode.IsSpace)
}

// identityMapFile is the brain's contact map, in its folder.
const identityMapFile = "memories/people/whatsapp-identity-map.md"

// parseIdentityMap parses a brain's contact map and returns name by
// identityKey.
//
// The map is a markdown table at identityMapFile in the brain's folder:
//
//	| WhatsApp Number | Person | Notes |
//	| --------------- | ------ | ----- |
//	| 15551234567     | Alex   | …     |
//
// It is entirely optional — without it, chat history shows raw JIDs.
func parseIdentityMap(data []byte) map[string]string {
	identities := make(map[string]string)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) < 3 || isTableRule(parts) {
			continue
		}

		jid := strings.TrimSpace(parts[1])
		name := strings.TrimSpace(parts[2])
		if jid == "" || name == "" || jid == "WhatsApp Number" {
			continue
		}
		// A cell like "+" or "@lid" holds no number to look up. A number
		// is read however it was written: "+1 555 123 4567" is 15551234567.
		number := identityKey(jid)
		if number == "" {
			continue
		}
		identities[number] = name
	}

	return identities
}

// isTableRule reports whether the cells of a markdown table row hold only
// dashes and colons, as the line under the header does. A row that merely
// contains "---", say in its notes, is a contact like any other.
func isTableRule(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: \t") != "" {
			return false
		}
	}
	return true
}

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

	settings atomic.Pointer[brainSettings]
	sandbox  *SandboxProfiler
	hooks    *BrainHooks
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
	retired bool // removed from the configuration
	last    *RunInfo
	// slotWait is set while the worker waits for a run slot.
	slotWait *slotWait

	// tmuxViewer is an optional Terminal window tailing this brain's
	// activity log. A run starts it in the background and a reload can close
	// it, so viewerMu guards it, with viewerStarting, set while a start is
	// under way, and viewerGen, which closeViewer moves on so that a start
	// that ends after it knows to close what it opened.
	viewerMu       sync.Mutex
	tmuxViewer     *TmuxViewer
	viewerStarting bool
	viewerGen      int
}

// brainSettings is everything about a brain that can change while it runs.
// Each run takes one snapshot, so an edit never changes a run half-way.
type brainSettings struct {
	name            string
	manifest        BrainManifest
	claudePath      string
	provider        string
	codexPath       string
	model           string
	effort          string
	disallowedTools []string
	promptFile      string // relative to the folder; see BrainSpec
	dangerously     bool
	inheritUser     bool // inherit_claude_user_config
	otpGuard        bool
	otpMarkers      []string
	viewer          bool
	runTimeout      time.Duration // 0 for none
	queueMode       string
	maxPending      int
	observe         string // BrainManifest.Observe
	credsDir        string // where per-brain credential files live
	mediaDir        string // the media folder, which holds the brain's inbox
	envPassthrough  []string
	toolsDir        string // the bridge's Python helpers, or ""
	dataDir         string // the data dir, absolute
	dbPath          string // the bridge's DB, absolute
	plaudPATH       string // the PATH to run with when brains may use Plaud's CLI
	// ownPATH are the folders of the bridge's PATH inside this brain's
	// folder, which setBridgePATH took out of the bridge's: they go first
	// on the PATH of this brain's runs, and of no one else's.
	ownPATH []string
}

// BrainHooks connect brains to the rest of the bridge.
type BrainHooks struct {
	// Notice tells the chat jid, ready to send, that a run is working on
	// its message: of use only now.
	Notice func(jid, text string)
	// Question sends the chat jid a question br asks mid-run, marked as
	// one, and reports whether it is on its way, ahead of the run's final
	// reply. br's history records it once it arrived, like an answer: the
	// reply to it comes as a new message, whose run must see what it
	// answers. A silent run asks nothing (see readStream).
	Question func(br *Brain, jid, text string) bool
	// Deliver receives every finished run. It records the answer in the
	// brain's history once it is sent.
	Deliver func(br *Brain, m Inbound, res RunResult)
	// Undelivered returns the brain's answers still on their way to
	// WhatsApp, as its history will record them once they arrived. A
	// chat gets them before anything a later run says there, so that run
	// reads them as said (see undelivered).
	Undelivered func(brainID string) []historyEntry
	// OnHistory sees every line written to a brain's history.
	OnHistory func(brainID string, e historyEntry)
	// ExtraArgs adds CLI arguments to a brain's runs (the site builder's
	// plugin and MCP server, for brains allowed to build the site).
	ExtraArgs func(brainID string) []string
	// CodexSetup supplies the site builder's instructions and MCP settings
	// using Codex's format; credentials travel in the environment, not argv.
	CodexSetup func(brainID string) (args, env []string, prompt string, err error)
	// RunEnv adds variables to one run's environment (the brain API's
	// address and a token for the run), and returns what to call once the
	// run is over.
	RunEnv func(brainID string) (env []string, done func())
}

// workingNoticeAfter is how long a run goes before the chat is told that
// it is working on it (workingNotice), a variable so tests need not wait
// for it.
var workingNoticeAfter = 10 * time.Second

// RunResult is the outcome of one brain run.
type RunResult struct {
	Text string
	// Asked is set when the run forwarded a question mid-run. Its answer
	// comes as a new message; Text, the run's own final reply, is sent too.
	Asked    bool
	Err      error
	Duration time.Duration
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
	cancel  context.CancelFunc
	done    chan struct{}
	pid     int
	once    sync.Once // guards close(done) against a double close
	msg     Inbound
	started time.Time
}

var (
	errBrainInterrupted = errors.New("brain run interrupted")
	errBrainDropped     = errors.New("dropped before it ran")
	errBrainRetired     = errors.New("brain was removed")
	errQueueFull        = errors.New("brain queue is full")
	errNoResult         = errors.New("no result from the agent (empty output)")
	// errRunTimedOut is a run that took longer than run_timeout_minutes.
	errRunTimedOut = errors.New("the run took too long and was stopped")
	// errSandboxUnavailable is what the chat sees. The cause goes to the log
	// only, because it can name private paths.
	errSandboxUnavailable = errors.New("the sandbox could not be set up, so this message was not run (details in the bridge log)")
)

// ID is the brain's stable identifier.
func (br *Brain) ID() string { return br.id }

// Name is the brain's display name.
func (br *Brain) Name() string { return br.settings.Load().name }

// Dir is the brain's folder.
func (br *Brain) Dir() string { return br.workDir }

// historyFileName is the brain's conversation history, in its folder.
const historyFileName = "conversation-history.jsonl"

// logInbound writes m to the brain's history: a line for each part of a
// merged message, with its ID, so that one deleted later is taken out
// alone, and with the time it was sent, not the time the debouncer let it
// through. It returns m with each part's line kept on it
// (Inbound.logged), so that its own run shows the message once, as the
// new message, after the chat's history before those lines.
func (br *Brain) logInbound(m Inbound) Inbound {
	parts := m.ownParts()
	for i, p := range parts {
		at := p.At
		if at.IsZero() {
			at = m.At
		}
		parts[i].logged = br.appendEntry(historyEntry{TS: historyTime(at), Chat: m.JID, From: p.Sender, Text: historyTextWithMedia(p.textWithQuote(), p.Media), Type: historyIn, ID: p.ID})
	}
	if len(m.parts) > 0 {
		m.parts = parts
	}
	m.logged = parts[0].logged
	return m
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
	queues := !m.Silent || observeRuns(s.observe, *m)

	// Turned away before anything is written: a message the brain does not
	// take is not in its history, where the sender's retry after "queue is
	// full", or the brain it is routed to instead of a retired one, would
	// put it a second time.
	br.mu.Lock()
	if br.retired {
		br.mu.Unlock()
		return 0, false, errBrainRetired
	}
	if queues && !br.hasRoomLocked(*m, s) {
		if !m.Silent {
			br.mu.Unlock()
			return 0, true, errQueueFull
		}
		// Chatter that finds no place is kept in the history all the
		// same, without a run of its own: it was said in the chat, and
		// the next run there reads it.
		queues = false
	}
	br.mu.Unlock()

	// The files go into this brain's inbox, the only part of the media
	// folder its sandbox opens, before the history names where they are.
	*m = m.withMediaMoved(claimMedia(s.mediaDir, br.id, m.Media))

	// History first, so that later runs see the message even when it is
	// merged into a queue that is already moving.
	*m = br.logInbound(*m)
	if !queues {
		return 0, false, nil
	}

	br.mu.Lock()
	if br.retired {
		br.mu.Unlock()
		return 0, false, errBrainRetired
	}

	// A newer message from the same chat supersedes queued untagged chatter:
	// it is already in the history the next run reads. In interrupt mode, a
	// tagged message also replaces its sender's own earlier request in that
	// chat, waiting or running. No other request is touched: other chats,
	// and other people's requests in a group, keep their place, and untagged
	// chatter never interrupts a run. Nor do ingested files interrupt a
	// request, or get replaced: each is a job of its own, not a correction of
	// the last one. Observe-only runs are another matter, see below.
	interrupting, replaces := supersedes(*m, s)
	var gone []Inbound
	kept := br.pending[:0]
	for _, p := range br.pending {
		if replaces(p) {
			gone = append(gone, p)
		} else {
			kept = append(kept, p)
		}
	}
	br.pending = kept
	defer dropped(gone)
	if len(br.pending) >= s.maxPending {
		// Only when another message took the last place since the check
		// above.
		dropped := false
		for i, p := range br.pending {
			if p.Silent {
				br.pending = append(br.pending[:i], br.pending[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			br.mu.Unlock()
			return 0, true, errQueueFull
		}
	}
	// Work addressed to the brain does not wait for an observe-only run: it
	// stops the one in flight and goes ahead of those waiting. Their chatter
	// is in the history, where the next run in its chat reads it.
	var interrupt *brainRun
	if cur := br.current; cur != nil && (interrupting && replaces(cur.msg) || !m.Silent && cur.msg.Silent) {
		interrupt = cur
	}
	br.pending = append(br.pending, *m)
	pos = slices.Index(br.queueOrderLocked(), len(br.pending)-1) + 1
	busy = pos > 1 || (br.current != nil && interrupt == nil) || br.slots.busy(!m.Silent)
	start := !br.working
	br.working = true
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
func (br *Brain) takeTurnLocked(chat string) {
	if br.turns == nil {
		br.turns = map[string]uint64{}
	}
	br.turn++
	br.turns[chat] = br.turn
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
func supersedes(m Inbound, s *brainSettings) (interrupting bool, replaces func(Inbound) bool) {
	interrupting = s.queueMode == queueModeInterrupt && !m.Silent && m.Source != sourceIngest
	return interrupting, func(p Inbound) bool {
		return p.JID == m.JID && (p.Silent || (interrupting && p.Sender == m.Sender && p.Source != sourceIngest))
	}
}

// hasRoomLocked reports whether the queue takes m: it has a free place,
// or one m makes by what it replaces, or untagged chatter to drop. br.mu is
// held.
func (br *Brain) hasRoomLocked(m Inbound, s *brainSettings) bool {
	_, replaces := supersedes(m, s)
	n, silent := 0, false
	for _, p := range br.pending {
		if !replaces(p) {
			n++
			silent = silent || p.Silent
		}
	}
	return n < s.maxPending || silent
}

// worker drains the queue one message at a time, each in a run slot (see
// runSlots).
func (br *Brain) worker() {
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
			dropped([]Inbound{m})
			continue
		}
		br.takeTurnLocked(m.JID)
		s := br.settings.Load()
		ctx, cancel := runContext(s.runTimeout)
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

// errRunPanicked is what a run that panicked inside the bridge ends with;
// the panic and its stack go to the log only.
var errRunPanicked = errors.New("the bridge failed while running this message (details in the bridge log)")

// invokeGuarded is invoke with a panic in it turned into the run's error,
// so that the run is finished and recorded like any failed one and the
// worker goes on with the next message. Unrecovered, the panic would end
// the whole bridge and leave every brain's running claude behind.
func (br *Brain) invokeGuarded(run *brainRun, ctx context.Context, s *brainSettings, m Inbound) (res RunResult) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("brain %s: the run for %s panicked: %v\n%s", br.id, m.JID, p, debug.Stack())
			res = RunResult{Err: errRunPanicked}
		}
	}()
	return br.invoke(run, ctx, s, m)
}

// deliverGuarded hands a finished run to the bridge and then to whoever
// waits for the message, each with a panic logged rather than ending the
// worker, and the one waiting told even when the delivery panicked.
func (br *Brain) deliverGuarded(m Inbound, res RunResult) {
	guard := func(what string, fn func()) {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("brain %s: %s the run for %s panicked: %v\n%s", br.id, what, m.JID, p, debug.Stack())
			}
		}()
		fn()
	}
	if br.hooks != nil && br.hooks.Deliver != nil {
		guard("delivering", func() { br.hooks.Deliver(br, m, res) })
	}
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

// runTimedOut is the error of a run stopped at run_timeout_minutes.
func runTimedOut(timeout time.Duration) error {
	after := timeout.String()
	if timeout%time.Minute == 0 {
		after = fmt.Sprintf("%d minutes", int(timeout/time.Minute))
	}
	return fmt.Errorf("%w after %s (run_timeout_minutes)", errRunTimedOut, after)
}

// dropped tells the messages in ms that will not run, outside br.mu.
func dropped(ms []Inbound) {
	for _, m := range ms {
		if m.Done != nil {
			m.Done(errBrainDropped)
		}
	}
}

// waitIdle waits until the brain has nothing running or queued, for at
// most max, and reports whether it got there. A backup asks before it
// copies the brain's folder (see backupBrain). Between two queued runs no
// run is current for a moment, so it waits for the worker to stop.
func (br *Brain) waitIdle(ctx context.Context, max time.Duration) bool {
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

func (br *Brain) recordLast(m Inbound, res RunResult) {
	info := &RunInfo{Chat: m.JID, Finished: time.Now(), Seconds: res.Duration.Seconds(), Outcome: outcomeOK}
	switch {
	case errors.Is(res.Err, errBrainInterrupted):
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
	dropped(gone)
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
	dropped(gone)
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
		br.appendEntry(historyEntry{Chat: jid, From: by, Type: historyReset})
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
	dropped(gone)
	return br.stopRun(cur), len(gone)
}

// stopRun kills run, if there is one, and waits for it to end.
func (br *Brain) stopRun(run *brainRun) bool {
	if run == nil {
		return false
	}
	br.interruptRun(run)
	select {
	case <-run.done:
	case <-time.After(10 * time.Second):
		log.Printf("brain %s: run did not stop within 10s", br.id)
	}
	return true
}

// retire stops the brain for good after it was removed from the
// configuration, or when the bridge stops. With closeViewer it also ends
// the activity viewer: not when the bridge stops, so the window stays until
// the next start replaces it, nor when the brain moved to another folder,
// where the brain that runs there replaces the tmux session of that name.
func (br *Brain) retire(closeViewer bool) {
	br.mu.Lock()
	br.retired = true
	gone := br.pending
	br.pending = nil
	if br.slotWait != nil {
		br.slots.giveUp(br.slotWait)
	}
	br.mu.Unlock()
	dropped(gone)
	br.StopCurrent(true)
	if closeViewer {
		br.closeViewer()
	}
	// Done with the folder. A run that would not stop finds it closed.
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
	f, err := openInFolder(br.root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAtMost(f)
}

// BrainRuntime is a brain's live state, for the dashboard.
type BrainRuntime struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Dir      string        `json:"dir"`
	Routing  BrainRouting  `json:"routing"`
	Model    string        `json:"model"`
	Effort   string        `json:"effort"`
	Running  *RunView      `json:"running"`
	Queue    []QueuedView  `json:"queue"`
	Last     *RunInfo      `json:"last"`
	Backup   BrainBackup   `json:"backup"`
	Manifest BrainManifest `json:"manifest"`
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
func (br *Brain) Snapshot() BrainRuntime {
	s := br.settings.Load()
	rt := BrainRuntime{
		ID: br.id, Name: s.name, Dir: br.workDir,
		Routing: s.manifest.Routing, Model: s.model, Effort: s.effort,
		Backup: s.manifest.Backup, Manifest: s.manifest, Queue: []QueuedView{},
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if cur := br.current; cur != nil {
		rt.Running = &RunView{Chat: cur.msg.JID, Sender: cur.msg.Sender, Preview: truncate(cur.msg.textWithQuote(), 160),
			Silent: cur.msg.Silent, Started: cur.started, PID: cur.pid}
	}
	for _, i := range br.queueOrderLocked() {
		p := br.pending[i]
		rt.Queue = append(rt.Queue, QueuedView{Chat: p.JID, Sender: p.Sender, Preview: truncate(p.textWithQuote(), 160), Silent: p.Silent, Queued: p.At})
	}
	if br.last != nil {
		l := *br.last
		rt.Last = &l
	}
	return rt
}

// startViewer is NewTmuxViewer, a variable so tests need no tmux.
var startViewer = NewTmuxViewer

// ensureViewer starts the tmux activity viewer if enabled and not running.
// It does so in the background, one start at a time: tmux and osascript,
// which can wait on an Automation consent prompt under launchd, must not
// hold up the run.
func (br *Brain) ensureViewer(s *brainSettings) {
	if !s.viewer {
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
func (br *Brain) startViewerUnlessAlive(tv *TmuxViewer, s *brainSettings) *TmuxViewer {
	if tv != nil && tv.IsAlive() {
		return nil
	}
	// tail fails on a log that does not exist yet, so create it first,
	// through the folder as a run opens it: touch would have followed a
	// dangling symlink there and created the file it points to.
	f, err := openInFolder(br.root, streamLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("brain %s: no tmux viewer: could not open stream log: %v", br.id, err)
		return nil
	}
	f.Close()
	nv, err := startViewer("claude-"+br.id, br.streamLogPath(), br.workDir, childEnv(anyChild, s.envPassthrough))
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
const streamLogName = "claude-stream.log"

func (br *Brain) streamLogPath() string {
	return filepath.Join(br.workDir, streamLogName)
}

// claudeArgs builds the CLI arguments for one run. mcpConfig is what
// mcpConfigOf returned for the brain's folder, or "" for none; settings is
// the brain's excludeInstructionsAbove. They end in -p: the prompt goes on
// stdin (see invoke).
func claudeArgs(s *brainSettings, mcpConfig, settings string) []string {
	args := []string{"--output-format", "stream-json", "--verbose"}
	if !s.inheritUser {
		// Only the brain folder's own Claude Code setup. Your user settings
		// (with their hooks, plugins and permissions), skills, agents,
		// commands and the MCP servers in ~/.claude.json are yours, and a
		// brain answers whoever may message it; under
		// --dangerously-skip-permissions it would call those MCP tools
		// without asking.
		args = append(args, "--setting-sources", "project,local", "--strict-mcp-config")
		if settings != "" {
			args = append(args, "--settings", settings)
		}
		if mcpConfig != "" {
			// --mcp-config takes several files: an option must follow.
			args = append(args, "--mcp-config", mcpConfig)
		}
	}
	if s.dangerously {
		args = append(args, "--dangerously-skip-permissions")
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.effort != "" {
		args = append(args, "--effort", s.effort)
	}
	if len(s.disallowedTools) > 0 {
		args = append(args, "--disallowed-tools", strings.Join(s.disallowedTools, ","))
	}
	return append(args, "-p")
}

// withArgsBeforePrint puts extra before the trailing -p that claudeArgs
// ends with, which ends an option list such as --mcp-config's.
func withArgsBeforePrint(args, extra []string) []string {
	if len(extra) == 0 || len(args) < 1 {
		return args
	}
	out := append([]string{}, args[:len(args)-1]...)
	out = append(out, extra...)
	return append(out, args[len(args)-1:]...)
}

// excludeInstructionsAbove returns the --settings for a brain in dir that
// keep the CLI from loading your instructions above the folder, which
// --setting-sources does not cover: the CLI reads the CLAUDE.md files and
// .claude rules of every folder up to / as part of the project, and your
// user memory in ~/.claude whatever the sources. Its claudeMdExcludes
// setting skips them, and it merges with the brain's own. It does not
// cover the skills, agents and commands in those .claude folders, nor
// stop a brain from reading the files with its tools; that takes the
// sandbox, which denies them all.
func excludeInstructionsAbove(dir string) string {
	data, err := json.Marshal(map[string][]string{"claudeMdExcludes": instructionsAbove(dir, homeDir())})
	if err != nil {
		return ""
	}
	return string(data)
}

// instructionsAbove lists the claudeMdExcludes patterns for a brain in dir:
// the CLAUDE.md, CLAUDE.local.md, .claude/CLAUDE.md and .claude/rules of
// every folder above dir, and those in home's .claude, the user memory,
// for a brain outside home too. The patterns match paths as the CLI spells
// them, which differ below a symlink (a brains folder linked from another
// volume), so the folders above dir come along its path as configured and
// along its real path, and home both ways too.
func instructionsAbove(dir, home string) []string {
	var folders []string
	seen := map[string]bool{}
	for _, start := range []string{absPath(dir), resolvePath(absPath(dir))} {
		for d := filepath.Dir(filepath.Clean(start)); ; d = filepath.Dir(d) {
			if !seen[d] {
				seen[d] = true
				folders = append(folders, d)
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	var out []string
	patterns := func(d string, names ...string) {
		for _, name := range names {
			out = append(out, filepath.Join(globEscape(d), name))
		}
	}
	inDotClaude := []string{filepath.Join(".claude", "CLAUDE.md"), filepath.Join(".claude", "rules", "**")}
	for _, d := range folders {
		patterns(d, instructionFiles...)
		patterns(d, inDotClaude...)
	}
	if filepath.IsAbs(home) {
		for _, h := range []string{home, resolvePath(home)} {
			if !seen[h] {
				seen[h] = true
				patterns(h, inDotClaude...)
			}
		}
	}
	return out
}

// globEscape makes p a picomatch pattern, which the CLI matches
// claudeMdExcludes with, that matches the path p alone. A backslash does
// not do it, since the CLI turns every one into a / first, so each glob
// character goes in brackets: a folder named "notes (old)" would otherwise
// be a pattern for "notes old".
func globEscape(p string) string {
	var b strings.Builder
	for _, r := range p {
		if strings.ContainsRune("*?[]{}()!+@|", r) {
			b.WriteByte('[')
			b.WriteRune(r)
			b.WriteByte(']')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mcpConfigOf returns what to pass with --mcp-config for the .mcp.json in
// the brain folder dir: the file, or its JSON without the servers the
// folder's settings turn off (see disabledMCPServers), or "" when there is
// nothing to pass. --strict-mcp-config makes the CLI ignore the file
// otherwise. The CLI refuses to start with an --mcp-config file that is not
// a JSON object holding an "mcpServers" object, which it never did for a
// project .mcp.json: `{}` or a misspelt key stops it as surely as a typo.
// Such a file is left out, as one mistake, or a file a manipulated brain
// wrote, would otherwise stop every run of the brain. Broken entries inside
// mcpServers it takes in its stride.
func mcpConfigOf(brainID, dir string) string {
	p := filepath.Join(dir, ".mcp.json")
	data, err := readBrainFile(dir, ".mcp.json")
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	var servers map[string]json.RawMessage
	if err == nil {
		servers, err = mcpServersOf(data)
	}
	var disabled map[string]bool
	if err == nil {
		disabled, err = disabledMCPServers(dir)
	}
	if err != nil {
		log.Printf("brain %s: leaving out %s: %v", brainID, p, err)
		return ""
	}
	kept := make(map[string]json.RawMessage, len(servers))
	for name, server := range servers {
		if !disabled[name] {
			kept[name] = server
		}
	}
	switch {
	case len(kept) == len(servers):
		return p
	case len(kept) == 0:
		return ""
	}
	// --mcp-config takes the JSON itself as well as a file.
	inline, err := json.Marshal(map[string]any{"mcpServers": kept})
	if err != nil {
		log.Printf("brain %s: leaving out %s: %v", brainID, p, err)
		return ""
	}
	return string(inline)
}

// disabledMCPServers returns the .mcp.json servers that the
// disabledMcpjsonServers of the brain folder dir's .claude/settings.json or
// settings.local.json turn off, where Claude Code records a project's
// server you declined. The CLI honours that list for a project's .mcp.json
// but not for --mcp-config, which would start a server you turned off on
// every run. Either file turns a server off, as it does for the CLI, and
// enabledMcpjsonServers keeps no other server from starting there
// (checked against CLI 2.1.302). When a file cannot be read, nothing can
// tell which servers it turns off, so the brain gets none.
func disabledMCPServers(dir string) (map[string]bool, error) {
	disabled := map[string]bool{}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		p := filepath.Join(dir, ".claude", name)
		data, err := readBrainFile(dir, filepath.Join(".claude", name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var settings map[string]json.RawMessage
		if err == nil {
			settings, err = jsonObject(data)
		}
		var names []string
		if raw, ok := settings["disabledMcpjsonServers"]; err == nil && ok {
			err = json.Unmarshal(raw, &names)
		}
		if err != nil {
			return nil, fmt.Errorf("cannot tell which servers %s turns off: %w", p, err)
		}
		for _, n := range names {
			disabled[n] = true
		}
	}
	return disabled, nil
}

// mcpServersOf returns the servers in the .mcp.json data by name, or why
// the CLI would refuse the file: it must be a JSON object whose
// "mcpServers", spelt exactly so, is an object.
func mcpServersOf(data []byte) (map[string]json.RawMessage, error) {
	top, err := jsonObject(data)
	if err != nil {
		return nil, err
	}
	var servers map[string]json.RawMessage
	if raw, ok := top["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, fmt.Errorf("mcpServers is not an object: %w", err)
		}
	}
	if servers == nil {
		return nil, errors.New(`it has no "mcpServers" object`)
	}
	return servers, nil
}

// jsonObject decodes data, a JSON object, by key. Unlike a struct, a map
// matches keys exactly, as the CLI does, and it skips a leading byte order
// mark, as the CLI does too.
func jsonObject(data []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\uFEFF")), &obj); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if obj == nil {
		return nil, errors.New("not a JSON object: null")
	}
	return obj, nil
}

// maxBrainFile bounds what the bridge reads of the files a brain can
// write: its .mcp.json, settings, prompt, identity map and calendar token.
// That is far more than any real one holds, so that a brain cannot make
// the bridge load gigabytes into memory on every run, and little enough to
// pass a .mcp.json on the command line.
const maxBrainFile = 256 << 10

// readAtMost reads r to the end, and refuses it when that is more than
// maxBrainFile.
func readAtMost(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBrainFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBrainFile {
		return nil, fmt.Errorf("larger than %d KiB", maxBrainFile>>10)
	}
	return data, nil
}

// errSymlink is why the bridge refuses a file that a brain could have
// replaced with a symlink.
var errSymlink = errors.New("it is a symlink, which the bridge does not follow")

// openInFolder opens the file name, relative to the brain folder root, for
// the bridge, which works on it outside the sandbox while the brain
// controls what is there: a brain may replace any file in its folder with
// a symlink to ~/.ssh, to another brain's history or to a file the sandbox
// denies it, and have the bridge read that into its prompt or append to
// it. os.Root refuses, at every step of the open, a path that leads out of
// the folder, through a symlinked .claude folder say. It refuses a symlink
// at the file itself too, even one that stays inside, and anything but a
// regular file, and the file it opens must be the one it looked at, so
// that nothing swapped in between gets through. The open does not block,
// so a FIFO cannot hang the bridge either. With os.O_CREATE it creates
// the file when there is none, never through a symlink.
func openInFolder(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	for tries := 0; ; tries++ {
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) && flag&os.O_CREATE != 0 {
			// With O_EXCL, os.Root follows no symlink, not even one
			// planted since the Lstat; that makes the create fail.
			f, err := root.OpenFile(name, flag|os.O_EXCL|syscall.O_NONBLOCK, perm)
			if errors.Is(err, fs.ErrExist) && tries < 2 {
				continue // something appeared there meanwhile: look again
			}
			return f, err
		}
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, errSymlink
		}
		if !fi.Mode().IsRegular() {
			return nil, errors.New("not a regular file")
		}
		f, err := root.OpenFile(name, flag&^os.O_CREATE|syscall.O_NONBLOCK, perm)
		if err != nil {
			return nil, err
		}
		if st, err := f.Stat(); err != nil || !os.SameFile(fi, st) {
			f.Close()
			if err == nil {
				err = errors.New("it was replaced while the bridge opened it")
			}
			return nil, err
		}
		return f, nil
	}
}

// readBrainFile reads the file name, relative to the brain folder dir, as
// openInFolder opens it. It refuses a file larger than maxBrainFile.
func readBrainFile(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openInFolder(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAtMost(f)
}

// invoke runs the selected CLI once for m.
func (br *Brain) invoke(run *brainRun, ctx context.Context, s *brainSettings, m Inbound) RunResult {
	prompt := br.buildPrompt(s, m)

	mcpConfig, settings := "", ""
	if s.provider != "codex" && !s.inheritUser {
		mcpConfig = mcpConfigOf(br.id, br.workDir)
		settings = excludeInstructionsAbove(br.workDir)
	}
	args := claudeArgs(s, mcpConfig, settings)
	if s.provider == "codex" {
		if err := validateCodexTools(s.disallowedTools); err != nil {
			return RunResult{Err: err}
		}
		args = codexArgs(s, br.workDir, br.sandbox != nil && br.sandbox.Enabled())
		prompt = strings.ReplaceAll(prompt, askUserQuestionNote, "If you need clarification, ask in your final reply and end the turn. The answer arrives as a new message.")
	}
	var providerEnv []string
	if s.provider == "codex" && br.hooks != nil && br.hooks.CodexSetup != nil {
		extra, env, instructions, err := br.hooks.CodexSetup(br.id)
		if err != nil {
			return RunResult{Err: err}
		}
		args = withArgsBeforePrint(args, extra)
		providerEnv = env
		prompt = instructions + prompt
	}
	if s.provider != "codex" && br.hooks != nil && br.hooks.ExtraArgs != nil {
		extra := br.hooks.ExtraArgs(br.id)
		args = withArgsBeforePrint(args, extra)
	}

	// Wrap the spawn in sandbox-exec so prompt injection in an incoming message
	// cannot reach another brain's tree or cross-session Claude state. The brain
	// still runs with --dangerously-skip-permissions for tool freedom; the
	// sandbox enforces the read scope at the OS layer regardless. That is also
	// why a sandbox that cannot be set up refuses the run instead of spawning
	// without it: the fallback would quietly drop the only barrier left.
	binary := s.claudePath
	if s.provider == "codex" {
		binary = s.codexPath
	}
	agentBinary := binary
	finalArgs := args
	extraEnv := append([]string{"FRIDOLIN_BRAIN_ID=" + br.id}, dataEnv(s)...)
	extraEnv = append(extraEnv, providerEnv...)
	extraEnv = append(extraEnv, calendarEnv(br.id, s.manifest, s.credsDir)...)
	extraEnv = append(extraEnv, plaudOptOut...)
	if s.toolsDir != "" {
		// Prompts and skills run the helpers as $FRIDOLIN_TOOLS_DIR/<tool>,
		// wherever the bridge is installed.
		extraEnv = append(extraEnv, "FRIDOLIN_TOOLS_DIR="+s.toolsDir)
	}
	if path := s.plaudPATH; path != "" || len(s.ownPATH) > 0 {
		if path == "" {
			path = os.Getenv("PATH")
		}
		dirs := append(slices.Clone(s.ownPATH), filepath.SplitList(path)...)
		extraEnv = append(extraEnv, "PATH="+strings.Join(dirs, string(filepath.ListSeparator))) // replaces the bridge's
	}
	if br.hooks != nil && br.hooks.RunEnv != nil {
		env, done := br.hooks.RunEnv(br.id)
		// After Wait: the tools the run started have exited by then, or
		// are left behind and lose the brain API with the run.
		defer done()
		extraEnv = append(extraEnv, env...)
	}
	if s.provider == "codex" || (br.sandbox != nil && br.sandbox.Enabled()) {
		// The brain's own Claude Code config dir, TMPDIR and caches, the
		// only places outside its folder and inbox the profile lets it
		// write, made before the profile pins them. With a config dir of
		// its own the CLI no longer finds your login in the keychain,
		// which the profile closes anyway: brains log in with
		// CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY from the bridge's
		// environment.
		stateEnv, err := prepareBrainState(s.dataDir, s.mediaDir, br.id, s.inheritUser)
		if err == nil && br.sandbox != nil && br.sandbox.Enabled() {
			binary, finalArgs, err = br.sandbox.Command(br.id, agentBinary, args)
		}
		if err != nil {
			log.Printf("brain %s: sandbox unavailable, refusing to run: %v", br.id, err)
			return RunResult{Err: errSandboxUnavailable}
		}
		extraEnv = append(extraEnv, stateEnv...)
		if s.provider == "codex" {
			extraEnv = append(extraEnv, "CODEX_HOME="+brainStateOf(s.dataDir, br.id).codex)
		}
		// The profile denies writes to the CLI's own install, so its
		// auto-updater could download a new version on every run and never
		// switch to it. This replaces the bridge's own value.
		extraEnv = append(extraEnv, "DISABLE_AUTOUPDATER=1")
	}
	// Only the variables on childEnvAllowlist: anyone who may message the
	// brain can ask it to print its environment.
	scope := claudeChild
	if s.provider == "codex" {
		scope = codexChild
	}
	env := childEnv(scope, s.envPassthrough, extraEnv...)

	// Only once the sandbox is settled: a refused run starts no process.
	br.ensureViewer(s)

	cmd := exec.CommandContext(ctx, binary, finalArgs...)
	cmd.Dir = br.workDir
	cmd.Env = env
	isolateProcessGroup(cmd)

	// The prompt goes on stdin, which -p reads to its end, rather than in
	// argv: argv is bounded by ARG_MAX, about 1 MiB on macOS with the
	// environment, and on Linux one argument by 128 KiB, which a long
	// prompt file and a few pasted texts reach; and every process listing
	// shows it. The CLI takes 10 MB on stdin.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return RunResult{Err: fmt.Errorf("stdin pipe: %w", err)}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{Err: fmt.Errorf("stdout pipe: %w", err)}
	}

	// Only the end of stderr is kept: the CLI, or something it started that
	// holds on to its stderr, could otherwise make the bridge hold any
	// amount of it, and only the last error is of use.
	stderrBuf := newTailBuffer(stderrTailBytes)
	cmd.Stderr = stderrBuf
	// Wait waits for that copy to end, which a background process holding
	// stderr could put off for good; after this long it stops waiting.
	cmd.WaitDelay = runWaitDelay

	if err := cmd.Start(); err != nil {
		return RunResult{Err: fmt.Errorf("starting %s: %w", agentName(s.provider), err)}
	}
	br.setRunPID(run, cmd.Process.Pid)
	// Removed once Wait has returned and the run is over; a bridge that
	// crashes before leaves it to the next start (reapOrphanedRuns).
	if rec := recordRun(s.dataDir, br.id, cmd.Process.Pid); rec != "" {
		defer os.Remove(rec)
	}
	// A panic from here on, in a hook say, skips the Wait below. The run's
	// process group is then stopped and reaped here, before its record
	// goes: cancelling the run alone would leave the CLI a zombie for as
	// long as the bridge runs.
	waited := false
	defer func() {
		if !waited {
			_ = killProcessGroup(cmd.Process.Pid)
			_ = cmd.Wait()
		}
	}()
	go func() {
		// Nothing waits for this: Wait closes the pipe once the CLI has
		// exited, which ends a write it never read.
		_, _ = io.WriteString(stdin, prompt)
		stdin.Close()
	}()

	// Parse the stream in a goroutine so we can race it against the 10s timer.
	parseDone := make(chan RunResult, 1)
	resultSeen := make(chan struct{})
	var resultOnce sync.Once
	go func() {
		// A panic while the output is read, in the hook that sends a
		// question to the chat say, ends the run as one in invoke does
		// (invokeGuarded). In a goroutine of its own, unrecovered, it would
		// end the whole bridge.
		defer func() {
			if p := recover(); p != nil {
				log.Printf("brain %s: reading the output of the run for %s panicked: %v\n%s", br.id, m.JID, p, debug.Stack())
				parseDone <- RunResult{Err: errRunPanicked}
			}
		}()
		parseDone <- br.readAgentStream(m, stdout, runHeading(m, s, cmd.Process.Pid), func() { resultOnce.Do(func() { close(resultSeen) }) }, s.provider)
	}()

	notice := time.NewTimer(workingNoticeAfter)
	defer notice.Stop()

	invocationStart := time.Now()
	var res RunResult
	var grace <-chan time.Time
	resultIn := resultSeen // nil once the grace runs
	for parsing := true; parsing; {
		select {
		case res = <-parseDone:
			parsing = false
		case <-notice.C:
			// Untagged (silent) messages never produce a visible reply, so they
			// must not produce a visible "working" notice either.
			if !m.Silent && br.hooks != nil && br.hooks.Notice != nil {
				br.hooks.Notice(m.JID, workingNotice)
			}
		case <-resultIn:
			// The answer is in, and the CLI exits right after it, unless
			// something it started still holds its output open, a command
			// left running in the background, say. That gets
			// postResultGrace, or the brain would stay busy as long as it ran.
			resultIn, grace = nil, time.After(postResultGrace)
			notice.Stop()
		case <-grace:
			grace = nil
			log.Printf("brain %s: %s's output was still open %v after its result; stopping what is left of the run", br.id, agentName(s.provider), postResultGrace)
			if err := killProcessGroup(cmd.Process.Pid); err != nil {
				log.Printf("brain %s: failed to kill process group %d: %v", br.id, cmd.Process.Pid, err)
			}
		}
	}

	if elapsed := time.Since(invocationStart); elapsed > 5*time.Minute && res.Text != "" {
		log.Printf("brain %s: response took %v for %s (slow response)", br.id, elapsed, m.JID)
	}

	// The result event is final, but the CLI may still be winding down. Ask it
	// to exit, then hard-kill anything still alive 5 seconds later.
	if cmd.Process != nil {
		exited := make(chan struct{})
		defer close(exited)
		go func() {
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				if err := killProcessGroup(cmd.Process.Pid); err == nil {
					log.Printf("brain %s: killed %s process group %d", br.id, agentName(s.provider), cmd.Process.Pid)
				}
			}
		}()
		_ = cmd.Process.Signal(os.Interrupt)
	}

	waitErr := cmd.Wait()
	waited = true
	stderr := strings.TrimSpace(stderrBuf.String())
	br.logStderr(m, cmd.Process.Pid, stderr)
	// Whether the result event came: readStream has returned, so it would
	// have closed resultSeen by now.
	gotResult := false
	select {
	case <-resultSeen:
		gotResult = true
	default:
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// The deadline may come in the grace after the result, which is
		// the run's answer then all the same. A question sent before the
		// run hung is not: the chat must hear that the run was stopped.
		if res.Err != nil || !gotResult {
			return RunResult{Err: runTimedOut(s.runTimeout)}
		}
	case ctx.Err() != nil:
		if waitErr != nil || res.Text == "" {
			return RunResult{Err: errBrainInterrupted}
		}
	case waitErr != nil:
		// Without a result, what the CLI wrote to stderr is the only
		// account of what went wrong.
		if errors.Is(res.Err, errNoResult) {
			return RunResult{Err: fmt.Errorf("%s exited: %w — stderr: %s", agentName(s.provider), waitErr, tail(stderr, 500))}
		}
	}
	return res
}

// postResultGrace is how long a run may keep its output open after its
// result, a variable so tests need not wait for it.
var postResultGrace = 5 * time.Second

// runWaitDelay is how long Wait waits, once the CLI has exited, for the
// copy of its stderr to end.
const runWaitDelay = 5 * time.Second

// logStderr adds what the CLI wrote to stderr to the brain's activity log,
// the end of it when there is much, where a failed run can be looked into
// even when the run succeeded or its error kept only a little of it. The
// block names the run, the run for m with process pid, as its heading
// does (runHeading): runs can end out of the order they started in.
func (br *Brain) logStderr(m Inbound, pid int, stderr string) {
	if stderr == "" {
		return
	}
	f, err := openInFolder(br.root, streamLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("brain %s: could not open stream log: %v", br.id, err)
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "[%s] stderr of the run for %s from %s, pid %d:\n%s\n",
		time.Now().Format("15:04:05"), m.JID, m.Sender, pid, tail(stderr, stderrLogBytes))
}

// stderrLogBytes is how much of a run's stderr goes to its activity log.
const stderrLogBytes = 4000

// The most of a chat's recent messages a run's prompt shows.
const (
	dmHistoryWindow    = 10
	groupHistoryWindow = 20
)

// promptHistoryBudget is how far back from the end of the history a run's
// prompt looks for its chat's messages: far more than a window of them
// takes in a busy brain, so that a chat quiet while others talked still
// gets its last messages, without reading years of history on every run.
var promptHistoryBudget int64 = 16 << 20

// historyBefore returns what m's run is shown of its chat, the one that
// goes by chatJIDs(m), oldest first and at most limit entries in all.
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
func (br *Brain) historyBefore(m Inbound, limit int) (before, since []historyEntry) {
	chats := chatJIDs(m)
	// The answers on their way first, then the history: one delivered in
	// between is in both, and withUndelivered takes it once.
	pending := br.undelivered(chats)
	entries := br.chatHistory(chats, m.enoughBefore(limit))
	at := m.loggedIn(entries)
	if at < 0 {
		before = withUndelivered(entries, pending)
	} else {
		before = entries[:at]
		for _, e := range entries[at+1:] {
			if e.Type == historyOut {
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
func (m Inbound) enoughBefore(limit int) func(e historyEntry) bool {
	var lines []historyEntry
	for _, p := range m.ownParts() {
		if p.logged != (historyEntry{}) {
			lines = append(lines, p.logged)
		}
	}
	older := 0
	return func(e historyEntry) bool {
		if len(lines) > 0 {
			// Newest first: the first of m's lines is the last found.
			if i := slices.IndexFunc(lines, func(l historyEntry) bool { return l.sameLine(e) }); i >= 0 {
				lines = slices.Delete(lines, i, i+1)
			}
			return false
		}
		older++
		return older >= limit
	}
}

// chatHistory returns the entries of the chat that goes by chats in the
// brain's history, oldest first. It reads them back from the end of the
// file, each newest first to enough, until enough reports that it has
// what it needs, or it reaches the chat's newest reset marker (see
// historyReset), or promptHistoryBudget is read, or the file's start is
// reached. A DM's lines may be under its LID or its number, and both are
// the one chat. It returns none when the history cannot be read (see
// openInFolder).
func (br *Brain) chatHistory(chats []string, enough func(historyEntry) bool) []historyEntry {
	var entries []historyEntry
	br.scanHistory(promptHistoryBudget, mentionsChat(chats), func(e historyEntry) bool {
		if !slices.Contains(chats, e.Chat) {
			return true
		}
		if e.Type == historyReset {
			return false
		}
		entries = append(entries, e)
		return !enough(e)
	})
	slices.Reverse(entries)
	return entries
}

// loggedIn returns where m's own lines begin in entries, the chat's
// history: at the last line that is the line of the first of m's parts
// still there (see Inbound.logged), or -1 for none. A part deleted since
// took its line along; one edited while it waited has its line rewritten,
// with the new text.
func (m Inbound) loggedIn(entries []historyEntry) int {
	for _, p := range m.ownParts() {
		if p.logged == (historyEntry{}) {
			continue
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if p.logged.sameLine(entries[i]) {
				return i
			}
		}
	}
	return -1
}

// sameLine reports whether e is the history line l: equal, or for a
// message from WhatsApp, whose line an edit rewrites, equal but for the
// text.
func (l historyEntry) sameLine(e historyEntry) bool {
	if l.ID != "" {
		e.Text = l.Text
	}
	return e == l
}

// identities reads the brain's identity map, through the folder: a map
// swapped for a symlink to another brain's would put that brain's contacts
// in this one's prompt.
func (br *Brain) identities() map[string]string {
	idMap, err := br.readFile(identityMapFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("brain %s: could not read identity map %s: %v", br.id, identityMapFile, err)
	}
	return parseIdentityMap(idMap)
}

// buildPrompt gathers what m's run is told, through the brain's folder: its
// prompt file, the chat's recent history and the identity map. renderPrompt
// puts it into words.
func (br *Brain) buildPrompt(s *brainSettings, m Inbound) string {
	in := promptInput{Now: time.Now(), BrainID: br.id, Message: m, ObserveOnly: m.Silent}
	if name := s.promptFile; name != "" {
		if data, err := br.readFile(name); err != nil {
			log.Printf("brain %s: could not read prompt file %s: %v", br.id, name, err)
		} else {
			in.Instructions = strings.TrimSpace(string(data))
		}
	}
	limit := dmHistoryWindow
	if m.IsGroup() {
		limit = groupHistoryWindow
	}
	in.History, in.Since = br.historyBefore(m, limit)
	in.Names = br.identities()
	return renderPrompt(in)
}

// parseStream reads the stream-json output of m's run from the Claude CLI
// and returns the result, writing a human-readable activity log alongside
// it.
func (br *Brain) parseStream(m Inbound, reader io.Reader) RunResult {
	return br.readStream(m, reader, "── New invocation ──", nil)
}

// runHeading starts a run's part of the activity log: what it answers and
// with what, so that it can be told from the others and matched to the
// run the dashboard shows.
func runHeading(m Inbound, s *brainSettings, pid int) string {
	model := s.model
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

// runHeadingPID returns the pid of the run whose heading line is, and
// false for any other line of the activity log.
func runHeadingPID(line string) (int, bool) {
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
	fi, err := br.root.Lstat(streamLogName)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < streamLogMax {
		return
	}
	if err := br.root.Rename(streamLogName, streamLogOld); err != nil {
		log.Printf("brain %s: could not rotate its stream log: %v", br.id, err)
	}
}

// readStream is parseStream with heading as the first line of the run in
// the activity log, calling onResult, when set, as soon as the result is
// in, while the stream may still go on.
func (br *Brain) readStream(m Inbound, reader io.Reader, heading string, onResult func()) RunResult {
	return br.readAgentStream(m, reader, heading, onResult, "claude")
}

func (br *Brain) readAgentStream(m Inbound, reader io.Reader, heading string, onResult func(), provider string) RunResult {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	br.rotateStreamLog()
	// Through the folder, like the history: a brain can swap the log for a
	// symlink to a file its sandbox denies it writing.
	streamLog, logErr := openInFolder(br.root, streamLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if logErr != nil {
		log.Printf("brain %s: could not open stream log: %v", br.id, logErr)
	}
	writeLog := func(msg string) {
		if streamLog != nil {
			_, _ = fmt.Fprintf(streamLog, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
		}
	}
	defer func() {
		if streamLog != nil {
			streamLog.Close()
		}
	}()

	writeLog(heading)

	var finalResult, lastText string
	asked := false
	var codex codexStream

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var event streamEvent
		if provider == "codex" {
			event = codex.event([]byte(line))
		} else if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Type {
		case "assistant":
			if event.Message == nil {
				continue
			}
			for _, content := range event.Message.Content {
				switch content.Type {
				case "text":
					if content.Text != "" {
						lastText = content.Text
						writeLog("💬 " + truncate(content.Text, 2000))
					}
				case "tool_use":
					writeLog("🔧 Tool: " + content.Name)
					if content.Name == "AskUserQuestion" {
						// Untagged (silent) messages never produce a visible
						// reply, so they must not ask anything either. Only a
						// question on its way counts as asked.
						question := extractAskQuestion(content.Input)
						if question != "" && !m.Silent && br.hooks != nil && br.hooks.Question != nil && br.hooks.Question(br, m.JID, questionPrefix+question) {
							asked = true
						}
					}
				}
			}

		case "result":
			if event.IsError || event.Error != "" {
				errMsg := event.Error
				if errMsg == "" {
					errMsg = event.Result
				}
				if errMsg == "" {
					// The CLI reports some failures (authentication, quota) only
					// as its last message.
					errMsg = truncate(lastText, 300)
				}
				if errMsg == "" {
					errMsg = "(claude exited with an error but no message)"
				}
				writeLog("❌ Error: " + errMsg)
				return RunResult{Err: &cliError{msg: errMsg, provider: provider}, Asked: asked}
			}
			finalResult = event.Result
			writeLog("✅ Result: " + truncate(finalResult, 2000))
			if onResult != nil {
				onResult()
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		if errors.Is(err, os.ErrClosed) {
			return RunResult{Err: errBrainInterrupted}
		}
		return RunResult{Err: fmt.Errorf("reading %s output: %w", agentName(provider), err)}
	}

	// A -p run is stateless and nobody answers its AskUserQuestion, so the
	// model goes on after asking, often to an answer of its own ("I booked
	// Monday"), which the chat must get too. A run that only asked has said
	// what it had to say.
	if finalResult == "" && !asked {
		if provider == "codex" && codex.err != "" {
			return RunResult{Err: &cliError{msg: codex.err, provider: "codex"}}
		}
		return RunResult{Err: errNoResult}
	}

	return RunResult{Text: finalResult, Asked: asked}
}

// truncate shortens s to at most n bytes without splitting a UTF-8 rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// tail is truncate's counterpart for the end of s, for the last lines of
// a command's output: s without surrounding space, cut after an ellipsis
// to its last n bytes when longer, or a few fewer so that no rune is cut
// in two.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !isRuneStart(s[i]) {
		i++
	}
	return "…" + s[i:]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func (br *Brain) finishRun(run *brainRun) {
	run.cancel()

	br.mu.Lock()
	if br.current == run {
		br.current = nil
	}
	br.mu.Unlock()

	run.once.Do(func() { close(run.done) })
}

func (br *Brain) setRunPID(run *brainRun, pid int) {
	br.mu.Lock()
	run.pid = pid
	br.mu.Unlock()
}

// interruptRun cancels a run and kills its process.
func (br *Brain) interruptRun(run *brainRun) {
	if run == nil {
		return
	}

	run.cancel()

	br.mu.Lock()
	pid := run.pid
	br.mu.Unlock()

	if pid <= 0 {
		return
	}
	// The run's process group: Claude and anything it started.
	if err := killProcessGroup(pid); err != nil {
		log.Printf("brain %s: failed to kill process group %d: %v", br.id, pid, err)
	}
}

// appendHistory appends one JSONL entry to the brain's history file.
func (br *Brain) appendHistory(chatJID, from, text, typ string) {
	br.appendHistoryVia(chatJID, from, text, typ, "")
}

// appendHistoryVia is appendHistory for a line marked with how it came in
// (see historyEntry.Via).
func (br *Brain) appendHistoryVia(chatJID, from, text, typ, via string) {
	br.appendEntry(historyEntry{From: from, Chat: chatJID, Text: text, Type: typ, Via: via})
}

// appendEntry appends entry to the brain's history file, stamped with the
// time now unless it has a time. It returns the line as it reads back, or
// none when the file could not be opened.
func (br *Brain) appendEntry(entry historyEntry) historyEntry {
	br.historyMu.Lock()
	entry, err := br.writeEntryLocked(entry)
	br.historyMu.Unlock()
	br.entryWritten(entry, err)
	if err != nil {
		return historyEntry{}
	}
	return entry
}

// recordDelivered writes the brain's answer text to chat into its
// history once it reached WhatsApp, after remove took it out of the
// outbox; when remove fails, it writes nothing and returns the error, and
// the outbox tries again later, so the answer is recorded once. Both
// happen under historyMu, which undelivered reads the outbox under: a run
// finds the answer in the outbox or in the history, never in neither.
func (br *Brain) recordDelivered(chat, text string, remove func() error) error {
	br.historyMu.Lock()
	if err := remove(); err != nil {
		br.historyMu.Unlock()
		return err
	}
	entry, err := br.writeEntryLocked(historyEntry{From: br.id, Chat: chat, Text: text, Type: historyOut})
	br.historyMu.Unlock()
	br.entryWritten(entry, err)
	return nil
}

// writeEntryLocked appends entry to the history file, stamped with the
// time now unless it has a time, and returns it as it reads back, which
// historyBefore looks for: invalid UTF-8 in its text is written as U+FFFD.
// historyMu is held.
func (br *Brain) writeEntryLocked(entry historyEntry) (historyEntry, error) {
	if entry.TS == "" {
		entry.TS = time.Now().Format(time.RFC3339)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return entry, err
	}
	var written historyEntry
	if json.Unmarshal(data, &written) == nil {
		entry = written
	}
	// Through the folder: appending through a symlink would let a brain
	// write to files its sandbox denies it, another brain's among them.
	f, err := openInFolder(br.root, historyFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return entry, err
	}
	_, _ = f.Write(append(data, '\n'))
	f.Close()
	return entry, nil
}

// entryWritten logs an entry that could not be written, or hands one that
// was to OnHistory, outside historyMu.
func (br *Brain) entryWritten(entry historyEntry, err error) {
	if err != nil {
		log.Printf("brain %s: failed to open history file %s: %v", br.id, filepath.Join(br.workDir, historyFileName), err)
		return
	}
	if br.hooks != nil && br.hooks.OnHistory != nil {
		br.hooks.OnHistory(br.id, entry)
	}
}

// undelivered returns the brain's answers in the chat that goes by chats
// which are still on their way (BrainHooks.Undelivered). It reads them
// under historyMu, which recordDelivered moves an answer from the outbox
// into the history under: the history, read after, holds every answer
// this did not return.
func (br *Brain) undelivered(chats []string) []historyEntry {
	if br.hooks == nil || br.hooks.Undelivered == nil {
		return nil
	}
	br.historyMu.Lock()
	all := br.hooks.Undelivered(br.id)
	br.historyMu.Unlock()
	var out []historyEntry
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
func withUndelivered(entries, pending []historyEntry) []historyEntry {
	at := func(e historyEntry) time.Time {
		t, _ := time.Parse(time.RFC3339, e.TS)
		return t
	}
	for _, p := range pending {
		pt := at(p)
		if slices.ContainsFunc(entries, func(e historyEntry) bool {
			return e.Type == historyOut && e.Chat == p.Chat && e.Text == p.Text && !at(e).Before(pt)
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

// ── BrainManager ─────────────────────────────────────────────────────────

// BrainManager owns the brains and routes messages to them. Its brain set can
// be replaced at any time with Apply: unchanged brains keep running untouched,
// removed brains finish being stopped in the background.
type BrainManager struct {
	reloadMu sync.Mutex // serialises Apply

	mu       sync.RWMutex // guards the fields below; never held while calling into a Brain
	brains   map[string]*Brain
	order    []*Brain
	byGroup  map[string]*Brain
	dmBrain  *Brain // brain with handles_dms; nil falls through to fallback
	fallback *Brain // brain marked default, else the first brain
	issues   []BrainIssue
	// held routes what an approved brain left out (see
	// BrainIssue.leavesOut) would take to that brain's id, so that no
	// other brain gets it meanwhile: your DMs from a DM brain whose
	// folder has a problem went to the default brain. See heldRoutes.
	held heldRoutes

	profiler *SandboxProfiler
	hooks    *BrainHooks
	// slots bound the runs of every brain together, across reloads.
	slots *runSlots
}

// NewBrainManager builds the configured brains.
func NewBrainManager(cfg *Config) (*BrainManager, error) {
	bm := &BrainManager{
		brains:   map[string]*Brain{},
		byGroup:  map[string]*Brain{},
		profiler: NewSandboxProfiler(nil, cfg),
		hooks:    &BrainHooks{},
		slots:    newRunSlots(cfg.RunLimit()),
	}
	if err := bm.Apply(cfg); err != nil {
		return nil, err
	}
	return bm, nil
}

// SetHooks wires the brains to the bridge. Call before the first message.
func (bm *BrainManager) SetHooks(h BrainHooks) { *bm.hooks = h }

// Apply makes the running brains match cfg. The bridge's PATH goes first,
// before any brain folder is checked against it: the folders of it inside
// a brain's folder leave it for that brain's runs (see setBridgePATH).
func (bm *BrainManager) Apply(cfg *Config) error {
	ownPATH := setBridgePATH(cfg)
	specs, issues := ResolveBrains(cfg)
	return bm.reload(specs, issues, cfg, ownPATH)
}

// reload runs the brains specs, with issues the problems ResolveBrains
// found and ownPATH each brain's own PATH folders (see setBridgePATH).
func (bm *BrainManager) reload(specs []BrainSpec, issues []BrainIssue, cfg *Config, ownPATH map[string][]string) error {
	bm.reloadMu.Lock()
	defer bm.reloadMu.Unlock()

	if err := ValidateBrainSet(specs); err != nil {
		return err
	}

	bm.mu.RLock()
	old := bm.brains
	bm.mu.RUnlock()

	next := make(map[string]*Brain, len(specs))
	order := make([]*Brain, 0, len(specs))
	byGroup := map[string]*Brain{}
	var dm, def *Brain
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
		s := settingsFor(cfg, sp)
		s.ownPATH = ownPATH[m.ID]
		br, ok := old[m.ID]
		if !ok || br.workDir != sp.Dir || !br.holdsFolder(sp.Dir) {
			root, err := os.OpenRoot(sp.Dir)
			if err != nil {
				log.Printf("brain %s: leaving it out: %v", m.ID, err)
				issues = append(issues, BrainIssue{ID: m.ID, Path: sp.Dir, Kind: "missing", Detail: "the folder cannot be opened: " + err.Error(), atLoad: true, entry: sp.entry, routing: m.Routing})
				continue
			}
			br = &Brain{
				id:      m.ID,
				workDir: sp.Dir,
				root:    root,
				sandbox: bm.profiler,
				hooks:   bm.hooks,
				slots:   bm.slots,
			}
			created = append(created, br)
		}
		br.settings.Store(s)
		if !s.viewer {
			br.closeViewer() // the viewer setting was turned off
		}

		next[m.ID] = br
		order = append(order, br)
		for _, g := range m.Routing.Groups {
			byGroup[g] = br
		}
		if m.Routing.HandlesDMs {
			dm = br
		}
		if m.Routing.Default {
			def = br
		}
	}
	held := heldRoutesOf(issues, dm, byGroup)
	if def == nil && len(order) > 0 && held.fallback == "" {
		def = order[0]
	}

	var removed []*Brain
	for id, br := range old {
		if nb, ok := next[id]; !ok || nb != br {
			removed = append(removed, br)
		}
	}

	bm.profiler.Update(workDirs, cfg)
	bm.slots.setLimit(cfg.RunLimit())

	bm.mu.Lock()
	bm.brains, bm.order, bm.byGroup, bm.dmBrain, bm.fallback, bm.issues, bm.held = next, order, byGroup, dm, def, issues, held
	bm.mu.Unlock()
	applied = true

	for _, br := range removed {
		if nb, ok := next[br.id]; ok {
			log.Printf("brain %s: now in the folder at %s; stopping it in the old one", br.id, nb.workDir)
		} else {
			log.Printf("brain %s: removed from configuration; stopping it", br.id)
		}
		_, moved := next[br.id]
		go br.retire(!moved)
	}
	return nil
}

// settingsFor combines global settings with a brain's own.
func settingsFor(cfg *Config, sp BrainSpec) *brainSettings {
	m := sp.Manifest
	model := m.Model
	if model == "" {
		model = cfg.BrainModel
	}
	effort := m.Effort
	if effort == "" {
		effort = cfg.BrainEffort
	}
	tools := cfg.DisallowedTools
	if m.DisallowedTools != nil {
		tools = *m.DisallowedTools
	}
	if cfg.AgentProvider == "codex" {
		model, effort = cfg.CodexModel, cfg.CodexEffort
	}
	return &brainSettings{
		name:            m.Name,
		manifest:        m,
		claudePath:      cfg.ClaudePath,
		provider:        cfg.AgentProvider,
		codexPath:       cfg.CodexPath,
		model:           model,
		effort:          effort,
		disallowedTools: append([]string(nil), tools...),
		promptFile:      sp.PromptFile,
		dangerously:     cfg.DangerouslySkipPermissions,
		inheritUser:     cfg.InheritClaudeUserConfig,
		otpGuard:        cfg.OTPGuard,
		otpMarkers:      append([]string(nil), cfg.OTPMarkers...),
		viewer:          cfg.Viewer,
		runTimeout:      cfg.RunTimeout(),
		queueMode:       cfg.Queue.Mode,
		maxPending:      cfg.Queue.MaxPending,
		observe:         m.Observe,
		credsDir:        credentialsDirOf(cfg),
		mediaDir:        cfg.MediaDir,
		envPassthrough:  append([]string(nil), cfg.ChildEnvPassthrough...),
		toolsDir:        ownLayout().Tools,
		// Absolute: brains run in their own folder, where a relative
		// data_dir would name another one.
		dataDir:   absPath(cfg.DataDir),
		dbPath:    absPath(cfg.DBPath),
		plaudPATH: brainPlaudPATH(cfg),
	}
}

// dataEnv tells brains where the bridge keeps its data. The tools in
// tools/ read these until the bridge answered them (brainapi.go); compat:
// brains and their scripts rely on these names, as on the other BRIDGE_
// variables.
func dataEnv(s *brainSettings) []string {
	return []string{"BRIDGE_DATA_DIR=" + s.dataDir, "BRIDGE_DB_PATH=" + s.dbPath}
}

// credentialsDirOf is where per-brain credential files live.
func credentialsDirOf(cfg *Config) string {
	if cfg.Sandbox.CredentialsDir != "" {
		return cfg.Sandbox.CredentialsDir
	}
	return cfg.DataDir
}

// heldRoutes are the chats of approved brains that are left out, by the
// id of the brain that would take them.
type heldRoutes struct {
	dms      string            // the DM brain's
	groups   map[string]string // by group JID
	fallback string            // the default brain's: what no other brain takes
}

// heldRoutesOf returns what the brains issues leaves out would take, of
// what no brain that runs (dm, the DM brain, and byGroup) takes instead.
func heldRoutesOf(issues []BrainIssue, dm *Brain, byGroup map[string]*Brain) heldRoutes {
	h := heldRoutes{groups: map[string]string{}}
	for _, is := range issues {
		if !is.leavesOut() {
			continue
		}
		if is.routing.HandlesDMs && dm == nil && h.dms == "" {
			h.dms = is.ID
		}
		for _, g := range is.routing.Groups {
			if _, ok := byGroup[g]; !ok && h.groups[g] == "" {
				h.groups[g] = is.ID
			}
		}
		if is.routing.Default && h.fallback == "" {
			h.fallback = is.ID
		}
	}
	return h
}

// BrainFor returns the brain that handles the given chat, or nil: also
// when the brain that would is left out (see HeldFor), rather than the
// default brain.
func (bm *BrainManager) BrainFor(jid string, isGroup bool) *Brain {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	if isGroup {
		if br, ok := bm.byGroup[jid]; ok {
			return br
		}
		if bm.held.groups[jid] != "" {
			return nil
		}
		return bm.fallback
	}
	if bm.dmBrain != nil {
		return bm.dmBrain
	}
	if bm.held.dms != "" {
		return nil
	}
	return bm.fallback
}

// HeldFor returns the id of the brain left out (see BrainIssue.leavesOut)
// that would handle the given chat, which therefore goes to no brain, or
// "" when BrainFor answers for it.
func (bm *BrainManager) HeldFor(jid string, isGroup bool) string {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	if isGroup {
		if _, ok := bm.byGroup[jid]; ok {
			return ""
		}
		if id := bm.held.groups[jid]; id != "" {
			return id
		}
	} else {
		if bm.dmBrain != nil {
			return ""
		}
		if bm.held.dms != "" {
			return bm.held.dms
		}
	}
	if bm.fallback == nil {
		return bm.held.fallback
	}
	return ""
}

// DMBrain returns the brain that gets direct messages: the one with
// handles_dms, else the fallback brain, or nil.
func (bm *BrainManager) DMBrain() *Brain { return bm.BrainFor("", false) }

// Get returns a brain by id.
func (bm *BrainManager) Get(id string) *Brain {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.brains[id]
}

// Brains returns the brains in configuration order.
func (bm *BrainManager) Brains() []*Brain {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return append([]*Brain(nil), bm.order...)
}

// Issues returns the problems found the last time the brains were loaded.
func (bm *BrainManager) Issues() []BrainIssue {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return append([]BrainIssue(nil), bm.issues...)
}

// Route returns the brain that handles m: the brain it names, else the one
// its chat routes to. A message that names a brain that no longer exists gets
// nil rather than whichever brain owns its chat.
func (bm *BrainManager) Route(m Inbound) *Brain {
	if m.Brain != "" {
		return bm.Get(m.Brain)
	}
	return bm.BrainFor(m.JID, m.IsGroup())
}

// Submit routes a message to its brain. A message that lands on a brain being
// removed at that moment is routed again once, with its files where that
// brain left them (see Brain.submit).
func (bm *BrainManager) Submit(m Inbound) (br *Brain, pos int, busy bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		br = bm.Route(m)
		if br == nil && m.Brain != "" {
			return nil, 0, false, fmt.Errorf("brain %s is not configured", m.Brain)
		}
		if br == nil {
			return nil, 0, false, fmt.Errorf("no brain configured for %s", m.JID)
		}
		pos, busy, err = br.submit(&m)
		if !errors.Is(err, errBrainRetired) {
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
func (bm *BrainManager) StopHandOffs(jid string) (stopped bool, dropped int) {
	for _, br := range bm.Brains() {
		s, d := br.StopHandOffs(jid)
		stopped, dropped = stopped || s, dropped+d
	}
	return stopped, dropped
}

// KillRuns kills every brain's run in flight at once, for a second signal
// while the bridge stops: nothing waits for the runs to end.
func (bm *BrainManager) KillRuns() {
	for _, br := range bm.Brains() {
		br.mu.Lock()
		cur := br.current
		br.mu.Unlock()
		br.interruptRun(cur)
	}
}

// Stop interrupts every run and drops every queue.
func (bm *BrainManager) Stop() {
	var wg sync.WaitGroup
	for _, br := range bm.Brains() {
		wg.Add(1)
		go func(br *Brain) {
			defer wg.Done()
			br.retire(false)
		}(br)
	}
	wg.Wait()
}
