package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/gitident"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// The hand-over of the git identities to the nodes (docs/NODES.md, "Git
// identities for FrIDE"). Every online node whose fride is approved and
// offered gets all of them, or those nodes.git_identities names for it,
// with their tokens (fride.identities): when it connects, its fride is
// approved or comes up, an identity changes, the node asks (a GitHub
// token runs out soon), and every few minutes for one that did not take
// them. A node is sent the same again only when it asks. Nothing goes in
// a lockdown.

// gitIDsEvery is how often the core looks for nodes whose identities are
// not current, such as after a hand edit of nodes.git_identities.
const gitIDsEvery = 5 * time.Minute

// Start looks at the nodes every gitIDsEvery until ctx ends.
func (s *gitIdentityService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	t := time.NewTicker(gitIDsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.kick("", false)
		}
	}
}

// changed hands every node what changed: the GitLab connection, a commit
// name, the GitHub app; and the running FrIDE instances theirs.
func (s *gitIdentityService) changed() {
	if s != nil {
		s.mu.Lock()
		s.ghAt = time.Time{}
		s.mu.Unlock()
		s.kick("", false)
		if s.b.fride != nil {
			s.b.fride.inst.changed()
		}
	}
}

// nodeReady hands node id its identities: it connected or its fride was
// approved (again, since it may have lost them), or what it offers
// changed, such as FrIDE coming up (only when they changed).
func (s *gitIdentityService) nodeReady(id string, again bool) {
	if s != nil {
		s.kick(id, again)
	}
}

// onEvent hears a node ask for its identities again.
func (s *gitIdentityService) onEvent(node string, ev nodeproto.Event) {
	if s == nil || ev.Kind != nodeproto.EventFrideIdentities {
		return
	}
	var ask nodeproto.FrideIdentitiesAsk
	if json.Unmarshal(ev.Data, &ask) != nil || ask.Check() != nil {
		log.Printf("git identities: an unreadable ask from node %s", node)
		return
	}
	s.kick(node, true)
}

// kick hands node id (every node for "") its identities in the
// background; again sends them even when the node has them.
func (s *gitIdentityService) kick(id string, again bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		s.all = true
	} else {
		s.pending[id] = s.pending[id] || again
	}
	if s.running {
		return
	}
	s.running = true
	s.idle = make(chan struct{})
	go s.loop()
}

func (s *gitIdentityService) loop() {
	for {
		s.mu.Lock()
		pending, all := s.pending, s.all
		if len(pending) == 0 && !all {
			s.running = false
			close(s.idle)
			s.mu.Unlock()
			return
		}
		s.pending, s.all = map[string]bool{}, false
		s.mu.Unlock()
		s.handOver(pending, all)
	}
}

// wait returns once no hand-over runs, for the tests.
func (s *gitIdentityService) wait() {
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	if idle != nil {
		<-idle
	}
}

// takesIdentities reports whether node v can take its identities now.
func takesIdentities(v nodes.View) bool {
	return v.Device == nodeproto.DeviceApproved && v.Online && v.Effective.Allows(nodeproto.MethodFrideIdentities)
}

// handOver sends the nodes in pending (all of them with all) their
// identities.
func (s *gitIdentityService) handOver(pending map[string]bool, all bool) {
	b := s.b
	if b.nodes == nil || b.cfgs == nil || b.locked() {
		return
	}
	views, err := b.nodes.Views()
	if err != nil {
		log.Printf("git identities: reading the nodes: %v", err)
		return
	}
	var targets []nodes.View
	for _, v := range views {
		if _, asked := pending[v.ID]; (all || asked) && takesIdentities(v) {
			targets = append(targets, v)
		}
	}
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	ids := s.build(ctx)
	cancel()
	cfg := b.cfgs.Get()
	for _, v := range targets {
		set := filterIdentities(ids, allowedIdentities(cfg, v))
		fp := identitiesFingerprint(set)
		s.mu.Lock()
		st := s.nodes[v.ID]
		// Settled: the node's FrIDE took them, or the node holds them
		// back on purpose.
		same := st != nil && st.sent == fp && (st.held != "" || st.err == "" && !st.pending && st.supported)
		s.mu.Unlock()
		if same && !pending[v.ID] {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		res, err := b.nodes.FrideIdentities(ctx, v.ID, nodeproto.FrideIdentities{Identities: set})
		cancel()
		next := &gitIDNode{sent: fp, at: time.Now().UTC().Truncate(time.Second), got: res.Pushed, supported: res.Supported, pending: res.Pending, err: res.Error, held: res.Held}
		if err != nil {
			next.err = err.Error()
		}
		// Said once, not at every retry.
		if st == nil || st.say() != next.say() {
			switch {
			case next.err != "":
				log.Printf("git identities: node %s (%s): %s", v.Name, v.ID, next.err)
			case next.held != "":
				log.Printf("git identities: node %s (%s) holds them back: %s", v.Name, v.ID, next.held)
			case !res.Pending && res.Supported:
				log.Printf("git identities: FrIDE on %s has %s", v.Name, listOrNone(res.Pushed))
			}
		}
		s.mu.Lock()
		s.nodes[v.ID] = next
		s.mu.Unlock()
	}
}

// listOrNone joins ids, or says none.
func listOrNone(ids []string) string {
	if len(ids) == 0 {
		return "no git identity"
	}
	return strings.Join(ids, ", ")
}

// build is every identity with its token: GitHub when the app (or a typed
// token) gives one, GitLab when it is set. Why GitHub gives none is noted
// for the dashboard.
func (s *gitIdentityService) build(ctx context.Context) []nodeproto.GitIdentity {
	var out []nodeproto.GitIdentity
	commit, err := s.store.GitHubCommit()
	if err != nil {
		log.Printf("git identities: the GitHub commit name: %v", err)
	}
	note := ""
	if tok, expires, err := s.b.github.FrideToken(ctx); err != nil {
		note = err.Error()
	} else {
		out = append(out, nodeproto.GitIdentity{ID: gitident.GitHub, Label: "GitHub", Kind: nodeproto.GitKindGitHub, Host: "github.com",
			Name: commit.Name, Email: commit.Email, Token: tok, Expires: expires})
	}
	s.mu.Lock()
	s.githubNote = note
	s.mu.Unlock()
	if c, ok, err := s.store.GitLab(); err != nil {
		log.Printf("git identities: the GitLab connection: %v", err)
	} else if ok {
		out = append(out, nodeproto.GitIdentity{ID: gitident.GitLab, Label: "GitLab", Kind: nodeproto.GitKindGitLab, Host: c.Host(),
			Name: c.Name, Email: c.Email, Token: c.Token})
	}
	return out
}

// allowedIdentities are the ids node v may get: those nodes.git_identities
// names for it, by id or name (each key that names it narrows them), else
// all.
func allowedIdentities(cfg *config.Settings, v nodes.View) []string {
	allowed := slices.Clone(config.GitIdentityIDs)
	for _, key := range slices.Sorted(maps.Keys(cfg.Nodes.GitIdentities)) {
		if key != v.ID && !strings.EqualFold(strings.TrimSpace(key), v.Name) {
			continue
		}
		ids := cfg.Nodes.GitIdentities[key]
		allowed = slices.DeleteFunc(allowed, func(id string) bool { return !slices.Contains(ids, id) })
	}
	return allowed
}

func filterIdentities(ids []nodeproto.GitIdentity, allowed []string) []nodeproto.GitIdentity {
	out := []nodeproto.GitIdentity{}
	for _, id := range ids {
		if slices.Contains(allowed, id.ID) {
			out = append(out, id)
		}
	}
	return out
}

// identitiesFingerprint tells sets apart, tokens included; it stays in
// memory.
func identitiesFingerprint(ids []nodeproto.GitIdentity) string {
	data, _ := json.Marshal(ids)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
