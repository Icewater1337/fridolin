package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
func normalizeJID(jid string) string {
	if idx := strings.Index(jid, "@"); idx != -1 {
		jid = jid[:idx]
	}
	return strings.TrimPrefix(jid, "+")
}

// parseIdentityMap reads a brain's contact map and returns normalized JID → name.
//
// The map is a markdown table at <work_dir>/memories/people/whatsapp-identity-map.md:
//
//	| WhatsApp Number | Person | Notes |
//	| --------------- | ------ | ----- |
//	| 15551234567     | Alex   | …     |
//
// It is entirely optional — without it, chat history shows raw JIDs.
func parseIdentityMap(mapPath string) map[string]string {
	identities := make(map[string]string)

	data, err := os.ReadFile(mapPath)
	if err != nil {
		return identities
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || strings.Contains(line, "---") {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}

		jid := strings.TrimSpace(parts[1])
		name := strings.TrimSpace(parts[2])
		if jid == "" || name == "" || jid == "WhatsApp Number" {
			continue
		}
		identities[normalizeJID(jid)] = name
	}

	return identities
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
// outbound messages asked for a one-time code. With no markers given it uses
// the built-in list.
func recentOutboundLooksLikeOTPRequest(historyFile, brainID string, markers ...string) bool {
	if len(markers) == 0 {
		markers = defaultOTPMarkers
	}
	lines := tailLines(historyFile, 256*1024)
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

// tailLines returns the complete lines in the last maxBytes of a file.
func tailLines(path string, maxBytes int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
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
// history, system prompt and stored session.
//
// A brain does not keep a Claude process alive between messages. Each message
// spawns a fresh `claude -p` run seeded with the recent chat history. Messages
// for a brain run one at a time from its queue (or, in interrupt mode, a newer
// message stops the run in flight).
type Brain struct {
	// Fixed for the brain's lifetime. Changing a brain's folder replaces the
	// Brain; everything else is in settings and changes live.
	id          string
	workDir     string
	historyFile string
	storeKey    string

	settings atomic.Pointer[brainSettings]
	store    *Store
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
	source          string // "inline" or "folder"
	manifest        BrainManifest
	claudePath      string
	model           string
	effort          string
	disallowedTools []string
	promptPath      string
	dangerously     bool
	otpGuard        bool
	otpMarkers      []string
	viewer          bool
	queueMode       string
	maxPending      int
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
		recentOutboundLooksLikeOTPRequest(br.historyFile, br.id, s.otpMarkers...)
}

// Submit queues a message for the brain. It returns the message's position in
// the queue (1 = next) and whether the brain was busy when it arrived.
func (br *Brain) Submit(m Inbound) (pos int, busy bool, err error) {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	s := br.settings.Load()

	// History first, so the run (and any later run) sees the message even when
	// it is merged into a queue that is already moving.
	br.appendHistory(m.JID, m.Sender, historyTextWithMedia(m.Text, m.Media), "in")

	br.mu.Lock()
	if br.retired {
		br.mu.Unlock()
		return 0, false, errBrainRetired
	}
	busy = br.current != nil || len(br.pending) > 0

	var interrupt *brainRun
	if s.queueMode == "interrupt" {
		br.pending = br.pending[:0]
		interrupt = br.current
	} else {
		// A newer message from the same chat supersedes queued untagged
		// chatter: it is already in the history the next run reads.
		kept := br.pending[:0]
		for _, p := range br.pending {
			if !(p.Silent && p.JID == m.JID) {
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

// StopCurrent kills the run in flight and, with clearQueue, drops everything
// waiting. It reports whether a run was stopped.
func (br *Brain) StopCurrent(clearQueue bool) bool {
	br.mu.Lock()
	if clearQueue {
		br.pending = nil
	}
	cur := br.current
	br.mu.Unlock()
	if cur == nil {
		return false
	}
	br.interruptRun(cur)
	select {
	case <-cur.done:
	case <-time.After(10 * time.Second):
		log.Printf("brain %s: run did not stop within 10s", br.id)
	}
	return true
}

// ResetSession stops the brain and clears its stored session.
func (br *Brain) ResetSession() error {
	br.StopCurrent(true)
	return errors.Join(
		br.store.DeleteSession(br.storeKey),
		br.store.DeleteProcessPID(br.storeKey),
	)
}

// retire stops the brain for good after it was removed from the configuration.
func (br *Brain) retire() {
	br.mu.Lock()
	br.retired = true
	br.pending = nil
	br.mu.Unlock()
	br.StopCurrent(true)
}

// BrainRuntime is a brain's live state, for the dashboard.
type BrainRuntime struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Dir      string        `json:"dir"`
	Source   string        `json:"source"`
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
		ID: br.id, Name: s.name, Dir: br.workDir, Source: s.source,
		Routing: s.manifest.Routing, Model: s.model, Effort: s.effort,
		Backup: s.manifest.Backup, Manifest: s.manifest, Queue: []QueuedView{},
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if cur := br.current; cur != nil {
		rt.Running = &RunView{Chat: cur.msg.JID, Sender: cur.msg.Sender, Preview: truncate(cur.msg.Text, 160),
			Silent: cur.msg.Silent, Started: cur.started, PID: cur.pid}
	}
	for _, p := range br.pending {
		rt.Queue = append(rt.Queue, QueuedView{Chat: p.JID, Sender: p.Sender, Preview: truncate(p.Text, 160), Silent: p.Silent, Queued: p.At})
	}
	if br.last != nil {
		l := *br.last
		rt.Last = &l
	}
	return rt
}

// ensureViewer starts the tmux activity viewer if enabled and not running.
func (br *Brain) ensureViewer(s *brainSettings) {
	if !s.viewer {
		return
	}
	if br.tmuxViewer != nil && br.tmuxViewer.IsAlive() {
		return
	}
	tv, err := NewTmuxViewer("claude-"+br.id, br.streamLogPath(), br.workDir)
	if err != nil {
		log.Printf("brain %s: could not start tmux viewer: %v", br.id, err)
		return
	}
	br.tmuxViewer = tv
}

func (br *Brain) streamLogPath() string {
	return filepath.Join(br.workDir, "claude-stream.log")
}

// claudeArgs builds the CLI arguments for one run.
func claudeArgs(s *brainSettings, prompt string) []string {
	args := []string{"--output-format", "stream-json", "--verbose"}
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

// invoke runs the Claude CLI once for m.
func (br *Brain) invoke(run *brainRun, ctx context.Context, s *brainSettings, m Inbound) RunResult {
	prompt := br.buildPrompt(s, m)

	br.ensureViewer(s)

	args := claudeArgs(s, prompt)

	// Wrap the spawn in sandbox-exec so prompt injection in an incoming message
	// cannot reach another brain's tree or cross-session Claude state. The brain
	// still runs with --dangerously-skip-permissions for tool freedom; the
	// sandbox enforces the read scope at the OS layer regardless.
	binary := s.claudePath
	finalArgs := args
	var sbProfilePath string
	if br.sandbox != nil && br.sandbox.Enabled() {
		if profilePath, sbErr := br.sandbox.Write(br.id); sbErr != nil {
			log.Printf("brain %s: sandbox profile generation failed (%v); falling back to unsandboxed spawn", br.id, sbErr)
		} else {
			sbProfilePath = profilePath
			binary = sandboxExecPath
			finalArgs = append([]string{"-f", sbProfilePath, s.claudePath}, args...)
		}
	}

	cmd := exec.CommandContext(ctx, binary, finalArgs...)
	cmd.Dir = br.workDir
	if sbProfilePath != "" {
		defer os.Remove(sbProfilePath)
	}

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
	if err := br.store.SaveProcessPID(br.storeKey, cmd.Process.Pid); err != nil {
		log.Printf("brain %s: failed to save pid %d: %v", br.id, cmd.Process.Pid, err)
	}

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
				if err := cmd.Process.Kill(); err == nil {
					log.Printf("brain %s: sent SIGKILL to claude process %d", br.id, cmd.Process.Pid)
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
	histFile, err := os.Open(br.historyFile)
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

	identities := parseIdentityMap(filepath.Join(br.workDir, "memories", "people", "whatsapp-identity-map.md"))

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

// buildPrompt constructs the -p prompt: system prompt, chat-history preface,
// attachments, then the new message.
func (br *Brain) buildPrompt(s *brainSettings, m Inbound) string {
	var sb strings.Builder
	isGroup := m.IsGroup()

	if s.promptPath != "" {
		sysPrompt, err := os.ReadFile(s.promptPath)
		if err != nil {
			log.Printf("brain %s: could not read prompt file %s: %v", br.id, s.promptPath, err)
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
	sb.WriteString(fmt.Sprintf("%s says: %s\n", m.Sender, m.Text))
	sb.WriteString("</new_message>")
	return sb.String()
}

// parseStream reads the stream-json output from the Claude CLI and returns
// the result, writing a human-readable activity log alongside it.
func (br *Brain) parseStream(jid string, reader io.Reader) RunResult {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	streamLog, logErr := os.OpenFile(br.streamLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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

	var finalResult string
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

	_ = br.store.DeleteProcessPID(br.storeKey)
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

	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if err := proc.Kill(); err != nil && !strings.Contains(err.Error(), "process already finished") {
		log.Printf("brain %s: failed to kill pid %d: %v", br.id, pid, err)
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
	f, err := os.OpenFile(br.historyFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("brain %s: failed to open history file %s: %v", br.id, br.historyFile, err)
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
	store    *Store
	hooks    *BrainHooks
}

// NewBrainManager builds the configured brains.
func NewBrainManager(cfg *Config, store *Store) (*BrainManager, error) {
	bm := &BrainManager{
		brains:   map[string]*Brain{},
		byGroup:  map[string]*Brain{},
		profiler: NewSandboxProfiler(nil, cfg),
		store:    store,
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
	workDirs := map[string]string{}

	for _, sp := range specs {
		if sp.Source == "inline" {
			if err := os.MkdirAll(sp.Dir, 0o700); err != nil {
				return fmt.Errorf("creating work dir %s: %w", sp.Dir, err)
			}
		}
		m := sp.Manifest
		s := settingsFor(cfg, sp)
		br, ok := old[m.ID]
		if !ok || br.workDir != sp.Dir {
			br = &Brain{
				id:          m.ID,
				workDir:     sp.Dir,
				historyFile: filepath.Join(sp.Dir, "conversation-history.jsonl"),
				storeKey:    "brain:" + m.ID,
				store:       bm.store,
				sandbox:     bm.profiler,
				hooks:       bm.hooks,
			}
		}
		br.settings.Store(s)

		next[m.ID] = br
		order = append(order, br)
		workDirs[m.ID] = sp.Dir
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

	for _, br := range removed {
		log.Printf("brain %s: removed from configuration; stopping it", br.id)
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
		source:          sp.Source,
		manifest:        m,
		claudePath:      cfg.ClaudePath,
		model:           model,
		effort:          effort,
		disallowedTools: append([]string(nil), tools...),
		promptPath:      sp.PromptPath,
		dangerously:     cfg.DangerouslySkipPermissions,
		otpGuard:        cfg.OTPGuard,
		otpMarkers:      append([]string(nil), cfg.OTPMarkers...),
		viewer:          cfg.Viewer,
		queueMode:       cfg.Queue.Mode,
		maxPending:      cfg.Queue.MaxPending,
	}
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

// Submit routes a message to its brain. A message that lands on a brain being
// removed at that moment is routed again once.
func (bm *BrainManager) Submit(m Inbound) (br *Brain, pos int, busy bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		br = bm.BrainFor(m.JID, m.IsGroup())
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

// ResetSession clears the stored session for the brain that owns the chat.
func (bm *BrainManager) ResetSession(jid string, isGroup bool) error {
	br := bm.BrainFor(jid, isGroup)
	if br == nil {
		return fmt.Errorf("no brain configured for %s", jid)
	}
	return br.ResetSession()
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
