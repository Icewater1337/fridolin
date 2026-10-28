package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Git identities for FrIDE (docs/NODES.md, "Git identities for FrIDE"):
// the core hands the node all of its FrIDE's identities with their tokens
// (fride.identities). The node keeps them in memory only, puts each into
// FrIDE through its companion API (PUT /api/companion/git/identities/<id>)
// once FrIDE says it takes them, removes those FrIDE no longer gets, and
// asks the core for a new GitHub token before the one FrIDE has runs out
// (EventFrideIdentities). The state dir keeps only which ids it put
// there, never a token (fride-identities.json).

// frideIdentitiesFile names the ids the node put into FrIDE.
const frideIdentitiesFile = "fride-identities.json"

// Timing of the identities.
const (
	// identitiesRefreshBefore is how long before a token ends the node
	// asks the core for a new one: an installation token lasts an hour,
	// and gh and glab take a new one only in sessions started after it.
	identitiesRefreshBefore = 25 * time.Minute
	// identitiesAskEvery bounds the asks while no new token comes, and
	// identitiesHelloEvery those of a node the core gave nothing yet.
	identitiesAskEvery   = 2 * time.Minute
	identitiesHelloEvery = 10 * time.Minute
)

// gitIdentities is the agent's part of it.
type gitIdentities struct {
	once sync.Once
	asks chan nodeproto.FrideIdentitiesAsk
	// pushMu holds the pushes to one at a time.
	pushMu sync.Mutex

	mu sync.Mutex
	// want are the core's identities, with tokens; have says the core
	// sent some (maybe none) since the agent started.
	want []nodeproto.GitIdentity
	have bool
	// pushed is the fingerprint of what FrIDE took last (frideFingerprint).
	pushed string
	// ids are the ids put into FrIDE, when the agent has no state dir.
	ids []string
	// asked is when the node last asked the core; lastErr is the last
	// push's failure, logged once.
	asked   time.Time
	lastErr string
}

// init makes the channel of the asks, which the session sends on.
func (g *gitIdentities) init() chan nodeproto.FrideIdentitiesAsk {
	g.once.Do(func() { g.asks = make(chan nodeproto.FrideIdentitiesAsk, 1) })
	return g.asks
}

// frideIdentitiesCall serves fride.identities.
func (a *Agent) frideIdentitiesCall(ctx context.Context, raw json.RawMessage) (any, *nodeproto.Error) {
	var p nodeproto.FrideIdentities
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "fride.identities takes {\"identities\": [...]}")
	}
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	g := &a.gitIDs
	g.mu.Lock()
	g.want, g.have = slices.Clone(p.Identities), true
	g.mu.Unlock()
	return a.pushIdentities(ctx), nil
}

// frideFingerprint is what FrIDE took: which FrIDE (its address and
// token) and the identities.
func frideFingerprint(base, token string, ids []nodeproto.GitIdentity) string {
	data, _ := json.Marshal(ids)
	h := sha256.Sum256(append([]byte(base+"\x00"+token+"\x00"), data...))
	return hex.EncodeToString(h[:])
}

func identityIDs(ids []nodeproto.GitIdentity) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.ID)
	}
	return out
}

// pushIdentities puts the identities the core gave into FrIDE and removes
// the ones it put there before that are not among them, unless FrIDE has
// them already from the same pairing.
func (a *Agent) pushIdentities(ctx context.Context) nodeproto.FrideIdentitiesResult {
	g := &a.gitIDs
	g.pushMu.Lock()
	defer g.pushMu.Unlock()
	res := nodeproto.FrideIdentitiesResult{Pushed: []string{}, Removed: []string{}}
	g.mu.Lock()
	want, have := slices.Clone(g.want), g.have
	g.mu.Unlock()
	a.fride.mu.Lock()
	cfg, token := a.fride.cfg, a.fride.token
	a.fride.mu.Unlock()
	if !have {
		return res
	}
	if why := a.identitiesHeld(); why != "" {
		res.Held = why
		return res
	}
	// None to give and none given before: nothing to ask FrIDE.
	if len(want) == 0 && len(a.pushedIdentities()) == 0 {
		a.fride.mu.Lock()
		res.Supported = a.fride.takesIDs
		a.fride.mu.Unlock()
		return res
	}
	if cfg == nil || token == "" {
		res.Pending, res.Error = true, "FrIDE is not paired with this node"
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, frideCallTimeout)
	defer cancel()
	fc := a.nativeConn(cfg, token)
	info, err := fc.probe(ctx)
	if err != nil {
		res.Pending, res.Error = true, scrubTokens(frideErr(err).Message, want)
		return res
	}
	if !info.GitIdentities {
		res.Error = "this FrIDE takes no git identities: update it"
		return res
	}
	res.Supported = true
	g.mu.Lock()
	same := g.pushed == frideFingerprint(cfg.URL, fc.token, want)
	g.mu.Unlock()
	if same {
		res.Pushed = identityIDs(want)
		return res
	}
	put := a.pushedIdentities()
	var problems []string
	for _, id := range want {
		body := map[string]string{"label": id.Label, "kind": id.Kind, "host": id.Host, "name": id.Name, "email": id.Email, "token": id.Token}
		if err := fc.do(ctx, http.MethodPut, "/api/companion/git/identities/"+url.PathEscape(id.ID), body, nil); err != nil {
			problems = append(problems, fmt.Sprintf("FrIDE did not take %s: %s", id.ID, frideErr(err).Message))
			continue
		}
		res.Pushed = append(res.Pushed, id.ID)
		if !slices.Contains(put, id.ID) {
			put = append(put, id.ID)
		}
	}
	ids := identityIDs(want)
	for _, old := range slices.Clone(put) {
		if slices.Contains(ids, old) {
			continue
		}
		err := fc.do(ctx, http.MethodDelete, "/api/companion/git/identities/"+url.PathEscape(old), nil, nil)
		// FrIDE answers 404 for one it no longer has: removed already.
		if fe, ok := errors.AsType[*FrideError](err); err != nil && !(ok && fe.Status == http.StatusNotFound) {
			problems = append(problems, fmt.Sprintf("FrIDE did not remove %s: %s", old, frideErr(err).Message))
			continue
		}
		res.Removed = append(res.Removed, old)
		put = slices.DeleteFunc(put, func(s string) bool { return s == old })
	}
	if err := a.savePushedIdentities(put); err != nil {
		problems = append(problems, "noting what FrIDE has: "+err.Error())
	}
	if len(problems) > 0 {
		res.Error = scrubTokens(strings.Join(problems, "; "), want)
		g.mu.Lock()
		again := g.lastErr == res.Error
		g.lastErr = res.Error
		g.mu.Unlock()
		if !again {
			a.logf("node: FrIDE's git identities: %s", res.Error)
		}
		return res
	}
	g.mu.Lock()
	g.pushed, g.lastErr = frideFingerprint(cfg.URL, fc.token, want), ""
	g.mu.Unlock()
	a.logf("node: FrIDE has the git identities %s%s", listOrNone(res.Pushed), removedSuffix(res.Removed))
	return res
}

func listOrNone(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	return strings.Join(ids, ", ")
}

func removedSuffix(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return "; removed " + strings.Join(ids, ", ")
}

// scrubTokens takes the identities' tokens out of a text FrIDE answered,
// should it ever echo one.
func scrubTokens(s string, ids []nodeproto.GitIdentity) string {
	for _, id := range ids {
		if id.Token != "" {
			s = strings.ReplaceAll(s, id.Token, "[token]")
		}
	}
	return s
}

// pushedIdentities are the ids the node put into FrIDE.
func (a *Agent) pushedIdentities() []string {
	if a.Dir == "" {
		a.gitIDs.mu.Lock()
		defer a.gitIDs.mu.Unlock()
		return slices.Clone(a.gitIDs.ids)
	}
	var f struct {
		Pushed []string `json:"pushed"`
	}
	data, err := os.ReadFile(filepath.Join(a.Dir, frideIdentitiesFile))
	if err != nil || json.Unmarshal(data, &f) != nil {
		return nil
	}
	return slices.DeleteFunc(f.Pushed, func(id string) bool { return !nodeproto.ValidGitIdentityID(id) })
}

// savePushedIdentities keeps ids, never a token.
func (a *Agent) savePushedIdentities(ids []string) error {
	slices.Sort(ids)
	if a.Dir == "" {
		a.gitIDs.mu.Lock()
		a.gitIDs.ids = ids
		a.gitIDs.mu.Unlock()
		return nil
	}
	p := filepath.Join(a.Dir, frideIdentitiesFile)
	if len(ids) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, _ := json.Marshal(map[string][]string{"pushed": ids})
	return writeFile(p, append(data, '\n'), 0o600)
}

// legacyFrideSandboxService is the service of the second node a
// sandboxed FrIDE had before FrIDE instances replaced it. compat: a
// node set up so before still runs until its owner removes it, and its
// FrIDE's VM must never hold a real token.
const legacyFrideSandboxService = "fride-sandbox"

// identitiesHeld says why this node keeps the identities from its FrIDE:
// the FrIDE of the old "-sbx" node (service fride-sandbox) must never
// hold a real token in its VM. "" for a node that passes them on. FrIDE
// instances get theirs with fride.instance.start, as placeholders
// (sbx_tokens.go).
func (a *Agent) identitiesHeld() string {
	if a.Config.Service == legacyFrideSandboxService {
		return "the FrIDE in a Docker Sandbox gets no git token: FrIDE instances get placeholders instead"
	}
	return ""
}

// identitiesTick runs with each check of FrIDE: it puts the identities
// into a FrIDE that does not have them yet from this pairing, and asks
// the core for new ones before a token runs out, or for the first ones.
func (a *Agent) identitiesTick(ctx context.Context) {
	g := &a.gitIDs
	g.init()
	if a.identitiesHeld() != "" {
		return
	}
	a.fride.mu.Lock()
	up, token, base := a.fride.cap != nil && a.fride.takesIDs && a.fride.cfg != nil, a.fride.token, ""
	if up {
		base = a.fride.cfg.URL
	}
	a.fride.mu.Unlock()
	if !up {
		return
	}
	if st := a.State(); st.Device != nodeproto.DeviceApproved || !st.Approved.Allows(nodeproto.MethodFrideIdentities) {
		return
	}
	now := time.Now()
	g.mu.Lock()
	want, have, pushed, asked := g.want, g.have, g.pushed, g.asked
	reason := ""
	switch {
	case !have && now.Sub(asked) > identitiesHelloEvery:
		reason = "none yet"
	case have && now.Sub(asked) > identitiesAskEvery && slices.ContainsFunc(want, func(id nodeproto.GitIdentity) bool {
		return !id.Expires.IsZero() && id.Expires.Sub(now) < identitiesRefreshBefore
	}):
		reason = "expiring"
	}
	if reason != "" {
		select {
		case g.asks <- nodeproto.FrideIdentitiesAsk{Reason: reason}:
			g.asked = now
		default:
		}
	}
	g.mu.Unlock()
	if have && pushed != frideFingerprint(base, token, want) {
		a.pushIdentities(ctx)
	}
}
