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
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
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
	// No letters and no digits, in any script: emoji, punctuation, or similar.
	return strings.IndexFunc(trimmed, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0
}

// Bridge connects WhatsApp to the brains, and serves the dashboard.
type Bridge struct {
	cfgs     *ConfigStore
	lock     *instanceLock
	store    *Store
	wa       Messenger
	dev      *devMessenger // non-nil in --no-whatsapp mode
	brainMgr *BrainManager
	deb      *Debouncer
	logs     *LogRing
	activity *Activity
	token    *DashboardToken
	// tokenAtStart is dashboard.require_token as the bridge started.
	tokenAtStart bool
	sessions     *dashSessions
	// sessionCookie names the session cookie for the address served on.
	sessionCookie string
	scriptAuth    *scriptAuth
	backup        *BackupManager
	google        *GoogleAuth
	plaud         *PlaudManager
	site          *SiteManager
	started       time.Time

	// listen is the address the HTTP server binds, "" when it is off;
	// runs reach the brain API there with a token from runTokens.
	listen    string
	runTokens runTokens
	// exe is the binary brains run (see brainRunEnv).
	exe string
	// lids is whatsmeow's map between LIDs and phone numbers, nil in dev
	// mode, for what knows a person by LID outside an event: a /send to
	// one.
	lids func() store.LIDStore

	httpSrv *http.Server

	mu       sync.Mutex
	warnings []string // from startup checks

	// addMu makes adding a brain one step at a time (apiAddBrain).
	addMu sync.Mutex
}

// BridgeOptions are the run-time switches from the command line.
type BridgeOptions struct {
	NoWhatsApp bool
	Logs       *LogRing
}

// NewBridge creates and wires up all components. It holds the data
// directory's instance lock until Stop.
func NewBridge(cfgs *ConfigStore, opts BridgeOptions) (_ *Bridge, err error) {
	cfg := cfgs.Get()
	// Taken before the store opens: a second bridge on the same data dir, dev
	// mode or a test included, must not open the live WhatsApp session's
	// database, let alone run whatsmeow's migrations on it.
	lock, err := acquireInstanceLock(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			lock.release()
		}
	}()
	protectDataDir(cfg)
	pruneRunTokens(cfg.DataDir)
	// With the lock held no other bridge runs here, so any run the last one
	// recorded that is still going was left behind by a crash.
	reapOrphanedRuns(cfg.DataDir)
	store, err := NewStore(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating store: %w", err)
	}
	defer func() {
		// Runs before the lock is given back: whoever takes it next must be
		// the only one with bridge.db open.
		if err != nil {
			if cerr := store.Close(); cerr != nil {
				log.Printf("closing store: %v", cerr)
			}
		}
	}()

	b := &Bridge{
		cfgs:     cfgs,
		lock:     lock,
		store:    store,
		logs:     opts.Logs,
		activity: NewActivity(500),
		started:  time.Now(),
	}
	if cfg.HTTPEnabled() {
		// A change to http_listen waits for a restart: until then the
		// server listens, and runs are told, where it started.
		b.listen = cfg.HTTPListen
	}
	b.exe = bridgeBinary()
	if b.logs == nil {
		b.logs = NewLogRing(logRingLines)
	}

	mgr, err := NewBrainManager(cfg)
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
		wa.onVote = b.recordVote
		b.wa = wa
		b.lids = wa.lidStore
	}

	b.brainMgr.SetHooks(BrainHooks{
		Intermediate: func(jid, text string) {
			if err := b.wa.SendChunked(context.Background(), jid, text); err != nil {
				log.Printf("failed to send brain intermediate message to %s: %v", jid, err)
			}
		},
		Deliver:   b.deliver,
		OnHistory: b.activity.Add,
		ExtraArgs: func(id string) []string { return b.site.BuilderArgs(id) },
		RunEnv:    b.brainRunEnv,
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
		logSecurityChanges(prev, next)
		for _, br := range b.brainMgr.Brains() {
			b.activity.Seed(br)
		}
	})

	tok, err := LoadDashboardToken(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	b.token = tok
	b.tokenAtStart = cfg.DashboardRequiresToken()
	if b.sessions, err = loadDashSessions(cfg.DataDir); err != nil {
		return nil, err
	}
	if b.scriptAuth, err = newScriptAuth(cfg.DataDir, b.runTokens.brainOf, func(id string) bool { return b.brainMgr.Get(id) != nil }); err != nil {
		return nil, err
	}
	if !b.tokenAtStart {
		log.Printf("warning: dashboard.require_token is off: any program on this computer, brains included, can change every setting through the dashboard API")
	}
	b.backup = NewBackupManager(cfgs, b.brainMgr)
	// A refused scheduled backup goes to the brain's chat, like Plaud's
	// expired-login notice: the owner may not open the dashboard for weeks.
	b.backup.notify = func(ctx context.Context, br *Brain, text string) error {
		jid := b.replyChatFor(br, b.cfgs.Get())
		if jid == "" {
			return fmt.Errorf("brain %s has no chat to send it to — set its reply chat", br.ID())
		}
		return b.wa.SendMessage(ctx, jid, text)
	}
	b.google = NewGoogleAuth(cfgs)
	b.plaud = NewPlaudManager(cfgs, b.brainMgr)
	b.plaud.submit = b.submitHandOff
	b.plaud.notify = b.wa.SendMessage
	b.site = NewSiteManager(cfg.DataDir, b.google.clientPath)
	b.site.hooks = b.siteBuilderHooks()
	// What builder brains say in the Build page's chats goes there, not to WhatsApp.
	b.wa = siteChatMessenger{Messenger: b.wa, chats: b.site.chats}
	return b, nil
}

// Start serves the dashboard, connects to WhatsApp and blocks until ctx is
// cancelled. The HTTP port is bound first and failing to bind is fatal: an
// already-running bridge holds it, and two bridges must never share one
// WhatsApp session.
func (b *Bridge) Start(ctx context.Context) error {
	cfg := b.cfgs.Get()
	if b.listen != "" {
		ln, err := net.Listen("tcp", b.listen)
		if err != nil {
			return fmt.Errorf("listening on %s (is another bridge running?): %w", b.listen, err)
		}
		b.httpSrv = newHTTPServer(b.newHTTPHandler(b.listen))
		go func() {
			if err := b.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTP server error: %v", err)
			}
		}()
		if !cfg.Dashboard.Disabled {
			log.Printf("Dashboard: http://%s/  (open it with: fridolin open)", b.listen)
		}
	}

	go b.connectLoop(ctx)
	go b.runStartupChecks(ctx)
	b.backup.Start(ctx)
	b.plaud.Start(ctx)
	b.site.Start(ctx)

	log.Println("Bridge started. Waiting for messages…")
	<-ctx.Done()
	return nil
}

// newHTTPServer is the server for the dashboard, /send, /ingest and
// /brain/. Any local program can connect, so none may hold a connection
// for ever: reading a request (headers and body, which is 1 MiB at most)
// is cut off after 30 seconds and an idle keep-alive connection after two
// minutes. There is no write timeout, because some handlers rightly take
// minutes: a folder picker waits for you, rclone lists versions, a sign-in
// checks back with Google.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
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
	b.site.Stop()
	b.deb.DropAll()
	b.brainMgr.Stop()
	b.wa.Disconnect()
	if err := b.store.Close(); err != nil {
		log.Printf("closing store: %v", err)
	}
	b.lock.release()
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
	if m.Source != sourceIngest && m.Brain == "" && !m.Silent && b.handleCommand(ctx, m, cfg) {
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

	// Resolved like the run itself (BrainManager.Submit), so the OTP guard
	// checks the brain that would read the message.
	br := b.brainMgr.Route(m)
	if br == nil && m.Brain != "" {
		log.Printf("brain %s is not configured; dropping the message for %s", m.Brain, m.JID)
		return
	}
	if br == nil {
		log.Printf("No brain configured for %s — add one in the dashboard", m.JID)
		if !m.Silent && m.Source != sourceIngest {
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
	now := time.Now()
	data, _ := json.Marshal(map[string]any{
		"brain":    br.ID(),
		"chat":     m.JID,
		"sender":   m.Sender,
		"code":     strings.TrimSpace(m.Text),
		"received": now.Format(time.RFC3339), // local time with offset, like the history
		// What scripts compare with the time of their request: it needs no
		// time zone (received ends in "Z" on a host in UTC, which Python
		// before 3.11 cannot read) and tells apart two codes in one second.
		"received_unix_ms": now.UnixMilli(),
	})
	if err := writeFileAtomic(filepath.Join(dir, br.ID()+".json"), data, 0o600, false); err != nil {
		log.Printf("otp: %v", err)
	}
}

// handleCommand runs !help, !reset and !stop. It reports whether text was one.
func (b *Bridge) handleCommand(ctx context.Context, m Inbound, cfg *Config) bool {
	switch strings.ToLower(m.Text) {
	case "!reset":
		// A reset stops what !stop stops for the chat, hand-offs from other
		// brains included. That is all there is to reset: a brain run never
		// resumes a session, each starts from the recent chat history, which
		// a reset keeps. Say only what happened, and mention the history only
		// where a brain will read the next message.
		b.stopChat(m)
		reply := "✅ Cleared anything this chat had running or waiting."
		if b.brainMgr.BrainFor(m.JID, m.IsGroup()) != nil {
			reply += " The recent chat history stays, so the next message still sees it."
		}
		_ = b.wa.SendMessage(ctx, m.JID, reply)
		return true

	case "!stop":
		stopped, dropped := b.stopChat(m)
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
			"• `!stop` — stop what I'm doing for this chat and drop its queued messages\n" +
			"• `!reset` — like `!stop`; I still see the recent chat history afterwards"
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
func (b *Bridge) stopChat(m Inbound) (stopped bool, dropped int) {
	// Only this chat's work: a brain serves several chats, and one chat must
	// not cancel what another asked for. A DM goes by two JIDs (see
	// chatJIDs), and its run may have come in under either.
	chats := chatJIDs(m)
	for _, jid := range chats {
		b.deb.Drop(jid)
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

// chatJIDs lists the JIDs m's chat goes by: the chat and, for a DM, its
// other JID and the sender's phone number. The bridge keys a DM by the
// number, but WhatsApp may address it by LID, history from before may be
// under the LID, and a hand-off that answers there (a reply_chat, the
// owner's DM) may name either.
func chatJIDs(m Inbound) []string {
	jids := []string{m.JID}
	if m.IsGroup() {
		return jids
	}
	for _, jid := range []string{m.ChatAlt, userJID(m.Sender)} {
		if jid != "@"+types.DefaultUserServer && jid != "" && !slices.Contains(jids, jid) {
			jids = append(jids, jid)
		}
	}
	return jids
}

// dispatch hands a (debounced) message to its brain.
func (b *Bridge) dispatch(m Inbound) {
	ctx := context.Background()
	cfg := b.cfgs.Get()
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
		// The error in full goes to the log and the dashboard only: it can
		// hold the CLI's stderr, and a group's members read the chat.
		log.Printf("brain %s: error for %s: %v", br.ID(), m.JID, res.Err)
		if text := chatErrorText(res.Err, !m.IsGroup()); text != "" && !m.Silent {
			_ = b.wa.SendMessage(ctx, m.JID, text)
		}
		return
	}

	// An empty response means a question was already forwarded mid-stream.
	// Silent runs never reply at all.
	if res.AlreadySent || res.Text == "" || m.Silent {
		return
	}
	log.Printf("brain %s: sending response (%d chars) to %s after %s", br.ID(), len(res.Text), m.JID, res.Duration.Round(time.Second))
	b.sendResponse(ctx, br.ID(), m.JID, res.Text)
}

// sendResponse sends what brain replied in jid: each [POLL]…[/POLL] block as a real
// WhatsApp poll (see sendPolls), then the rest as text.
func (b *Bridge) sendResponse(ctx context.Context, brain, jid, text string) {
	polls, remaining := extractPolls(text)
	b.sendPolls(ctx, brain, jid, polls)
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
		if !fileExists(f) {
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
	if jid == "" {
		jid = ownerChat(cfg)
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
		log.Printf("WARNING: anyone who can message this WhatsApp account can use the assistant: allowed_numbers is empty and allow_any_dm is true. Add your number under Chats → Allowed numbers, or turn allow_any_dm off.")
	case prev == nil && len(next.AllowedNumbers) == 0:
		log.Printf("allowed_numbers is empty, so nobody can send the bridge direct messages yet. Add your number under Chats → Allowed numbers.")
	}
}

// Warnings returns the startup-check findings.
func (b *Bridge) Warnings() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.warnings...)
}
