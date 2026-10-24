package app

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"

	sitesvc "github.com/Icewater1337/fridolin/internal/site"

	"github.com/Icewater1337/fridolin/internal/backup"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/devchat"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/pipeline"
	"github.com/Icewater1337/fridolin/internal/plaud"

	"github.com/Icewater1337/fridolin/internal/activity"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/intent"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/otp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/tailnet"
	"github.com/Icewater1337/fridolin/internal/whatsapp"
	"github.com/Icewater1337/fridolin/internal/wiki"

	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// App connects WhatsApp to the brains and serves the dashboard.
type App struct {
	logger    *slog.Logger
	bind      func(string, string) (net.Listener, error)
	runMu     sync.Mutex
	runCancel context.CancelCauseFunc
	runUsed   bool
	runDone   chan struct{}
	cfgs      *config.Store
	lock      *platform.Lock
	store     *store.Store
	wa        Messenger
	dev       *devchat.Messenger // non-nil in --no-whatsapp mode
	brainMgr  *BrainManager
	deb       *pipeline.Debouncer
	pipeline  *pipeline.Workflow
	logs      *logx.Ring
	activity  *activity.Feed
	token     *httpapi.Token
	// tokenAtStart is dashboard.require_token as the bridge started.
	tokenAtStart bool
	sessions     *httpapi.DashboardSessions
	// sessionCookie names the session cookie for the address served on.
	sessionCookie string
	scriptAuth    *scriptAuth
	backup        *backup.Manager
	vault         *vaultService
	// housekeeping is the job that stops what the bridge left running
	// (housekeeping_runtime.go).
	housekeeping *housekeepingService
	targets      *targetService
	google       *google.Auth
	plaud        *plaud.Manager
	// github is the GitHub App and the runs' tokens (github_runtime.go).
	github *githubService
	site   *sitesvc.SiteManager
	// nodes is the fleet's registry and node port; nodesEnabled and
	// nodesListen are nodes.enabled and nodes.listen as the bridge
	// started, which wait for a restart.
	nodes *nodes.Service
	// coreMove is the !movecore checked or waiting for its vote
	// (coremove_runtime.go).
	coreMove coreMoveAsk
	// programs keeps the nodes' allowlists in step with the approved
	// command agents (node_programs.go).
	programs     *nodePrograms
	nodesEnabled bool
	nodesListen  string
	// fride is the core's side of FrIDE on the nodes (fride_runtime.go).
	fride *frideRuntime
	// work is the core's side of the workstation mode: agent sessions
	// and terminals on nodes (workstation_runtime.go).
	work    *workRuntime
	outbox  *outboundmsg.Outbox
	otp     otp.Handoff
	started time.Time
	// restart is the optional embedding owner callback.
	restart func()
	// env is the environment the bridge reads and hands its programs
	// (Options.Env).
	env Environment
	// cliVersions caches the versions of the agent CLIs the bridge found
	// (claudeInfo).
	cliVersions claudecli.Prober

	// listen is the address the HTTP server binds, "" when it is off;
	// runs reach the brain API there with a token from runTokens.
	listen    string
	runTokens runTokens
	// exe is the binary brains run (see brainRunEnv).
	exe string
	// mcpTools are the tools runs reach through fridolin mcp (mcpTools),
	// delegations the tasks core runs handed to sub-brains that have not
	// ended, and audit the log they are written to.
	mcpTools    *mcp.Registry[mcpCaller]
	delegations delegations
	audit       *audit.Log
	// wikis caches the brains' wiki indexes (files_runtime.go).
	wikis *wiki.Manager
	// fileTickets open /files/raw for the dashboard's previews.
	fileTickets *httpapi.Tickets
	// siteBridge serves the site's /files and /wiki (site_bridge.go).
	siteBridge siteBridge
	// intents are the brains' standing intents and scheduled jobs, which
	// sched fires.
	intents *intent.Store
	sched   *scheduler
	// agents are the brains' scheduled agents (schedule_runtime.go),
	// which sched starts when they fall due.
	agents *agents
	// policy is the lockdown and the runs' rate, waiters the tool calls
	// that wait for the owner's answer (approvals.go).
	policy  policyState
	waiters approvalWaiters
	// ownerAsks counts the owner's !join and !dashboard (owner_commands.go).
	ownerAsks ownerCommandRate
	// lids is whatsmeow's map between LIDs and phone numbers, nil in dev
	// mode, for what knows a person by LID outside an event: a /send to
	// one.
	lids func() wastore.LIDStore

	supervisor Supervisor
	// intake is what comes in from WhatsApp, which Stop closes first.
	intake IntakeGate

	// core is the tailnet node and the core lease (core_runtime.go).
	core coreRuntime
	// b2 counts the Backblaze B2 transactions of the lease and the vault
	// per UTC day (b2usage.go); b2Warned the warnings logged today.
	b2       *cloud.Usage
	b2Warned map[string]bool

	// registry owns the brain workflows, including add serialization.
	registry *registry.Registry
	// applyMu keeps reading the configuration and applying it to the
	// brains together, see the config subscriber in New.
	applyMu sync.Mutex
}

// Options supplies configuration and host capabilities for construction.
type Options struct {
	// Logger receives lifecycle records; nil uses the standard process bridge.
	Logger *slog.Logger
	// Listen overrides listener creation for an embedding host or isolated tests.
	Listen     func(string, string) (net.Listener, error)
	Config     *config.Store
	NoWhatsApp bool
	Logs       *logx.Ring
	// Restart overrides restart handling for an embedding owner. When nil,
	// Run handles a dashboard restart and returns ErrRestart after teardown.
	Restart func()
	// Container: the bridge runs in the fridolin-core image, whose secrets
	// come only from its environment (the vault never reads or makes host
	// secrets there).
	Container bool
	// Tailnet makes the core's tailnet node when core.tailnet.enabled; nil
	// uses tsnet (tailnet.New). Tests pass a fake.
	Tailnet tailnet.Factory
	// TailnetAuthKey joins the tailnet the first time (TS_AUTHKEY).
	TailnetAuthKey string
	// LeaseStore is the vault bucket that holds the core lease; New
	// refuses core.lease.enabled without one.
	LeaseStore cloud.ObjectStore
	// LeaseSettle is how long taking the lease waits before it reads its
	// write back (default 3 s; tests shorten it).
	LeaseSettle time.Duration
	// LeaseRetry is the first wait before a lease renewal that failed on
	// the store is tried again (default 30 s; tests shorten it).
	LeaseRetry time.Duration
	// Env is the environment the bridge reads while it runs and hands, in
	// part, to the programs it starts; nil is the process's own. The home
	// folder comes from the config store (Boot.Home), not from Env.
	Env Environment
}

// New creates and wires up all components. It holds the data
// directory's instance lock until Stop.
func New(opts Options) (_ *App, err error) {
	cfgs := opts.Config
	if cfgs == nil {
		return nil, fmt.Errorf("application configuration is required")
	}
	cfg := RuntimeConfig(cfgs)
	// Before the lock, which is a file in the data dir.
	if err := cfgs.Boot().EnsureDirs(); err != nil {
		return nil, err
	}
	// Taken before the store opens: a second bridge on the same data dir, dev
	// mode or a test included, must not open the live WhatsApp session's
	// database, let alone run whatsmeow's migrations on it.
	lock, err := platform.AcquireLock(cfgs.Boot().Layout().Lock())
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

	b := &App{
		logger:   opts.Logger,
		bind:     opts.Listen,
		cfgs:     cfgs,
		registry: registryFor(cfgs),
		lock:     lock,
		store:    store,
		logs:     opts.Logs,
		activity: activity.New(500),
		started:  time.Now(),
		restart:  opts.Restart,
		env:      envOr(opts.Env),
		core: coreRuntime{tailnetFactory: opts.Tailnet, tailnetAuthKey: opts.TailnetAuthKey,
			leaseStore: opts.LeaseStore, leaseSettle: opts.LeaseSettle, leaseRetry: opts.LeaseRetry},
		b2: loadB2Usage(cfgs.Boot()),
	}
	if st, ok := opts.LeaseStore.(interface{ SetUsage(*cloud.Usage, string) }); ok {
		st.SetUsage(b.b2, "lease")
	}
	// The lease before any channel exists: a copy of the core that another
	// device runs must not even connect.
	if err := b.acquireLease(); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			b.releaseLease()
			b.closeLeaseStore()
		}
	}()
	if cfg.HTTPEnabled {
		// A change to http_listen waits for a restart: until then the
		// server listens, and runs are told, where it started.
		b.listen = cfg.HTTPListen
	}
	b.exe = bridgeBinary()
	b.audit = newAuditLog(cfg.DataDir)
	b.wikis = wiki.NewManager()
	b.fileTickets = httpapi.NewTickets()
	b.loadPolicy()
	b.mcpTools = b.newMCPTools()
	b.fride = newFrideRuntime(b)
	b.work = newWorkRuntime(b)
	b.programs = newNodePrograms(b)
	if b.nodes, err = newNodes(b); err != nil {
		return nil, err
	}
	b.nodesEnabled, b.nodesListen = cfgs.Boot().NodesEnabled, cfgs.Boot().NodesListen
	if b.logs == nil {
		b.logs = logx.NewRing(LogRingLines)
		var sink slog.Handler = logx.StandardHandler()
		if b.logger != nil {
			sink = b.logger.Handler()
		}
		b.logger = slog.New(logx.Fanout(sink, logx.NewTextHandler(b.logs, nil)))
	}

	mgr, err := NewBrainManager(cfg, b.env)
	if err != nil {
		return nil, fmt.Errorf("creating brain manager: %w", err)
	}
	defer func() {
		if err != nil {
			mgr.Stop()
		}
	}()
	b.brainMgr = mgr
	mgr.profiler.SetConfigFile(cfgs.Path())
	for _, is := range mgr.Issues() {
		log.Printf("brain %s (%s): %s — %s", is.ID, is.Path, is.Kind, is.Detail)
	}
	logDMAccess(nil, cfg)

	b.pipeline = b.newMessagePipeline()
	if b.nodesEnabled {
		b.pipeline.Move = b.fride.moveCommand
	}
	b.deb = pipeline.NewDebouncer(
		func() time.Duration { return b.cfgs.Get().Live.Debounce.Quiet },
		func() time.Duration { return b.cfgs.Get().Live.Debounce.MaxWait },
		b.pipeline.Dispatch,
	)
	b.pipeline.Debouncer = b.deb

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
	if b.core.lease != nil {
		b.wa = leaseGate{Messenger: b.wa, lost: b.core.lost.Load}
	}

	b.intents = intent.NewStore(cfgs.Boot().Layout().Intents)
	b.sched = &scheduler{b: b, every: schedulerEvery}
	b.agents = newAgents(b, cfgs.Boot().Layout())
	// Runs the last stop cut short end now, before anyone lists them.
	b.agents.recover(time.Now())
	b.brainMgr.SetHooks(BrainHooks{
		Notice:      b.workingNotice,
		Question:    b.askQuestion,
		Deliver:     b.deliverRun,
		Undelivered: b.undelivered,
		Waiting:     b.keepWaiting,
		Interrupted: b.keepInterrupted,
		OnHistory: func(id string, e history.Entry) {
			b.activity.Add(id, e)
			b.observeIntents(id, e)
		},
		ExtraArgs:  func(id string) []string { return b.site.BuilderArgs(id) },
		CodexSetup: func(id string) ([]string, []string, string, error) { return b.site.CodexBuilder(id) },
		RunEnv:     b.brainRunEnv,
		Recall:     b.recall,
		Hydrate:    func(ctx context.Context, id string, paths []string) { b.vault.hydrate(ctx, id, paths) },
		Admit:      b.admitRun,
		Ended:      b.runEnded,
	})
	for _, br := range b.brainMgr.Brains() {
		seedActivity(b.activity, br)
	}

	tok, err := httpapi.LoadDashboardToken(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	b.token = tok
	b.tokenAtStart = cfg.Dashboard.RequireToken
	if b.sessions, err = httpapi.LoadDashboardSessions(cfg.DataDir, time.Now); err != nil {
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
	b.vault = newVaultService(cfgs, b.env, opts.Container, b.logger, b.vaultBrains, b.openMedia)
	b.housekeeping = newHousekeeping(b, opts.Container)
	b.vault.usage = b.b2
	b.targets = newTargetService(cfgs, b.vault, b.nodes, opts.Container, b.logger)
	b.github = newGitHubService(cfgs, b.env, opts.Container, b.logger)
	b.vault.targets = b.targets
	b.google = google.New(googleOptionsFor(cfgs))
	b.backup = newProjectedBackup(cfgs, b.env, b.brainMgr, b.notifyBackup, b.vault.driveSuperseded, b.google)
	b.plaud = NewPlaudManager(cfgs, b.env, b.brainMgr, b.submitHandOff, b.submitAsk, b.wa.SendMessage, func() string {
		if d := b.site.Settings().Domain; d != "" {
			return "https://" + d
		}
		return ""
	}, newPlaudFleet(b.nodes, b.locked))
	b.site = NewSiteManager(sitesvc.SiteOptions{DataDir: cfg.DataDir, GoogleClient: b.google.ClientPath, Builder: b.siteBuilderHooks(), Container: opts.Container,
		Host: siteHostFor(b.env, func() string { return cfgs.Boot().Home })})
	// What builder brains say in the Build page's chats goes there, not to WhatsApp.
	b.wa = siteChatMessenger{Messenger: b.wa, appendMessage: b.site.AppendBuildMessage}
	pace := outboundmsg.OutboxPace
	if opts.NoWhatsApp {
		pace = 0
	}
	b.outbox = outboundmsg.NewPausedOutbox(store, func() outboundmsg.Link { return b.wa }, pace, b.delivered)
	cfgs.Subscribe(b.configChanged)
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
func (b *App) Start(ctx context.Context) error {
	cfg := RuntimeConfig(b.cfgs)
	bind := b.bind
	if bind == nil {
		bind = listenHTTP
	}
	return b.supervisor.Run(ctx, RunOptions{Logger: b.logger,
		Address: b.listen, Listen: bind,
		Handler:          func() http.Handler { return b.newHTTPHandler(b.listen) },
		DashboardEnabled: !cfg.Dashboard.Disabled,
		Resume:           func(ctx context.Context) { b.outbox.Start(); b.resume(ctx) }, Connect: b.connectLoop,
		Watch: func(ctx context.Context) { b.watchRequiredFiles(ctx, requiredFilesEvery) },
		// The tailnet first: the node port listens on it.
		Background: []func(context.Context){b.startTailnet, b.runLease, b.runB2Usage, b.backup.Start, b.plaud.Start, b.site.Start, b.startNodes, b.sched.Start, b.vault.Start, b.targets.Start, b.watchApprovals, b.housekeeping.Start},
	})
}

// connectLoop retains the bridge's ownership of the connection goroutine.
// A lost core lease ends it (leaseLost).
func (b *App) connectLoop(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.core.mu.Lock()
	b.core.stopConnect = cancel
	b.core.mu.Unlock()
	if b.core.lost.Load() {
		return
	}
	whatsapp.RunConnectLoop(ctx, b.wa.Connect)
}

// Stop shuts everything down in dependency order, once: a restart and a
// signal that come together, or a test and its cleanup, stop the bridge
// one time between them.
// Stop cancels an owned Run and waits for its teardown. Before Run it releases
// construction resources and permanently closes the application.
func (b *App) Stop() {
	b.runMu.Lock()
	done, cancel := b.runDone, b.runCancel
	b.runUsed = true
	b.runMu.Unlock()
	if done != nil {
		if cancel != nil {
			cancel(nil)
		}
		<-done
		return
	}
	b.stopResources()
}

func (b *App) stopResources() {
	b.supervisor.Stop(StopOptions{Logger: b.logger,
		CloseIntake: b.intake.Close, HTTPGrace: httpStopWait,
		Background: []func(){b.sched.Stop, b.backup.Stop, b.plaud.Stop, b.site.Stop, b.stopSiteBridge, b.nodes.Stop, b.fride.Stop, b.vault.Stop, b.targets.Stop, b.stopTailnet},
		KeepPending: func() {
			for _, m := range b.deb.Drain() {
				b.keep(InboundHeld, "", m)
			}
		},
		Brains: b.brainMgr.Stop, Outbox: b.outbox.Stop, OTP: b.otp.Stop,
		Disconnect: b.wa.Disconnect, ConnectionGrace: connectStopWait,
		EndIntake: b.intake.End, CloseStore: b.store.Close,
		// The lease goes free once nothing here sends any more, and before
		// the data dir's lock: the next core may start right after.
		ReleaseLock: func() { b.releaseLease(); b.closeLeaseStore(); b.saveB2Usage(); b.lock.Release() },
	})
}

// keepWaiting keeps what br had waiting as the bridge stops, which never
// ran, for the next start (BrainHooks.Waiting).
func (b *App) keepWaiting(br *brainruntime.Brain, ms []brainruntime.Inbound) {
	for _, m := range ms {
		b.keep(InboundQueued, br.ID(), m)
	}
}

// keep keeps m, of kind (see inbound_store.go), which brain had ("" for
// none), for the next start: a message from WhatsApp, which nothing would
// send again. Whoever handed in another hears that it was dropped
// (Inbound.Done), and can hand it in again.
func (b *App) keep(kind, brain string, m brainruntime.Inbound) {
	if m.Source != chat.SourceWhatsApp {
		log.Printf("dropping a message for %s from %s as the bridge stops", m.JID, m.Source)
		brainruntime.Dropped([]brainruntime.Inbound{m})
		return
	}
	if err := KeepInbound(context.Background(), b.store, kind, brain, m); err != nil {
		log.Printf("%v; it is lost", err)
	}
}

// keepInterrupted keeps, for the next start, that the stop cut short br's
// run for m, so that m's chat hears of it then (BrainHooks.Interrupted).
// Not for an untagged message, whose run answers nobody, nor for a
// hand-off, whose sender heard through Done.
func (b *App) keepInterrupted(br *brainruntime.Brain, m brainruntime.Inbound) {
	if m.Source != chat.SourceWhatsApp || m.Silent {
		return
	}
	if err := KeepInterrupted(context.Background(), b.store, br.ID(), m); err != nil {
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
func (b *App) resume(ctx context.Context) {
	// brain.json's scheduled agents move into their own files
	// (.brain/schedules), once as the bridge starts.
	b.registry.MigrateScheduleFiles()
	resumeRuns := b.brainMgr.PauseRuns()
	defer resumeRuns()
	// Taken out of bridge.db, what was kept is handed on whole: a stop
	// that begins meanwhile keeps what is still waiting again, but a
	// notice it cut off would be lost.
	ctx = context.WithoutCancel(ctx)
	rows, err := TakeInbound(ctx, b.store)
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
		len(rows)-kinds[InboundChange]-kinds[InboundInterrupted], kinds[InboundChange], kinds[InboundInterrupted])
	now := time.Now()
	for _, r := range rows {
		if age := now.Sub(r.SentAt); age > outboundmsg.OutboxTTL && r.Kind != InboundChange {
			log.Printf("dropping a message for %s that the last stop kept: it came in %v ago, more than %v", r.Chat, age.Round(time.Minute), outboundmsg.OutboxTTL)
			continue
		}
		switch r.Kind {
		case InboundInterrupted:
			if err := b.outbox.Text(ctx, r.Chat, outboundmsg.RestartedNotice, b.cfgs.Get().MaxChunkSize); err != nil {
				log.Printf("brain %s: telling %s its run was cut short: %v", r.Brain, r.Chat, err)
			}
		case InboundQueued, InboundHeld:
			if _, _, _, err := b.brainMgr.Submit(r.Msg); err != nil {
				log.Printf("dropping a message for %s that the last stop kept: %v", r.Chat, err)
			}
		case InboundLate:
			b.handleInbound(ctx, r.Msg)
		case InboundChange:
			b.handleChange(r.Change)
		}
	}
}

// httpStopWait is how long Stop waits for the requests in flight to end
// before it cuts them off, a variable so tests need not wait for it.
var httpStopWait = 5 * time.Second

// connectStopWait is how long Stop waits for a WhatsApp connect under way.
const connectStopWait = 5 * time.Second

// keepLate keeps m, which came in once the bridge had begun to stop, for
// the next start, which handles it as if it came in then: nothing has
// looked at it yet, so no command ran, and the OTP guard is still to see
// it (see resume).
func (b *App) keepLate(m brainruntime.Inbound) {
	if !b.intake.Keep(func() { b.keep(InboundLate, "", m) }) {
		log.Printf("dropping a message for %s: it came in as the bridge closed its store", m.JID)
	}
}

// handleChange applies an edit or a deletion of a WhatsApp message: to
// the debouncer's batch that holds it, which then reaches the brain as
// changed, or else to the brain's queue, its run and its history. Once
// the bridge stops, it is kept for the next start instead, as messages
// are (see intake).
func (b *App) handleChange(c chat.Change) {
	if !b.intake.Enter() {
		kept := b.intake.Keep(func() {
			if err := KeepChange(context.Background(), b.store, c, time.Now()); err != nil {
				log.Printf("%v; it is lost", err)
			}
		})
		if !kept {
			log.Printf("dropping the %s of message %s in %s: it came in as the bridge closed its store", c.Kind, c.Target, c.Chat)
		}
		return
	}
	defer b.intake.Leave()
	br := b.brainMgr.BrainFor(c.Chat, chat.IsGroupJID(c.Chat))
	// An edit to a one-time code is caught like the code itself
	// (handleInbound), before anything else sees it: a sender who corrects
	// a code hands the new one to the script that asked for it, and the
	// message edited stays as it was.
	if c.Kind == chat.ChangeEdit && br != nil {
		edited := brainruntime.Inbound{Inbound: chat.Inbound{JID: c.Chat, ChatAlt: c.ChatAlt, Sender: c.Sender, Text: c.Text, Source: chat.SourceWhatsApp}}
		if code, ok := br.CatchesCode(edited, time.Now()); ok {
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
	br.RecordChange(c, waiting, edited)
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
func (b *App) groupRules(jid string) groupRules {
	if br := b.brainMgr.BrainFor(jid, true); br != nil {
		return groupRulesFor(br.Manifest().Routing)
	}
	return defaultGroupRules
}

// recordIgnored notes a chat that wrote without being allowed.
func (b *App) recordIgnored(kind, jid, name, sender, pushName string) {
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
func (b *App) handleInbound(ctx context.Context, m brainruntime.Inbound) {
	if !b.intake.Enter() {
		b.keepLate(m)
		return
	}
	defer b.intake.Leave()
	b.pipeline.Handle(ctx, m)
}

// chatJIDs lists the JIDs m's chat goes by: the chat and, for a DM, its
// other JID and the sender's phone number. The bridge keys a DM by the
// number, but WhatsApp may address it by LID, history from before may be
// under the LID, and a hand-off that answers there (a reply_chat, the
// owner's DM) may name either.
func chatJIDs(m brainruntime.Inbound) []string {
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

// delivered takes a reply that went out in full out of the outbox and
// records it in the history of the brain that wrote it: only now is it
// something the brain said. The history gets it only once its row is gone,
// so the retry after a failed removal does not record it twice.
func (b *App) delivered(ctx context.Context, r store.OutboxRow) error {
	remove := func() error { return b.store.OutboxDelivered(ctx, r.ID) }
	if r.Brain == "" || r.History == "" {
		return remove()
	}
	br := b.brainMgr.Get(r.Brain)
	if br == nil {
		log.Printf("brain %s: delivered a reply to %s, but the brain is gone", r.Brain, r.Chat)
		return remove()
	}
	return br.RecordDelivered(r.Chat, r.History, remove)
}

// undelivered is what brainID answered that waits in the outbox, as its
// history will record it (BrainHooks.Undelivered).
func (b *App) undelivered(brainID string) []history.Entry {
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
func logDMAccess(prev, next *config.Settings) {
	switch {
	case next.AnyoneCanDM() && (prev == nil || !prev.AnyoneCanDM()):
		log.Printf("WARNING: anyone who can message this WhatsApp account can use the assistant: allowed_numbers is empty and allow_any_dm is true. Add your number under Chats → Allowed numbers, or turn allow_any_dm off.")
	case prev == nil && len(next.AllowedNumbers) == 0:
		log.Printf("allowed_numbers is empty, so nobody can send the bridge direct messages yet. Add your number under Chats → Allowed numbers.")
	}
}

// groupRulesFor is what addresses the bridge in the brain's groups.
func groupRulesFor(r manifest.Routing) groupRules {
	return groupRules{files: r.AnswerFiles == nil || *r.AnswerFiles, forwards: r.AnswerForwards == nil || *r.AnswerForwards}
}

// Configuration edits (from the dashboard) apply to the brains at once.
// The brains get the configuration as it is now rather than next, under
// a lock, so that whichever notification comes last, the brains end on
// the latest configuration and never on one an update has replaced.
func (b *App) configChanged(prev, next *config.Settings) {
	b.applyMu.Lock()
	err := b.brainMgr.Apply(RuntimeConfig(b.cfgs))
	b.applyMu.Unlock()
	if err != nil {
		log.Printf("applying configuration: %v", err)
	}
	logDMAccess(prev, next)
	config.LogSecurityChanges(prev, next)
	b.vault.Apply(next)
	for _, br := range b.brainMgr.Brains() {
		seedActivity(b.activity, br)
	}
}
