package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
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
// bridge, and such a message still goes into the history (see observeRuns).
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
	outbox        *Outbox
	otp           otpHandoffs
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
	sweepOTPHandoffs(filepath.Join(cfg.DataDir, "otp"), time.Now())
	// With the lock held no other bridge runs here, so any run the last one
	// recorded that is still going was left behind by a crash.
	reapOrphanedRuns(cfg.DataDir)
	// What older versions left in the media folder goes aside before any
	// brain has an inbox there, one named like it would read it (see
	// retireLegacyMedia). Here, with the lock held, and not wherever the
	// brains are loaded: check loads them too, and run with a new build
	// against the config of a bridge still running, it reorganised that
	// bridge's media folder under it. A failure here leaves it to the
	// first inbox.
	if cfg.MediaDir != "" {
		if err := retireLegacyMedia(cfg.MediaDir); err != nil {
			log.Printf("media: readying %s for inboxes: %v", cfg.MediaDir, err)
		}
	}
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
		wa.onChange = b.handleChange
		wa.groupRules = b.groupRules
		b.wa = wa
		b.lids = wa.lidStore
	}

	b.brainMgr.SetHooks(BrainHooks{
		Notice: func(jid, text string) {
			// At once or not at all: a notice that arrives after the
			// answer would only confuse.
			if err := b.wa.SendMessage(context.Background(), jid, text); err != nil {
				log.Printf("failed to send the working notice to %s: %v", jid, err)
			}
		},
		Question: func(br *Brain, jid, text string) bool {
			if err := b.outbox.Question(context.Background(), br.ID(), jid, text, b.cfgs.Get().MaxChunkSize); err != nil {
				log.Printf("brain %s: question to %s: %v", br.ID(), jid, err)
				return false
			}
			return true
		},
		Deliver:     b.deliver,
		Undelivered: b.undelivered,
		OnHistory:   b.activity.Add,
		ExtraArgs:   func(id string) []string { return b.site.BuilderArgs(id) },
		CodexSetup:  func(id string) ([]string, []string, string, error) { return b.site.CodexBuilder(id) },
		RunEnv:      b.brainRunEnv,
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
	b.plaud.siteURL = func() string {
		if d := b.site.Settings().Domain; d != "" {
			return "https://" + d
		}
		return ""
	}
	b.site.hooks = b.siteBuilderHooks()
	// What builder brains say in the Build page's chats goes there, not to WhatsApp.
	b.wa = siteChatMessenger{Messenger: b.wa, chats: b.site.chats}
	pace := outboxPace
	if opts.NoWhatsApp {
		pace = 0
	}
	b.outbox = newOutbox(store, func() Messenger { return b.wa }, pace, b.delivered)
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
	// Waited for: it queues alerts in the outbox, which Stop closes.
	var wg sync.WaitGroup
	wg.Go(func() { b.watchRequiredFiles(ctx, requiredFilesEvery) })
	b.backup.Start(ctx)
	b.plaud.Start(ctx)
	b.site.Start(ctx)

	log.Println("Bridge started. Waiting for messages…")
	<-ctx.Done()
	wg.Wait()
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

// Connection retries: the first after connectFirstRetry, doubling up to
// connectMaxRetry.
const (
	connectFirstRetry = 5 * time.Second
	connectMaxRetry   = 2 * time.Minute
)

// connectLoop connects to WhatsApp, retrying while the network is down.
// Once connected, whatsmeow reconnects by itself. An error that another
// try cannot fix ends it: the dashboard's state says what to do.
func (b *Bridge) connectLoop(ctx context.Context) {
	for delay := connectFirstRetry; ; delay = min(2*delay, connectMaxRetry) {
		err := b.wa.Connect()
		if err == nil {
			return
		}
		if errors.Is(err, store.ErrDeviceDeleted) || errors.Is(err, errPairingLive) {
			log.Printf("WhatsApp connect failed: %v", err)
			return
		}
		log.Printf("WhatsApp connect failed: %v — retrying in %v", err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
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
	// After the brains, whose last runs may still reply; what does not go
	// out stays in bridge.db for the next start.
	b.outbox.Stop()
	b.otp.stop()
	b.wa.Disconnect()
	if err := b.store.Close(); err != nil {
		log.Printf("closing store: %v", err)
	}
	b.lock.release()
	log.Println("Bridge stopped.")
}

// handleChange applies an edit or a deletion of a WhatsApp message: to
// the debouncer's batch that holds it, which then reaches the brain as
// changed, or else to the brain's queue, its run and its history.
func (b *Bridge) handleChange(c messageChange) {
	br := b.brainMgr.BrainFor(c.Chat, isGroupJID(c.Chat))
	// An edit to a one-time code is caught like the code itself
	// (handleInbound), before anything else sees it: a sender who corrects
	// a code hands the new one to the script that asked for it, and the
	// message edited stays as it was.
	if c.Kind == changeEdit && br != nil {
		edited := Inbound{JID: c.Chat, ChatAlt: c.ChatAlt, Sender: c.Sender, Text: c.Text, Source: sourceWhatsApp}
		if code, ok := br.catchesCode(edited, time.Now()); ok {
			log.Printf("brain %s: kept an edited one-time code out of the conversation (OTP guard)", br.ID())
			b.handOffCode(br, edited, code)
			return
		}
	}
	if b.deb.Change(c) {
		return
	}
	if br == nil {
		return
	}
	waiting, edited := br.Change(c)
	br.recordChange(c, waiting, edited)
	// The dashboard's feed shows the history's lines: a deleted message
	// must not linger there either.
	text := ""
	if edited != nil {
		text = historyTextWithMedia(edited.textWithQuote(), edited.Media)
	}
	b.activity.Change(br.ID(), c, text)
}

// groupRules is what addresses the bridge in the group jid: what the
// routing of the brain that answers there says.
func (b *Bridge) groupRules(jid string) groupRules {
	if br := b.brainMgr.BrainFor(jid, true); br != nil {
		return br.settings.Load().manifest.Routing.groupRules()
	}
	return defaultGroupRules
}

// recordIgnored notes a chat that wrote without being allowed.
func (b *Bridge) recordIgnored(kind, jid, name, sender, pushName string) {
	if err := b.store.RecordIgnored(kind, jid, name, sender, pushName); err != nil {
		log.Printf("recording ignored chat %s: %v", jid, err)
	}
}

// handleInbound is the entry point for every allowed message. Access control
// already happened, for WhatsApp and dev messages alike: groups against
// allowed_groups and their senders against group_members (whom
// allowed_numbers admits, for a group without an entry), DMs against
// allowed_numbers. Group messages without the trigger arrive Silent, and go
// on to their brain like the rest: it keeps them in its history.
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

	// Resolved like the run itself (BrainManager.Submit), so the OTP guard
	// checks the brain that would read the message.
	br := b.brainMgr.Route(m)
	if br == nil && m.Brain != "" {
		log.Printf("brain %s is not configured; dropping the message for %s", m.Brain, m.JID)
		return
	}
	if br == nil {
		reply := noBrainNotice
		if id := b.brainMgr.HeldFor(m.JID, m.IsGroup()); id != "" {
			// Not the default brain instead: what was meant for this
			// brain, your DMs say, must not land in another one.
			log.Printf("brain %s, which handles %s, cannot run (see its issue on the Brains page); the message goes to no other brain", id, m.JID)
			reply = heldBrainNotice
		} else {
			log.Printf("No brain configured for %s — add one in the dashboard", m.JID)
		}
		if !m.Silent && m.Source != sourceIngest {
			_ = b.wa.SendMessage(ctx, m.JID, reply)
		}
		return
	}
	// The OTP guard runs before anything is written to history, so a
	// two-factor code the chat was asked for is neither logged nor sent to
	// the model. Whoever sent it is told, unless it was untagged chatter,
	// which the bridge never answers: a code that vanished without a word
	// looks lost.
	if code, ok := br.catchesCode(m, time.Now()); ok {
		log.Printf("brain %s: kept a one-time code out of the conversation (OTP guard)", br.ID())
		b.handOffCode(br, m, code)
		if !m.Silent {
			_ = b.wa.SendMessage(ctx, m.JID, otpCaughtNotice)
		}
		return
	}

	b.deb.Add(m)
}

// handleCommand runs !help, !reset and !stop. It reports whether text was one.
func (b *Bridge) handleCommand(ctx context.Context, m Inbound, cfg *Config) bool {
	switch strings.ToLower(m.Text) {
	case "!reset":
		// A reset stops what !stop stops for the chat, hand-offs from other
		// brains included, and starts the chat afresh: a brain run never
		// resumes a session, each starts from the chat's recent history,
		// which the next one reads only from the reset on. Say only what
		// happened: the brain's memory files stay, and a chat no brain
		// answers has no history to start afresh.
		b.stopChat(m)
		_ = b.wa.SendMessage(ctx, m.JID, resetReply(b.resetChat(m)))
		return true

	case "!stop":
		_ = b.wa.SendMessage(ctx, m.JID, stopReply(b.stopChat(m)))
		return true

	case "!help":
		trigger := ""
		if m.IsGroup() {
			trigger = cfg.Triggers.First()
		}
		_ = b.wa.SendMessage(ctx, m.JID, helpReply(trigger))
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

// resetChat starts m's chat afresh (Brain.ResetChat) in the brain that
// answers there and in every brain whose reply chat it is, which answers
// files handed to it there: !reset is about the chat, whichever brain
// speaks in it. It reports whether any brain does.
func (b *Bridge) resetChat(m Inbound) bool {
	cfg := b.cfgs.Get()
	chats := chatJIDs(m)
	owner := b.brainMgr.BrainFor(m.JID, m.IsGroup())
	reset := false
	for _, br := range b.brainMgr.Brains() {
		if br == owner || slices.Contains(chats, b.replyChatFor(br, cfg)) {
			br.ResetChat(chats, m.Sender)
			reset = true
		}
	}
	return reset
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
			_ = b.wa.SendMessage(ctx, m.JID, queueFullNotice(stop))
		}
	case err != nil:
		log.Printf("routing message for %s: %v", m.JID, err)
	case busy && !m.Silent && cfg.QueueNotify():
		// Interrupt mode queues too: behind other chats and other people.
		_ = b.wa.SendMessage(ctx, m.JID, queuedNotice(pos))
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

	// An empty response follows a question forwarded mid-run, which said
	// it all. Silent runs never reply at all.
	if res.Text == "" || m.Silent {
		return
	}
	log.Printf("brain %s: sending response (%d chars) to %s after %s", br.ID(), len(res.Text), m.JID, res.Duration.Round(time.Second))
	if err := b.outbox.Reply(ctx, br.ID(), m.JID, res.Text, b.cfgs.Get().MaxChunkSize); err != nil {
		log.Printf("brain %s: %v", br.ID(), err)
	}
}

// delivered takes a reply that went out in full out of the outbox and
// records it in the history of the brain that wrote it: only now is it
// something the brain said. The history gets it only once its row is gone,
// so the retry after a failed removal does not record it twice.
func (b *Bridge) delivered(ctx context.Context, r outboxRow) error {
	remove := func() error { return b.store.OutboxDelivered(ctx, r.ID) }
	if r.Brain == "" || r.History == "" {
		return remove()
	}
	br := b.brainMgr.Get(r.Brain)
	if br == nil {
		log.Printf("brain %s: delivered a reply to %s, but the brain is gone", r.Brain, r.Chat)
		return remove()
	}
	return br.recordDelivered(r.Chat, r.History, remove)
}

// undelivered is what brainID answered that waits in the outbox, as its
// history will record it (BrainHooks.Undelivered).
func (b *Bridge) undelivered(brainID string) []historyEntry {
	rows, err := b.store.UndeliveredReplies(context.Background(), brainID)
	if err != nil {
		log.Printf("brain %s: reading its replies in the outbox: %v", brainID, err)
		return nil
	}
	out := make([]historyEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, historyEntry{TS: r.CreatedAt.Local().Format(time.RFC3339), From: brainID, Chat: r.Chat, Text: r.History, Type: historyOut})
	}
	return out
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
