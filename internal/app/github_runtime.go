package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/ghapp"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// githubService is the bridge's GitHub App (docs/GITHUB.md): the manifest
// flow that makes it, its status for the dashboard, and the tokens runs
// get.
type githubService struct {
	client *ghapp.Client
	// web is GitHub's site, for the manifest form and the links.
	web     string
	secrets githubSecrets
	log     *slog.Logger
	now     func() time.Time

	mu      sync.Mutex
	pending map[string]githubPending
	// status and installs are what GitHub said last, until statusTTL.
	status   *httpapi.GitHubStatus
	installs []ghapp.Installation
	readAt   time.Time
	// frideTok is the installation token the nodes' FrIDEs have now
	// (FrideToken), until it nears its end.
	frideTok ghapp.Token
	// onChange hears that what the FrIDEs get from GitHub changed; nil
	// for nothing.
	onChange func()
}

// githubPending is a manifest flow started from the dashboard.
type githubPending struct {
	created  time.Time
	name     string
	org      string
	manifest []byte
}

const (
	// githubStateTTL is how long a started flow waits: GitHub's code is
	// good for an hour.
	githubStateTTL = time.Hour
	// githubStatusTTL is how long the dashboard's status is reused before
	// GitHub is asked again.
	githubStatusTTL = time.Minute
)

// githubHome is the app's homepage, which GitHub requires: the module's
// repository, from the build.
func githubHome() string {
	if bi, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(bi.Main.Path, "github.com/") {
		return "https://" + bi.Main.Path
	}
	return ghapp.DefaultWeb
}

func newGitHubService(cfgs *config.Store, env Environment, container bool, logger *slog.Logger) *githubService {
	return &githubService{
		client: &ghapp.Client{},
		web:    ghapp.DefaultWeb,
		secrets: githubSecrets{getenv: func(name string) string { return getenv(env, name) }, store: githubHostStore(container),
			secretsDir: cfgs.Boot().Layout().Secrets()},
		log: logger, now: time.Now,
		pending: map[string]githubPending{},
	}
}

func (g *githubService) forgetStatus() {
	g.mu.Lock()
	g.status, g.installs = nil, nil
	g.mu.Unlock()
}

// githubFrideReuse is how long a FrIDE token must still last to be handed
// out again; the node asks for a new one sooner than that before the end
// (nodeagent identitiesRefreshBefore), so it gets a fresh one.
const githubFrideReuse = 30 * time.Minute

// FrideToken is the GitHub credential the nodes' FrIDEs get (docs/NODES.md,
// "Git identities for FrIDE"). With the app: an installation token of its
// installation on the app's owner (else the first one), for all its
// repositories and permissions, handed out again while it lasts more
// than githubFrideReuse. The installation must let it write code, else
// FrIDE could not push with it and gets none, so that its own git login
// keeps working. Without the app: the typed token as it is, which does
// not end.
func (g *githubService) FrideToken(ctx context.Context) (string, time.Time, error) {
	creds, ok, err := g.secrets.load()
	if err != nil {
		return "", time.Time{}, err
	}
	if !ok {
		if t := g.secrets.typedToken(); t != "" {
			return t, time.Time{}, nil
		}
		return "", time.Time{}, errNoGitHub
	}
	g.mu.Lock()
	cached := g.frideTok
	g.mu.Unlock()
	if cached.Value != "" && cached.ExpiresAt.Sub(g.now()) > githubFrideReuse {
		return cached.Value, cached.ExpiresAt, nil
	}
	insts, err := g.readInstallations(ctx, creds)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("asking GitHub where the app is installed: %w", err)
	}
	insts = slices.DeleteFunc(slices.Clone(insts), func(in ghapp.Installation) bool { return in.Suspended })
	if len(insts) == 0 {
		return "", time.Time{}, fmt.Errorf("the GitHub app %s is installed nowhere: Integrations → GitHub → Install", creds.Slug)
	}
	in := insts[0]
	if i := slices.IndexFunc(insts, func(in ghapp.Installation) bool { return strings.EqualFold(in.Account, creds.Owner) }); i >= 0 {
		in = insts[i]
	}
	if in.Permissions["contents"] != "write" {
		return "", time.Time{}, fmt.Errorf("the GitHub app %s may only read code on %s, so FrIDE could not push with its token: give it Contents: Read and write in its settings on GitHub and accept that on the installation", creds.Slug, in.Account)
	}
	tok, err := g.client.Mint(ctx, creds, in.ID, nil, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("minting a GitHub token for FrIDE: %w", err)
	}
	if tok.ExpiresAt.IsZero() {
		tok.ExpiresAt = g.now().Add(time.Hour)
	}
	g.mu.Lock()
	g.frideTok = tok
	g.mu.Unlock()
	return tok.Value, tok.ExpiresAt, nil
}

// forgetFrideToken drops the FrIDE token: the app it is of went.
func (g *githubService) forgetFrideToken() {
	g.mu.Lock()
	g.frideTok = ghapp.Token{}
	g.mu.Unlock()
}

// Start makes the one-time state of a new app whose redirects go back to
// origin, and returns the hand-off page's path.
func (g *githubService) Start(origin, org string, pullRequests bool) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.Path != "" {
		return "", errors.New("the dashboard's address is not one GitHub can send the browser back to")
	}
	if org = strings.TrimSpace(org); org != "" {
		if err := ghapp.CheckAccount(org); err != nil {
			return "", fmt.Errorf("organization %q: %w", org, err)
		}
	}
	name := ghapp.NewName()
	manifest, err := ghapp.Manifest(ghapp.ManifestOptions{Name: name, HomeURL: githubHome(),
		RedirectURL: origin + "/github/created", SetupURL: origin + "/github/installed", PullRequests: pullRequests})
	if err != nil {
		return "", err
	}
	state := randomToken(16)
	g.mu.Lock()
	defer g.mu.Unlock()
	for s, p := range g.pending {
		if g.now().Sub(p.created) > githubStateTTL {
			delete(g.pending, s)
		}
	}
	if len(g.pending) >= 16 {
		return "", errors.New("too many GitHub sign-ups are waiting; finish one or wait an hour")
	}
	g.pending[state] = githubPending{created: g.now(), name: name, org: org, manifest: manifest}
	return "/github/connect?state=" + url.QueryEscape(state), nil
}

func (g *githubService) pendingFor(state string) (githubPending, bool) {
	p, ok := g.pending[state]
	if !ok || state == "" || g.now().Sub(p.created) > githubStateTTL {
		return githubPending{}, false
	}
	return p, true
}

// Handoff is the form for state. It stays usable until GitHub returns,
// so a reload of the page works.
func (g *githubService) Handoff(state string) (httpapi.GitHubHandoff, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pendingFor(state)
	if !ok {
		return httpapi.GitHubHandoff{}, httpapi.ErrGitHubState
	}
	return httpapi.GitHubHandoff{Action: ghapp.NewAppURL(g.web, p.org, state), Manifest: string(p.manifest), Name: p.name}, nil
}

// Created takes GitHub's code for state, once, keeps the new app's
// credentials and sends the browser on to install it.
func (g *githubService) Created(ctx context.Context, code, state string) (string, error) {
	g.mu.Lock()
	_, ok := g.pendingFor(state)
	delete(g.pending, state)
	g.mu.Unlock()
	if !ok {
		return "", httpapi.ErrGitHubState
	}
	creds, err := g.client.Convert(ctx, code)
	if err != nil {
		return "", fmt.Errorf("GitHub made the app, but handing over its key failed: %w. Delete the app on GitHub (Settings → Developer settings → GitHub Apps) and connect again", err)
	}
	if err := g.secrets.save(creds); err != nil {
		return "", fmt.Errorf("keeping the GitHub app's key failed: %w. Delete the app %s on GitHub and connect again", err, creds.Slug)
	}
	g.forgetStatus()
	g.changed()
	g.log.Info("github: connected", "app", creds.Slug, "owner", creds.Owner, "kept_in", g.secrets.where())
	return ghapp.InstallURL(g.web, creds.Slug), nil
}

func (g *githubService) Installed() {
	g.forgetStatus()
	g.changed()
}

// changed drops the FrIDE token and tells onChange that what the FrIDEs
// get changed: an app came, was installed elsewhere, or went.
func (g *githubService) changed() {
	g.forgetFrideToken()
	if g.onChange != nil {
		go g.onChange()
	}
}

// readInstallations asks GitHub where the app is installed, or reuses
// what Status read within the minute.
func (g *githubService) readInstallations(ctx context.Context, creds ghapp.Credentials) ([]ghapp.Installation, error) {
	g.mu.Lock()
	insts, fresh := g.installs, g.installs != nil && g.now().Sub(g.readAt) < githubStatusTTL
	g.mu.Unlock()
	if fresh {
		return insts, nil
	}
	return g.client.Installations(ctx, creds)
}

// Status is what the dashboard shows. It asks GitHub at most once a
// minute.
func (g *githubService) Status(ctx context.Context) httpapi.GitHubStatus {
	g.mu.Lock()
	cached := g.status
	if cached != nil && g.now().Sub(g.readAt) >= githubStatusTTL {
		cached = nil
	}
	g.mu.Unlock()
	if cached != nil {
		return *cached
	}
	st := httpapi.GitHubStatus{Installations: []httpapi.GitHubInstallation{}, TypedToken: g.secrets.typedToken() != ""}
	creds, ok, err := g.secrets.load()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if !ok {
		return st
	}
	st.Connected, st.Where = true, g.secrets.where()
	st.App = &httpapi.GitHubAppInfo{ID: creds.ID, Slug: creds.Slug, Name: creds.Name, Owner: creds.Owner, HTMLURL: creds.HTMLURL}
	st.InstallURL, st.SettingsURL, st.DeleteURL = ghapp.InstallURL(g.web, creds.Slug), ghapp.SettingsURL(g.web, creds), ghapp.DeleteURL(g.web, creds)
	insts, err := g.client.Installations(ctx, creds)
	if err != nil {
		st.Error = "asking GitHub where the app is installed: " + err.Error()
		return st
	}
	for _, in := range insts {
		row := httpapi.GitHubInstallation{ID: in.ID, Account: in.Account, HTMLURL: in.HTMLURL, RepositorySelection: in.RepositorySelection,
			Permissions: in.Permissions, Suspended: in.Suspended, Repos: []string{}}
		if !in.Suspended {
			// A token that may only read metadata, for the list, revoked at once.
			tok, err := g.client.Mint(ctx, creds, in.ID, nil, map[string]string{"metadata": "read"})
			if err == nil {
				repos, rerr := g.client.Repositories(ctx, tok.Value)
				g.revoke(tok)
				if rerr == nil {
					row.Repos = repos
				}
				err = rerr
			}
			if err != nil && st.Error == "" {
				st.Error = "reading the repositories of " + in.Account + ": " + err.Error()
			}
		}
		st.Installations = append(st.Installations, row)
	}
	g.mu.Lock()
	if insts == nil {
		insts = []ghapp.Installation{}
	}
	g.status, g.installs, g.readAt = &st, insts, g.now()
	g.mu.Unlock()
	return st
}

// revoke ends a token, on a context of its own: the use that had it may
// be over, its context with it.
func (g *githubService) revoke(t ghapp.Token) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := g.client.Revoke(ctx, t.Value); err != nil {
		g.log.Warn("github: revoking a token failed; it ends by itself within the hour", "repos", strings.Join(t.Repos, ","), "err", err)
	}
}

// defaultGitHubPermissions are a grant's when it names none.
var defaultGitHubPermissions = map[string]string{"contents": "read", "metadata": "read"}

// errNoGitHub is a grant with no app and no typed token.
var errNoGitHub = errors.New("GitHub is not connected (Integrations → GitHub) and there is no typed token")

// Mint makes the token of grant: an installation token for exactly its
// repositories and permissions, and the function that revokes it. Without
// an app, a typed token is the second choice, which cannot be narrowed
// and is not revoked.
func (g *githubService) Mint(ctx context.Context, grant chat.GitHubGrant) (string, func(), error) {
	if len(grant.Repos) == 0 {
		return "", nil, errors.New("a GitHub grant names no repository")
	}
	owner := ""
	for _, r := range grant.Repos {
		o, _, err := ghapp.SplitRepo(r)
		if err != nil {
			return "", nil, err
		}
		if owner != "" && !strings.EqualFold(o, owner) {
			return "", nil, errors.New("a GitHub grant's repositories must all be of one owner: one token is of one installation")
		}
		owner = o
	}
	perms := grant.Permissions
	if len(perms) == 0 {
		perms = defaultGitHubPermissions
	}
	if err := ghapp.CheckPermissions(perms); err != nil {
		return "", nil, err
	}
	creds, ok, err := g.secrets.load()
	if err != nil {
		return "", nil, err
	}
	if !ok {
		if t := g.secrets.typedToken(); t != "" {
			return t, func() {}, nil
		}
		return "", nil, errNoGitHub
	}
	insts, err := g.readInstallations(ctx, creds)
	if err != nil {
		return "", nil, fmt.Errorf("asking GitHub where the app is installed: %w", err)
	}
	i := slices.IndexFunc(insts, func(in ghapp.Installation) bool { return strings.EqualFold(in.Account, owner) && !in.Suspended })
	if i < 0 {
		return "", nil, fmt.Errorf("the GitHub app %s is not installed on %s: Integrations → GitHub → Install on more repositories", creds.Slug, owner)
	}
	tok, err := g.client.Mint(ctx, creds, insts[i].ID, grant.Repos, perms)
	if err != nil {
		return "", nil, fmt.Errorf("minting a GitHub token for %s: %w", strings.Join(grant.Repos, ", "), err)
	}
	return tok.Value, sync.OnceFunc(func() { g.revoke(tok) }), nil
}

// Test mints a token that may only read repo's issues, reads the newest,
// and revokes the token: the dashboard's proof that it all works.
func (g *githubService) Test(ctx context.Context, repo string) (httpapi.GitHubTestResult, error) {
	creds, ok, err := g.secrets.load()
	if err != nil {
		return httpapi.GitHubTestResult{}, err
	}
	if !ok {
		return httpapi.GitHubTestResult{}, httpapi.ErrGitHubNotConnected
	}
	if repo = strings.TrimSpace(repo); repo == "" {
		st := g.Status(ctx)
		for _, in := range st.Installations {
			if len(in.Repos) > 0 {
				repo = in.Repos[0]
				break
			}
		}
		if repo == "" {
			return httpapi.GitHubTestResult{}, fmt.Errorf("the GitHub app %s is installed on no repository yet", creds.Slug)
		}
	}
	perms := map[string]string{"issues": "read"}
	tok, done, err := g.Mint(ctx, chat.GitHubGrant{Repos: []string{repo}, Permissions: perms})
	if err != nil {
		return httpapi.GitHubTestResult{}, err
	}
	issue, err := g.client.LatestIssue(ctx, tok, repo)
	done()
	if err != nil {
		return httpapi.GitHubTestResult{}, fmt.Errorf("reading an issue of %s: %w", repo, err)
	}
	res := httpapi.GitHubTestResult{Repo: repo, Permissions: perms, Revoked: true}
	if issue != nil {
		res.Issue = &httpapi.GitHubIssue{Number: issue.Number, Title: issue.Title, URL: issue.URL}
	}
	return res, nil
}

// Disconnect forgets the app's key here. The app stays on GitHub until
// the owner deletes it there, at DeleteURL.
func (g *githubService) Disconnect() (httpapi.GitHubDisconnected, error) {
	creds, ok, err := g.secrets.load()
	if err != nil && !ok {
		// A key that no longer opens is forgotten all the same.
		g.log.Warn("github: forgetting an app whose key does not open", "err", err)
	}
	if err := g.secrets.forget(); err != nil {
		return httpapi.GitHubDisconnected{}, err
	}
	g.forgetStatus()
	g.changed()
	res := httpapi.GitHubDisconnected{Status: "disconnected"}
	if ok {
		res.DeleteURL = ghapp.DeleteURL(g.web, creds)
		g.log.Info("github: disconnected; the app stays on GitHub until deleted there", "app", creds.Slug)
	}
	return res, nil
}

// withGitHubToken mints the token of a run's grant and hands it to the
// run as gh's own login: hosts.yml in a folder of its own, beside the
// run's brain API token (run-tokens/<brain>/run-*.gh), which only that
// brain may read, and GH_CONFIG_DIR naming it. The token stays out of the
// run's environment, which macOS shows to the user's other processes,
// other brains' runs among them (TestRunTokenStaysOutOfTheEnvironment).
// gh reads it from there, and git through gh auth git-credential. Done
// revokes it and removes the folder once the run is over. A token that
// cannot be minted leaves the run without one, and the log says why.
func (b *App) withGitHubToken(access runAccess, brain string, grant chat.GitHubGrant) runAccess {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, done, err := b.github.Mint(ctx, grant)
	if err != nil {
		b.logger.Warn("github: no token for this run", "brain", brain, "repos", strings.Join(grant.Repos, ","), "err", err)
		return access
	}
	dir, err := writeGitHubLogin(runTokenDir(b.cfgs.Boot().DataDir, brain), tok)
	if err != nil {
		done()
		b.logger.Warn("github: no token for this run", "brain", brain, "err", err)
		return access
	}
	access.Env = append(access.Env, "GH_CONFIG_DIR="+dir, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	prev := access.Done
	access.Done = func() {
		done()
		if err := os.RemoveAll(dir); err != nil {
			b.logger.Warn("github: removing the run's gh login", "brain", brain, "err", err)
		}
		prev()
	}
	return access
}

// writeGitHubLogin writes gh's hosts.yml with tok into a new folder in
// dir, readable by the bridge's user only, and returns the folder.
func writeGitHubLogin(dir, tok string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	gh, err := os.MkdirTemp(dir, "run-*.gh")
	if err != nil {
		return "", err
	}
	// The user an installation token acts as; gh only shows it.
	const user = "x-access-token"
	hosts := "github.com:\n" +
		"    users:\n" +
		"        " + user + ":\n" +
		"            oauth_token: " + tok + "\n" +
		"    git_protocol: https\n" +
		"    oauth_token: " + tok + "\n" +
		"    user: " + user + "\n"
	// version 1 is gh's config of more than one account, which hosts.yml
	// is written as: without it gh migrates the folder, which asks GitHub
	// for the user and fails for an installation token.
	for name, data := range map[string]string{"hosts.yml": hosts, "config.yml": "version: \"1\"\n"} {
		if err := os.WriteFile(filepath.Join(gh, name), []byte(data), 0o600); err != nil {
			_ = os.RemoveAll(gh)
			return "", err
		}
	}
	return gh, nil
}
