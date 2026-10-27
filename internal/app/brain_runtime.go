package app

import (
	"cmp"
	"context"
	"log"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/sandboxrun"
)

// brainRuntimes chooses where each brain's runs happen: under the
// bridge's own sandbox (bubblewrap, sandbox-exec on a native Mac) or in
// a Docker Sandbox on a node (docs/SANDBOXES.md). Brains set to
// bubblewrap, the default, never reach it: nothing changes for them.
type brainRuntimes struct {
	b       *App
	cache   *sandboxrun.Cache
	outages sandboxrun.Outages
	// marker is this core's runtime id, which it writes into the folder
	// of each brain it runs in a Docker Sandbox (markerFile) and which
	// sandbox.ensure checks: a new one each start, so a node never runs a
	// brain in a copy of its folder that a core move left behind.
	marker string
}

func newBrainRuntimes(b *App) *brainRuntimes {
	r := &brainRuntimes{b: b, marker: randomToken(16)}
	r.cache = sandboxrun.NewCache(r.look, b.waits.sandboxLook, nil)
	return r
}

// find picks the node whose sandboxes run brains: sandbox.node, or the
// one approved node that offers them. Why says why there is none to use.
func (r *brainRuntimes) find(fleet sandboxFleet) sandboxrun.State {
	if fleet == nil {
		return sandboxrun.State{Why: "nodes are off (nodes.enabled)"}
	}
	ref := r.b.cfgs.Get().Sandbox.Node
	var found []sandboxNode
	for _, n := range fleet.SandboxNodes() {
		if ref == "" || n.ID == ref || n.Name == ref {
			found = append(found, n)
		}
	}
	switch {
	case len(found) == 0 && ref != "":
		return sandboxrun.State{Why: "the node " + ref + " (sandbox.node) does not offer Docker Sandboxes, or they are not approved"}
	case len(found) == 0:
		return sandboxrun.State{Why: "no approved node offers Docker Sandboxes"}
	case len(found) > 1:
		return sandboxrun.State{Why: "several nodes offer Docker Sandboxes: name one in sandbox.node"}
	case !found[0].Online:
		return sandboxrun.State{Node: found[0].ID, Name: found[0].Name, Why: found[0].Name + " is offline"}
	}
	return sandboxrun.State{Node: found[0].ID, Name: found[0].Name}
}

// look asks the node about its sandboxes, and tells the owner when they
// stopped being ready or are back.
func (r *brainRuntimes) look(ctx context.Context) sandboxrun.State {
	fleet := r.b.sandboxFleet()
	s := r.find(fleet)
	if s.Why == "" {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := fleet.Call(ctx, s.Node, nodeproto.MethodSandboxInfo, struct{}{}, &s.Info); err != nil {
			s.Why = "asking " + s.Name + " about its Docker Sandboxes failed: " + err.Error()
		} else {
			// With the provider every brain runs (agent_provider): its login
			// missing in sbx is an outage too.
			s.Why = sandboxNotReady(&s.Info.SandboxCap, cmp.Or(string(r.b.cfgs.Get().AgentProvider), nodeproto.SandboxProviderClaude))
		}
	}
	r.observe(s)
	return s
}

// runtimeChoice is where a brain's next run happens: the "sandbox" of a
// brain in GET /api/brains, doctor's brain-runtime and the run itself.
type runtimeChoice struct {
	Configured string `json:"configured"`
	// Effective is docker-sandbox, bubblewrap, sandbox-exec (a native
	// Mac's) or refused (fallback "refuse" while the sandbox is not
	// ready).
	Effective string   `json:"effective"`
	Fallback  string   `json:"fallback"`
	Node      string   `json:"node"`
	Ready     bool     `json:"ready"`
	Reason    string   `json:"reason"`
	Egress    []string `json:"egress"`
	state     sandboxrun.State
}

// effectiveRefused is Effective for a run its fallback refuses.
const effectiveRefused = "refused"

// localRuntime is the name of the bridge's own sandbox: sandbox-exec on
// a native Mac with the sandbox on, bubblewrap otherwise.
func localRuntime(cfg *config.Settings) string {
	if cfg.Sandbox.Enabled && sandboxBackend(ConfigGOOS()) == backendSandboxExec {
		return backendSandboxExec
	}
	return manifest.RuntimeBubblewrap
}

// choose is where the next run of a brain with sb, for provider, happens.
// wait looks at the node when the last look is too old, as a run does;
// without it the last look serves (the dashboard never waits).
func (r *brainRuntimes) choose(ctx context.Context, sb *manifest.Sandbox, provider string, wait bool) runtimeChoice {
	cfg := r.b.cfgs.Get()
	c := runtimeChoice{Configured: sb.RuntimeOf(cfg.Sandbox.Runtime), Fallback: sb.FallbackOf(), Egress: []string{}}
	if sb != nil && len(sb.Egress) > 0 {
		c.Egress = slices.Clone(sb.Egress)
	}
	local := localRuntime(cfg)
	if c.Configured != manifest.RuntimeDockerSandbox {
		c.Effective, c.Ready = local, true
		return c
	}
	s := r.cache.Peek()
	if wait {
		s = r.cache.Fresh(ctx)
	}
	c.state, c.Node = s, s.Name
	why := s.Why
	switch {
	case why == "" && s.At.IsZero():
		why = "not looked at yet"
	case why == "":
		why = sandboxNotReady(&s.Info.SandboxCap, cmp.Or(provider, nodeproto.SandboxProviderClaude))
	}
	c.Ready, c.Reason = why == "", why
	switch {
	case c.Ready:
		c.Effective = manifest.RuntimeDockerSandbox
	case c.Fallback == manifest.FallbackRefuse:
		c.Effective = effectiveRefused
	default:
		c.Effective = local
	}
	if since := r.outages.Since(); !c.Ready && !since.IsZero() {
		c.Reason += ", since " + since.Format("15:04")
	}
	return c
}

// sandboxNotReady says why the node's sandboxes cannot run brains now,
// "" when sbx is ready; with a provider, also when its login is missing
// (nodeproto.SandboxCap.Ready is the test).
func sandboxNotReady(c *nodeproto.SandboxCap, provider string) string {
	switch {
	case c == nil:
		return "the node offers no Docker Sandboxes"
	case c.Problem != "":
		return c.Problem
	case !c.Daemon:
		return "sbx's daemon does not answer"
	case !c.SignedIn:
		return "sbx is not signed in to Docker"
	case !c.Settings:
		return "sbx's global settings are not Fridolin's"
	case provider != "" && !c.Ready(provider):
		return provider + " has no login in Docker Sandboxes on the node"
	}
	return ""
}

// observe tells the owner, once per outage, that the sandboxes stopped
// being ready, and once that they are back. Only brains set to Docker
// Sandboxes make the bridge look, so no one else hears of it.
func (r *brainRuntimes) observe(s sandboxrun.State) {
	node := s.Name
	if node == "" {
		node = "the node"
	}
	var text string
	switch r.outages.Observe(s.Why == "", s.Why, time.Now()) {
	case sandboxrun.Down:
		log.Printf("docker sandboxes: not available, brains set to them fall back: %s", s.Why)
		text = outboundmsg.SandboxDownAlert(node, s.Why)
	case sandboxrun.Back:
		log.Printf("docker sandboxes: available again on %s", node)
		text = outboundmsg.SandboxBackAlert(node)
	case sandboxrun.Same:
		return
	}
	cfg := r.b.cfgs.Get()
	target := ownerChat(cfg)
	if target == "" || r.b.outbox == nil {
		return
	}
	if err := r.b.outbox.Text(context.Background(), target, text, cfg.MaxChunkSize); err != nil {
		log.Printf("docker sandboxes: telling the owner: %v", err)
	}
}
