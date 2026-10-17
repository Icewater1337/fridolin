package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// isJunkMessage reports whether a message is too trivial to be worth a Claude
// invocation — a bare emoji, a single character, or a one-word acknowledgement.
// Only applied to untagged group chatter, never to a message addressed to the
// bridge.
func isJunkMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if len([]rune(trimmed)) <= 1 {
		return true
	}
	switch strings.ToLower(trimmed) {
	case "ok", "ok!", "okay", "thanks", "thank you", "yes", "no", "yep", "nope", "lol", "haha", "hehe":
		return true
	}
	// No letters and no digits: emoji, punctuation, or similar.
	return !strings.ContainsAny(trimmed, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
}

// Bridge connects WhatsApp to the brains, and serves the dashboard.
type Bridge struct {
	cfgs     *ConfigStore
	store    *Store
	wa       Messenger
	dev      *devMessenger // non-nil in --no-whatsapp mode
	claude   *ClaudeRunner
	brainMgr *BrainManager
	deb      *Debouncer
	logs     *LogRing
	activity *Activity
	token    *DashboardToken
	backup   *BackupManager
	google   *GoogleAuth
	plaud    *PlaudManager
	started  time.Time

	httpSrv *http.Server

	mu       sync.Mutex
	warnings []string // from startup checks
}

// BridgeOptions are the run-time switches from the command line.
type BridgeOptions struct {
	NoWhatsApp bool
	Logs       *LogRing
}

// NewBridge creates and wires up all components.
func NewBridge(cfgs *ConfigStore, opts BridgeOptions) (*Bridge, error) {
	cfg := cfgs.Get()
	store, err := NewStore(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating store: %w", err)
	}

	b := &Bridge{
		cfgs:     cfgs,
		store:    store,
		claude:   NewClaudeRunner(cfgs.Get, store),
		logs:     opts.Logs,
		activity: NewActivity(500),
		started:  time.Now(),
	}
	if b.logs == nil {
		b.logs = NewLogRing(2000)
	}

	mgr, err := NewBrainManager(cfg, store)
	if err != nil {
		return nil, fmt.Errorf("creating brain manager: %w", err)
	}
	b.brainMgr = mgr
	mgr.profiler.SetConfigFile(cfgs.Path())
	for _, is := range mgr.Issues() {
		log.Printf("brain %s (%s): %s — %s", is.ID, is.Path, is.Kind, is.Detail)
	}
	logDMAccess(nil, cfg)

	b.deb = NewDebouncer(
		func() time.Duration { return time.Duration(b.cfgs.Get().DebounceQuietMS()) * time.Millisecond },
		func() time.Duration { return time.Duration(b.cfgs.Get().DebounceMaxWaitMS()) * time.Millisecond },
		b.dispatch,
	)

	if opts.NoWhatsApp {
		b.dev = &devMessenger{cfg: cfgs.Get}
		b.wa = b.dev
	} else {
		wa, err := NewWhatsAppClient(cfgs.Get, store, b.handleInbound, b.recordIgnored)
		if err != nil {
			return nil, fmt.Errorf("creating whatsapp client: %w", err)
		}
		b.wa = wa
	}

	// "Still working" notification for the classic runner.
	globalWorkingNotify = func(jid string) {
		if err := b.wa.SendMessage(context.Background(), jid, "⏳ Working on it..."); err != nil {
			log.Printf("failed to send working notification to %s: %v", jid, err)
		}
	}

	// Mid-stream question forwarding for the classic runner.
	b.claude.IntermediateSendFn = func(jid, text string) {
		if err := b.wa.SendChunked(context.Background(), jid, "❓ "+text); err != nil {
			log.Printf("failed to send intermediate question to %s: %v", jid, err)
		}
	}

	b.brainMgr.SetHooks(BrainHooks{
		Intermediate: func(jid, text string) {
			msg := text
			if !strings.HasPrefix(text, "⏳") {
				msg = "❓ " + text
			}
			if err := b.wa.SendChunked(context.Background(), jid, msg); err != nil {
				log.Printf("failed to send brain intermediate message to %s: %v", jid, err)
			}
		},
		Deliver:   b.deliver,
		OnHistory: b.activity.Add,
	})
	for _, br := range b.brainMgr.Brains() {
		b.activity.Seed(br)
	}

	// Configuration edits (from the dashboard) apply to the brains at once.
	cfgs.Subscribe(func(prev, next *Config) {
		if err := b.brainMgr.Apply(next); err != nil {
			log.Printf("applying configuration: %v", err)
		}
		logDMAccess(prev, next)
		for _, br := range b.brainMgr.Brains() {
			b.activity.Seed(br)
		}
	})

	tok, err := LoadDashboardToken(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	b.token = tok
	b.backup = NewBackupManager(cfgs, b.brainMgr)
	b.google = NewGoogleAuth(cfgs)
	b.plaud = NewPlaudManager(b)
	return b, nil
}

// Start serves the dashboard, connects to WhatsApp and blocks until ctx is
// cancelled. The HTTP port is bound first and failing to bind is fatal: an
// already-running bridge holds it, and two bridges must never share one
// WhatsApp session.
func (b *Bridge) Start(ctx context.Context) error {
	cfg := b.cfgs.Get()
	if cfg.HTTPEnabled() {
		ln, err := net.Listen("tcp", cfg.HTTPListen)
		if err != nil {
			return fmt.Errorf("listening on %s (is another bridge running?): %w", cfg.HTTPListen, err)
		}
		b.httpSrv = &http.Server{
			Handler:           b.newHTTPHandler(cfg.HTTPListen),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			if err := b.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTP server error: %v", err)
			}
		}()
		if !cfg.Dashboard.Disabled {
			log.Printf("Dashboard: http://%s/  (open it with: fridolin open)", cfg.HTTPListen)
		}
	}

	go b.connectLoop(ctx)
	go b.runStartupChecks(ctx)
	b.backup.Start(ctx)
	b.plaud.Start(ctx)

	log.Println("Bridge started. Waiting for messages…")
	<-ctx.Done()
	return nil
}

// connectLoop connects to WhatsApp, retrying while the network is down.
func (b *Bridge) connectLoop(ctx context.Context) {
	for delay := 5 * time.Second; ; {
		err := b.wa.Connect()
		if err == nil {
			return
		}
		log.Printf("WhatsApp connect failed: %v — retrying in %v", err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 2*time.Minute {
			delay *= 2
		}
	}
}

// Stop shuts everything down in dependency order.
func (b *Bridge) Stop() {
	if b.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = b.httpSrv.Shutdown(ctx)
		cancel()
	}
	b.backup.Stop()
	b.plaud.Stop()
	b.deb.DropAll()
	b.brainMgr.Stop()
	b.wa.Disconnect()
	if err := b.store.Close(); err != nil {
		log.Printf("closing store: %v", err)
	}
	log.Println("Bridge stopped.")
}

// recordIgnored notes a chat that wrote without being allowed.
func (b *Bridge) recordIgnored(kind, jid, name, sender, pushName string) {
	if err := b.store.RecordIgnored(kind, jid, name, sender, pushName); err != nil {
		log.Printf("recording ignored chat %s: %v", jid, err)
	}
}

// handleInbound is the entry point for every allowed message. Access control
// already happened: groups against allowed_groups, DMs against
// allowed_numbers. Group messages without the trigger arrive Silent.
func (b *Bridge) handleInbound(ctx context.Context, m Inbound) {
	m.Text = strings.TrimSpace(m.Text)
	cfg := b.cfgs.Get()

	// Commands come from a chat. A message handed to a brain (/ingest, Plaud,
	// a dev message that names one) carries none: its text is for that brain.
	// A command must also be addressed to the bridge. In a group, an untagged
	// "!stop" is someone talking to the others: it must not stop anything,
	// and the bridge must not answer it.
	if m.Source != "ingest" && m.Brain == "" && !m.Silent && b.handleCommand(ctx, m, cfg) {
		return
	}
	if m.Text == "" && len(m.Media) == 0 && m.Quoted == nil {
		return
	}

	// Untagged group chatter that carries no content is not worth a run.
	if m.Silent && len(m.Media) == 0 && isJunkMessage(m.Text) {
		log.Printf("Junk message from %s: skipping Claude invocation", m.Sender)
		return
	}

	if cfg.BrainMode {
		// Resolved like the run itself (BrainManager.Submit), so the OTP guard
		// checks the brain that would read the message.
		br := b.brainMgr.Route(m)
		if br == nil && m.Brain != "" {
			log.Printf("brain %s is not configured; dropping the message for %s", m.Brain, m.JID)
			return
		}
		if br == nil {
			log.Printf("No brain configured for %s — add one in the dashboard", m.JID)
			if !m.Silent && m.Source != "ingest" {
				_ = b.wa.SendMessage(ctx, m.JID, "🧠 No brain is set up for this chat yet. Open the bridge dashboard and add one.")
			}
			return
		}
		// The OTP guard runs before anything is written to history, so a
		// stray two-factor code is neither logged nor sent to the model.
		if len(m.Media) == 0 && br.DropsOTP(m.Text) {
			log.Printf("brain %s: kept a one-time code out of the conversation (OTP guard)", br.ID())
			b.recordOTP(br, m)
			return
		}
	}

	b.deb.Add(m)
}

// recordOTP hands a code the OTP guard caught to the local script that asked
// for it (it sent the request through /send): it is written to
// <data_dir>/otp/<brain>.json, readable only by this user. Brains cannot read
// it (the sandbox denies the data folder), and it never reaches the history
// or the model. Each code replaces the previous one.
func (b *Bridge) recordOTP(br *Brain, m Inbound) {
	dir := filepath.Join(b.cfgs.Get().DataDir, "otp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("otp: %v", err)
		return
	}
	data, _ := json.Marshal(map[string]string{
		"brain":    br.ID(),
		"chat":     m.JID,
		"sender":   m.Sender,
		"code":     strings.TrimSpace(m.Text),
		"received": time.Now().Format(time.RFC3339), // local time with offset, like the history
	})
	if err := writeFileAtomic(filepath.Join(dir, br.ID()+".json"), data, 0o600, false); err != nil {
		log.Printf("otp: %v", err)
	}
}

// handleCommand runs !help, !reset and !stop. It reports whether text was one.
func (b *Bridge) handleCommand(ctx context.Context, m Inbound, cfg *Config) bool {
	switch strings.ToLower(m.Text) {
	case "!reset":
		// Whatever else it does, a reset stops what !stop stops for the
		// chat, hand-offs from other brains included: its reply says the
		// chat has nothing left running or waiting.
		b.stopChat(m, cfg)
		var resetErr error
		if cfg.BrainMode {
			resetErr = b.brainMgr.ResetSession(m.JID, m.IsGroup())
		} else {
			resetErr = b.store.DeleteSession(m.JID)
		}
		switch {
		case resetErr != nil:
			log.Printf("error resetting session for %s: %v", m.JID, resetErr)
			_ = b.wa.SendMessage(ctx, m.JID, "❌ Failed to reset session.")
		case cfg.BrainMode:
			// A brain run never resumes a session: each starts from the
			// recent chat history, which a reset keeps. Say only what
			// happened.
			_ = b.wa.SendMessage(ctx, m.JID, "✅ Cleared anything this chat had running or waiting. The recent chat history stays, so the next message still sees it.")
		default:
			_ = b.wa.SendMessage(ctx, m.JID, "✅ Session reset. The next message starts a fresh conversation.")
		}
		return true

	case "!stop":
		stopped, dropped := b.stopChat(m, cfg)
		switch {
		case stopped:
			_ = b.wa.SendMessage(ctx, m.JID, "🛑 Stopped, and cleared anything this chat had waiting.")
		case dropped > 0:
			_ = b.wa.SendMessage(ctx, m.JID, "🛑 Nothing was running for this chat; cleared what it had waiting.")
		default:
			_ = b.wa.SendMessage(ctx, m.JID, "Nothing was running for this chat.")
		}
		return true

	case "!help":
		help := "🤖 *Fridolin*\n\n" +
			"Send a message to talk to Claude. Images, documents, audio, and video are passed along as files.\n\n" +
			"*Commands:*\n" +
			"• `!help` — show this help\n" +
			"• `!stop` — stop what I'm doing for this chat and drop its queued messages\n"
		if cfg.BrainMode {
			help += "• `!reset` — like `!stop`; I still see the recent chat history afterwards"
		} else {
			help += "• `!reset` — clear the current session and start fresh"
		}
		if m.IsGroup() {
			if t := cfg.Triggers.First(); t != "" {
				help += "\n\nIn this group, start your message with `" + t + "` to address me, commands included."
			}
		}
		_ = b.wa.SendMessage(ctx, m.JID, help)
		return true
	}
	return false
}

// stopChat stops what is going on for m's chat, for !stop and !reset. It
// reports whether a run was stopped and how many waiting messages were
// dropped.
func (b *Bridge) stopChat(m Inbound, cfg *Config) (stopped bool, dropped int) {
	// Only this chat's work: a brain serves several chats, and one chat must
	// not cancel what another asked for. A DM goes by two JIDs (see
	// chatJIDs), and its run may have come in under either.
	chats := chatJIDs(m)
	for _, jid := range chats {
		b.deb.Drop(jid)
	}
	if !cfg.BrainMode {
		return false, 0
	}
	if br := b.brainMgr.BrainFor(m.JID, m.IsGroup()); br != nil {
		for _, jid := range chats {
			s, d := br.StopChat(jid)
			stopped, dropped = stopped || s, dropped+d
		}
	}
	// A file or recording handed to another brain may answer here too, and
	// the chat's own brain knows nothing about it. What waits for one counts
	// as this chat's, for the reply.
	for _, jid := range chats {
		s, d := b.brainMgr.StopHandOffs(jid)
		stopped, dropped = stopped || s, dropped+d
	}
	return stopped, dropped
}

// chatJIDs lists the JIDs m's chat goes by: the chat and, for a DM, the
// sender's phone number. WhatsApp may address a DM by LID, while a hand-off
// that answers there (a reply_chat, the owner's DM) names the number.
func chatJIDs(m Inbound) []string {
	jids := []string{m.JID}
	if pn := m.Sender + "@s.whatsapp.net"; !m.IsGroup() && m.Sender != "" && pn != m.JID {
		jids = append(jids, pn)
	}
	return jids
}

// dispatch hands a (debounced) message to its brain or the classic runner.
func (b *Bridge) dispatch(m Inbound) {
	ctx := context.Background()
	cfg := b.cfgs.Get()
	if !cfg.BrainMode {
		b.handleClassicMessage(ctx, m)
		return
	}
	br, pos, busy, err := b.brainMgr.Submit(m)
	switch {
	case errors.Is(err, errQueueFull):
		log.Printf("brain %s: queue full, rejecting message from %s", br.ID(), m.Sender)
		if !m.Silent {
			// !stop drops only this chat's work, and in a group it needs the
			// trigger like any command.
			stop := "!stop"
			if t := cfg.Triggers.First(); m.IsGroup() && t != "" {
				stop = t + " !stop"
			}
			_ = b.wa.SendMessage(ctx, m.JID, "⏳ I'm still busy with earlier messages and my queue is full. Try again in a bit, or send `"+stop+"` to drop what this chat has waiting.")
		}
	case err != nil:
		log.Printf("routing message for %s: %v", m.JID, err)
	case busy && !m.Silent && cfg.QueueNotify():
		// Interrupt mode queues too: behind other chats and other people.
		_ = b.wa.SendMessage(ctx, m.JID, fmt.Sprintf("⏳ Queued (#%d) — I'll get to this right after the current task.", pos))
	}
}

// deliver sends a finished brain run's answer.
func (b *Bridge) deliver(br *Brain, m Inbound, res RunResult) {
	ctx := context.Background()
	if res.Err != nil {
		if errors.Is(res.Err, errBrainInterrupted) || errors.Is(res.Err, context.Canceled) {
			log.Printf("brain %s: run for %s was stopped", br.ID(), m.JID)
			return
		}
		log.Printf("brain %s: error for %s: %v", br.ID(), m.JID, res.Err)
		if !m.Silent {
			_ = b.wa.SendMessage(ctx, m.JID, fmt.Sprintf("❌ Error: %v", res.Err))
		}
		return
	}

	// An empty response means a question was already forwarded mid-stream.
	// Silent runs never reply at all.
	if res.AlreadySent || res.Text == "" || m.Silent {
		return
	}
	log.Printf("brain %s: sending response (%d chars) to %s after %s", br.ID(), len(res.Text), m.JID, res.Duration.Round(time.Second))
	b.sendResponse(ctx, m.JID, res.Text)
}

// handleClassicMessage runs the message through the single-session runner.
func (b *Bridge) handleClassicMessage(ctx context.Context, m Inbound) {
	replyChan := make(chan ClaudeResult, 1)
	b.claude.Enqueue(ClaudeRequest{
		JID:       m.JID,
		Text:      m.textWithQuote(),
		Media:     m.Media,
		ReplyChan: replyChan,
	})

	result := <-replyChan

	if result.Error != nil {
		log.Printf("Claude error for %s: %v", m.JID, result.Error)
		_ = b.wa.SendMessage(ctx, m.JID, fmt.Sprintf("❌ Error: %v", result.Error))
		return
	}

	// AlreadySent means a question went out mid-stream; the final text would
	// just repeat it.
	if result.AlreadySent || m.Silent {
		return
	}

	b.sendResponse(ctx, m.JID, result.Text)
}

// sendResponse parses Claude's response for [POLL]…[/POLL] blocks, sends each
// as a real WhatsApp poll, and sends the remaining text normally.
func (b *Bridge) sendResponse(ctx context.Context, jid, text string) {
	polls, remaining := extractPolls(text)

	for _, p := range polls {
		log.Printf("Sending poll to %s: %q (%d options)", jid, p.Question, len(p.Options))
		msgID, err := b.wa.SendPoll(ctx, jid, p.Question, p.Options, p.MaxSelectable)
		if err != nil {
			log.Printf("failed to send poll to %s: %v — falling back to text", jid, err)
			fallback := "📊 *" + p.Question + "*\n"
			for i, opt := range p.Options {
				fallback += fmt.Sprintf("%d. %s\n", i+1, opt)
			}
			_ = b.wa.SendMessage(ctx, jid, strings.TrimSpace(fallback))
			continue
		}
		// Store the option names so incoming vote hashes can be matched back.
		if err := b.store.SavePoll(msgID, jid, p.Question, p.Options); err != nil {
			log.Printf("failed to save poll %s to DB: %v", msgID, err)
		}
	}

	if remaining != "" {
		if err := b.wa.SendChunked(ctx, jid, remaining); err != nil {
			log.Printf("failed to send response to %s: %v", jid, err)
		}
	}
}

// ── startup checks ────────────────────────────────────────────────────────

// runStartupChecks reports configured files that are missing — typically
// credentials a brain's tools need. The alert goes to the log and the
// dashboard, and to WhatsApp once connected when configured.
func (b *Bridge) runStartupChecks(ctx context.Context) {
	cfg := b.cfgs.Get()
	var missing []string
	for _, f := range cfg.StartupChecks.RequiredFiles {
		if _, err := os.Stat(f); err != nil {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return
	}
	msg := fmt.Sprintf("%d required file(s) are missing: %s", len(missing), strings.Join(missing, ", "))
	if cfg.StartupChecks.Hint != "" {
		msg += " — " + cfg.StartupChecks.Hint
	}
	log.Printf("STARTUP CHECK FAILED: %s", msg)
	b.mu.Lock()
	b.warnings = append(b.warnings, msg)
	b.mu.Unlock()

	if !cfg.StartupChecks.NotifyWhatsApp {
		return
	}
	jid := cfg.StartupChecks.NotifyJID
	if jid == "" && len(cfg.AllowedNumbers) > 0 {
		jid = cfg.AllowedNumbers[0] + "@s.whatsapp.net"
	}
	if jid == "" {
		return
	}
	deadline := time.Now().Add(2 * time.Minute)
	for b.wa.State().Mode != "connected" {
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	time.Sleep(3 * time.Second)
	_ = b.wa.SendMessage(ctx, jid, "⚠️ Bridge startup check: "+msg)
}

// logDMAccess says in the log who may DM the bridge: at startup (prev nil),
// and again when an edit opens DMs to everyone. Allowed senders run Claude
// with all its permissions, so an open allowlist must not go unnoticed, and
// a closed one explains why DMs get no answer.
func logDMAccess(prev, next *Config) {
	switch {
	case next.AnyoneCanDM() && (prev == nil || !prev.AnyoneCanDM()):
		log.Printf("WARNING: anyone who can message this WhatsApp account can use the assistant: allowed_numbers is empty and allow_any_dm is true. Add your number under Settings → Access, or turn allow_any_dm off.")
	case prev == nil && len(next.AllowedNumbers) == 0:
		log.Printf("allowed_numbers is empty, so nobody can send the bridge direct messages yet. Add your number under Settings → Access.")
	}
}

// Warnings returns the startup-check findings.
func (b *Bridge) Warnings() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.warnings...)
}

// ── poll parsing ──────────────────────────────────────────────────────────

// pollData represents a parsed [POLL]…[/POLL] block.
type pollData struct {
	Question      string
	Options       []string
	MaxSelectable int // 1 = single choice, 0 = unlimited
}

// maxPollOptions is WhatsApp's own limit on options per poll.
const maxPollOptions = 12

// pollHeaderRe matches [POLL] or [POLL max=N].
var pollHeaderRe = regexp.MustCompile(`(?i)\[POLL(?:\s+max=(\d+))?\]`)

// extractPolls finds all [POLL]…[/POLL] blocks in text, parses them, and
// returns the remaining text with the blocks removed.
func extractPolls(text string) ([]pollData, string) {
	var polls []pollData
	remaining := text

	for {
		loc := pollHeaderRe.FindStringIndex(remaining)
		if loc == nil {
			break
		}
		end := strings.Index(remaining[loc[1]:], "[/POLL]")
		if end == -1 {
			break
		}
		end += loc[1] // absolute position of "[/POLL]"

		maxSelectable := 1
		headerMatch := pollHeaderRe.FindStringSubmatch(remaining[loc[0]:loc[1]])
		if len(headerMatch) > 1 && headerMatch[1] != "" {
			if n, err := strconv.Atoi(headerMatch[1]); err == nil {
				maxSelectable = n
			}
		}

		block := remaining[loc[1]:end]
		remaining = remaining[:loc[0]] + remaining[end+len("[/POLL]"):]

		var question string
		var options []string
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(line), "question:") {
				question = strings.TrimSpace(line[len("question:"):])
			} else if strings.HasPrefix(line, "- ") {
				if opt := strings.TrimSpace(line[2:]); opt != "" {
					options = append(options, opt)
				}
			}
		}

		if len(options) > maxPollOptions {
			log.Printf("poll %q had %d options; truncating to WhatsApp's limit of %d", question, len(options), maxPollOptions)
			options = options[:maxPollOptions]
		}
		if maxSelectable > len(options) {
			maxSelectable = len(options)
		}

		if question != "" && len(options) >= 2 {
			polls = append(polls, pollData{
				Question:      question,
				Options:       options,
				MaxSelectable: maxSelectable,
			})
		}
	}

	return polls, strings.TrimSpace(remaining)
}
