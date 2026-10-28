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
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// frideInstanceStartTimeout bounds a start: making a sandbox, waiting
// for FrIDE and cloning the repositories.
const frideInstanceStartTimeout = 20 * time.Minute

// frideInstanceStart serves fride.instance.start: it makes the
// instance's sandbox when sbx has none, makes it anew for another memory
// (or, while it is stopped, another template), brings its network rules,
// tokens, Claude login, git identity and repositories up to date, has the
// agent keep FrIDE running, pairs with it and returns the instance once
// FrIDE answers. It is idempotent: on a running instance of the same
// memory it only brings those up to date.
func (a *Agent) frideInstanceStart(ctx context.Context, p nodeproto.FrideInstanceStartParams) (nodeproto.FrideInstance, *nodeproto.Error) {
	var none nodeproto.FrideInstance
	if err := p.Check(); err != nil {
		return none, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if err := checkInstanceIdentities(p.Identities); err != nil {
		return none, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if _, err := frideInstanceRules(0, p.Identities, p.Egress, false); err != nil {
		return none, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	o, host, perr := a.frideHostReady(ctx)
	if perr != nil {
		return none, perr
	}
	lock := a.inst.lock(p.Name)
	lock.Lock()
	defer lock.Unlock()
	a.inst.setStarting(p.Name, true)
	a.frideHostKick()
	defer func() {
		a.inst.setStarting(p.Name, false)
		a.frideHostKick()
	}()
	ctx, cancel := context.WithTimeout(ctx, frideInstanceStartTimeout)
	defer cancel()
	a.logf("node: starting the FrIDE instance %s (%d MB)", p.Name, p.MemoryMB)
	st, err := a.ensureInstance(ctx, o, host, p)
	if err != nil {
		return none, a.instanceFailed(o, p.Name, st, err)
	}
	out, err := a.readyInstance(ctx, o, st, p)
	if err != nil {
		return none, a.instanceFailed(o, p.Name, st, err)
	}
	a.logf("node: the FrIDE instance %s runs at %s (repositories: %s)%s", p.Name, out.URL, orNoneList(out.Repos), problemSuffix(out.Problem))
	return out, nil
}

// frideHostReady is what an instance needs on this node: fride_host
// set up, sbx running and signed in.
func (a *Agent) frideHostReady(ctx context.Context) (instanceOps, *FrideHostState, *nodeproto.Error) {
	o := a.instanceOps()
	if a.Dir == "" || a.Config.Box != nil {
		return o, nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "this node runs no FrIDE instances")
	}
	host, err := LoadFrideHost(a.Dir)
	if err != nil {
		return o, nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "%v", err)
	}
	if host == nil {
		return o, nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "FrIDE instances are not set up on this node: fridolin node fride host setup")
	}
	if o.d == nil {
		return o, nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx is not installed on this node")
	}
	sctx, cancel := context.WithTimeout(ctx, sbxSettingsTimeout)
	defer cancel()
	if _, _, problem := sbxState(sctx, o.d); problem != "" {
		return o, nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "%s", problem)
	}
	return o, host, nil
}

// checkInstanceIdentities refuses identities an instance cannot hold:
// one host's token stands for one placeholder, so two identities for
// one host would share it.
func checkInstanceIdentities(ids []nodeproto.FrideIdentity) error {
	seen := map[string]string{}
	for _, id := range ids {
		if !nodeproto.ValidGitIdentityID(id.ID) {
			return fmt.Errorf("identity %q: not an identity's id", id.ID)
		}
		host := strings.ToLower(id.Host)
		if !hostTokenRe.MatchString(host) {
			return fmt.Errorf("identity %s: %q is not a host (like github.com or gitlab.example.com:8443)", id.ID, id.Host)
		}
		if other, ok := seen[host]; ok {
			return fmt.Errorf("identities %s and %s are both for %s: an instance takes one identity per host", other, id.ID, host)
		}
		seen[host] = id.ID
	}
	return nil
}

// ensureInstance has the instance's sandbox, made with p's memory from
// the host's template: it makes it, or makes it anew, as needed.
func (a *Agent) ensureInstance(ctx context.Context, o instanceOps, host *FrideHostState, p nodeproto.FrideInstanceStartParams) (*FrideInstanceState, error) {
	st, err := loadFrideInstance(a.Dir, p.Name)
	if errors.Is(err, errNoFrideInstance) {
		st, err = a.newInstance(o, host, p)
	}
	if err != nil {
		return st, err
	}
	status, err := o.status(ctx, st)
	if err != nil {
		return st, err
	}
	fresh := func() error {
		st.MemoryMB, st.Template, st.DockerDisk = p.MemoryMB, host.Template, host.DockerDisk
		caches, err := instanceCaches(host, st.Name, a.Dir)
		if err != nil {
			o.say("Note: %v; %s has no shared caches.", err, st.Name)
		}
		st.Caches = caches
		return nil
	}
	switch {
	case status == "":
		_ = fresh()
		return st, o.make(ctx, st, host)
	case st.MemoryMB != p.MemoryMB || st.Template != host.Template && status != "running":
		why := fmt.Sprintf("memory %d MB, now %d MB", st.MemoryMB, p.MemoryMB)
		if st.MemoryMB == p.MemoryMB {
			why = "a new template, " + host.Template
		}
		a.logf("node: making the FrIDE instance %s anew (%s), with its workspaces, repositories, sessions and login", st.Name, why)
		if st.Run {
			// The agent lets go of the old sandbox's FrIDE.
			st.Run = false
			if err := saveFrideInstance(a.Dir, st); err != nil {
				return st, err
			}
			a.inst.syncServe(ctx)
		}
		_ = fresh()
		return st, o.remake(ctx, st, host, status)
	}
	return st, nil
}

// newInstance is a new instance's state, with a free pair of ports,
// saved so that no other instance takes them.
func (a *Agent) newInstance(o instanceOps, host *FrideHostState, p nodeproto.FrideInstanceStartParams) (*FrideInstanceState, error) {
	a.inst.allocMu.Lock()
	defer a.inst.allocMu.Unlock()
	port, hook, err := freePortPair(a.Dir, o.portBase)
	if err != nil {
		return nil, err
	}
	st := &FrideInstanceState{Name: p.Name, Sandbox: FrideInstanceSandbox(p.Name), MemoryMB: p.MemoryMB, Template: host.Template,
		Port: port, WebhookPort: hook, DockerDisk: host.DockerDisk}
	return st, saveFrideInstance(a.Dir, st)
}

// instanceCaches are the instance's own shared caches under the host's
// caches folder: shared with the instance's next sandbox, never with
// another trust domain's.
func instanceCaches(host *FrideHostState, name, stateDir string) (string, error) {
	if host.Caches == "" {
		return "", nil
	}
	return prepareCaches(filepath.Join(host.Caches, name), stateDir)
}

// readyInstance brings a made instance up to date with p, has the agent
// keep its FrIDE running, pairs with it and returns it once it answers.
func (a *Agent) readyInstance(ctx context.Context, o instanceOps, st *FrideInstanceState, p nodeproto.FrideInstanceStartParams) (nodeproto.FrideInstance, error) {
	var notes []string
	rules, err := frideInstanceRules(st.WebhookPort, p.Identities, p.Egress, o.codexLogin(ctx))
	if err != nil {
		return nodeproto.FrideInstance{}, err
	}
	if err := o.applyRules(ctx, st, rules); err != nil {
		return nodeproto.FrideInstance{}, err
	}
	phs, err := o.instanceTokens(ctx, st, p.Identities)
	if err != nil {
		return nodeproto.FrideInstance{}, err
	}
	if why := o.login(ctx, st.Sandbox); why != "" {
		notes = append(notes, why)
	}
	if g := p.Git; g != nil && (g.Name != "" || g.Email != "") {
		if err := o.exec(ctx, st.Sandbox, nil, frideInstanceGit, g.Name, g.Email); err != nil {
			return nodeproto.FrideInstance{}, err
		}
	}
	st.Run, st.Problem, st.Repos, st.Started = true, "", slices.Clone(p.Repos), time.Now().UTC().Truncate(time.Second)
	if err := saveFrideInstance(a.Dir, st); err != nil {
		return nodeproto.FrideInstance{}, err
	}
	a.inst.kickServe()
	url := o.urlOf(st)
	if err := waitAnswers(ctx, url, o.wait); err != nil {
		return nodeproto.FrideInstance{}, err
	}
	fc, info, err := o.pair(ctx, st)
	if err != nil {
		return nodeproto.FrideInstance{}, err
	}
	if note := o.pushIdentities(ctx, st, fc, info, p.Identities, phs); note != "" {
		notes = append(notes, note)
	}
	cloned, problems := o.cloneRepos(ctx, st, p.Repos)
	notes = append(notes, problems...)
	if cloned > 0 {
		if err := fc.do(ctx, http.MethodPost, "/api/config", nil, nil); err != nil {
			notes = append(notes, "FrIDE did not read its base folder again: "+frideErr(err).Message)
		}
		if i, err := fc.probe(ctx); err == nil {
			info = i
		}
	}
	if err := saveFrideInstance(a.Dir, st); err != nil {
		return nodeproto.FrideInstance{}, err
	}
	out := nodeproto.FrideInstance{Name: st.Name, State: nodeproto.FrideInstanceRunning, MemoryMB: st.MemoryMB, URL: url,
		Repos: info.Repos, Problem: problemText(strings.Join(notes, "; "))}
	if (&nodeproto.FrideHostCap{Instances: []nodeproto.FrideInstance{out}}).Check() != nil {
		out.Repos = nil
	}
	return out, nil
}

// instanceFailed records why a start failed, has the agent let go of
// FrIDE and stops the sandbox, so that it holds no memory; the instance
// is failed until the next start.
func (a *Agent) instanceFailed(o instanceOps, name string, st *FrideInstanceState, err error) *nodeproto.Error {
	msg := problemText(err.Error())
	a.logf("node: the FrIDE instance %s did not start: %s", name, msg)
	if st != nil {
		st.Run, st.Problem = false, msg
		if serr := saveFrideInstance(a.Dir, st); serr != nil {
			a.logf("node: the FrIDE instance %s: %v", name, serr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), frideServeStopWait)
		defer cancel()
		a.inst.syncServe(ctx)
		if status, _ := o.status(ctx, st); status == "running" {
			_ = o.exec(ctx, st.Sandbox, nil, frideInstanceStop)
			_, _, _ = o.d.run(ctx, nil, nil, "stop", st.Sandbox)
		}
	}
	code := nodeproto.CodeFailed
	if errors.Is(err, context.DeadlineExceeded) {
		code = nodeproto.CodeTimeout
	}
	return nodeproto.Errorf(code, "the FrIDE instance %s did not start: %s", name, msg)
}

func problemSuffix(p string) string {
	if p == "" {
		return ""
	}
	return "; " + p
}

// instanceTokens gives the sandbox the identities' tokens through
// setHostToken and takes those of hosts no identity has any more. It
// returns each identity's placeholder by id. The tokens stay in the
// instance's folder on the host; the VM never sees one.
func (o instanceOps) instanceTokens(ctx context.Context, st *FrideInstanceState, ids []nodeproto.FrideIdentity) (map[string]string, error) {
	dir := o.tokensDir(st.Name)
	// sbx reads the token files on the host; the VM must not reach them.
	if _, in := within(st.Caches, dir); st.Caches != "" && in {
		return nil, fmt.Errorf("the state dir %s lies in the caches %s, which the sandbox can write: its tokens cannot be kept there", o.dir, st.Caches)
	}
	phs := map[string]string{}
	var hosts []string
	for _, id := range ids {
		host := strings.ToLower(id.Host)
		hosts = append(hosts, host)
		if id.Token == "" {
			continue
		}
		ph, err := setHostToken(ctx, o.d, dir, st.Sandbox, host, id.Token)
		if err != nil {
			return nil, fmt.Errorf("identity %s: %w", id.ID, err)
		}
		phs[id.ID] = ph
	}
	for _, h := range hostTokenHosts(dir) {
		if !slices.Contains(hosts, h) {
			if err := removeHostToken(ctx, o.d, dir, st.Sandbox, h); err != nil {
				return nil, fmt.Errorf("taking the token for %s: %w", h, err)
			}
		}
	}
	return phs, nil
}

// pair is the link to the instance's FrIDE: with the token the node
// paired with before while FrIDE takes it, else paired anew with a code
// from FrIDE in the sandbox, its news to the instance's webhook port.
func (o instanceOps) pair(ctx context.Context, st *FrideInstanceState) (*frideConn, FrideInfo, error) {
	url := o.urlOf(st)
	fc := &frideConn{cfg: &FrideConfig{URL: url, Webhook: webhookAddr(st.WebhookPort)}}
	if tok := readInstanceToken(o.dir, st.Name); tok != "" {
		fc.token = tok
		info, err := ProbeFride(ctx, url, tok)
		if err == nil {
			return fc, info, nil
		}
		if !frideRefused(err) {
			return nil, info, err
		}
	}
	out, stderr, err := o.d.run(ctx, nil, nil, append([]string{"exec", st.Sandbox}, frideInstanceScript(frideInstancePairCode)...)...)
	if err != nil {
		return nil, FrideInfo{}, fmt.Errorf("fride pair-companion in the sandbox: %w", sbxError(err, stderr))
	}
	var code struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(out, &code) != nil || code.Code == "" {
		return nil, FrideInfo{}, errors.New("fride pair-companion in the sandbox printed no code")
	}
	tok, err := pairCompanion(ctx, url, code.Code, fc.cfg.Webhook)
	if err != nil {
		return nil, FrideInfo{}, err
	}
	if err := writeFile(filepath.Join(frideInstanceDir(o.dir, st.Name), frideInstanceTokenFile), []byte(tok+"\n"), 0o600); err != nil {
		return nil, FrideInfo{}, err
	}
	// A FrIDE paired anew has none of the identities from before.
	st.Identities, st.Pushed = nil, ""
	fc.token = tok
	info, err := ProbeFride(ctx, url, tok)
	return fc, info, err
}

// webhookAddr is the loopback address of a webhook port.
func webhookAddr(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

// pushIdentities puts the identities into the instance's FrIDE, each
// with its placeholder as the token, and removes those it had from the
// node that are gone. It says what did not work.
func (o instanceOps) pushIdentities(ctx context.Context, st *FrideInstanceState, fc *frideConn, info FrideInfo, ids []nodeproto.FrideIdentity, phs map[string]string) string {
	if len(ids) == 0 && len(st.Identities) == 0 {
		return ""
	}
	if !info.GitIdentities {
		return "this FrIDE takes no git identities: set up the host again with a newer FrIDE"
	}
	type body struct {
		Label string `json:"label"`
		Kind  string `json:"kind"`
		Host  string `json:"host"`
		Name  string `json:"name"`
		Email string `json:"email"`
		Token string `json:"token"`
	}
	bodies := make([]body, len(ids))
	for i, id := range ids {
		bodies[i] = body{Label: id.ID, Kind: id.Kind, Host: strings.ToLower(id.Host), Name: id.Name, Email: id.Email, Token: phs[id.ID]}
	}
	data, _ := json.Marshal(bodies)
	sum := sha256.Sum256(append([]byte(fc.cfg.URL+"\x00"+fc.token+"\x00"), data...))
	fp := hex.EncodeToString(sum[:])
	if fp == st.Pushed {
		return ""
	}
	var problems []string
	var have []FrideInstanceIdentity
	for i, id := range ids {
		if err := fc.do(ctx, http.MethodPut, "/api/companion/git/identities/"+url.PathEscape(id.ID), bodies[i], nil); err != nil {
			problems = append(problems, fmt.Sprintf("FrIDE did not take %s: %s", id.ID, frideErr(err).Message))
			continue
		}
		have = append(have, FrideInstanceIdentity{ID: id.ID, Kind: id.Kind, Host: bodies[i].Host, Name: id.Name, Email: id.Email})
	}
	for _, old := range st.Identities {
		if slices.ContainsFunc(ids, func(id nodeproto.FrideIdentity) bool { return id.ID == old.ID }) {
			continue
		}
		err := fc.do(ctx, http.MethodDelete, "/api/companion/git/identities/"+url.PathEscape(old.ID), nil, nil)
		if fe, ok := errors.AsType[*FrideError](err); err != nil && !(ok && fe.Status == http.StatusNotFound) {
			problems = append(problems, fmt.Sprintf("FrIDE did not remove %s: %s", old.ID, frideErr(err).Message))
			have = append(have, old)
		}
	}
	st.Identities = have
	if len(problems) > 0 {
		st.Pushed = ""
		return strings.Join(problems, "; ")
	}
	st.Pushed = fp
	return ""
}

// repoCloneName is the folder a repository's URL is cloned into: its
// last path element, without .git.
func repoCloneName(u string) string {
	p := u
	if pu, err := url.Parse(u); err == nil && pu.Path != "" {
		p = pu.Path
	} else if _, after, ok := strings.Cut(u, ":"); ok {
		p = after
	}
	return strings.TrimSuffix(path.Base(strings.TrimRight(p, "/")), ".git")
}

// cloneInstanceURL reports what is wrong with a URL to clone in an
// instance: an https URL, since its git reaches hosts through sbx's
// proxy, which swaps the placeholders only there.
func cloneInstanceURL(u string) (string, error) {
	if err := checkCloneURL(u); err != nil {
		return "", err
	}
	if !strings.HasPrefix(u, "https://") {
		return "", fmt.Errorf("the URL %q: an instance clones over https only", u)
	}
	name := repoCloneName(u)
	if err := checkRepoName(name); err != nil {
		return "", fmt.Errorf("the URL %q: %w", u, err)
	}
	return name, nil
}

// cloneRepos clones those of repos the instance's base folder lacks, in
// the VM. It returns how many it cloned, and what failed.
func (o instanceOps) cloneRepos(ctx context.Context, st *FrideInstanceState, repos []string) (int, []string) {
	n := 0
	var problems []string
	for _, u := range repos {
		name, err := cloneInstanceURL(u)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, repoCloneTimeout)
		out, stderr, err := o.d.run(cctx, nil, nil, append([]string{"exec", st.Sandbox}, frideInstanceScript(frideInstanceClone, u, name)...)...)
		cancel()
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("cloning %s failed: %v", u, sbxError(err, lastLineOf(stderr))))
		case parseKeyValues(out)["clone"] == "done":
			o.say("Cloned %s into %s's base folder.", u, st.Name)
			n++
		}
	}
	return n, problems
}

// instanceTokenFileOf is the pairing token's file of the instance name.
func instanceTokenFileOf(dir, name string) string {
	return filepath.Join(frideInstanceDir(dir, name), frideInstanceTokenFile)
}

// removeInstanceFiles takes an instance's state, pairing, tokens and
// carry from the node; its history stays.
func removeInstanceFiles(dir, name string) error {
	d := frideInstanceDir(dir, name)
	var errs []error
	for _, p := range []string{filepath.Join(d, frideInstanceTokens), filepath.Join(d, frideInstanceCarryDir)} {
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, err)
		}
	}
	for _, f := range []string{instanceTokenFileOf(dir, name), filepath.Join(d, frideInstanceFile)} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
