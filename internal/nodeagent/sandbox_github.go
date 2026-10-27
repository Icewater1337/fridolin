package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// A run's GitHub in its sandbox (#149). The token the core sends in
// sandbox.run's github_token becomes the sandbox's own `github` secret,
// through stdin, and the sandbox may reach nodeproto.SandboxGitHubHosts
// while a run with a token goes on. sbx's proxy then sets the token on
// every call to those hosts, whatever the client sends: gh's Bearer and
// git's Basic alike, so git over HTTPS needs no credential helper. The VM
// holds only sbx's placeholder in GH_TOKEN. Tested with sbx v0.50.0.

// sandboxGitHub is what state.json keeps of a brain's GitHub: the rules
// opened for its runs, so that an agent that stopped during a run closes
// them at the brain's next ensure.
type sandboxGitHub struct {
	Rules []string `json:"rules,omitempty"`
}

// githubOpen stores token as the brain's sandbox's github secret and,
// for the first run with one, opens GitHub's hosts. A failure is
// CodeFailed: the run is refused rather than run without what it asked.
func (a *Agent) githubOpen(ctx context.Context, d sbxDriver, brain, token string) *nodeproto.Error {
	sb := &a.sandbox
	sb.ensure.Lock()
	defer sb.ensure.Unlock()
	name := nodeproto.SandboxName(brain)
	st, err := a.loadSandboxState()
	rec, ok := st.Sandboxes[brain]
	if err != nil || !ok {
		return nodeproto.Errorf(nodeproto.CodeFailed, "%s is not made yet: sandbox.ensure first", name)
	}
	if rec.GitHub == nil {
		// Recorded first: an agent that stops from here on still closes it.
		rec.GitHub = &sandboxGitHub{}
		st.Sandboxes[brain] = rec
		if err := a.saveSandboxState(st); err != nil {
			return nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
		}
	}
	fail := func(format string, args ...any) *nodeproto.Error {
		if sb.ghRuns[brain] == 0 {
			a.githubCloseLocked(ctx, d, brain)
		}
		return nodeproto.Errorf(nodeproto.CodeFailed, format, args...)
	}
	// Through stdin, never an argument. sbx prints its prompt on stderr,
	// and an error after it, which must not show the token.
	if _, stderr, err := d.run(ctx, strings.NewReader(token), nil, "secret", "set", "github", "--sandbox", name); err != nil {
		msg := strings.TrimSpace(strings.TrimPrefix(sbxError(err, stderr).Error(), "Enter secret:"))
		return fail("sbx could not store the run's GitHub token: %s", strings.ReplaceAll(msg, token, "…"))
	}
	if sb.ghRuns[brain] == 0 {
		added, err := a.githubAllow(ctx, d, name)
		rec.GitHub.Rules = append(rec.GitHub.Rules, added...)
		st.Sandboxes[brain] = rec
		if serr := a.saveSandboxState(st); err == nil {
			err = serr
		}
		if err != nil {
			return fail("opening GitHub for %s: %v", name, err)
		}
	}
	if sb.ghRuns == nil {
		sb.ghRuns = map[string]int{}
	}
	sb.ghRuns[brain]++
	return nil
}

// githubAllow opens GitHub's hosts for the sandbox and returns the ids of
// the rules that made: a host its egress covers already gets none, so
// closing never takes a rule of the brain's own. Rules added before an
// error are returned too, to be closed.
func (a *Agent) githubAllow(ctx context.Context, d sbxDriver, name string) ([]string, error) {
	before, err := sandboxRuleIDs(ctx, d, name)
	if err != nil {
		return nil, err
	}
	_, stderr, aerr := d.run(ctx, nil, nil, "policy", "allow", "network", "--sandbox", name, strings.Join(nodeproto.SandboxGitHubHosts, ","))
	after, err := sandboxRuleIDs(ctx, d, name)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, id := range after {
		if !slices.Contains(before, id) {
			added = append(added, id)
		}
	}
	if aerr != nil {
		return added, sbxError(aerr, stderr)
	}
	return added, nil
}

// githubClose ends a run's GitHub: once no other run of the brain has
// one, the secret goes and the rules close.
func (a *Agent) githubClose(d sbxDriver, brain string) {
	ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeTimeout)
	defer cancel()
	sb := &a.sandbox
	sb.ensure.Lock()
	defer sb.ensure.Unlock()
	if n := sb.ghRuns[brain] - 1; n > 0 {
		sb.ghRuns[brain] = n
		return
	}
	delete(sb.ghRuns, brain)
	a.githubCloseLocked(ctx, d, brain)
}

// githubCloseLocked removes the brain's sandbox's github secret and the
// rules state.json names, and forgets those that are gone. Call it with
// the ensure lock held and no run of the brain using GitHub.
func (a *Agent) githubCloseLocked(ctx context.Context, d sbxDriver, brain string) {
	st, err := a.loadSandboxState()
	rec, ok := st.Sandboxes[brain]
	if err != nil || !ok || rec.GitHub == nil {
		return
	}
	name := nodeproto.SandboxName(brain)
	// Its output shows part of the value: never logged.
	_, _, _ = d.run(ctx, nil, nil, "secret", "rm", "github", "--sandbox", name, "-f")
	for _, id := range rec.GitHub.Rules {
		_, _, _ = d.run(ctx, nil, nil, "policy", "rm", "network", "--sandbox", name, "--id", id, "--force")
	}
	// What is still there stays recorded, for the next ensure.
	now, err := sandboxRuleIDs(ctx, d, name)
	left := slices.DeleteFunc(rec.GitHub.Rules, func(id string) bool { return err == nil && !slices.Contains(now, id) })
	rec.GitHub = nil
	if len(left) > 0 {
		rec.GitHub = &sandboxGitHub{Rules: left}
		a.logf("node: %d GitHub rule(s) of %s could not be removed; the next ensure tries again", len(left), name)
	}
	st.Sandboxes[brain] = rec
	if err := a.saveSandboxState(st); err != nil {
		a.logf("node: closing GitHub for %s: %v", name, err)
	}
}

// sandboxRuleIDs lists the ids of the sandbox's own network rules, as
// `sbx policy ls NAME --json` prints them.
func sandboxRuleIDs(ctx context.Context, d sbxDriver, name string) ([]string, error) {
	out, stderr, err := d.run(ctx, nil, nil, "policy", "ls", name, "--json")
	if err != nil {
		return nil, sbxError(err, stderr)
	}
	var p struct {
		Rules []struct {
			ID    string `json:"id"`
			Scope string `json:"scope"`
			Type  string `json:"resource_type"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, errors.New("sbx policy ls: not the JSON it prints")
	}
	var ids []string
	for _, r := range p.Rules {
		if r.Scope == "sandbox:"+name && r.Type == "network" && r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}
