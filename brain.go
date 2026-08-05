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

// otpMarkers are the substrings that mark an outbound message as a request for
// a one-time code. When the last thing the brain said contains one of these and
// the reply is a bare numeric code, the message is dropped instead of being fed
// to the model — a 2FA code pasted into the chat should not end up in a prompt.
var otpMarkers = []string{"🔐", "OTP", "one-time code", "2FA", "verification code"}

// recentOutboundLooksLikeOTPRequest reports whether any of the brain's last 50
// outbound messages asked for a one-time code.
func recentOutboundLooksLikeOTPRequest(historyFile, brainName string) bool {
	file, err := os.Open(historyFile)
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
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
		if entry.Type != "out" || entry.From != brainName {
			continue
		}
		for _, marker := range otpMarkers {
			if strings.Contains(entry.Text, marker) {
				return true
			}
		}
	}

	return false
}

// Brain is one isolated Claude Code context: its own working directory,
// conversation history, system prompt, and stored session.
//
// A brain does not keep a Claude process alive between messages. Each message
// spawns a fresh `claude -p` run seeded with the recent chat history, and a
// newer message interrupts the run in flight.
type Brain struct {
	name        string // brain name from config, e.g. "personal"
	storeKey    string // key in the sessions table, e.g. "brain:personal"
	workDir     string
	historyFile string // append-only JSONL conversation log
	promptFile  string // path to the system-prompt markdown file
	claudePath  string
	model       string
	dangerously bool
	otpGuard    bool
	viewer      bool

	// sandbox is the profile builder shared by all brains; nil disables sandboxing.
	sandbox *SandboxProfiler

	mu      sync.Mutex
	current *brainRun
	store   *Store

	// tmuxViewer is an optional Terminal window tailing this brain's activity log.
	tmuxViewer *TmuxViewer

	// IntermediateSendFn delivers a message to WhatsApp before the brain's
	// final response (working notifications, AskUserQuestion, etc.).
	IntermediateSendFn func(jid, text string)
}

type brainRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	pid    int
	once   sync.Once // guards close(done) against a double close
}

var errBrainInterrupted = errors.New("brain invocation interrupted by newer message")

// brainResult is the internal return value from a single brain invocation.
type brainResult struct {
	text        string
	alreadySent bool // true when AskUserQuestion was forwarded mid-stream
	err         error
}

// ProcessMessage delivers an incoming WhatsApp message to the brain, blocks
// until the brain responds, and returns the response text.
// Returns ("", nil) when an AskUserQuestion was already forwarded to WhatsApp
// and no further message should be sent.
func (br *Brain) ProcessMessage(ctx context.Context, jid, sender, text, mediaPath string, silent bool) (string, error) {
	// The OTP guard runs before anything is written to history, so a stray
	// two-factor code is neither logged nor sent to the model.
	if br.otpGuard && bareCodeRe.MatchString(text) &&
		recentOutboundLooksLikeOTPRequest(br.historyFile, br.name) {
		log.Printf("brain %s: dropping bare numeric code (OTP guard)", br.name)
		return "", nil
	}

	run, runCtx, err := br.acquireRun(ctx)
	if err != nil {
		return "", err
	}
	defer br.finishRun(run)

	br.appendHistory(jid, sender, text, "in")

	if silent {
		log.Printf("brain %s: running silently for untagged message from %s", br.name, sender)
	}

	res := br.invoke(run, runCtx, jid, sender, text, mediaPath, silent)

	if res.err != nil {
		return "", res.err
	}
	if !res.alreadySent && res.text != "" {
		br.appendHistory(jid, br.name, res.text, "out")
	}

	if res.alreadySent {
		return "", nil
	}

	if res.text != "" {
		log.Printf("brain %s: returning %d chars of response text to %s", br.name, len(res.text), jid)
	}

	return res.text, nil
}

// ResetSession clears the brain's stored session ID.
func (br *Brain) ResetSession() error {
	br.interruptCurrent()
	return errors.Join(
		br.store.DeleteSession(br.storeKey),
		br.store.DeleteProcessPID(br.storeKey),
	)
}

// ensureViewer starts the tmux activity viewer if enabled and not running.
func (br *Brain) ensureViewer() {
	if !br.viewer {
		return
	}
	if br.tmuxViewer != nil && br.tmuxViewer.IsAlive() {
		return
	}
	tv, err := NewTmuxViewer("claude-"+br.name, br.streamLogPath(), br.workDir)
	if err != nil {
		log.Printf("brain %s: could not start tmux viewer: %v", br.name, err)
		return
	}
	br.tmuxViewer = tv
}

func (br *Brain) streamLogPath() string {
	return filepath.Join(br.workDir, "claude-stream.log")
}

// invoke runs the Claude CLI once and returns a brainResult.
// Each invocation starts fresh with a preface of recent chat history.
func (br *Brain) invoke(run *brainRun, ctx context.Context, jid, sender, text, mediaPath string, silent bool) brainResult {
	isGroup := strings.HasSuffix(jid, "@g.us")
	prompt := br.buildPrompt(jid, isGroup, sender, text, mediaPath)

	br.ensureViewer()

	args := []string{"--output-format", "stream-json", "--verbose"}
	if br.dangerously {
		args = append(args, "--dangerously-skip-permissions")
	}
	if br.model != "" {
		args = append(args, "--model", br.model)
	}
	args = append(args, "-p", prompt)

	// Wrap the spawn in sandbox-exec so prompt injection in an incoming message
	// cannot reach another brain's tree or cross-session Claude state. The brain
	// still runs with --dangerously-skip-permissions for tool freedom; the
	// sandbox enforces the read scope at the OS layer regardless.
	binary := br.claudePath
	finalArgs := args
	var sbProfilePath string
	if br.sandbox != nil {
		if profilePath, sbErr := br.sandbox.Write(br.name); sbErr != nil {
			log.Printf("brain %s: sandbox profile generation failed (%v); falling back to unsandboxed spawn", br.name, sbErr)
		} else {
			sbProfilePath = profilePath
			binary = sandboxExecPath
			finalArgs = append([]string{"-f", sbProfilePath, br.claudePath}, args...)
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
		return brainResult{err: fmt.Errorf("opening /dev/null: %w", err)}
	}
	defer devNull.Close()
	cmd.Stdin = devNull

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return brainResult{err: fmt.Errorf("stdout pipe: %w", err)}
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return brainResult{err: fmt.Errorf("starting claude: %w", err)}
	}
	br.setRunPID(run, cmd.Process.Pid)
	if err := br.store.SaveProcessPID(br.storeKey, cmd.Process.Pid); err != nil {
		log.Printf("brain %s: failed to save pid %d: %v", br.name, cmd.Process.Pid, err)
	}

	// Parse the stream in a goroutine so we can race it against the 10s timer.
	parseDone := make(chan brainResult, 1)
	go func() {
		parseDone <- br.parseStream(jid, stdout)
	}()

	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	invocationStart := time.Now()
	var res brainResult
	select {
	case res = <-parseDone:
	case <-timer.C:
		// Untagged (silent) messages never produce a visible reply, so they
		// must not produce a visible "working" notice either.
		if !silent && br.IntermediateSendFn != nil {
			br.IntermediateSendFn(jid, "⏳ Working on it...")
		}
		res = <-parseDone
	}

	if elapsed := time.Since(invocationStart); elapsed > 5*time.Minute && res.text != "" {
		log.Printf("brain %s: response took %v for %s (slow response)", br.name, elapsed, jid)
	}

	// The result event is final, but the CLI may still be winding down. Ask it
	// to exit, then hard-kill anything still alive 5 seconds later.
	if cmd.Process != nil {
		go func() {
			time.Sleep(5 * time.Second)
			if err := cmd.Process.Kill(); err == nil {
				log.Printf("brain %s: sent SIGKILL to claude process %d", br.name, cmd.Process.Pid)
			}
		}()
		_ = cmd.Process.Signal(os.Interrupt)
	}

	if waitErr := cmd.Wait(); waitErr != nil {
		if ctx.Err() != nil {
			return brainResult{err: errBrainInterrupted}
		}
		stderr := strings.TrimSpace(stderrBuf.String())
		if res.err == nil && res.text == "" && !res.alreadySent {
			return brainResult{err: fmt.Errorf("claude exited: %w — stderr: %s", waitErr, stderr)}
		}
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
			senderDisplay = fmt.Sprintf("You (%s)", br.name)
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
// optional media path, then the new message.
func (br *Brain) buildPrompt(chatJID string, isGroup bool, sender, text, mediaPath string) string {
	var sb strings.Builder

	sysPrompt, err := os.ReadFile(br.promptFile)
	if err != nil {
		log.Printf("brain %s: could not read prompt file %s: %v", br.name, br.promptFile, err)
	} else {
		sb.WriteString(strings.TrimSpace(string(sysPrompt)))
		sb.WriteString("\n\n")
	}

	if preface := br.composePreface(chatJID, isGroup); preface != "" {
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

	if mediaPath != "" {
		sb.WriteString(fmt.Sprintf("A file was shared via WhatsApp and saved to: %s\n\n", mediaPath))
	}

	sb.WriteString("<new_message>\n")
	sb.WriteString(fmt.Sprintf("%s says: %s\n", sender, text))
	sb.WriteString("</new_message>")
	return sb.String()
}

// parseStream reads the stream-json output from the Claude CLI and returns a
// brainResult, writing a human-readable activity log alongside it.
func (br *Brain) parseStream(jid string, reader io.Reader) brainResult {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	streamLog, logErr := os.OpenFile(br.streamLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if logErr != nil {
		log.Printf("brain %s: could not open stream log: %v", br.name, logErr)
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
						if question != "" && br.IntermediateSendFn != nil {
							br.IntermediateSendFn(jid, question)
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
				return brainResult{err: fmt.Errorf("claude error: %s", errMsg)}
			}
			finalResult = event.Result
			writeLog("✅ Result: " + truncate(finalResult, 2000))
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		if errors.Is(err, os.ErrClosed) {
			return brainResult{err: errBrainInterrupted}
		}
		return brainResult{err: fmt.Errorf("reading claude output: %w", err)}
	}

	if questionSent {
		// The question already went to WhatsApp; sending the final text too
		// would duplicate it.
		return brainResult{alreadySent: true}
	}

	if finalResult == "" {
		return brainResult{err: fmt.Errorf("no result from claude (empty output)")}
	}

	return brainResult{text: finalResult}
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

func (br *Brain) acquireRun(ctx context.Context) (*brainRun, context.Context, error) {
	for {
		br.mu.Lock()
		if br.current == nil {
			runCtx, cancel := context.WithCancel(ctx)
			run := &brainRun{cancel: cancel, done: make(chan struct{})}
			br.current = run
			br.mu.Unlock()
			return run, runCtx, nil
		}
		current := br.current
		br.mu.Unlock()

		br.interruptRun(current)

		select {
		case <-current.done:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(60 * time.Second):
			// The previous run is wedged and not responding to interruption.
			// Force-finalize it rather than waiting forever.
			log.Printf("brain %s: acquireRun timeout waiting for stuck run; force-finalizing", br.name)
			current.once.Do(func() { close(current.done) })
			br.mu.Lock()
			if br.current == current {
				br.current = nil
			}
			br.mu.Unlock()
		}
	}
}

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
	if br.current == run {
		run.pid = pid
	}
	br.mu.Unlock()
}

func (br *Brain) interruptCurrent() {
	br.mu.Lock()
	current := br.current
	br.mu.Unlock()
	br.interruptRun(current)
	if current != nil {
		<-current.done
	}
}

func (br *Brain) interruptRun(run *brainRun) {
	if run == nil {
		return
	}

	run.cancel()

	br.mu.Lock()
	pid := run.pid
	br.mu.Unlock()

	if pid <= 0 {
		pid, _ = br.store.GetProcessPID(br.storeKey)
	}
	if pid <= 0 {
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if err := proc.Kill(); err != nil && !strings.Contains(err.Error(), "process already finished") {
		log.Printf("brain %s: failed to kill pid %d: %v", br.name, pid, err)
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
		log.Printf("brain %s: failed to open history file %s: %v", br.name, br.historyFile, err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// ── BrainManager ─────────────────────────────────────────────────────────

// BrainManager owns every configured brain and routes messages to the right one.
type BrainManager struct {
	brains     []*Brain
	byName     map[string]*Brain
	byGroupJID map[string]*Brain
	dmBrain    *Brain // brain with handles_dms; nil falls through to fallback
	fallback   *Brain // brain marked default, else the first brain
}

// NewBrainManager builds the brains from config and ensures their working
// directories exist.
func NewBrainManager(cfg *Config, store *Store) (*BrainManager, error) {
	defaultModel := cfg.BrainModel
	if defaultModel == "" {
		defaultModel = cfg.Model
	}

	// Every brain's work dir is denied to every other brain.
	workDirs := make(map[string]string, len(cfg.Brains))
	for _, bc := range cfg.Brains {
		workDirs[bc.Name] = bc.WorkDir
	}
	var profiler *SandboxProfiler
	if cfg.Sandbox.Enabled {
		profiler = NewSandboxProfiler(workDirs, cfg)
	}

	mgr := &BrainManager{
		byName:     make(map[string]*Brain, len(cfg.Brains)),
		byGroupJID: make(map[string]*Brain),
	}

	for _, bc := range cfg.Brains {
		if err := os.MkdirAll(bc.WorkDir, 0o700); err != nil {
			return nil, fmt.Errorf("creating work dir %s: %w", bc.WorkDir, err)
		}

		model := bc.Model
		if model == "" {
			model = defaultModel
		}

		br := &Brain{
			name:        bc.Name,
			storeKey:    "brain:" + bc.Name,
			workDir:     bc.WorkDir,
			historyFile: filepath.Join(bc.WorkDir, "conversation-history.jsonl"),
			promptFile:  bc.PromptFile,
			claudePath:  cfg.ClaudePath,
			model:       model,
			dangerously: cfg.DangerouslySkipPermissions,
			otpGuard:    cfg.OTPGuard,
			viewer:      cfg.Viewer,
			sandbox:     profiler,
			store:       store,
		}

		mgr.brains = append(mgr.brains, br)
		mgr.byName[bc.Name] = br
		for _, g := range bc.Groups {
			mgr.byGroupJID[g] = br
		}
		if bc.HandlesDMs {
			mgr.dmBrain = br
		}
		if bc.Default {
			mgr.fallback = br
		}
	}

	if mgr.fallback == nil && len(mgr.brains) > 0 {
		mgr.fallback = mgr.brains[0]
	}

	return mgr, nil
}

// BrainFor returns the brain that handles the given chat.
func (bm *BrainManager) BrainFor(jid string, isGroup bool) *Brain {
	if isGroup {
		if br, ok := bm.byGroupJID[jid]; ok {
			return br
		}
		return bm.fallback
	}
	if bm.dmBrain != nil {
		return bm.dmBrain
	}
	return bm.fallback
}

// SetIntermediateSendFn wires the notification callback into every brain.
func (bm *BrainManager) SetIntermediateSendFn(fn func(jid, text string)) {
	for _, br := range bm.brains {
		br.IntermediateSendFn = fn
	}
}

// ProcessMessage routes the message to the brain that owns the chat.
func (bm *BrainManager) ProcessMessage(ctx context.Context, isGroup bool, jid, sender, text, mediaPath string, silent bool) (string, error) {
	br := bm.BrainFor(jid, isGroup)
	if br == nil {
		return "", fmt.Errorf("no brain configured for %s", jid)
	}
	return br.ProcessMessage(ctx, jid, sender, text, mediaPath, silent)
}

// ResetSession clears the stored session for the brain that owns the chat.
func (bm *BrainManager) ResetSession(jid string, isGroup bool) error {
	br := bm.BrainFor(jid, isGroup)
	if br == nil {
		return fmt.Errorf("no brain configured for %s", jid)
	}
	return br.ResetSession()
}

// Stop interrupts any in-flight invocations and clears stored PIDs.
func (bm *BrainManager) Stop() {
	for _, br := range bm.brains {
		br.interruptCurrent()
	}
}
