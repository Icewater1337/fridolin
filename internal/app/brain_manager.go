package app

import (
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/route"
)

// BrainManager composes the runtime with configuration, host policy and
// resolution diagnostics. Those dependencies stay outside the runtime core.
type BrainManager struct {
	*brain.Manager
	applyMu  sync.Mutex
	profiler *SandboxProfiler
	hooks    *BrainHooks
	// waits are how long its runs and its stop wait.
	waits waits

	// Diagnostics publish just after a successful runtime reload. Independent
	// route and issue reads are not a transaction, but reading diagnostics
	// never waits for filesystem checks or a sandbox profile update.
	issuesMu sync.RWMutex
	issues   []registry.Issue
}

// newCommandBrainManager builds the manager whose runs get their
// environment from env and their home folder from cfg, and wait as w says.
func newCommandBrainManager(cfg *config.Settings, env Environment, w waits) *BrainManager {
	hooks := &BrainHooks{}
	profiler := NewSandboxProfiler(nil, cfg)
	return &BrainManager{
		profiler: profiler, hooks: hooks, waits: w,
		Manager: brain.NewManager(brain.Options{
			Runner: &commandRunner{sandbox: profiler, hooks: hooks, env: env, home: cfg.Home, waits: w},
			Sink:   brainHooksSink{hooks}, Input: commandBrainInputRules(hooks),
			ViewerStart: brainViewerStart(env), ClaimMedia: claimBrainMedia,
			ShutdownGrace: func() time.Duration { return w.shutdownGrace },
		}),
	}
}

// NewBrainManager builds the configured brains, whose runs read env (nil
// is the process's own).
func NewBrainManager(cfg *config.Settings, env Environment) (*BrainManager, error) {
	return newBrainManager(cfg, env, defaultWaits())
}

// newBrainManager is NewBrainManager with the waits w.
func newBrainManager(cfg *config.Settings, env Environment, w waits) (*BrainManager, error) {
	bm := newCommandBrainManager(cfg, envOr(env), w)
	if err := bm.Apply(cfg); err != nil {
		return nil, err
	}
	return bm, nil
}

// SetHooks wires the command's provider and delivery adapters before intake.
func (bm *BrainManager) SetHooks(h BrainHooks) {
	*bm.hooks = h
	bm.SetSink(brainHooksSink{bm.hooks})
}

// Apply establishes the bridge PATH before resolving brain folders against
// it, then passes a value projection and host callbacks to the runtime.
func (bm *BrainManager) Apply(cfg *config.Settings) error {
	bm.applyMu.Lock()
	defer bm.applyMu.Unlock()
	ownPATH := SetBridgePATH(cfg)
	specs, issues := ResolveBrains(cfg)
	return bm.reloadLocked(specs, issues, cfg, ownPATH)
}

func (bm *BrainManager) reload(specs []manifest.Spec, issues []registry.Issue, cfg *config.Settings, ownPATH map[string][]string) error {
	bm.applyMu.Lock()
	defer bm.applyMu.Unlock()
	return bm.reloadLocked(specs, issues, cfg, ownPATH)
}

func (bm *BrainManager) reloadLocked(specs []manifest.Spec, issues []registry.Issue, cfg *config.Settings, ownPATH map[string][]string) error {
	failures, err := bm.Manager.Apply(specs, brainGlobalsOf(cfg, bm.waits.runTimeoutUnit), brain.ApplyOptions{
		Held: HeldBrainRoutes(issues), OwnPATH: ownPATH,
		SetDirs: func(dirs map[string][]string) { bm.profiler.Update(dirs, cfg) },
	})
	if err != nil {
		return err
	}
	next := append([]registry.Issue(nil), issues...)
	for _, failure := range failures {
		sp := failure.Spec
		next = append(next, registry.Issue{
			ID: sp.Manifest.ID, Path: sp.Dir, Kind: "missing",
			Detail: "the folder cannot be opened: " + failure.Err.Error(),
			AtLoad: true, Entry: sp.Entry, Routing: sp.Manifest.Routing,
		})
	}
	bm.issuesMu.Lock()
	bm.issues = next
	bm.issuesMu.Unlock()
	return nil
}

func HeldBrainRoutes(issues []registry.Issue) []route.Held {
	var held []route.Held
	for _, issue := range issues {
		if issue.LeavesOut() {
			held = append(held, route.Held{ID: issue.ID, Routing: issue.Routing})
		}
	}
	return held
}

// Issues reports the last successful reload's resolution and open failures.
func (bm *BrainManager) Issues() []registry.Issue {
	bm.issuesMu.RLock()
	defer bm.issuesMu.RUnlock()
	return append([]registry.Issue(nil), bm.issues...)
}
