package main

// The site repository: connecting it, the starter files, what changed since
// the approved commit, and deploying a commit.
//
// <data_dir>/site/repo is Fridolin's clone. Each deployed commit gets its own
// folder, deploys/<commit>, checked out as a git worktree with its npm
// dependencies installed and its modules built. The site process then serves
// that folder, so a deploy that fails to build never touches the live site,
// and rolling back to a recent commit needs no rebuild.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sitesvc "github.com/Icewater1337/fridolin/internal/site"
	"io/fs"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/textx"
	"github.com/Icewater1337/fridolin/site"
)

// The identity of the commits Fridolin makes in the site repository.
const siteGitAuthor = "Fridolin"
const siteGitEmail = "fridolin@example.com" // personal-data:ignore (a placeholder by design)

var siteCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (m *SiteManager) repoDir() string { return m.dir("repo") }

func (m *SiteManager) deployDir(commit string) string { return m.dir("deploys", commit[:12]) }

// modulesDirOf is the modules folder of a deployed commit.
func (m *SiteManager) modulesDirOf(commit string) string {
	if commit == "" {
		return m.dir("deploys", "none", "modules")
	}
	return filepath.Join(m.deployDir(commit), filepath.FromSlash(m.Settings().Repo.Subdir), "modules")
}

// git runs git in dir with the deploy key and no prompts.
func (m *SiteManager) git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	full := append([]string{"-c", "user.name=" + siteGitAuthor, "-c", "user.email=" + siteGitEmail, "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(gitEnv(m.host.Environ()), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false")
	if key := m.dir("deploy_key"); fsx.Exists(key) {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+fmt.Sprintf(
			"ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s",
			siteShellQuote(key), siteShellQuote(m.dir("known_hosts"))))
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], textx.Tail(msg, 800))
	}
	return strings.TrimSpace(out.String()), nil
}

// gitEnv is env without the GIT_ variables, so that git works on the
// repository in its working directory and nothing else. The bridge's own
// environment may carry them: run from a git hook or `git rebase -x`, it
// inherits GIT_DIR and GIT_INDEX_FILE, and the site's commits and pushes
// would then go to that repository instead of the site's.
func gitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}

// ── deploy key ──────────────────────────────────────────────────────────────

// DeployKey returns the public deploy key, creating the key pair on first use.
func (m *SiteManager) DeployKey(ctx context.Context) (string, error) {
	priv := m.dir("deploy_key")
	if !fsx.Exists(priv) {
		if err := os.MkdirAll(m.dir(), 0o700); err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "fridolin-site", "-f", priv)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ssh-keygen: %v %s", err, out)
		}
	}
	pub, err := os.ReadFile(priv + ".pub")
	return strings.TrimSpace(string(pub)), err
}

// githubRepo is "owner/name" for a GitHub URL, or "". An owner starts with a
// letter or digit on GitHub, so the result never reads as a flag to gh.
func githubRepo(u string) string {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`^git@github\.com:([[:alnum:]][\w.-]*/[\w.-]+?)(\.git)?$`),
		regexp.MustCompile(`^(?:ssh://git@|https://)github\.com/([[:alnum:]][\w.-]*/[\w.-]+?)(\.git)?/?$`),
	} {
		if mm := re.FindStringSubmatch(u); mm != nil {
			if _, name, _ := strings.Cut(mm[1], "/"); name == "." || name == ".." {
				return ""
			}
			return mm[1]
		}
	}
	return ""
}

// AddDeployKeyWithGH adds the deploy key to the GitHub repository with write
// access, using the GitHub CLI's own login, and returns the repository's
// "owner/name". Once a repository is connected that is the connected one.
// Before, it is addr, the address typed for Connect: the key has to be on a
// private repository before Connect can clone it.
func (m *SiteManager) AddDeployKeyWithGH(ctx context.Context, addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	u := m.Settings().Repo.URL
	if fsx.Exists(m.repoDir()) {
		if addr != "" && addr != u {
			return "", errors.New("another repository is connected; disconnect it first")
		}
	} else {
		if addr == "" {
			return "", errors.New("enter the repository's SSH address first, e.g. git@github.com:you/site.git, so gh knows where to add the key")
		}
		if !validRepoURL(addr) {
			return "", errors.New("enter the repository's SSH address, e.g. git@github.com:you/site.git")
		}
		u = addr
	}
	repo := githubRepo(u)
	if repo == "" {
		return "", errors.New("the repository is not on GitHub; add the key in its settings by hand")
	}
	gh, err := m.host.Tool("gh")
	if err != nil {
		return "", errors.New("the GitHub CLI (gh) is not installed")
	}
	if _, err := m.DeployKey(ctx); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, gh, "repo", "deploy-key", "add", m.dir("deploy_key.pub"), "--repo", repo, "--allow-write", "--title", "Fridolin site")
	if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(out), "already in use") {
		return "", fmt.Errorf("gh: %s", textx.Tail(string(out), 400))
	}
	return repo, nil
}

// CreateGitHubRepo creates a private repository with the GitHub CLI and
// returns its SSH URL.
func (m *SiteManager) CreateGitHubRepo(ctx context.Context, name string) (string, error) {
	if !regexp.MustCompile(`^[\w.-]{1,100}$`).MatchString(name) {
		return "", errors.New("a repository name is letters, digits, . _ and -")
	}
	gh, err := m.host.Tool("gh")
	if err != nil {
		return "", errors.New("the GitHub CLI (gh) is not installed")
	}
	out, err := exec.CommandContext(ctx, gh, "repo", "create", name, "--private", "--description", "My website's modules, served by Fridolin").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh: %s", textx.Tail(string(out), 400))
	}
	owner, err := exec.CommandContext(ctx, gh, "api", "user", "--jq", ".login").Output()
	if err != nil {
		return "", errors.New("created, but could not read your GitHub user name")
	}
	return fmt.Sprintf("git@github.com:%s/%s.git", strings.TrimSpace(string(owner)), name), nil
}

// ── connecting ──────────────────────────────────────────────────────────────

var siteBranchRE = regexp.MustCompile(`^[\w][\w./-]{0,99}$`)

func validRepoURL(u string) bool {
	if strings.HasPrefix(u, "-") || strings.ContainsAny(u, " \t\n") {
		return false
	}
	if regexp.MustCompile(`^[\w.-]+@[\w.-]+:[\w./~-]+$`).MatchString(u) {
		return true // scp-like ssh: git@host:owner/repo.git
	}
	if filepath.IsAbs(u) {
		return true // a repository on this machine
	}
	p, err := url.Parse(u)
	return err == nil && slices.Contains([]string{"ssh", "https", "file"}, p.Scheme) && (p.Host != "" || p.Scheme == "file")
}

// ConnectRepo clones the site repository. Changing to another repository
// means disconnecting first.
func (m *SiteManager) ConnectRepo(ctx context.Context, r sitesvc.SiteRepo) error {
	r.URL, r.Branch = strings.TrimSpace(r.URL), strings.TrimSpace(r.Branch)
	r.Subdir = strings.Trim(path.Clean("/"+strings.TrimSpace(r.Subdir)), "/")
	if r.Branch == "" {
		r.Branch = "main"
	}
	if !validRepoURL(r.URL) {
		return errors.New("enter the repository's SSH address, e.g. git@github.com:you/site.git")
	}
	if !siteBranchRE.MatchString(r.Branch) || strings.Contains(r.Branch, "..") {
		return fmt.Errorf("%q is not a branch name", r.Branch)
	}
	if strings.Contains(r.Subdir, "..") {
		return errors.New("the folder must be inside the repository")
	}
	m.mu.Lock()
	cur := m.settings.Repo
	m.mu.Unlock()
	if fsx.Exists(m.repoDir()) {
		if cur.URL != r.URL {
			return errors.New("another repository is connected; disconnect it first")
		}
	} else {
		if err := os.MkdirAll(m.dir(), 0o700); err != nil {
			return err
		}
		if _, err := m.git(ctx, m.dir(), "clone", "--no-checkout", "--", r.URL, m.repoDir()); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.settings.Repo = r
	err := m.saveSettingsLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = m.Fetch(ctx)
	return err
}

// DisconnectRepo forgets the repository. The clone is moved aside, not deleted.
func (m *SiteManager) DisconnectRepo() error {
	m.mu.Lock()
	enabled := m.settings.Enabled
	m.mu.Unlock()
	if enabled {
		return errors.New("turn the site off first")
	}
	if fsx.Exists(m.repoDir()) {
		aside := m.dir(fmt.Sprintf("repo.disconnected-%s", time.Now().Format("20060102-150405")))
		if err := os.Rename(m.repoDir(), aside); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings.Repo = sitesvc.SiteRepo{Branch: "main"}
	m.settings.Approved = ""
	m.settings.History = nil
	return m.saveSettingsLocked()
}

// AddStarter commits the starter site to the branch and pushes it, for an
// empty repository or one without a modules folder yet.
func (m *SiteManager) AddStarter(ctx context.Context) error {
	s := m.Settings()
	if !fsx.Exists(m.repoDir()) {
		return errors.New("connect a repository first")
	}
	if _, err := m.Fetch(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir("work"), 0o700); err != nil {
		return err
	}
	head, _ := m.git(ctx, m.repoDir(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+s.Repo.Branch)
	var work string
	if head != "" {
		work = m.dir("work", "starter-"+siteRandomToken(6))
		if _, err := m.git(ctx, m.repoDir(), "worktree", "add", "--detach", work, head); err != nil {
			return err
		}
		defer func() { _, _ = m.git(context.Background(), m.repoDir(), "worktree", "remove", "--force", work) }()
	} else {
		// An empty repository: a fresh repository pushed to it (works with any git version).
		dir, err := os.MkdirTemp(m.dir("work"), "starter-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		work = dir
		if _, err := m.git(ctx, work, "init", "-q", "-b", s.Repo.Branch); err != nil {
			return err
		}
		if _, err := m.git(ctx, work, "remote", "add", "origin", s.Repo.URL); err != nil {
			return err
		}
	}
	root := filepath.Join(work, filepath.FromSlash(s.Repo.Subdir))
	if fsx.Exists(filepath.Join(root, "modules")) {
		return errors.New("the repository already has a modules folder")
	}
	if err := copyEmbedded(site.FS, "template", root); err != nil {
		return err
	}
	if _, err := m.git(ctx, work, "add", "-A"); err != nil {
		return err
	}
	if _, err := m.git(ctx, work, "commit", "-q", "-m", "Start the site from Fridolin's starter"); err != nil {
		return err
	}
	if _, err := m.git(ctx, work, "push", "origin", "HEAD:refs/heads/"+s.Repo.Branch); err != nil {
		return err
	}
	_, err := m.Fetch(ctx)
	return err
}

func copyEmbedded(fsys fs.FS, root, dst string) error {
	return fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dst, strings.TrimPrefix(strings.TrimPrefix(p, root), "/"))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}

// ── what changed ────────────────────────────────────────────────────────────

// Fetch updates the clone and reports what the branch has beyond the approved commit.
func (m *SiteManager) Fetch(ctx context.Context) (sitesvc.SiteUpdates, error) {
	s := m.Settings()
	if !fsx.Exists(m.repoDir()) {
		return sitesvc.SiteUpdates{}, errors.New("no repository connected")
	}
	if _, err := m.git(ctx, m.repoDir(), "fetch", "--prune", "origin"); err != nil {
		return sitesvc.SiteUpdates{}, err
	}
	head, _ := m.git(ctx, m.repoDir(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+s.Repo.Branch)
	up := sitesvc.SiteUpdates{Head: head, Approved: s.Approved, Commits: []sitesvc.SiteCommit{}, Changes: []sitesvc.ModuleChange{}}
	if head == "" {
		return up, nil // empty repository
	}
	rng := head
	if s.Approved != "" {
		rng = s.Approved + ".." + head
	}
	out, err := m.git(ctx, m.repoDir(), "log", "--max-count=50", "--format=%H%x1f%s%x1f%an%x1f%aI", rng)
	if err != nil {
		return up, err
	}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Split(line, "\x1f"); len(f) == 4 {
			up.Commits = append(up.Commits, sitesvc.SiteCommit{Commit: f[0], Subject: f[1], Author: f[2], At: f[3]})
		}
	}
	if head != s.Approved {
		up.Changes, err = m.ModuleChanges(ctx, s.Approved, head)
	}
	return up, err
}

// ModuleChanges compares the modules of two commits ("" = nothing approved yet).
func (m *SiteManager) ModuleChanges(ctx context.Context, from, to string) ([]sitesvc.ModuleChange, error) {
	prefix := m.Settings().Repo.Subdir
	if prefix != "" {
		prefix += "/"
	}
	prefix += "modules/"
	var files []string
	if from == "" {
		out, err := m.git(ctx, m.repoDir(), "ls-tree", "-r", "--name-only", to, "--", prefix)
		if err != nil {
			return nil, err
		}
		files = strings.Fields(out)
	} else {
		out, err := m.git(ctx, m.repoDir(), "diff", "--name-only", from, to, "--", prefix)
		if err != nil {
			return nil, err
		}
		files = strings.Fields(out)
	}
	byID := map[string]*sitesvc.ModuleChange{}
	var order []string
	for _, f := range files {
		rest := strings.TrimPrefix(f, prefix)
		id, _, _ := strings.Cut(rest, "/")
		if id == "" || !strings.Contains(rest, "/") {
			continue
		}
		c := byID[id]
		if c == nil {
			c = &sitesvc.ModuleChange{ID: id, Review: []sitesvc.ReviewItem{}}
			byID[id] = c
			order = append(order, id)
		}
		c.Files = append(c.Files, rest)
	}
	slices.Sort(order)
	out := make([]sitesvc.ModuleChange, 0, len(order))
	for _, id := range order {
		c := byID[id]
		oldM := m.manifestAt(ctx, from, prefix+id+"/module.json")
		newM := m.manifestAt(ctx, to, prefix+id+"/module.json")
		switch {
		case oldM == nil && newM != nil:
			c.Status = "added"
		case oldM != nil && newM == nil:
			c.Status = "removed"
		default:
			c.Status = "changed"
		}
		c.Review = reviewManifest(oldM, newM)
		out = append(out, *c)
	}
	return out, nil
}

func (m *SiteManager) manifestAt(ctx context.Context, commit, file string) map[string]any {
	if commit == "" {
		return nil
	}
	data, err := m.git(ctx, m.repoDir(), "show", commit+":"+file)
	if err != nil {
		return nil
	}
	var v map[string]any
	if json.Unmarshal([]byte(data), &v) != nil {
		return map[string]any{"invalid": "module.json is not valid JSON"}
	}
	return v
}

// reviewManifest lists the manifest fields an approver must look at.
func reviewManifest(oldM, newM map[string]any) []sitesvc.ReviewItem {
	fields := []struct{ path, note string }{
		{"route", "where it is served"},
		{"access", "who may open it"},
		{"server", "runs code on this machine"},
		{"server.access", "who may call its server"},
		{"server.mount", "where its server answers"},
		{"capabilities.files", "files its code may use"},
		{"capabilities.commands", "programs its code runs"},
		{"capabilities.secrets", "secrets it needs"},
		{"capabilities.ingest", "brains it may hand files to"},
		{"capabilities.search", "brains whose folder it may search with a read-only Claude agent"},
		{"app.dist", "the files it serves"},
		{"app.public", "files anyone can fetch without signing in"},
		{"app.build", "a command Fridolin runs when deploying"},
		{"invalid", "the manifest does not parse"},
	}
	items := []sitesvc.ReviewItem{}
	for _, f := range fields {
		o, n := lookupPath(oldM, f.path), lookupPath(newM, f.path)
		if f.path == "server" {
			// Only whether there is server code, the details are listed separately.
			o, n = boolOrNil(o != nil), boolOrNil(n != nil)
		}
		if !reflect.DeepEqual(o, n) {
			items = append(items, sitesvc.ReviewItem{Field: f.path, Old: o, New: n, Note: f.note})
		}
	}
	return items
}

func boolOrNil(b bool) any {
	if b {
		return true
	}
	return nil
}

func lookupPath(m map[string]any, p string) any {
	var cur any = m
	for _, k := range strings.Split(p, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// ── deploying ───────────────────────────────────────────────────────────────

// SiteJob is one deploy in progress (or the last one).
type SiteJob struct {
	mu       sync.Mutex
	Commit   string   `json:"commit"`
	State    string   `json:"state"` // running, done, error
	Step     string   `json:"step"`
	Error    string   `json:"error,omitempty"`
	Log      []string `json:"log"`
	Started  string   `json:"started"`
	Finished string   `json:"finished,omitempty"`
}

func (j *SiteJob) step(format string, args ...any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Step = fmt.Sprintf(format, args...)
	j.Log = append(j.Log, j.Step)
}

func (j *SiteJob) snapshot() *sitesvc.DeploySnapshot {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return &sitesvc.DeploySnapshot{Commit: j.Commit, State: j.State, Step: j.Step, Error: j.Error, Log: slices.Clone(j.Log), Started: j.Started, Finished: j.Finished}
}

// DeployStatus returns the running or last deploy.
func (m *SiteManager) DeployStatus() *sitesvc.DeploySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deploy.snapshot()
}

// Deploy approves a commit of the branch (or one deployed before, for a
// rollback) and makes it live. It runs in the background; DeployStatus shows
// how it goes. The site keeps serving the previous commit until the new one
// is built.
func (m *SiteManager) Deploy(ctx context.Context, commit string) error {
	commit = strings.TrimSpace(commit)
	if !siteCommitRE.MatchString(commit) {
		return errors.New("name a commit by its full id")
	}
	s := m.Settings()
	if !fsx.Exists(m.repoDir()) {
		return errors.New("no repository connected")
	}
	known := slices.ContainsFunc(s.History, func(d sitesvc.SiteDeploy) bool { return d.Commit == commit })
	if !known {
		// Only what is on the branch can be approved: not a stray commit or another branch.
		if _, err := m.git(ctx, m.repoDir(), "merge-base", "--is-ancestor", commit, "refs/remotes/origin/"+s.Repo.Branch); err != nil {
			return fmt.Errorf("%s is not on the %s branch", commit[:12], s.Repo.Branch)
		}
	}
	m.mu.Lock()
	if m.deploy != nil && m.deploy.snapshot().State == "running" {
		m.mu.Unlock()
		return errors.New("a deploy is already running")
	}
	job := &SiteJob{Commit: commit, State: "running", Started: time.Now().UTC().Format(time.RFC3339), Log: []string{}}
	m.deploy = job
	m.mu.Unlock()

	go func() {
		err := m.runDeploy(ctx, job, commit)
		job.mu.Lock()
		job.Finished = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			job.State, job.Error = "error", err.Error()
			log.Printf("[site] deploy of %s failed: %v", commit[:12], err)
		} else {
			job.State = "done"
		}
		job.mu.Unlock()
	}()
	return nil
}

func (m *SiteManager) runDeploy(ctx context.Context, job *SiteJob, commit string) error {
	dir := m.deployDir(commit)
	if err := m.prepareCheckout(ctx, job, commit, dir); err != nil {
		return err
	}
	subject, _ := m.git(ctx, m.repoDir(), "log", "-1", "--format=%s", commit)
	m.mu.Lock()
	m.settings.Approved = commit
	m.settings.History = slices.DeleteFunc(m.settings.History, func(d sitesvc.SiteDeploy) bool { return d.Commit == commit })
	m.settings.History = append(m.settings.History, sitesvc.SiteDeploy{Commit: commit, Subject: subject, At: time.Now().UTC().Format(time.RFC3339)})
	if len(m.settings.History) > 30 {
		m.settings.History = m.settings.History[len(m.settings.History)-30:]
	}
	err := m.saveSettingsLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	job.step("Restarting the site on %s", commit[:12])
	m.Restart()
	m.pruneDeploys(ctx)
	job.step("Live: %s", subject)
	return nil
}

// prepareCheckout checks a commit out into dir, installs its dependencies and
// builds its modules. A folder from an earlier deploy of the same commit is reused.
func (m *SiteManager) prepareCheckout(ctx context.Context, job *SiteJob, commit, dir string) error {
	ready := filepath.Join(dir, ".fridolin-ready")
	if fsx.Exists(ready) {
		job.step("Reusing the build of %s", commit[:12])
		return nil
	}
	if fsx.Exists(dir) {
		_, _ = m.git(ctx, m.repoDir(), "worktree", "remove", "--force", dir)
	}
	job.step("Checking out %s", commit[:12])
	if _, err := m.git(ctx, m.repoDir(), "worktree", "add", "--force", "--detach", dir, commit); err != nil {
		return err
	}
	root := filepath.Join(dir, filepath.FromSlash(m.Settings().Repo.Subdir))
	if fsx.Exists(filepath.Join(root, "package.json")) {
		job.step("Installing dependencies (npm)")
		if err := m.npmInstall(ctx, root, false); err != nil {
			return err
		}
	}
	modulesDir := filepath.Join(root, "modules")
	entries, _ := os.ReadDir(modulesDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(modulesDir, e.Name(), "module.json"))
		if err != nil {
			continue
		}
		var mf struct {
			App *struct {
				Build *struct {
					Cwd string   `json:"cwd"`
					Run []string `json:"run"`
				} `json:"build"`
			} `json:"app"`
		}
		if json.Unmarshal(data, &mf) != nil || mf.App == nil || mf.App.Build == nil || len(mf.App.Build.Run) == 0 {
			continue
		}
		job.step("Building %s: %s", e.Name(), strings.Join(mf.App.Build.Run, " "))
		if err := m.runBuild(ctx, filepath.Join(modulesDir, e.Name()), mf.App.Build.Cwd, mf.App.Build.Run); err != nil {
			return fmt.Errorf("building %s: %w", e.Name(), err)
		}
	}
	return os.WriteFile(ready, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// runBuild runs a module's app.build command (paths as in docs/SITE.md: ~/…
// is the home folder, a relative path is inside the module folder).
func (m *SiteManager) runBuild(ctx context.Context, moduleDir, cwd string, run []string) error {
	dir := moduleDir
	switch cwd = fsx.ExpandHome(cwd, m.host.Home()); {
	case filepath.IsAbs(cwd):
		dir = cwd
	case cwd != "":
		dir = filepath.Join(moduleDir, cwd)
	}
	node, _ := m.host.Node()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	bin := run[0]
	if !strings.Contains(bin, "/") {
		if p, err := m.host.Tool(bin); err == nil {
			bin = p
		}
	}
	cmd := exec.CommandContext(ctx, bin, run[1:]...)
	cmd.Dir = dir
	cmd.Env = m.host.Env(node)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v\n%s", err, textx.Tail(string(out), 1500))
	}
	return nil
}

// pruneDeploys removes deploy folders except the live one and the last few.
func (m *SiteManager) pruneDeploys(ctx context.Context) {
	s := m.Settings()
	keep := map[string]bool{}
	for i := len(s.History) - 1; i >= 0 && len(keep) < siteKeepDeploys; i-- {
		keep[s.History[i].Commit[:12]] = true
	}
	if s.Approved != "" {
		keep[s.Approved[:12]] = true
	}
	entries, _ := os.ReadDir(m.dir("deploys"))
	for _, e := range entries {
		if !e.IsDir() || keep[e.Name()] || !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(e.Name()) {
			continue
		}
		if _, err := m.git(ctx, m.repoDir(), "worktree", "remove", "--force", m.dir("deploys", e.Name())); err != nil {
			log.Printf("[site] could not remove the old deploy %s: %v", e.Name(), err)
		}
	}
	_, _ = m.git(ctx, m.repoDir(), "worktree", "prune")
}

// sitePreview is a site process serving a branch that is not approved yet.
type sitePreview struct {
	Branch string
	Listen string
	// Token opens the preview: it knows no accounts.
	Token string
	proc  *siteProc
}
