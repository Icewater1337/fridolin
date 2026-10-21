package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// Adding a repository to FrIDE on the node (docs/NODES.md, "FrIDE"):
// fride.repo_clone clones one into FrIDE's base folder, fride.repo_new
// makes one there (git init, an initial commit, and with github gh repo
// create --source . --push). Both stay inside the base folder: the name is
// one folder name, git and gh are run by absolute path without a shell,
// each step has a timeout and its output is capped. FrIDE then reads its
// base folders again (POST /api/config, what its Settings page's Re-check
// does), so the repository is one it can work in at once.

// Bounds of the repository steps.
const (
	repoCloneTimeout = 10 * time.Minute
	repoStepTimeout  = time.Minute
	repoGHTimeout    = 3 * time.Minute
	repoOutputCap    = 16 << 10
	maxCloneURL      = 2048
)

// repoNameRE is a repository's folder name: no separators, no dot first.
var repoNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// scpURLRE is git's scp-like address, user@host:path.
var scpURLRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9.-]*:[A-Za-z0-9][A-Za-z0-9._~/-]*$`)

// checkRepoName reports what is wrong with a repository's name.
func checkRepoName(name string) error {
	if !repoNameRE.MatchString(name) {
		return fmt.Errorf("repository name %q: letters, digits, '.', '_' and '-', up to 100, not starting with '.' or '-'", name)
	}
	return nil
}

// checkCloneURL reports what is wrong with a URL to clone: an https or ssh
// URL with a host and a path, or git's user@host:path, and nothing git
// would read as an option, a local path or another transport.
func checkCloneURL(u string) error {
	if u == "" || len(u) > maxCloneURL || strings.HasPrefix(u, "-") || strings.ContainsAny(u, " \t\r\n\x00\\") {
		return fmt.Errorf("the URL %q is not one to clone", u)
	}
	if !strings.Contains(u, "://") {
		if scpURLRE.MatchString(u) {
			return nil
		}
		return fmt.Errorf("the URL %q is not one to clone: an https:// or ssh:// URL, or user@host:path", u)
	}
	p, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("the URL %q: %v", u, err)
	}
	if p.Scheme != "https" && p.Scheme != "ssh" {
		return fmt.Errorf("the URL %q: only https and ssh are cloned", u)
	}
	if p.Hostname() == "" || strings.HasPrefix(p.Host, "-") || strings.Trim(p.Path, "/") == "" {
		return fmt.Errorf("the URL %q has no host or no path", u)
	}
	if p.User != nil {
		if _, ok := p.User.Password(); ok || p.Scheme == "https" {
			return errors.New("the URL holds credentials: leave them out, the node's own git credentials are used")
		}
	}
	return nil
}

// checkBaseDir is dir, ~ expanded, as an absolute path with its symlinks
// resolved, which must be a folder.
func checkBaseDir(dir string) (string, error) {
	abs, err := filepath.Abs(platform.ExpandHome(dir))
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder", dir)
	}
	return real, nil
}

// frideBaseFolders are FrIDE's base folders, from its settings (the API
// its UI uses), as checkBaseDir has them; those that are no folder are
// left out.
func frideBaseFolders(ctx context.Context, cfg *FrideConfig, token string) ([]string, error) {
	var s struct {
		BaseFolders []string `json:"baseFolders"`
	}
	if err := frideDo(ctx, cfg.URL, token, http.MethodGet, "/api/settings", nil, &s); err != nil {
		return nil, err
	}
	var out []string
	for _, b := range s.BaseFolders {
		if d, err := checkBaseDir(b); err == nil && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out, nil
}

// frideRepoBase is the folder repositories are added to: node.json's
// fride.base, which must be one of FrIDE's base folders while FrIDE says
// which those are, else FrIDE's only one.
func frideRepoBase(ctx context.Context, cfg *FrideConfig, token string) (string, *nodeproto.Error) {
	folders, ferr := frideBaseFolders(ctx, cfg, token)
	if cfg.Base != "" {
		base, err := checkBaseDir(cfg.Base)
		if err != nil {
			return "", nodeproto.Errorf(nodeproto.CodeFailed, "fride.base in node.json: %v", err)
		}
		if ferr == nil && !slices.Contains(folders, base) {
			return "", nodeproto.Errorf(nodeproto.CodeFailed, "FrIDE does not scan %s (its base folders: %s): add it to FrIDE's baseFolders, or pair again with --base naming one of them", base, orNoneList(folders))
		}
		return base, nil
	}
	if ferr != nil {
		return "", nodeproto.Errorf(nodeproto.CodeUnavailable, "reading FrIDE's base folders: %v", ferr)
	}
	switch len(folders) {
	case 0:
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "FrIDE has no base folder to add a repository to")
	case 1:
		return folders[0], nil
	}
	return "", nodeproto.Errorf(nodeproto.CodeFailed, "FrIDE has several base folders (%s): name the one for new repositories with fridolin node fride pair --base DIR", strings.Join(folders, ", "))
}

func orNoneList(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}

// repoTool is the absolute path of git or gh.
func repoTool(name string) (string, *nodeproto.Error) {
	p, err := platform.LookTool(name)
	if err != nil {
		return "", nodeproto.Errorf(nodeproto.CodeNotFound, "%s is not installed on this node: %v", name, err)
	}
	return p, nil
}

// runRepoTool runs prog (an absolute path) with args in dir, without a
// shell, prompts or more than timeout, and returns its output, capped.
func runRepoTool(ctx context.Context, timeout time.Duration, dir, prog string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// prog is git or gh, found by repoTool; args are fixed here but for a
	// name checkRepoName passed and a URL checkCloneURL passed, which
	// follows "--" where git would otherwise read options.
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Dir = dir
	cmd.Env = platform.EnvWith("GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "GCM_INTERACTIVE=never", "GH_NO_UPDATE_NOTIFIER=1")
	out := &cappedBuffer{max: repoOutputCap}
	cmd.Stdout, cmd.Stderr = out, out
	platform.IsolateProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	text := strings.TrimSpace(out.buf.String())
	if err == nil {
		return text, nil
	}
	what := filepath.Base(prog) + " " + args[0]
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return text, fmt.Errorf("%s took longer than %v", what, timeout)
	}
	if text != "" {
		return text, fmt.Errorf("%s: %v: %s", what, err, clean(text, 2000))
	}
	return text, fmt.Errorf("%s: %v", what, err)
}

// frideRepo serves fride.repo_clone and fride.repo_new.
func (a *Agent) frideRepo(ctx context.Context, method string, cfg *FrideConfig, token string, p nodeproto.FrideRepoParams) (any, *nodeproto.Error) {
	p.Name, p.URL = strings.TrimSpace(p.Name), strings.TrimSpace(p.URL)
	if err := checkRepoName(p.Name); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	clone := method == nodeproto.MethodFrideRepoClone
	if clone {
		if err := checkCloneURL(p.URL); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
		}
	} else if p.URL != "" {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a new repository takes no URL")
	}
	pctx, cancel := context.WithTimeout(ctx, frideCallTimeout)
	base, perr := frideRepoBase(pctx, cfg, token)
	cancel()
	if perr != nil {
		return nil, perr
	}
	git, perr := repoTool("git")
	if perr != nil {
		return nil, perr
	}
	dest := filepath.Join(base, p.Name)
	if filepath.Dir(dest) != base {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "repository name %q", p.Name)
	}
	// Mkdir, not MkdirAll: the folder is this call's own, or the call is
	// refused; so removing it after a failure removes only what it made.
	if err := os.Mkdir(dest, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s already exists in %s", p.Name, base)
		}
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	out := nodeproto.FrideRepo{Name: p.Name, Path: dest}
	var err error
	if clone {
		a.logf("node: cloning a repository into %s for FrIDE", dest)
		_, err = runRepoTool(ctx, repoCloneTimeout, base, git, "clone", "--quiet", "--", p.URL, dest)
	} else {
		a.logf("node: making the repository %s for FrIDE", dest)
		err = initRepo(ctx, git, dest, p.Name)
	}
	if err != nil {
		if rerr := os.RemoveAll(dest); rerr != nil {
			a.logf("node: removing %s after a failure: %v", dest, rerr)
		}
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	if !clone && p.GitHub {
		out.GitHubURL, out.Warning = githubCreate(ctx, dest, p.Name, p.Public)
		if out.Warning != "" {
			a.logf("node: %s", out.Warning)
		}
	}
	out.Repos, out.Rescanned = a.frideRescan(ctx, cfg, token)
	return out, nil
}

// initRepo makes dest a git repository on main with one commit, a README.
func initRepo(ctx context.Context, git, dest, name string) error {
	if _, err := runRepoTool(ctx, repoStepTimeout, dest, git, "init", "--quiet", "--initial-branch=main"); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dest, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "--", "README.md"}, {"commit", "--quiet", "--no-verify", "-m", "Initial commit"}} {
		if _, err := runRepoTool(ctx, repoStepTimeout, dest, git, args...); err != nil {
			return err
		}
	}
	return nil
}

// githubCreate creates name on GitHub with gh, from dest, and pushes it.
// It returns the repository's URL, or why that did not work.
func githubCreate(ctx context.Context, dest, name string, public bool) (ghURL, warning string) {
	gh, perr := repoTool("gh")
	if perr != nil {
		return "", fmt.Sprintf("%s was made, but not on GitHub: %s", name, perr.Message)
	}
	vis := "--private"
	if public {
		vis = "--public"
	}
	text, err := runRepoTool(ctx, repoGHTimeout, dest, gh, "repo", "create", name, vis, "--source", ".", "--remote", "origin", "--push")
	if err != nil {
		return "", fmt.Sprintf("%s was made, but not on GitHub: %v", name, err)
	}
	for f := range strings.FieldsSeq(text) {
		if strings.HasPrefix(f, "https://") {
			return strings.TrimRight(f, ".,"), ""
		}
	}
	return "", ""
}

// frideRescan has FrIDE read its base folders again and tells the core
// what the node offers since. It reports the repositories FrIDE knows
// then, and whether it rescanned.
func (a *Agent) frideRescan(ctx context.Context, cfg *FrideConfig, token string) ([]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, frideCallTimeout)
	defer cancel()
	if err := frideDo(ctx, cfg.URL, token, http.MethodPost, "/api/config", nil, nil); err != nil {
		a.logf("node: asking FrIDE to read its base folders again: %v", err)
		return nil, false
	}
	info, err := ProbeFride(ctx, cfg.URL, token)
	if err != nil {
		return nil, true
	}
	if a.frideCheck(ctx) {
		select {
		case a.fride.changed <- struct{}{}:
		default:
		}
	}
	return info.Repos, true
}
