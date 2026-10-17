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
	Type string `json:"type"` // "in" or "out"
}

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

// parseIdentityMap parses a brain's contact map and returns normalized
// JID → name.
//
// The map is a markdown table at <work_dir>/memories/people/whatsapp-identity-map.md:
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
		// A cell like "+" or "@lid" holds no number to look up.
		number := normalizeJID(jid)
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
// code. With no markers given it uses the built-in list.
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
		if entry.Type != "out" || entry.From != brainID {
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

	mu      sync.Mutex
	current *brainRun
	pending []Inbound
	working bool // a worker goroutine is draining pending
	retired bool // removed from the configuration
	last    *RunInfo

	// tmuxViewer is an optional Terminal window tailing this brain's activity log.
	tmuxViewer *TmuxViewer
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
	queueMode       string
	maxPending      int
	credsDir        string // where per-brain credential files live
}

// BrainHooks connect brains to the rest of the bridge.
type BrainHooks struct {
	// Intermediate delivers a message before the final answer: the
	// "working on it" notice and questions the brain asks mid-run.
	Intermediate func(jid, text string)
	// Deliver receives every finished run.
	Deliver func(br *Brain, m Inbound, res RunResult)
	// OnHistory sees every line written to a brain's history.
	OnHistory func(brainID string, e historyEntry)
	// ExtraArgs adds CLI arguments to a brain's runs (the site builder's
	// plugin and MCP server, for brains allowed to build the site).
	ExtraArgs func(brainID string) []string
}

// RunResult is the outcome of one brain run.
type RunResult struct {
	Text        string
	AlreadySent bool // a question was forwarded mid-run; don't send Text too
	Err         error
	Duration    time.Duration
}

// RunInfo summarises the last finished run, for the dashboard.
type RunInfo struct {
	Chat     string    `json:"chat"`
	Finished time.Time `json:"finished"`
	Seconds  float64   `json:"seconds"`
	Outcome  string    `json:"outcome"` // ok, error, interrupted, question
	Error    string    `json:"error,omitempty"`
}

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
	errBrainRetired     = errors.New("brain was removed")
	errQueueFull        = errors.New("brain queue is full")
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

// Submit queues a message for the brain. It returns the message's position in
// the queue (1 = next) and whether it has to wait for other work; a run it
// interrupts does not count.
func (br *Brain) Submit(m Inbound) (pos int, busy bool, err error) {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	s := br.settings.Load()

	// History first, so the run (and any later run) sees the message even when
	// it is merged into a queue that is already moving.
	br.appendHistory(m.JID, m.Sender, historyTextWithMedia(m.textWithQuote(), m.Media), "in")

	br.mu.Lock()
	if br.retired {
		br.mu.Unlock()
		return 0, false, errBrainRetired
	}
	busy = br.current != nil || len(br.pending) > 0

	// A newer message from the same chat supersedes queued untagged chatter:
	// it is already in the history the next run reads. In interrupt mode, a
	// tagged message also replaces its sender's own earlier request in that
	// chat, waiting or running. Nothing else is touched: other chats, and
	// other people's requests in a group, keep their place, and untagged
	// chatter never interrupts a run. Neither do ingested files, nor are they
	// replaced: each is a job of its own, not a correction of the last one.
	interrupting := s.queueMode == "interrupt" && !m.Silent && m.Source != "ingest"
	replaces := func(p Inbound) bool {
		return p.JID == m.JID && (p.Silent || (interrupting && p.Sender == m.Sender && p.Source != "ingest"))
	}
	kept := br.pending[:0]
	for _, p := range br.pending {
		if !replaces(p) {
			kept = append(kept, p)
		}
	}
	br.pending = kept
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
			return 0, busy, errQueueFull
		}
	}
	var interrupt *brainRun
	if interrupting && br.current != nil && replaces(br.current.msg) {
		interrupt = br.current
		busy = len(br.pending) > 0
	}
	br.pending = append(br.pending, m)
	pos = len(br.pending)
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
		ctx, cancel := context.WithCancel(context.Background())
		run := &brainRun{cancel: cancel, done: make(chan struct{}), msg: m, started: time.Now()}
		br.current = run
		br.mu.Unlock()

		s := br.settings.Load()
		if m.Silent {
			log.Printf("brain %s: running silently for untagged message from %s", br.id, m.Sender)
		}
		res := br.invoke(run, ctx, s, m)
		res.Duration = time.Since(run.started)
		if res.Err == nil && !res.AlreadySent && res.Text != "" {
			br.appendHistory(m.JID, br.id, res.Text, "out")
		}
		br.finishRun(run)
		br.recordLast(m, res)

		if br.hooks != nil && br.hooks.Deliver != nil {
			br.hooks.Deliver(br, m, res)
		}
	}
}

func (br *Brain) recordLast(m Inbound, res RunResult) {
	info := &RunInfo{Chat: m.JID, Finished: time.Now(), Seconds: res.Duration.Seconds(), Outcome: "ok"}
	switch {
	case errors.Is(res.Err, errBrainInterrupted):
		info.Outcome = "interrupted"
	case res.Err != nil:
		info.Outcome, info.Error = "error", res.Err.Error()
	case res.AlreadySent:
		info.Outcome = "question"
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
	if clearQueue {
		br.pending = nil
	}
	cur := br.current
	br.mu.Unlock()
	return br.stopRun(cur)
}

// StopChat stops what the brain does for one chat: the run in flight if it
// is that chat's, and the chat's waiting messages. One brain serves several
// chats, so nothing another chat asked for is touched. It reports whether a
// run was stopped and how many waiting messages were dropped.
func (br *Brain) StopChat(jid string) (stopped bool, dropped int) {
	br.mu.Lock()
	kept := br.pending[:0]
	for _, p := range br.pending {
		if p.JID == jid {
			dropped++
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
	return br.stopRun(cur), dropped
}

// StopHandOffs stops the messages handed to this brain (/ingest, Plaud) that
// answer in jid: the run in flight if it is one, and those waiting. The rest
// of the brain's work goes on: jid may well be another brain's chat. It
// reports whether a run was stopped and how many waiting messages were
// dropped.
func (br *Brain) StopHandOffs(jid string) (stopped bool, dropped int) {
	handOff := func(m Inbound) bool { return m.Brain != "" && m.JID == jid }
	br.mu.Lock()
	kept := br.pending[:0]
	for _, p := range br.pending {
		if handOff(p) {
			dropped++
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
	return br.stopRun(cur), dropped
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

// retire stops the brain for good after it was removed from the configuration.
func (br *Brain) retire() {
	br.mu.Lock()
	br.retired = true
	br.pending = nil
	br.mu.Unlock()
	br.StopCurrent(true)
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
func (br *Brain) ensureViewer(s *brainSettings) {
	if !s.viewer {
		return
	}
	if br.tmuxViewer != nil && br.tmuxViewer.IsAlive() {
		return
	}
	// tail fails on a log that does not exist yet, so create it first,
	// through the folder as a run opens it: touch would have followed a
	// dangling symlink there and created the file it points to.
	f, err := openInFolder(br.root, streamLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("brain %s: no tmux viewer: could not open stream log: %v", br.id, err)
		return
	}
	f.Close()
	tv, err := startViewer("claude-"+br.id, br.streamLogPath(), br.workDir)
	if err != nil {
		log.Printf("brain %s: could not start tmux viewer: %v", br.id, err)
		return
	}
	br.tmuxViewer = tv
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
	data, err := json.Marshal(map[string][]string{"claudeMdExcludes": instructionsAbove(dir, os.Getenv("HOME"))})
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
	env := append(os.Environ(), calendarEnv(br.id, s.manifest, s.credsDir)...)
	if br.sandbox != nil && br.sandbox.Enabled() {
		sbBinary, sbArgs, sbErr := br.sandbox.Command(br.id, s.claudePath, args)
		if sbErr != nil {
			log.Printf("brain %s: sandbox unavailable, refusing to run: %v", br.id, sbErr)
			return RunResult{Err: errSandboxUnavailable}
		}
		binary, finalArgs = sbBinary, sbArgs
		// The profile denies writes to the CLI's own install, so its
		// auto-updater could download a new version on every run and never
		// switch to it. The last value wins, whatever the bridge's own is.
		env = append(env, "DISABLE_AUTOUPDATER=1")
	}

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

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return RunResult{Err: fmt.Errorf("starting claude: %w", err)}
	}
	br.setRunPID(run, cmd.Process.Pid)

	// Parse the stream in a goroutine so we can race it against the 10s timer.
	parseDone := make(chan RunResult, 1)
	go func() {
		parseDone <- br.parseStream(m.JID, stdout)
	}()

	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	invocationStart := time.Now()
	var res RunResult
	select {
	case res = <-parseDone:
	case <-timer.C:
		// Untagged (silent) messages never produce a visible reply, so they
		// must not produce a visible "working" notice either.
		if !m.Silent && br.hooks != nil && br.hooks.Intermediate != nil {
			br.hooks.Intermediate(m.JID, "⏳ Working on it...")
		}
		res = <-parseDone
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

	if waitErr := cmd.Wait(); waitErr != nil {
		if ctx.Err() != nil {
			return RunResult{Err: errBrainInterrupted}
		}
		stderr := strings.TrimSpace(stderrBuf.String())
		if res.Err == nil && res.Text == "" && !res.AlreadySent {
			return RunResult{Err: fmt.Errorf("claude exited: %w — stderr: %s", waitErr, truncate(stderr, 500))}
		}
	}
	if ctx.Err() != nil && res.Text == "" {
		return RunResult{Err: errBrainInterrupted}
	}
	return res
}

// composePreface reads the chat history for chatJID and formats recent messages:
// the last 10 for a DM, the last 20 for a group.
func (br *Brain) composePreface(chatJID string, isGroup bool) string {
	histFile, err := openInFolder(br.root, historyFileName, os.O_RDONLY, 0)
	if err != nil {
		return ""
	}
	defer histFile.Close()

	scanner := bufio.NewScanner(histFile)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var entries []historyEntry
	for scanner.Scan() {
		var entry historyEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Chat != chatJID {
			continue
		}
		entries = append(entries, entry)
	}

	limit := 10
	if isGroup {
		limit = 20
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	if len(entries) == 0 {
		return ""
	}

	// Through the folder: a map swapped for a symlink to another brain's
	// would put that brain's contacts in this one's prompt.
	idMap, err := br.readFile(identityMapFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("brain %s: could not read identity map %s: %v", br.id, identityMapFile, err)
	}
	identities := parseIdentityMap(idMap)

	var sb strings.Builder
	for _, entry := range entries {
		ts, _ := time.Parse(time.RFC3339, entry.TS)
		timeStr := ts.Format("15:04")

		var senderDisplay string
		if entry.Type == "out" {
			senderDisplay = fmt.Sprintf("You (%s)", br.id)
		} else if name, found := identities[normalizeJID(entry.From)]; found {
			senderDisplay = fmt.Sprintf("%s (%s)", name, entry.From)
		} else {
			senderDisplay = entry.From
		}

		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", timeStr, senderDisplay, entry.Text))
	}
	return sb.String()
}

// buildPrompt constructs the -p prompt: the prompt file, chat-history
// preface, attachments, then the new message.
func (br *Brain) buildPrompt(s *brainSettings, m Inbound) string {
	var sb strings.Builder
	isGroup := m.IsGroup()

	if name := s.promptFile; name != "" {
		if sysPrompt, err := br.readFile(name); err != nil {
			log.Printf("brain %s: could not read prompt file %s: %v", br.id, name, err)
		} else {
			sb.WriteString(strings.TrimSpace(string(sysPrompt)))
			sb.WriteString("\n\n")
		}
	}

	if preface := br.composePreface(m.JID, isGroup); preface != "" {
		count := "10 messages"
		if isGroup {
			count = "20 messages"
		}
		sb.WriteString("<chat_history>\n")
		sb.WriteString("The following is the last " + count + " in this WhatsApp chat (for context, not action). ")
		sb.WriteString("You do NOT have prior turns in your working memory — the only context you have about previous conversation is what's in this block. ")
		sb.WriteString("Names come from your identity map at `memories/people/whatsapp-identity-map.md`; the JID in parens is the raw WhatsApp identifier. ")
		sb.WriteString("For anything older or more detailed, search `conversation-history.jsonl` with tools/history_search.py.\n\n")
		sb.WriteString(preface)
		sb.WriteString("</chat_history>\n\n")
	}

	switch len(m.Media) {
	case 0:
	case 1:
		sb.WriteString(fmt.Sprintf("A file was shared via WhatsApp and saved to: %s\n\n", m.Media[0]))
	default:
		sb.WriteString(fmt.Sprintf("%d files were shared together and saved to:\n", len(m.Media)))
		for _, p := range m.Media {
			sb.WriteString("- " + p + "\n")
		}
		sb.WriteString("Handle them together as one request.\n\n")
	}

	sb.WriteString("<new_message>\n")
	sb.WriteString(fmt.Sprintf("%s says: %s\n", m.Sender, m.textWithQuote()))
	sb.WriteString("</new_message>")
	return sb.String()
}

// parseStream reads the stream-json output from the Claude CLI and returns
// the result, writing a human-readable activity log alongside it.
func (br *Brain) parseStream(jid string, reader io.Reader) RunResult {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

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

	writeLog("── New invocation ──")

	var finalResult, lastText string
	questionSent := false

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
						question := extractAskQuestion(content.Input)
						if question != "" && br.hooks != nil && br.hooks.Intermediate != nil {
							br.hooks.Intermediate(jid, question)
							questionSent = true
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
				return RunResult{Err: fmt.Errorf("claude error: %s", errMsg)}
			}
			finalResult = event.Result
			writeLog("✅ Result: " + truncate(finalResult, 2000))
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		if errors.Is(err, os.ErrClosed) {
			return RunResult{Err: errBrainInterrupted}
		}
		return RunResult{Err: fmt.Errorf("reading claude output: %w", err)}
	}

	if questionSent {
		// The question already went to WhatsApp; sending the final text too
		// would duplicate it.
		return RunResult{AlreadySent: true}
	}

	if finalResult == "" {
		return RunResult{Err: fmt.Errorf("no result from claude (empty output)")}
	}

	return RunResult{Text: finalResult}
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
	entry := historyEntry{
		TS:   time.Now().Format(time.RFC3339),
		From: from,
		Chat: chatJID,
		Text: text,
		Type: typ,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	// Through the folder: appending through a symlink would let a brain
	// write to files its sandbox denies it, another brain's among them.
	f, err := openInFolder(br.root, historyFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("brain %s: failed to open history file %s: %v", br.id, filepath.Join(br.workDir, historyFileName), err)
		return
	}
	_, _ = f.Write(append(data, '\n'))
	f.Close()
	if br.hooks != nil && br.hooks.OnHistory != nil {
		br.hooks.OnHistory(br.id, entry)
	}
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

// SetIntermediateSendFn wires the mid-run notification callback.
func (bm *BrainManager) SetIntermediateSendFn(fn func(jid, text string)) { bm.hooks.Intermediate = fn }

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
				issues = append(issues, BrainIssue{ID: m.ID, Path: sp.Dir, Kind: "missing", Detail: "the folder cannot be opened: " + err.Error(), atLoad: true})
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
		go br.retire()
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
	if model == "" {
		model = cfg.Model
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
		queueMode:       cfg.Queue.Mode,
		maxPending:      cfg.Queue.MaxPending,
		credsDir:        credentialsDirOf(cfg),
	}
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
			br.retire()
		}(br)
	}
	wg.Wait()
}
