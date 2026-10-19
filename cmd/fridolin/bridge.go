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

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/devchat"
	"github.com/Icewater1337/fridolin/internal/google"

	"github.com/Icewater1337/fridolin/internal/activity"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/otp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/whatsapp"

	wastore "go.mau.fi/whatsmeow/store"
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
	cfgs     *config.Store
	lock     *platform.Lock
	store    *store.Store
	wa       Messenger
	dev      *devchat.Messenger // non-nil in --no-whatsapp mode
	brainMgr *BrainManager
	deb      *Debouncer
	logs     *logx.Ring
	activity *activity.Feed
	token    *httpapi.Token
	// tokenAtStart is dashboard.require_token as the bridge started.
	tokenAtStart bool
	sessions     *dashSessions
	// sessionCookie names the session cookie for the address served on.
	sessionCookie string
	scriptAuth    *scriptAuth
	backup        *backup.Manager
	google        *google.Auth
	plaud         *PlaudManager
	site          *SiteManager
	outbox        *outboundmsg.Outbox
	otp           otp.Handoff
	started       time.Time
	// restart is BridgeOptions.Restart.
	restart func()

	// listen is the address the HTTP server binds, "" when it is off;
	// runs reach the brain API there with a token from runTokens.
	listen    string
	runTokens runTokens
	// exe is the binary brains run (see brainRunEnv).
	exe string
	// lids is whatsmeow's map between LIDs and phone numbers, nil in dev
	// mode, for what knows a person by LID outside an event: a /send to
	// one.
	lids func() wastore.LIDStore

	httpSrv *http.Server
	// loops are the goroutines Start leaves running past its return,
	// the WhatsApp connect loop: Stop waits for them.
	loops sync.WaitGroup
	// stopOnce makes Stop run once, whoever calls it how often.
	stopOnce sync.Once
	// intake is what comes in from WhatsApp, which Stop closes first.
	intake intake

	// registry owns the brain workflows, including add serialization.
	registry *registry.Registry
	// applyMu keeps reading the configuration and applying it to the
	// brains together, see the config subscriber in NewBridge.
	applyMu sync.Mutex
}

// BridgeOptions are the run-time switches from the command line.
type BridgeOptions struct {
	NoWhatsApp bool
	Logs       *logx.Ring
	// Restart asks whoever runs the bridge to stop it and run it again
	// (POST /api/restart): runCmd ends its context with errRestart. Nil
	// leaves the bridge unable to restart itself.
	Restart func()
}

// NewBridge creates and wires up all components. It holds the data
// directory's instance lock until Stop.
func NewBridge(cfgs *config.Store, opts BridgeOptions) (_ *Bridge, err error) {
	cfg := runtimeConfig(cfgs)
	// Before the lock, which is a file in the data dir.
	if err := cfgs.Boot().EnsureDirs(); err != nil {
		return nil, err
	}
	// Taken before the store opens: a second bridge on the same data dir, dev
	// mode or a test included, must not open the live WhatsApp session's
	// database, let alone run whatsmeow's migrations on it.
	lock, err := platform.AcquireLock(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			lock.Release()
		}
	}()
	protectDataDir(cfg)
	pruneRunTokens(cfg.DataDir)
	otp.Sweep(filepath.Join(cfg.DataDir, "otp"), time.Now())
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
		if err := mediaInboxAt(cfg.MediaDir).RetireLegacy(); err != nil {
			log.Printf("media: readying %s for inboxes: %v", cfg.MediaDir, err)
		}
	}
	store, err := store.Open(cfg.DBPath, cfg.LogLevel)
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
		registry: registryFor(cfgs),
		lock:     lock,
		store:    store,
		logs:     opts.Logs,
		activity: activity.New(500),
		started:  time.Now(),
		restart:  opts.Restart,
	}
	if cfg.HTTPEnabled() {
		// A change to http_listen waits for a restart: until then the
		// server listens, and runs are told, where it started.
		b.listen = cfg.HTTPListen
	}
	b.exe = bridgeBinary()
	if b.logs == nil {
		b.logs = logx.NewRing(logRingLines)
	}

	mgr, err := NewBrainManager(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating brain manager: %w", err)
	}
	b.brainMgr = mgr
	mgr.profiler.home = cfgs.Boot().Home
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
		b.dev = devchat.New(func(text string) []string { return outboundmsg.Chunk(text, cfgs.Get().MaxChunkSize) })
		b.wa = b.dev
	} else {
		wa, err := whatsapp.New(b.whatsAppOptions())
		if err != nil {
			return nil, fmt.Errorf("creating whatsapp client: %w", err)
		}
		b.wa = wa
		b.lids = wa.LIDStore
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
		Waiting:     b.keepWaiting,
		Interrupted: b.keepInterrupted,
		OnHistory:   b.activity.Add,
		ExtraArgs:   func(id string) []string { return b.site.BuilderArgs(id) },
		CodexSetup:  func(id string) ([]string, []string, string, error) { return b.site.CodexBuilder(id) },
		RunEnv:      b.brainRunEnv,
	})
	for _, br := range b.brainMgr.Brains() {
		seedActivity(b.activity, br)
	}

	cfgs.Subscribe(b.configChanged)

	tok, err := httpapi.LoadDashboardToken(cfg.DataDir)
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
	// A refused scheduled backup goes to the brain's chat, like Plaud's
	// expired-login notice: the owner may not open the dashboard for weeks.
	b.backup = newProjectedBackup(cfgs, b.brainMgr, b.notifyBackup)
	b.google = google.New(googleOptionsFor(cfgs))
	b.plaud = NewPlaudManager(cfgs, b.brainMgr)
	b.plaud.submit = b.submitHandOff
	b.plaud.notify = b.wa.SendMessage
	b.site = NewSiteManager(cfg.DataDir, b.google.ClientPath)
	b.plaud.siteURL = func() string {
		if d := b.site.Settings().Domain; d != "" {
			return "https://" + d
		}
		return ""
	}
	b.site.hooks = b.siteBuilderHooks()
	// What builder brains say in the Build page's chats goes there, not to WhatsApp.
	b.wa = siteChatMessenger{Messenger: b.wa, chats: b.site.chats}
	pace := outboundmsg.OutboxPace
	if opts.NoWhatsApp {
		pace = 0
	}
	b.outbox = outboundmsg.NewOutbox(store, func() outboundmsg.Link { return b.wa }, pace, b.delivered)
	return b, nil
}

// listenHTTP binds the HTTP server's address, a variable so that a test
// can hand Start a listener that fails.
var listenHTTP = net.Listen

// Start serves the dashboard, connects to WhatsApp and blocks until ctx is
// cancelled, or until the HTTP server fails: it returns that error then,
// and the caller stops the bridge. The HTTP port is bound first and
// failing to bind is fatal: an already-running bridge holds it, and two
// bridges must never share one WhatsApp session.
func (b *Bridge) Start(ctx context.Context) error {
	cfg := runtimeConfig(b.cfgs)
	run, fail := context.WithCancelCause(ctx)
	defer fail(nil)
	if b.listen != "" {
		ln, err := listenHTTP("tcp", b.listen)
		if err != nil {
			return fmt.Errorf("listening on %s (is another bridge running?): %w", b.listen, err)
		}
		b.httpSrv = newHTTPServer(b.newHTTPHandler(b.listen))
		// Requests end with the bridge: one that waits for you or for
		// Google (a folder picker, a sign-in) gives up once the bridge
		// stops, rather than hold up Stop.
		b.httpSrv.BaseContext = func(net.Listener) context.Context { return run }
		go func() {
			// Serve ends with ErrServerClosed when Stop shuts it down. Any
			// other end leaves /send and the dashboard dead while the
			// bridge would run on and look healthy, so it stops instead.
			if err := b.httpSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				fail(fmt.Errorf("HTTP server: %w", err))
			}
		}()
		if !cfg.Dashboard.Disabled {
			log.Printf("Dashboard: http://%s/  (open it with: fridolin open)", b.listen)
		}
	}

	// Before WhatsApp connects: what the last stop kept goes to the
	// brains ahead of what comes in now.
	b.resume(run)
	b.loops.Go(func() { b.connectLoop(run) })
	// Waited for: it queues alerts in the outbox, which Stop closes.
	var wg sync.WaitGroup
	wg.Go(func() { b.watchRequiredFiles(run, requiredFilesEvery) })
	b.backup.Start(run)
	b.plaud.Start(run)
	b.site.Start(run)

	log.Println("Bridge started. Waiting for messages…")
	<-run.Done()
	wg.Wait()
	if ctx.Err() == nil {
		return context.Cause(run)
	}
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

// connectLoop retains the bridge's ownership of the connection goroutine.
func (b *Bridge) connectLoop(ctx context.Context) { whatsapp.RunConnectLoop(ctx, b.wa.Connect) }

// Stop shuts everything down in dependency order, once: a restart and a
// signal that come together, or a test and its cleanup, stop the bridge
// one time between them.
func (b *Bridge) Stop() { b.stopOnce.Do(b.stop) }

func (b *Bridge) stop() {
	// Intake first: what comes in from now on waits for the next start,
	// while the brains finish and WhatsApp stays connected for their
	// replies, and nothing that came in before is still on its way to the
	// debouncer or a brain.
	b.intake.close()
	b.stopHTTP()
	b.backup.Stop()
	b.plaud.Stop()
	b.site.Stop()
	// What no brain has started on is kept in bridge.db for the next
	// start (resume): WhatsApp took it as delivered and would not send it
	// again. What the debouncer holds first, then what the brains have
	// waiting, at once, before their runs in flight get their grace.
	for _, m := range b.deb.Drain() {
		b.keep(inboundHeld, "", m)
	}
	b.brainMgr.Stop()
	// After the brains, whose last runs may still reply; what does not go
	// out stays in bridge.db for the next start.
	b.outbox.Stop()
	b.otp.Stop()
	b.wa.Disconnect()
	// The connect loop ends with Start's context, unless it is connecting
	// still, which Disconnect cuts short.
	if !waitWithin(&b.loops, connectStopWait) {
		log.Printf("WhatsApp: still connecting %v after the disconnect; stopping without waiting for it", connectStopWait)
	}
	b.intake.end()
	if err := b.store.Close(); err != nil {
		log.Printf("closing store: %v", err)
	}
	b.lock.Release()
	log.Println("Bridge stopped.")
}

// keepWaiting keeps what br had waiting as the bridge stops, which never
// ran, for the next start (BrainHooks.Waiting).
func (b *Bridge) keepWaiting(br *Brain, ms []Inbound) {
	for _, m := range ms {
		b.keep(inboundQueued, br.ID(), m)
	}
}

// keep keeps m, of kind (see inbound_store.go), which brain had ("" for
// none), for the next start: a message from WhatsApp, which nothing would
// send again. Whoever handed in another hears that it was dropped
// (Inbound.Done), and can hand it in again.
func (b *Bridge) keep(kind, brain string, m Inbound) {
	if m.Source != chat.SourceWhatsApp {
		log.Printf("dropping a message for %s from %s as the bridge stops", m.JID, m.Source)
		dropped([]Inbound{m})
		return
	}
	if err := keepInbound(context.Background(), b.store, kind, brain, m); err != nil {
		log.Printf("%v; it is lost", err)
	}
}

// keepInterrupted keeps, for the next start, that the stop cut short br's
// run for m, so that m's chat hears of it then (BrainHooks.Interrupted).
// Not for an untagged message, whose run answers nobody, nor for a
// hand-off, whose sender heard through Done.
func (b *Bridge) keepInterrupted(br *Brain, m Inbound) {
	if m.Source != chat.SourceWhatsApp || m.Silent {
		return
	}
	if err := keepInterrupted(context.Background(), b.store, br.ID(), m); err != nil {
		log.Printf("brain %s: %v", br.ID(), err)
	}
}

// resume takes up what the bridge kept as it last stopped (see Stop),
// before WhatsApp connects: each chat whose run the stop cut short is told
// so, the messages no brain had started on go to the brains that take
// them now, and the messages and changes that came in during the stop are
// handled as if they came in now. A message or notice older than
// outboxTTL is dropped, since an answer that late would only confuse; a
// change is not, since a deletion must take its message out of the
// history however late.
func (b *Bridge) resume(ctx context.Context) {
	resumeRuns := b.brainMgr.slots.pause()
	defer resumeRuns()
	// Taken out of bridge.db, what was kept is handed on whole: a stop
	// that begins meanwhile keeps what is still waiting again, but a
	// notice it cut off would be lost.
	ctx = context.WithoutCancel(ctx)
	rows, err := takeInbound(ctx, b.store)
	if err != nil {
		log.Printf("taking up what the last stop kept: %v", err)
	}
	if len(rows) == 0 {
		return
	}
	kinds := map[string]int{}
	for _, r := range rows {
		kinds[r.Kind]++
	}
	log.Printf("Taking up what the last stop kept: %d messages, %d edits or deletions, %d runs it cut short",
		len(rows)-kinds[inboundChange]-kinds[inboundInterrupted], kinds[inboundChange], kinds[inboundInterrupted])
	now := time.Now()
	for _, r := range rows {
		if age := now.Sub(r.SentAt); age > outboundmsg.OutboxTTL && r.Kind != inboundChange {
			log.Printf("dropping a message for %s that the last stop kept: it came in %v ago, more than %v", r.Chat, age.Round(time.Minute), outboundmsg.OutboxTTL)
			continue
		}
		switch r.Kind {
		case inboundInterrupted:
			if err := b.outbox.Text(ctx, r.Chat, outboundmsg.RestartedNotice, b.cfgs.Get().MaxChunkSize); err != nil {
				log.Printf("brain %s: telling %s its run was cut short: %v", r.Brain, r.Chat, err)
			}
		case inboundQueued, inboundHeld:
			if _, _, _, err := b.brainMgr.Submit(r.Msg); err != nil {
				log.Printf("dropping a message for %s that the last stop kept: %v", r.Chat, err)
			}
		case inboundLate:
			b.handleInbound(ctx, r.Msg)
		case inboundChange:
			b.handleChange(r.Change)
		}
	}
}

// httpStopWait is how long Stop waits for the requests in flight to end
// before it cuts them off, a variable so tests need not wait for it.
var httpStopWait = 5 * time.Second

// connectStopWait is how long Stop waits for a WhatsApp connect under way.
const connectStopWait = 5 * time.Second

// stopHTTP stops the HTTP server, if Start began one: it takes no more
// requests, and those in flight get httpStopWait to end.
func (b *Bridge) stopHTTP() {
	if b.httpSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpStopWait)
	defer cancel()
	if err := b.httpSrv.Shutdown(ctx); err != nil {
		// Shutdown leaves the connections it did not see end open: Close
		// ends them, and the handlers still running find out on their
		// next write.
		log.Printf("HTTP server: requests still running %v after the stop began (%v); cutting them off", httpStopWait, err)
		_ = b.httpSrv.Close()
	}
}

// waitWithin waits for wg for at most d, and reports whether it ended.
func waitWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// intake counts the calls of handleInbound and handleChange at work.
// Stop closes it before anything else stops: from then on what comes in
// is kept for the next start instead (keepLate), and close waits for the
// calls at work, which may still hand a message to the debouncer or a
// brain. What comes in once the store is about to close (end) is lost.
type intake struct {
	mu     sync.Mutex
	closed bool
	ended  bool
	busy   sync.WaitGroup
}

// enter reports whether a call may act on what came in, and then counts
// it until leave.
func (in *intake) enter() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.closed {
		return false
	}
	in.busy.Add(1)
	return true
}

func (in *intake) leave() { in.busy.Done() }

// close makes what comes in from now on wait for the next start, and
// waits for the calls acting on what came in before.
func (in *intake) close() {
	in.mu.Lock()
	in.closed = true
	in.mu.Unlock()
	in.busy.Wait()
}

// keep runs fn, which keeps what came in once intake closed, unless the
// store is closing (end), and reports whether it did.
func (in *intake) keep(fn func()) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.ended {
		return false
	}
	fn()
	return true
}

// end is when the store closes: what comes in after is lost.
func (in *intake) end() {
	in.mu.Lock()
	in.ended = true
	in.mu.Unlock()
}

// keepLate keeps m, which came in once the bridge had begun to stop, for
// the next start, which handles it as if it came in then: nothing has
// looked at it yet, so no command ran, and the OTP guard is still to see
// it (see resume).
func (b *Bridge) keepLate(m Inbound) {
	if !b.intake.keep(func() { b.keep(inboundLate, "", m) }) {
		log.Printf("dropping a message for %s: it came in as the bridge closed its store", m.JID)
	}
}

// handleChange applies an edit or a deletion of a WhatsApp message: to
// the debouncer's batch that holds it, which then reaches the brain as
// changed, or else to the brain's queue, its run and its history. Once
// the bridge stops, it is kept for the next start instead, as messages
// are (see intake).
func (b *Bridge) handleChange(c chat.Change) {
	if !b.intake.enter() {
		kept := b.intake.keep(func() {
			if err := keepChange(context.Background(), b.store, c, time.Now()); err != nil {
				log.Printf("%v; it is lost", err)
			}
		})
		if !kept {
			log.Printf("dropping the %s of message %s in %s: it came in as the bridge closed its store", c.Kind, c.Target, c.Chat)
		}
		return
	}
	defer b.intake.leave()
	br := b.brainMgr.BrainFor(c.Chat, chat.IsGroupJID(c.Chat))
	// An edit to a one-time code is caught like the code itself
	// (handleInbound), before anything else sees it: a sender who corrects
	// a code hands the new one to the script that asked for it, and the
	// message edited stays as it was.
	if c.Kind == chat.ChangeEdit && br != nil {
		edited := Inbound{Inbound: chat.Inbound{JID: c.Chat, ChatAlt: c.ChatAlt, Sender: c.Sender, Text: c.Text, Source: chat.SourceWhatsApp}}
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
		text = chat.HistoryText(edited.TextWithQuote(), edited.Media)
	}
	b.activity.Change(br.ID(), activity.Change{
		Target: c.Target, Sender: c.Sender, Chat: c.Chat, ChatAlt: c.ChatAlt,
		Revoke: c.Kind == chat.ChangeRevoke,
	}, text)
}

// groupRules is what addresses the bridge in the group jid: what the
// routing of the brain that answers there says.
func (b *Bridge) groupRules(jid string) groupRules {
	if br := b.brainMgr.BrainFor(jid, true); br != nil {
		return groupRulesFor(br.settings.Load().manifest.Routing)
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
// on to their brain like the rest: it keeps them in its history. Once the
// bridge stops, a message is kept for the next start instead: no command
// runs and no notice goes out (see intake).
func (b *Bridge) handleInbound(ctx context.Context, m Inbound) {
	if !b.intake.enter() {
		b.keepLate(m)
		return
	}
	defer b.intake.leave()
	m.Text = strings.TrimSpace(m.Text)
	cfg := b.cfgs.Get()

	// Commands come from a chat. A message handed to a brain (/ingest, Plaud,
	// a dev message that names one) carries none: its text is for that brain.
	// A command must also be addressed to the bridge. In a group, an untagged
	// "!stop" is someone talking to the others: it must not stop anything,
	// and the bridge must not answer it.
	if m.Source != chat.SourceIngest && m.Brain == "" && !m.Silent && b.handleCommand(ctx, m, cfg) {
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
		reply := outboundmsg.NoBrainNotice
		if id := b.brainMgr.HeldFor(m.JID, m.IsGroup()); id != "" {
			// Not the default brain instead: what was meant for this
			// brain, your DMs say, must not land in another one.
			log.Printf("brain %s, which handles %s, cannot run (see its issue on the Brains page); the message goes to no other brain", id, m.JID)
			reply = outboundmsg.HeldBrainNotice
		} else {
			log.Printf("No brain configured for %s — add one in the dashboard", m.JID)
		}
		if !m.Silent && m.Source != chat.SourceIngest {
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
			_ = b.wa.SendMessage(ctx, m.JID, outboundmsg.OtpCaughtNotice)
		}
		return
	}

	b.deb.Add(m)
}

// handleCommand runs !help, !reset and !stop. It reports whether text was one.
func (b *Bridge) handleCommand(ctx context.Context, m Inbound, cfg *config.Config) bool {
	switch strings.ToLower(m.Text) {
	case "!reset":
		// A reset stops what !stop stops for the chat, hand-offs from other
		// brains included, and starts the chat afresh: a brain run never
		// resumes a session, each starts from the chat's recent history,
		// which the next one reads only from the reset on. Say only what
		// happened: the brain's memory files stay, and a chat no brain
		// answers has no history to start afresh.
		b.stopChat(m)
		_ = b.wa.SendMessage(ctx, m.JID, outboundmsg.ResetReply(b.resetChat(m)))
		return true

	case "!stop":
		_ = b.wa.SendMessage(ctx, m.JID, outboundmsg.StopReply(b.stopChat(m)))
		return true

	case "!help":
		trigger := ""
		if m.IsGroup() {
			trigger = cfg.Triggers.First()
		}
		_ = b.wa.SendMessage(ctx, m.JID, outboundmsg.HelpReply(trigger))
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
	for _, jid := range []string{m.ChatAlt, chat.DMJID(m.Sender)} {
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
			_ = b.wa.SendMessage(ctx, m.JID, outboundmsg.QueueFullNotice(stop))
		}
	case err != nil:
		log.Printf("routing message for %s: %v", m.JID, err)
	case busy && !m.Silent && cfg.QueueNotify():
		// Interrupt mode queues too: behind other chats and other people.
		_ = b.wa.SendMessage(ctx, m.JID, outboundmsg.QueuedNotice(pos))
	}
}

// deliver sends a finished brain run's answer.
func (b *Bridge) deliver(br *Brain, m Inbound, res claudecli.Result) {
	ctx := context.Background()
	if res.Err != nil {
		if errors.Is(res.Err, claudecli.ErrInterrupted) || errors.Is(res.Err, context.Canceled) {
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
func (b *Bridge) delivered(ctx context.Context, r store.OutboxRow) error {
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
func (b *Bridge) undelivered(brainID string) []history.Entry {
	rows, err := b.store.UndeliveredReplies(context.Background(), brainID)
	if err != nil {
		log.Printf("brain %s: reading its replies in the outbox: %v", brainID, err)
		return nil
	}
	out := make([]history.Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, history.Entry{TS: r.CreatedAt.Local().Format(time.RFC3339), From: brainID, Chat: r.Chat, Text: r.History, Type: history.Out})
	}
	return out
}

// logDMAccess says in the log who may DM the bridge: at startup (prev nil),
// and again when an edit opens DMs to everyone. Allowed senders run Claude
// with all its permissions, so an open allowlist must not go unnoticed, and
// a closed one explains why DMs get no answer.
func logDMAccess(prev, next *config.Config) {
	switch {
	case next.AnyoneCanDM() && (prev == nil || !prev.AnyoneCanDM()):
		log.Printf("WARNING: anyone who can message this WhatsApp account can use the assistant: allowed_numbers is empty and allow_any_dm is true. Add your number under Chats → Allowed numbers, or turn allow_any_dm off.")
	case prev == nil && len(next.AllowedNumbers) == 0:
		log.Printf("allowed_numbers is empty, so nobody can send the bridge direct messages yet. Add your number under Chats → Allowed numbers.")
	}
}

// groupRulesFor is what addresses the bridge in the brain's groups.
func groupRulesFor(r manifest.Routing) groupRules {
	return groupRules{files: config.BoolOr(r.AnswerFiles, true), forwards: config.BoolOr(r.AnswerForwards, true)}
}

// Configuration edits (from the dashboard) apply to the brains at once.
// The brains get the configuration as it is now rather than next, under
// a lock, so that whichever notification comes last, the brains end on
// the latest configuration and never on one an update has replaced.
func (b *Bridge) configChanged(prev, next *config.Config) {
	b.applyMu.Lock()
	err := b.brainMgr.Apply(runtimeConfig(b.cfgs))
	b.applyMu.Unlock()
	if err != nil {
		log.Printf("applying configuration: %v", err)
	}
	logDMAccess(prev, next)
	config.LogSecurityChanges(prev, next)
	for _, br := range b.brainMgr.Brains() {
		seedActivity(b.activity, br)
	}
}
