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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
)

// historyEntry is one line in a brain's conversation-history.jsonl file.
type historyEntry struct {
	TS   string `json:"ts"`
	From string `json:"from"`
	Chat string `json:"chat"` // JID of the chat (DM participant or group)
	Text string `json:"text"`
	Type string `json:"type"` // historyIn, historyOut or historyEvent
	// Via marks an "out" line that came in through /send: "brain" (From is
	// the brain that sent it), "script" (the scripts token) or "api" (no
	// token). A brain's own replies have none.
	Via string `json:"via,omitempty"`
	// ID is the WhatsApp message's ID for a message from WhatsApp, which an
	// edit or a deletion of it names (see recordChange).
	ID string `json:"id,omitempty"`
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

// bareCodeRe matches a message that is nothing but a 4–8 digit number.
var bareCodeRe = regexp.MustCompile(`^\s*\d{4,8}\s*$`)

// defaultOTPMarkers are the substrings that mark an outbound message as a
// request for a one-time code. When the last thing the brain said contains one
// of these and the reply is a bare numeric code, the message is dropped
// instead of being fed to the model — a 2FA code pasted into the chat should
// not end up in a prompt. Add your own (a bank's name, say) with otp_markers.
var defaultOTPMarkers = []string{"🔐", "OTP", "one-time code", "2FA", "verification code"}

// recentOutboundLooksLikeOTPRequest reports whether any of the brain's last 50
// outbound messages in lines, the tail of its history, asked for a one-time
// code: its own replies, and what went out in its chats through /send, from
// a script or a brain. With no markers given it uses the built-in list.
func recentOutboundLooksLikeOTPRequest(lines []string, brainID string, markers ...string) bool {
	if len(markers) == 0 {
		markers = defaultOTPMarkers
	}
	start := len(lines) - 50
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		var entry historyEntry
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			continue
		}
		if entry.Type != historyOut || (entry.From != brainID && entry.Via == "") {
			continue
		}
		for _, marker := range markers {
			if marker != "" && strings.Contains(entry.Text, marker) {
				return true
			}
		}
	}
	return false
}

// tailLines returns the complete lines in the last maxBytes of f.
func tailLines(f *os.File, maxBytes int64) []string {
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := st.Size() - maxBytes
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line was cut in half
	}
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// ── Brain ────────────────────────────────────────────────────────────────

// Brain is one isolated Claude Code context: its own folder, conversation
// history and prompt file.
//
// A brain does not keep a Claude process alive between messages. Each message
// spawns a fresh `claude -p` run seeded with the recent chat history. Messages
// for a brain run one at a time from its queue (or, in interrupt mode, a newer
// request from the same person in the same chat stops the run in flight).
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

	// historyMu serialises the bridge's writes to the history file: an
	// append must not land in a copy that a rewrite then drops (see
	// rewriteHistory).
	historyMu sync.Mutex

	mu      sync.Mutex
	current *brainRun
	pending []Inbound
	working bool // a worker goroutine is draining pending
	retired bool // removed from the configuration
	last    *RunInfo

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
	envPassthrough  []string
	toolsDir        string // the bridge's Python helpers, or ""
	dataDir         string // the data dir, absolute
	dbPath          string // the bridge's DB, absolute
	plaudPATH       string // the PATH to run with when brains may use Plaud's CLI
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
	// RunEnv adds variables to one run's environment (the brain API's
	// address and a token for the run), and returns what to call once the
	// run is over.
	RunEnv func(brainID string) (env []string, done func())
}

// A run that takes longer than workingNoticeAfter tells the chat it is
// working on it, and questions it asks mid-run go out marked as such.
const (
	workingNotice  = "⏳ Working on it..."
	questionPrefix = "❓ "
)

// workingNoticeAfter is a variable so tests need not wait for it.
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
	errNoResult         = errors.New("no result from claude (empty output)")
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

// DropsOTP reports whether text is a one-time code the brain asked for, which
// must not be written to history or sent to the model.
func (br *Brain) DropsOTP(text string) bool {
	s := br.settings.Load()
	return s.otpGuard && bareCodeRe.MatchString(text) &&
		recentOutboundLooksLikeOTPRequest(br.historyTail(256*1024), br.id, s.otpMarkers...)
}

// historyFileName is the brain's conversation history, in its folder.
const historyFileName = "conversation-history.jsonl"

// historyTail returns the complete lines in the last maxBytes of the
// brain's history, or none when it cannot be read (see openInFolder).
func (br *Brain) historyTail(maxBytes int64) []string {
	f, err := openInFolder(br.root, historyFileName, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	return tailLines(f, maxBytes)
}

// logInbound writes m to the brain's history: a line for each part of a
// merged message, with its ID, so that one deleted later is taken out
// alone. It returns m with each part's line kept on it (Inbound.logged),
// so that its own run shows the message once, as the new message, after
// the chat's history before those lines.
func (br *Brain) logInbound(m Inbound) Inbound {
	parts := m.ownParts()
	for i, p := range parts {
		parts[i].logged = br.appendEntry(historyEntry{Chat: m.JID, From: p.Sender, Text: historyTextWithMedia(p.textWithQuote(), p.Media), Type: historyIn, ID: p.ID})
	}
	if len(m.parts) > 0 {
		m.parts = parts
	}
	m.logged = parts[0].logged
	return m
}

// Submit hands a message to the brain: it goes into the history and, unless
// it is untagged chatter that gets no run (see observeRuns), into the
// queue. It returns the message's position in the queue (1 = next, 0 for
// none) and whether it has to wait for other work; a run it interrupts
// does not count.
func (br *Brain) Submit(m Inbound) (pos int, busy bool, err error) {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	s := br.settings.Load()

	// History first, so that later runs see the message even when it is
	// merged into a queue that is already moving.
	m = br.logInbound(m)

	br.mu.Lock()
	if br.retired {
		br.mu.Unlock()
		return 0, false, errBrainRetired
	}
	// Untagged chatter needs nothing more, unless the brain observes it with
	// runs: the next run in the chat reads it in the history.
	if m.Silent && !observeRuns(s.observe, m) {
		br.mu.Unlock()
		return 0, false, nil
	}

	// A newer message from the same chat supersedes queued untagged chatter:
	// it is already in the history the next run reads. In interrupt mode, a
	// tagged message also replaces its sender's own earlier request in that
	// chat, waiting or running. No other request is touched: other chats,
	// and other people's requests in a group, keep their place, and untagged
	// chatter never interrupts a run. Nor do ingested files interrupt a
	// request, or get replaced: each is a job of its own, not a correction of
	// the last one. Observe-only runs are another matter, see below.
	interrupting := s.queueMode == queueModeInterrupt && !m.Silent && m.Source != sourceIngest
	replaces := func(p Inbound) bool {
		return p.JID == m.JID && (p.Silent || (interrupting && p.Sender == m.Sender && p.Source != sourceIngest))
	}
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
	at := len(br.pending)
	if !m.Silent {
		if i := slices.IndexFunc(br.pending, func(p Inbound) bool { return p.Silent }); i >= 0 {
			at = i
		}
	}
	br.pending = slices.Insert(br.pending, at, m)
	pos = at + 1
	busy = at > 0 || (br.current != nil && interrupt == nil)
	start := !br.working
	br.working = true
	br.mu.Unlock()

	if interrupt != nil {
		br.interruptRun(interrupt)
	}
	if start {
		go br.worker()
	}
	return pos, busy, nil
}

// worker drains the queue one message at a time.
func (br *Brain) worker() {
	for {
		br.mu.Lock()
		if br.retired || len(br.pending) == 0 {
			br.working = false
			br.mu.Unlock()
			return
		}
		m := br.pending[0]
		br.pending = br.pending[1:]
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
		br.recordLast(m, res)
		br.deliverGuarded(m, res)
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

// Snapshot returns the brain's live state.
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
	for _, p := range br.pending {
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
// the brain's excludeInstructionsAbove.
func claudeArgs(s *brainSettings, mcpConfig, settings, prompt string) []string {
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
	return append(args, "-p", prompt)
}

// withArgsBeforePrompt puts extra before the trailing "-p <prompt>" that
// claudeArgs ends with, where an option list such as --mcp-config's ends.
func withArgsBeforePrompt(args, extra []string) []string {
	if len(extra) == 0 || len(args) < 2 {
		return args
	}
	out := append([]string{}, args[:len(args)-2]...)
	out = append(out, extra...)
	return append(out, args[len(args)-2:]...)
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
// pass a .mcp.json or the prompt on the command line.
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

// invoke runs the Claude CLI once for m.
func (br *Brain) invoke(run *brainRun, ctx context.Context, s *brainSettings, m Inbound) RunResult {
	prompt := br.buildPrompt(s, m)

	mcpConfig, settings := "", ""
	if !s.inheritUser {
		mcpConfig = mcpConfigOf(br.id, br.workDir)
		settings = excludeInstructionsAbove(br.workDir)
	}
	args := claudeArgs(s, mcpConfig, settings, prompt)
	if br.hooks != nil && br.hooks.ExtraArgs != nil {
		args = withArgsBeforePrompt(args, br.hooks.ExtraArgs(br.id))
	}

	// Wrap the spawn in sandbox-exec so prompt injection in an incoming message
	// cannot reach another brain's tree or cross-session Claude state. The brain
	// still runs with --dangerously-skip-permissions for tool freedom; the
	// sandbox enforces the read scope at the OS layer regardless. That is also
	// why a sandbox that cannot be set up refuses the run instead of spawning
	// without it: the fallback would quietly drop the only barrier left.
	binary := s.claudePath
	finalArgs := args
	extraEnv := append([]string{"FRIDOLIN_BRAIN_ID=" + br.id}, dataEnv(s)...)
	extraEnv = append(extraEnv, calendarEnv(br.id, s.manifest, s.credsDir)...)
	extraEnv = append(extraEnv, plaudOptOut...)
	if s.toolsDir != "" {
		// Prompts and skills run the helpers as $FRIDOLIN_TOOLS_DIR/<tool>,
		// wherever the bridge is installed.
		extraEnv = append(extraEnv, "FRIDOLIN_TOOLS_DIR="+s.toolsDir)
	}
	if s.plaudPATH != "" {
		extraEnv = append(extraEnv, "PATH="+s.plaudPATH) // replaces the bridge's
	}
	if br.hooks != nil && br.hooks.RunEnv != nil {
		env, done := br.hooks.RunEnv(br.id)
		// After Wait: the tools the run started have exited by then, or
		// are left behind and lose the brain API with the run.
		defer done()
		extraEnv = append(extraEnv, env...)
	}
	if br.sandbox != nil && br.sandbox.Enabled() {
		sbBinary, sbArgs, sbErr := br.sandbox.Command(br.id, s.claudePath, args)
		if sbErr != nil {
			log.Printf("brain %s: sandbox unavailable, refusing to run: %v", br.id, sbErr)
			return RunResult{Err: errSandboxUnavailable}
		}
		binary, finalArgs = sbBinary, sbArgs
		// The profile denies writes to the CLI's own install, so its
		// auto-updater could download a new version on every run and never
		// switch to it. This replaces the bridge's own value.
		extraEnv = append(extraEnv, "DISABLE_AUTOUPDATER=1")
	}
	// Only the variables on childEnvAllowlist: anyone who may message the
	// brain can ask it to print its environment.
	env := childEnv(claudeChild, s.envPassthrough, extraEnv...)

	// Only once the sandbox is settled: a refused run starts no process.
	br.ensureViewer(s)

	cmd := exec.CommandContext(ctx, binary, finalArgs...)
	cmd.Dir = br.workDir
	cmd.Env = env
	isolateProcessGroup(cmd)

	// stdin must be /dev/null or the CLI blocks waiting for input.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return RunResult{Err: fmt.Errorf("opening /dev/null: %w", err)}
	}
	defer devNull.Close()
	cmd.Stdin = devNull

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
		return RunResult{Err: fmt.Errorf("starting claude: %w", err)}
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
		parseDone <- br.readStream(m, stdout, runHeading(m, s, cmd.Process.Pid), func() { resultOnce.Do(func() { close(resultSeen) }) })
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
			log.Printf("brain %s: claude's output was still open %v after its result; stopping what is left of the run", br.id, postResultGrace)
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
					log.Printf("brain %s: killed claude process group %d", br.id, cmd.Process.Pid)
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
			return RunResult{Err: fmt.Errorf("claude exited: %w — stderr: %s", waitErr, tail(stderr, 500))}
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

// promptHistoryTail is how much of the end of the history a run's prompt
// is looked for in first: far more than a window of messages takes.
const promptHistoryTail = 1 << 20

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
	split := func(entries []historyEntry) {
		at := m.loggedIn(entries)
		if at < 0 {
			before, since = withUndelivered(entries, pending), nil
			return
		}
		before, since = entries[:at], nil
		for _, e := range entries[at+1:] {
			if e.Type == historyOut {
				since = append(since, e)
			}
		}
		since = withUndelivered(since, pending)
	}
	entries, whole := br.chatHistory(chats, promptHistoryTail)
	split(entries)
	if len(before) < limit && !whole {
		// A chat that was quiet while others were busy: its window is
		// further back than the end of the file.
		entries, _ = br.chatHistory(chats, 0)
		split(entries)
	}
	// The newest count most: what was sent since, then what came before.
	since = since[max(0, len(since)-limit):]
	before = before[max(0, len(before)-(limit-len(since))):]
	return before, since
}

// chatHistory returns the entries of the chat that goes by chats in the
// brain's history, oldest first: those in the last tail bytes of the file,
// or all of them for a tail of 0, and whether that was all. A DM's lines
// may be under its LID or its number, and both are the one chat. It
// returns none when the history cannot be read (see openInFolder).
func (br *Brain) chatHistory(chats []string, tail int64) (entries []historyEntry, whole bool) {
	f, err := openInFolder(br.root, historyFileName, os.O_RDONLY, 0)
	if err != nil {
		return nil, true
	}
	defer f.Close()
	keep := func(line []byte) {
		var e historyEntry
		if json.Unmarshal(line, &e) == nil && slices.Contains(chats, e.Chat) {
			entries = append(entries, e)
		}
	}
	if st, err := f.Stat(); err == nil && tail > 0 && st.Size() > tail {
		for _, line := range tailLines(f, tail) {
			keep([]byte(line))
		}
		return entries, false
	}
	// A line at a time, however long one is.
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			keep(line)
		}
		if err != nil {
			return entries, true
		}
	}
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
	in := promptInput{Now: time.Now(), BrainID: br.id, Message: m}
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
	if len(in.History) > 0 {
		in.Names = br.identities()
	}
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

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
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
				return RunResult{Err: &cliError{msg: errMsg}, Asked: asked}
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
		return RunResult{Err: fmt.Errorf("reading claude output: %w", err)}
	}

	// A -p run is stateless and nobody answers its AskUserQuestion, so the
	// model goes on after asking, often to an answer of its own ("I booked
	// Monday"), which the chat must get too. A run that only asked has said
	// what it had to say.
	if finalResult == "" && !asked {
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
// time now. It returns the line as it reads back, or none when the file
// could not be opened.
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
// time now, and returns it as it reads back, which historyBefore looks
// for: invalid UTF-8 in its text is written as U+FFFD. historyMu is held.
func (br *Brain) writeEntryLocked(entry historyEntry) (historyEntry, error) {
	entry.TS = time.Now().Format(time.RFC3339)
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

	profiler *SandboxProfiler
	hooks    *BrainHooks
}

// NewBrainManager builds the configured brains.
func NewBrainManager(cfg *Config) (*BrainManager, error) {
	bm := &BrainManager{
		brains:   map[string]*Brain{},
		byGroup:  map[string]*Brain{},
		profiler: NewSandboxProfiler(nil, cfg),
		hooks:    &BrainHooks{},
	}
	if err := bm.Apply(cfg); err != nil {
		return nil, err
	}
	return bm, nil
}

// SetHooks wires the brains to the bridge. Call before the first message.
func (bm *BrainManager) SetHooks(h BrainHooks) { *bm.hooks = h }

// Apply makes the running brains match cfg.
func (bm *BrainManager) Apply(cfg *Config) error {
	specs, issues := ResolveBrains(cfg)
	return bm.reload(specs, issues, cfg)
}

func (bm *BrainManager) reload(specs []BrainSpec, issues []BrainIssue, cfg *Config) error {
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
		br, ok := old[m.ID]
		if !ok || br.workDir != sp.Dir || !br.holdsFolder(sp.Dir) {
			root, err := os.OpenRoot(sp.Dir)
			if err != nil {
				log.Printf("brain %s: leaving it out: %v", m.ID, err)
				issues = append(issues, BrainIssue{ID: m.ID, Path: sp.Dir, Kind: "missing", Detail: "the folder cannot be opened: " + err.Error(), atLoad: true, entry: sp.entry})
				continue
			}
			br = &Brain{
				id:      m.ID,
				workDir: sp.Dir,
				root:    root,
				sandbox: bm.profiler,
				hooks:   bm.hooks,
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
	if def == nil && len(order) > 0 {
		def = order[0]
	}

	var removed []*Brain
	for id, br := range old {
		if nb, ok := next[id]; !ok || nb != br {
			removed = append(removed, br)
		}
	}

	bm.profiler.Update(workDirs, cfg)

	bm.mu.Lock()
	bm.brains, bm.order, bm.byGroup, bm.dmBrain, bm.fallback, bm.issues = next, order, byGroup, dm, def, issues
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
	return &brainSettings{
		name:            m.Name,
		manifest:        m,
		claudePath:      cfg.ClaudePath,
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

// BrainFor returns the brain that handles the given chat, or nil.
func (bm *BrainManager) BrainFor(jid string, isGroup bool) *Brain {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	if isGroup {
		if br, ok := bm.byGroup[jid]; ok {
			return br
		}
		return bm.fallback
	}
	if bm.dmBrain != nil {
		return bm.dmBrain
	}
	return bm.fallback
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
// removed at that moment is routed again once.
func (bm *BrainManager) Submit(m Inbound) (br *Brain, pos int, busy bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		br = bm.Route(m)
		if br == nil && m.Brain != "" {
			return nil, 0, false, fmt.Errorf("brain %s is not configured", m.Brain)
		}
		if br == nil {
			return nil, 0, false, fmt.Errorf("no brain configured for %s", m.JID)
		}
		pos, busy, err = br.Submit(m)
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
