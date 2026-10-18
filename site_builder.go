package main

// Brains that build the site (docs/SITE.md, "Building with a brain").
//
// A brain you allow gets, in its own folder under .fridolin-site/: the
// site-builder plugin (skills about Fridolin and the module format), a
// workspace (a clone of the site repository on the branch builder/<brain>),
// a copy of the site process for previews, and an MCP server that talks to
// the bridge with a token of its own.
//
// The workspace is the brain's: anything in it, its .git included, may have
// been written by a manipulated brain. So Fridolin never runs git or npm on
// it outside the brain's own sandbox. Work moves between Fridolin's clone
// and the workspace only as git bundles, and checks and previews run inside
// the brain's sandbox, with nothing of the live site: no session key, no
// accounts. A brain can propose; only you merge and deploy, in the dashboard.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SiteProposal is a change a builder brain pushed for review.
type SiteProposal struct {
	Branch  string `json:"branch"` // builder/<brain>
	Brain   string `json:"brain"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Commit  string `json:"commit"`
	At      string `json:"at"`
}

// builderHooks connect the site to the brains. Set by the bridge.
type builderHooks struct {
	// brainDir returns a brain's folder, or "" for no such brain.
	brainDir func(id string) string
	// sandbox wraps bin+args to run under the brain's sandbox profile (or
	// returns them unchanged when sandboxing is off), with the variables
	// that point the command at the brain's own TMPDIR and caches, the only
	// places outside its folder it may write.
	sandbox func(brainID, bin string, args []string) (string, []string, []string, error)
	// bridgeURL is the bridge's own HTTP address, for the MCP server.
	bridgeURL func() string
	// executable is the fridolin binary the MCP server runs from.
	executable func() (string, error)
	// siteLimits is what the live site's sandbox keeps from module code
	// beyond its own list: the bridge's files (SandboxProfiler.sitePaths)
	// and its HTTP port.
	siteLimits func() siteBridgeLimits
	// ingest hands files (already in the media folder) to a brain, as
	// /ingest does; mediaDir is that folder (site_ingest.go).
	ingest   func(brainID, sender, text string, files []string) error
	mediaDir func() string
	// search runs a read-only Claude agent in a brain's folder, its
	// stream-json output to out (site_ingest.go).
	search func(ctx context.Context, brainID, prompt string, out io.Writer) error
}

func (m *SiteManager) builderBase(brainID string) string {
	dir := ""
	if m.hooks.brainDir != nil {
		dir = m.hooks.brainDir(brainID)
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ".fridolin-site")
}

func builderBranch(brainID string) string { return "builder/" + brainID }

// IsBuilder reports whether a brain may build the site.
func (m *SiteManager) IsBuilder(brainID string) bool {
	return slices.Contains(m.Settings().Builders, brainID)
}

// SetBuilder allows or stops a brain building the site. Allowing it sets
// up its workspace; stopping it leaves the folder (and the brain's work) as
// it is.
func (m *SiteManager) SetBuilder(ctx context.Context, brainID string, on bool) error {
	if m.builderBase(brainID) == "" {
		return fmt.Errorf("no brain %q", brainID)
	}
	if on && !fileExists(m.repoDir()) {
		return errors.New("connect the site repository first")
	}
	m.mu.Lock()
	builders := slices.DeleteFunc(slices.Clone(m.settings.Builders), func(b string) bool { return b == brainID })
	if on {
		builders = append(builders, brainID)
		slices.Sort(builders)
	}
	m.settings.Builders = builders
	err := m.saveSettingsLocked()
	m.mu.Unlock()
	if err != nil || !on {
		m.stopPreview(brainID)
		return err
	}
	return m.ensureBuilder(ctx, brainID)
}

// BuilderArgs are the extra CLI arguments for a brain's runs: the builder
// plugin and its MCP server, for brains allowed to build the site.
func (m *SiteManager) BuilderArgs(brainID string) []string {
	if !m.IsBuilder(brainID) {
		return nil
	}
	if err := m.ensureBuilder(m.baseContext(), brainID); err != nil {
		log.Printf("[site] brain %s builds the site, but its workspace is not ready: %v", brainID, err)
		return nil
	}
	base := m.builderBase(brainID)
	return []string{"--plugin-dir", filepath.Join(base, "plugin"), "--mcp-config", filepath.Join(base, "mcp.json")}
}

// ensureBuilder writes the plugin and MCP config and, the first time, the
// workspace and its copy of the site process.
func (m *SiteManager) ensureBuilder(ctx context.Context, brainID string) error {
	base := m.builderBase(brainID)
	if base == "" {
		return fmt.Errorf("no brain %q", brainID)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	plugin := filepath.Join(base, "plugin")
	if err := os.MkdirAll(plugin, 0o700); err != nil {
		return err
	}
	if err := copyEmbedded(siteShellFS, "site/builder", plugin); err != nil {
		return err
	}
	if err := m.writeMCPConfig(brainID); err != nil {
		return err
	}
	if err := m.ensureBuilderShell(ctx, brainID); err != nil {
		return err
	}
	ws := filepath.Join(base, "workspace")
	if fileExists(filepath.Join(ws, ".git")) {
		return nil
	}
	return m.createWorkspace(ctx, brainID)
}

func (m *SiteManager) writeMCPConfig(brainID string) error {
	exe := ""
	if m.hooks.executable != nil {
		var err error
		if exe, err = m.hooks.executable(); err != nil {
			return err
		}
	}
	url := ""
	if m.hooks.bridgeURL != nil {
		url = m.hooks.bridgeURL()
	}
	cfg := map[string]any{"mcpServers": map[string]any{
		"fridolin-site": map[string]any{
			"type":    "stdio",
			"command": exe,
			"args":    []string{"mcp-site"},
			"env":     map[string]string{"FRIDOLIN_BUILDER_URL": url, "FRIDOLIN_BUILDER_TOKEN": m.builderToken(brainID)},
		},
	}}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return writeFileAtomic(filepath.Join(m.builderBase(brainID), "mcp.json"), data, 0o600, false)
}

// builderToken is the brain's token for /builder/, made once per bridge run.
func (m *SiteManager) builderToken(brainID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tokens == nil {
		m.tokens = map[string]string{}
	}
	for tok, id := range m.tokens {
		if id == brainID {
			return tok
		}
	}
	tok := randomToken(24)
	m.tokens[tok] = brainID
	return tok
}

func (m *SiteManager) brainForToken(tok string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.tokens[tok]
	if id == "" || !slices.Contains(m.settings.Builders, id) {
		return ""
	}
	return id
}

// ensureBuilderShell copies the site process (with its dependencies) into
// the brain's folder, where its sandbox can reach it for checks and previews.
func (m *SiteManager) ensureBuilderShell(ctx context.Context, brainID string) error {
	shell, err := m.ensureShell(ctx)
	if err != nil {
		return err
	}
	dst := filepath.Join(m.builderBase(brainID), "shell")
	want, _ := os.ReadFile(filepath.Join(shell, ".fridolin-shell"))
	if have, err := os.ReadFile(filepath.Join(dst, ".fridolin-shell")); err == nil && string(have) == string(want) {
		return nil
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	// cp keeps node_modules' symlinks (its .bin) as they are.
	if out, err := exec.CommandContext(ctx, "cp", "-R", shell+"/.", dst).CombinedOutput(); err != nil {
		return fmt.Errorf("copying the site process: %v %s", err, out)
	}
	return nil
}

// sandboxed runs a command as the brain: under its sandbox, in dir.
func (m *SiteManager) sandboxed(ctx context.Context, brainID, dir string, timeout time.Duration, bin string, args ...string) (string, error) {
	if m.hooks.sandbox == nil {
		return "", errors.New("no sandbox hook")
	}
	sbBin, sbArgs, sbEnv, err := m.hooks.sandbox(brainID, bin, args)
	if err != nil {
		return "", fmt.Errorf("the brain's sandbox is not available: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sbBin, sbArgs...)
	// Like a brain's run: no terminal to type into, and a timeout stops
	// whatever the command started too.
	isolateProcessGroup(cmd)
	cmd.Dir = dir
	node, _ := findNode()
	cmd.Env = append(siteEnv(node, "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Fridolin ("+brainID+")", "GIT_COMMITTER_NAME=Fridolin ("+brainID+")",
		"GIT_AUTHOR_EMAIL="+siteGitEmail, "GIT_COMMITTER_EMAIL="+siteGitEmail), sbEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v\n%s", filepath.Base(bin), strings.Join(args, " "), err, tail(string(out), 1500))
	}
	return string(out), nil
}

// siteBaseRef is the branch head the bundles for workspaces carry.
const siteBaseRef = "site-base"

// writeBundle puts Fridolin's view of the branch into the brain's folder as
// a git bundle: the only way history goes into the workspace.
func (m *SiteManager) writeBundle(ctx context.Context, brainID string) (string, error) {
	if _, err := m.git(ctx, m.repoDir(), "fetch", "--prune", "origin"); err != nil {
		return "", err
	}
	branch := m.Settings().Repo.Branch
	if _, err := m.git(ctx, m.repoDir(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err != nil {
		return "", errors.New("the site repository has no commits yet; add the starter files first")
	}
	// A bundle carries refs by name, and a clone takes branch heads: the
	// branch goes in as the head site-base.
	if _, err := m.git(ctx, m.repoDir(), "update-ref", "refs/heads/"+siteBaseRef, "refs/remotes/origin/"+branch); err != nil {
		return "", err
	}
	bundle := filepath.Join(m.builderBase(brainID), "site.bundle")
	_ = os.Remove(bundle)
	if _, err := m.git(ctx, m.repoDir(), "bundle", "create", bundle, "refs/heads/"+siteBaseRef); err != nil {
		return "", err
	}
	return bundle, nil
}

// createWorkspace clones the branch from a bundle into the brain's workspace
// and installs its dependencies, as the brain.
func (m *SiteManager) createWorkspace(ctx context.Context, brainID string) error {
	base := m.builderBase(brainID)
	bundle, err := m.writeBundle(ctx, brainID)
	if err != nil {
		return err
	}
	ws := filepath.Join(base, "workspace")
	git := gitPath()
	if _, err := m.sandboxed(ctx, brainID, base, 2*time.Minute, git, "clone", "--quiet", "--no-checkout", bundle, ws); err != nil {
		return err
	}
	if _, err := m.sandboxed(ctx, brainID, ws, time.Minute, git, "checkout", "--quiet", "-B", builderBranch(brainID), "refs/remotes/origin/"+siteBaseRef); err != nil {
		return err
	}
	root := filepath.Join(ws, filepath.FromSlash(m.Settings().Repo.Subdir))
	if fileExists(filepath.Join(root, "package.json")) {
		return m.workspaceInstall(ctx, brainID, root)
	}
	return nil
}

// npmInstallAsBrain installs a workspace's dependencies inside the brain's
// sandbox: the package.json (and its install scripts) is the brain's to edit.
func (m *SiteManager) npmInstallAsBrain(ctx context.Context, brainID, dir string) error {
	node, err := findNode()
	if err != nil {
		return err
	}
	_, err = m.sandboxed(ctx, brainID, dir, 10*time.Minute, filepath.Join(filepath.Dir(node), "npm"), "install", "--no-audit", "--no-fund")
	return err
}

// ── what the MCP tools do ──────────────────────────────────────────────────

func (m *SiteManager) workspace(brainID string) string {
	return filepath.Join(m.builderBase(brainID), "workspace")
}

func (m *SiteManager) workspaceModules(brainID string) string {
	return filepath.Join(m.workspace(brainID), filepath.FromSlash(m.Settings().Repo.Subdir), "modules")
}

// liveModules lists the modules of the approved commit, as their manifests say.
func (m *SiteManager) liveModules() []map[string]any {
	s := m.Settings()
	out := []map[string]any{}
	if s.Approved == "" {
		return out
	}
	entries, _ := os.ReadDir(m.modulesDirOf(s.Approved))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(m.modulesDirOf(s.Approved), e.Name(), "module.json"))
		if err != nil {
			continue
		}
		var mf map[string]any
		if json.Unmarshal(data, &mf) == nil {
			out = append(out, map[string]any{"id": mf["id"], "title": mf["title"], "route": mf["route"], "access": mf["access"]})
		}
	}
	return out
}

// BuilderContext is what site_context returns.
func (m *SiteManager) BuilderContext(brainID string) map[string]any {
	s := m.Settings()
	ctx := map[string]any{
		"site": map[string]any{"title": s.Title, "domain": s.Domain, "roles": s.Roles, "on": s.Enabled},
		"live": map[string]any{"commit": s.Approved, "modules": m.liveModules()},
		"workspace": map[string]any{
			"path":        m.workspace(brainID),
			"modules_dir": m.workspaceModules(brainID),
			"branch":      builderBranch(brainID),
			"based_on":    s.Repo.Branch,
		},
	}
	if s.Domain != "" {
		ctx["site"].(map[string]any)["url"] = "https://" + s.Domain + "/"
	}
	if p := m.previewOf(brainID); p != nil {
		ctx["preview"] = m.previewURLs(brainID, p)
	}
	for _, p := range s.Proposals {
		if p.Brain == brainID {
			ctx["proposal"] = p
		}
	}
	return ctx
}

// CheckResult is what site_check returns.
type CheckResult struct {
	Mounted []string       `json:"mounted"`
	Broken  []BrokenModule `json:"broken"`
	Review  []ModuleChange `json:"review"`
}

// BrokenModule is a module the site would skip, and why.
type BrokenModule struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// BuilderCheck loads the workspace's modules the way the site will — inside
// the brain's sandbox — and lists what an approver will be asked to review.
func (m *SiteManager) BuilderCheck(ctx context.Context, brainID string) (CheckResult, error) {
	res := CheckResult{Mounted: []string{}, Broken: []BrokenModule{}, Review: []ModuleChange{}}
	node, err := findNode()
	if err != nil {
		return res, err
	}
	shell := filepath.Join(m.builderBase(brainID), "shell")
	out, err := m.sandboxed(ctx, brainID, shell, 2*time.Minute, node, "--import", "tsx/esm", filepath.Join(shell, "server", "check.ts"), m.workspaceModules(brainID))
	if err != nil {
		return res, err
	}
	// The report is the last line; module code may have printed before it.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		return res, fmt.Errorf("the check printed no report: %s", tail(out, 600))
	}
	res.Mounted = nonNil(res.Mounted)
	res.Broken = nonNil(res.Broken)
	res.Review = m.reviewWorkspace(ctx, brainID)
	return res, nil
}

// reviewWorkspace compares the workspace's manifests on disk with the live commit's.
func (m *SiteManager) reviewWorkspace(ctx context.Context, brainID string) []ModuleChange {
	s := m.Settings()
	prefix := s.Repo.Subdir
	if prefix != "" {
		prefix += "/"
	}
	prefix += "modules/"
	ids := map[string]bool{}
	entries, _ := os.ReadDir(m.workspaceModules(brainID))
	for _, e := range entries {
		if e.IsDir() {
			ids[e.Name()] = true
		}
	}
	for _, mod := range m.liveModules() {
		if id, ok := mod["id"].(string); ok {
			ids[id] = true
		}
	}
	out := []ModuleChange{}
	for _, id := range slices.Sorted(func(yield func(string) bool) {
		for id := range ids {
			if !yield(id) {
				return
			}
		}
	}) {
		oldM := m.manifestAt(ctx, s.Approved, prefix+id+"/module.json")
		var newM map[string]any
		if data, err := os.ReadFile(filepath.Join(m.workspaceModules(brainID), id, "module.json")); err == nil {
			if json.Unmarshal(data, &newM) != nil {
				newM = map[string]any{"invalid": "module.json is not valid JSON"}
			}
		}
		c := ModuleChange{ID: id, Review: reviewManifest(oldM, newM), Files: []string{}}
		switch {
		case oldM == nil && newM == nil:
			continue
		case oldM == nil:
			c.Status = "added"
		case newM == nil:
			c.Status = "removed"
		case len(c.Review) == 0:
			continue // unchanged as far as the manifest goes
		default:
			c.Status = "changed"
		}
		out = append(out, c)
	}
	return out
}

// BuilderSync rebases the workspace onto the newest commit of the branch, as the brain.
func (m *SiteManager) BuilderSync(ctx context.Context, brainID string) (string, error) {
	bundle, err := m.writeBundle(ctx, brainID)
	if err != nil {
		return "", err
	}
	ws := m.workspace(brainID)
	branch := m.Settings().Repo.Branch
	if _, err := m.sandboxed(ctx, brainID, ws, time.Minute, gitPath(), "fetch", "--quiet", bundle, "+refs/heads/"+siteBaseRef+":refs/remotes/site/"+branch); err != nil {
		return "", err
	}
	out, err := m.sandboxed(ctx, brainID, ws, time.Minute, gitPath(), "rebase", "refs/remotes/site/"+branch)
	if err != nil {
		_, _ = m.sandboxed(ctx, brainID, ws, time.Minute, gitPath(), "rebase", "--abort")
		return "", fmt.Errorf("your commits do not apply cleanly on the newest %s; the rebase was undone. Merge refs/remotes/site/%s yourself and resolve the conflicts: %w", branch, branch, err)
	}
	return strings.TrimSpace(out), nil
}

// BuilderPropose takes the workspace's committed work (through a bundle the
// brain writes), pushes it as builder/<brain> and records the proposal.
func (m *SiteManager) BuilderPropose(ctx context.Context, brainID, title, summary string) (SiteProposal, []ModuleChange, error) {
	title, summary = strings.TrimSpace(title), strings.TrimSpace(summary)
	if title == "" || len(title) > 120 {
		return SiteProposal{}, nil, errors.New("give the proposal a title of up to 120 characters")
	}
	if len(summary) > 4000 {
		summary = summary[:4000]
	}
	base := m.builderBase(brainID)
	bundle := filepath.Join(base, "proposal.bundle")
	_ = os.Remove(bundle)
	if _, err := m.sandboxed(ctx, brainID, m.workspace(brainID), time.Minute, gitPath(), "bundle", "create", bundle, "HEAD"); err != nil {
		return SiteProposal{}, nil, err
	}
	ref := "refs/builder/" + brainID
	if _, err := m.git(ctx, m.repoDir(), "fetch", "--quiet", "--no-tags", bundle, "+HEAD:"+ref); err != nil {
		return SiteProposal{}, nil, err
	}
	commit, err := m.git(ctx, m.repoDir(), "rev-parse", ref)
	if err != nil {
		return SiteProposal{}, nil, err
	}
	s := m.Settings()
	if base, _ := m.git(ctx, m.repoDir(), "rev-parse", "refs/remotes/origin/"+s.Repo.Branch); base == commit {
		return SiteProposal{}, nil, errors.New("the workspace has no commits beyond the branch: commit your work first")
	}
	if _, err := m.git(ctx, m.repoDir(), "push", "--force", "origin", ref+":refs/heads/"+builderBranch(brainID)); err != nil {
		return SiteProposal{}, nil, err
	}
	changes, err := m.ModuleChanges(ctx, s.Approved, commit)
	if err != nil {
		return SiteProposal{}, nil, err
	}
	p := SiteProposal{Branch: builderBranch(brainID), Brain: brainID, Title: title, Summary: summary, Commit: commit, At: time.Now().UTC().Format(time.RFC3339)}
	m.mu.Lock()
	m.settings.Proposals = slices.DeleteFunc(m.settings.Proposals, func(x SiteProposal) bool { return x.Brain == brainID })
	m.settings.Proposals = append(m.settings.Proposals, p)
	err = m.saveSettingsLocked()
	m.mu.Unlock()
	return p, changes, err
}

// Proposals lists the open proposals with what each changes.
func (m *SiteManager) Proposals(ctx context.Context) []map[string]any {
	s := m.Settings()
	out := []map[string]any{}
	for _, p := range s.Proposals {
		changes, err := m.ModuleChanges(ctx, s.Approved, p.Commit)
		item := map[string]any{"proposal": p, "changes": changes}
		if err != nil {
			item["error"] = err.Error()
		}
		if pv := m.previewOf(p.Brain); pv != nil {
			item["preview"] = m.previewURLs(p.Brain, pv)
		}
		out = append(out, item)
	}
	return out
}

// MergeProposal merges a proposal into the branch, pushes it and deploys the
// result. A conflict stops before anything is pushed.
func (m *SiteManager) MergeProposal(ctx context.Context, brainID string) error {
	s := m.Settings()
	i := slices.IndexFunc(s.Proposals, func(p SiteProposal) bool { return p.Brain == brainID })
	if i < 0 {
		return errors.New("no such proposal")
	}
	p := s.Proposals[i]
	if _, err := m.git(ctx, m.repoDir(), "fetch", "--prune", "origin"); err != nil {
		return err
	}
	work := m.dir("work", "merge-"+randomToken(6))
	if _, err := m.git(ctx, m.repoDir(), "worktree", "add", "--detach", work, "refs/remotes/origin/"+s.Repo.Branch); err != nil {
		return err
	}
	defer func() { _, _ = m.git(context.Background(), m.repoDir(), "worktree", "remove", "--force", work) }()
	if _, err := m.git(ctx, work, "merge", "--no-ff", "-m", "Merge "+p.Branch+": "+p.Title, p.Commit); err != nil {
		return fmt.Errorf("the proposal does not merge cleanly into %s; ask the brain to sync its workspace and propose again (%v)", s.Repo.Branch, err)
	}
	head, err := m.git(ctx, work, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := m.git(ctx, work, "push", "origin", "HEAD:refs/heads/"+s.Repo.Branch); err != nil {
		return err
	}
	if _, err := m.git(ctx, m.repoDir(), "fetch", "--prune", "origin"); err != nil {
		return err
	}
	m.mu.Lock()
	m.settings.Proposals = slices.DeleteFunc(m.settings.Proposals, func(x SiteProposal) bool { return x.Brain == brainID })
	err = m.saveSettingsLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.Deploy(m.baseContext(), head)
}

// DiscardProposal forgets a proposal. The branch stays in the repository.
func (m *SiteManager) DiscardProposal(brainID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.settings.Proposals)
	m.settings.Proposals = slices.DeleteFunc(m.settings.Proposals, func(x SiteProposal) bool { return x.Brain == brainID })
	if len(m.settings.Proposals) == n {
		return errors.New("no such proposal")
	}
	return m.saveSettingsLocked()
}

// ── previews ──────────────────────────────────────────────────────────────

func (m *SiteManager) previewOf(brainID string) *sitePreview {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.previews[brainID]
}

func (m *SiteManager) previewURLs(brainID string, p *sitePreview) map[string]string {
	urls := map[string]string{"local": "http://" + p.Listen + "/_fridolin/preview-login?token=" + p.Token}
	if d := m.Settings().Domain; d != "" {
		urls["public"] = "https://" + d + "/_fridolin/preview/" + brainID
	}
	return urls
}

// BuilderPreview (re)starts a preview of the workspace: a site process
// inside the brain's sandbox, on a port of its own, that knows no accounts.
// You open it through a link with its token (the dashboard has it), or, as
// an admin, through the live site, which forwards your requests to it.
func (m *SiteManager) BuilderPreview(ctx context.Context, brainID string) (map[string]string, error) {
	m.stopPreview(brainID)
	if err := m.ensureBuilderShell(ctx, brainID); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	listen := ln.Addr().String()
	ln.Close()
	p := &sitePreview{Branch: builderBranch(brainID), Listen: listen, Token: randomToken(24), proc: newSiteProc("preview " + brainID)}
	base := m.builderBase(brainID)
	rt := map[string]any{
		"listen": listen, "title": m.Settings().Title + " (preview)",
		"modules_dir": m.workspaceModules(brainID), "preview_label": p.Branch, "preview_token": p.Token,
		// Its own data folders, in the brain's folder where its sandbox lets it write.
		"data_dir": filepath.Join(base, "preview-data"),
	}
	data, _ := json.MarshalIndent(rt, "", "  ")
	rtFile := filepath.Join(base, "preview.runtime.json")
	if err := writeFileAtomic(rtFile, data, 0o600, false); err != nil {
		return nil, err
	}
	shell := filepath.Join(base, "shell")
	build := func(ctx context.Context) (*exec.Cmd, error) {
		node, err := findNode()
		if err != nil {
			return nil, err
		}
		bin, args, sbEnv, err := m.hooks.sandbox(brainID, node, []string{"--import", "tsx/esm", filepath.Join(shell, "server", "main.ts")})
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = shell
		cmd.Env = append(siteEnv(node, "FRIDOLIN_SITE_RUNTIME="+rtFile, "NODE_ENV=production"), sbEnv...)
		return cmd, nil
	}
	p.proc.ensure(m.baseContext(), build)
	deadline := time.Now().Add(20 * time.Second)
	for !siteHealthy(listen) {
		if time.Now().After(deadline) {
			st := p.proc.status()
			p.proc.stop()
			return nil, fmt.Errorf("the preview did not start: %s\n%s", st.Error, strings.Join(st.Log, "\n"))
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Known only once it answers, so nobody opens a link that is not up yet.
	m.mu.Lock()
	m.previews[brainID] = p
	m.mu.Unlock()
	if err := m.writePreviewsFile(); err != nil {
		return nil, err
	}
	return m.previewURLs(brainID, p), nil
}

func (m *SiteManager) stopPreview(brainID string) {
	m.mu.Lock()
	p := m.previews[brainID]
	delete(m.previews, brainID)
	m.mu.Unlock()
	if p != nil {
		p.proc.stop()
		_ = m.writePreviewsFile()
	}
}

// writePreviewsFile tells the live site which previews it may forward admins to.
func (m *SiteManager) writePreviewsFile() error {
	m.mu.Lock()
	list := map[string]map[string]string{}
	for id, p := range m.previews {
		list[id] = map[string]string{"listen": p.Listen, "token": p.Token, "label": p.Branch}
	}
	m.mu.Unlock()
	data, _ := json.MarshalIndent(list, "", "  ")
	return writeFileAtomic(m.dir("previews.json"), data, 0o600, false)
}

// ── the HTTP side of the MCP server ────────────────────────────────────────

// builderHandler serves /builder/ for the MCP servers of builder brains.
// Each brain's token works only for its own workspace.
func (b *Bridge) builderHandler() http.Handler {
	mux := http.NewServeMux()
	m := b.site
	call := func(fn func(r *http.Request, brainID string) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			brainID := m.brainForToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if brainID == "" {
				writeErr(w, http.StatusUnauthorized, "this brain may not build the site (Fridolin dashboard → Site → Builders)")
				return
			}
			out, err := fn(r, brainID)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, out)
		}
	}
	mux.HandleFunc("GET /builder/context", call(func(r *http.Request, id string) (any, error) {
		return m.BuilderContext(id), nil
	}))
	mux.HandleFunc("POST /builder/check", call(func(r *http.Request, id string) (any, error) {
		return m.BuilderCheck(r.Context(), id)
	}))
	mux.HandleFunc("POST /builder/preview", call(func(r *http.Request, id string) (any, error) {
		return m.BuilderPreview(r.Context(), id)
	}))
	mux.HandleFunc("POST /builder/sync", call(func(r *http.Request, id string) (any, error) {
		out, err := m.BuilderSync(r.Context(), id)
		return map[string]string{"result": out}, err
	}))
	mux.HandleFunc("POST /builder/propose", call(func(r *http.Request, id string) (any, error) {
		var req struct{ Title, Summary string }
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64*1024)).Decode(&req); err != nil {
			return nil, err
		}
		p, changes, err := m.BuilderPropose(r.Context(), id, req.Title, req.Summary)
		return map[string]any{"proposal": p, "changes": changes}, err
	}))
	return mux
}

// gitPath is git's full path: sandbox-exec runs a program by path.
func gitPath() string {
	if p, err := exec.LookPath("git"); err == nil {
		return p
	}
	return "/usr/bin/git"
}

// siteBuilderHooks connect the site to this bridge's brains and sandbox.
func (b *Bridge) siteBuilderHooks() builderHooks {
	return builderHooks{
		brainDir: func(id string) string {
			if br := b.brainMgr.Get(id); br != nil {
				return br.Dir()
			}
			return ""
		},
		sandbox: func(brainID, bin string, args []string) (string, []string, []string, error) {
			sp := b.brainMgr.profiler
			if sp == nil || !sp.Enabled() {
				return bin, args, nil, nil
			}
			cfg := b.cfgs.Get()
			env, err := prepareBrainState(absPath(cfg.DataDir), cfg.MediaDir, brainID, cfg.InheritClaudeUserConfig)
			if err != nil {
				return "", nil, nil, err
			}
			sbBin, sbArgs, err := sp.Command(brainID, bin, args)
			return sbBin, sbArgs, env, err
		},
		bridgeURL: func() string { return "http://" + b.cfgs.Get().HTTPListen },
		executable: func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(exe)
		},
		siteLimits: func() siteBridgeLimits {
			var l siteBridgeLimits
			if sp := b.brainMgr.profiler; sp != nil {
				l.deny, l.noWrite = sp.sitePaths()
			}
			if cfg := b.cfgs.Get(); cfg.HTTPEnabled() {
				if _, port, err := net.SplitHostPort(cfg.HTTPListen); err == nil {
					l.bridgePort = port
				}
			}
			return l
		},
		mediaDir: func() string { return b.cfgs.Get().MediaDir },
		search:   b.runBrainSearch,
		// As /ingest: the brain's reply chat gets the answer, and the brain
		// must take the files now (its queue is not full).
		ingest: func(brainID, sender, text string, files []string) error {
			cfg := b.cfgs.Get()
			if !cfg.Ingest.Enabled {
				return errors.New("ingest is turned off (ingest.enabled)")
			}
			br := b.brainMgr.Get(brainID)
			if br == nil {
				return fmt.Errorf("no brain %q", brainID)
			}
			jid := b.replyChatFor(br, cfg)
			if jid == "" {
				return fmt.Errorf("brain %s has no chat to reply in; set its reply chat", brainID)
			}
			return b.submitHandOff(br, jid, sender, text, files, nil)
		},
	}
}
