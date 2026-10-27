package app

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/gitident"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The git identities of the nodes' FrIDEs (docs/CONNECTIONS.md, "GitLab";
// docs/NODES.md, "Git identities for FrIDE"): the GitLab connection and
// the commit names, kept on the machines (internal/gitident), the
// dashboard's routes, and the hand-over to the nodes
// (git_identities_push.go).

// gitIdentityService holds them.
type gitIdentityService struct {
	b     *App
	store gitident.Store
	// client is the GitLab check's; nil is a plain one.
	client *http.Client
	// timeout bounds one node's hand-over.
	timeout time.Duration

	mu sync.Mutex
	// nodes is what each node's FrIDE took last, by node id.
	nodes map[string]*gitIDNode
	// githubNote says why the FrIDEs got no GitHub identity the last
	// time the core tried; ghSet whether GitHub is connected, as the
	// secret store said at ghAt.
	githubNote string
	ghSet      bool
	ghAt       time.Time
	// pending are the nodes to hand them to (true: even when unchanged);
	// all says every node. One hand-over runs at a time.
	pending map[string]bool
	all     bool
	running bool
	idle    chan struct{}
}

// gitIDNode is what one node's FrIDE took last.
type gitIDNode struct {
	sent      string
	got       []string
	at        time.Time
	supported bool
	pending   bool
	err       string
	held      string
}

// say is what the log says of it: the same twice is said once.
func (n *gitIDNode) say() string {
	return n.err + "\x00" + n.held + "\x00" + strings.Join(n.got, ",") + "\x00" + strconv.FormatBool(n.supported)
}

func newGitIdentities(b *App, client *http.Client) *gitIdentityService {
	return &gitIdentityService{b: b, store: gitident.Store{Dir: b.cfgs.Boot().Layout().LocalSecrets()}, client: client,
		timeout: 90 * time.Second, nodes: map[string]*gitIDNode{}, pending: map[string]bool{}}
}

// gitLabEntry is GET /api/gitlab: never the token.
type gitLabEntry struct {
	Configured bool   `json:"configured"`
	BaseURL    string `json:"base_url"`
	Host       string `json:"host"`
	TokenHint  string `json:"token_hint"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	// Where says where the token is kept.
	Where string `json:"where"`
}

// gitLabWhere is where the token is kept, for the card.
const gitLabWhere = "this machine's data folder (core/local-secrets): never in a backup, a snapshot or an export; a core move carries it end to end"

// tokenHint is the end of a token, for people to tell tokens apart: its
// last four characters, and nothing for one too short to spare them.
func tokenHint(t string) string {
	if len(t) < 12 {
		return ""
	}
	return "…" + t[len(t)-4:]
}

func (s *gitIdentityService) gitLab(context.Context) (any, error) {
	c, ok, err := s.store.GitLab()
	if err != nil {
		return nil, err
	}
	return gitLabEntry{Configured: ok, BaseURL: c.BaseURL, Host: c.Host(), TokenHint: tokenHint(c.Token), Name: c.Name, Email: c.Email, Where: gitLabWhere}, nil
}

// setGitLab answers PUT /api/gitlab. The audit says what changed, never
// the token.
func (s *gitIdentityService) setGitLab(ctx context.Context, req httpapi.GitLabRequest) (any, error) {
	old, had, err := s.store.GitLab()
	if err != nil {
		return nil, err
	}
	next, err := s.store.SetGitLab(gitident.Update{BaseURL: req.BaseURL, Token: req.Token, Name: req.Name, Email: req.Email})
	if err != nil {
		return nil, err
	}
	var what []string
	switch {
	case strings.TrimSpace(req.Token) == "":
	case had:
		what = append(what, "token replaced")
	default:
		what = append(what, "token set")
	}
	if next.BaseURL != old.BaseURL {
		what = append(what, "address "+next.BaseURL)
	}
	if next.Name != old.Name || next.Email != old.Email {
		what = append(what, "commit name and email saved")
	}
	if len(what) == 0 {
		what = append(what, "nothing changed")
	}
	s.b.audit.Append(audit.Entry{Kind: audit.ConnectionKey, By: "dashboard", Detail: "gitlab: " + strings.Join(what, "; "), Outcome: "ok"})
	s.changed()
	return s.gitLab(ctx)
}

// removeGitLab answers DELETE /api/gitlab: the FrIDEs lose it too.
func (s *gitIdentityService) removeGitLab(context.Context) error {
	if err := s.store.RemoveGitLab(); err != nil {
		return err
	}
	s.b.audit.Append(audit.Entry{Kind: audit.ConnectionKey, By: "dashboard", Detail: "gitlab: removed", Outcome: "ok"})
	s.changed()
	return nil
}

// checkGitLab answers POST /api/gitlab/check: who GitLab says the token is.
func (s *gitIdentityService) checkGitLab(ctx context.Context) (any, error) {
	c, ok, err := s.store.GitLab()
	if err != nil {
		return nil, err
	}
	if !ok {
		return connectionCheck{Error: "no GitLab connection: set one first"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, connectionCheckTimeout)
	defer cancel()
	user, err := gitident.Check(ctx, s.client, c)
	if err != nil {
		return connectionCheck{Error: err.Error()}, nil
	}
	return connectionCheck{OK: true, Detail: "GitLab at " + c.Host() + " knows the token as " + user}, nil
}

// gitHubCommit is the GitHub identity's commit name and email.
type gitHubCommit struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// setGitHubIdentity answers PUT /api/github/identity.
func (s *gitIdentityService) setGitHubIdentity(_ context.Context, req httpapi.GitCommitRequest) (any, error) {
	c, err := s.store.SetGitHubCommit(req.Name, req.Email)
	if err != nil {
		return nil, err
	}
	detail := "github: commit name and email saved"
	if c == (gitident.Commit{}) {
		detail = "github: commit name and email removed"
	}
	s.b.audit.Append(audit.Entry{Kind: audit.ConnectionKey, By: "dashboard", Detail: detail, Outcome: "ok"})
	s.changed()
	return gitHubCommit(c), nil
}

// gitIdentitiesView is GET /api/git-identities: the identities, never a
// token, and what each FrIDE node took.
type gitIdentitiesView struct {
	Identities []gitIdentityRow  `json:"identities"`
	Nodes      []gitIdentityNode `json:"nodes"`
}

// gitIdentityRow is one identity: whether it is there, and why not.
type gitIdentityRow struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Host  string `json:"host"`
	Name  string `json:"name"`
	Email string `json:"email"`
	// Set says it is there to hand out: GitHub connected (or a typed
	// token), GitLab set.
	Set bool `json:"set"`
	// Note says why the FrIDEs got none the last time the core tried.
	Note string `json:"note,omitempty"`
}

// gitIdentityNode is a node whose fride is approved.
type gitIdentityNode struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	// Allowed are the identities nodes.git_identities gives it.
	Allowed []string `json:"allowed"`
	// Got are those its FrIDE took last, at At; Supported says its FrIDE
	// takes them, Pending that it did not answer, Error what went wrong.
	Got       []string   `json:"got"`
	At        *time.Time `json:"at,omitempty"`
	Supported bool       `json:"supported"`
	Pending   bool       `json:"pending,omitempty"`
	Error     string     `json:"error,omitempty"`
	// Held says why the node keeps them from its FrIDE on purpose.
	Held string `json:"held,omitempty"`
}

// githubSet reports whether GitHub is connected or a token typed, asking
// the secret store (a keychain natively) at most once a minute: the
// dashboard polls the view.
func (s *gitIdentityService) githubSet() bool {
	s.mu.Lock()
	if !s.ghAt.IsZero() && time.Since(s.ghAt) < time.Minute {
		defer s.mu.Unlock()
		return s.ghSet
	}
	s.mu.Unlock()
	_, app, _ := s.b.github.secrets.load()
	set := app || s.b.github.secrets.typedToken() != ""
	s.mu.Lock()
	s.ghSet, s.ghAt = set, time.Now()
	s.mu.Unlock()
	return set
}

func (s *gitIdentityService) view(context.Context) (any, error) {
	commit, err := s.store.GitHubCommit()
	if err != nil {
		return nil, err
	}
	gl, glSet, err := s.store.GitLab()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	note := s.githubNote
	s.mu.Unlock()
	out := gitIdentitiesView{Identities: []gitIdentityRow{
		{ID: gitident.GitHub, Kind: "github", Host: "github.com", Name: commit.Name, Email: commit.Email, Set: s.githubSet(), Note: note},
		{ID: gitident.GitLab, Kind: "gitlab", Host: gl.Host(), Name: gl.Name, Email: gl.Email, Set: glSet},
	}, Nodes: []gitIdentityNode{}}
	if s.b.nodes == nil {
		return out, nil
	}
	views, err := s.b.nodes.Views()
	if err != nil {
		return nil, err
	}
	cfg := s.b.cfgs.Get()
	for _, v := range views {
		if v.Device != nodeproto.DeviceApproved || v.Approved.Fride == nil {
			continue
		}
		n := gitIdentityNode{ID: v.ID, Name: v.Name, Online: v.Online, Allowed: allowedIdentities(cfg, v), Got: []string{}}
		s.mu.Lock()
		if st := s.nodes[v.ID]; st != nil {
			at := st.at
			n.Got, n.At, n.Supported, n.Pending, n.Error, n.Held = slices.Clone(st.got), &at, st.supported, st.pending, st.err, st.held
			if n.Got == nil {
				n.Got = []string{}
			}
		}
		s.mu.Unlock()
		out.Nodes = append(out.Nodes, n)
	}
	return out, nil
}

func (b *App) newGitIdentitiesAPI() *httpapi.GitIdentitiesAPI {
	s := b.gitIDs
	return &httpapi.GitIdentitiesAPI{
		GitLab: s.gitLab, SetGitLab: s.setGitLab, RemoveGitLab: s.removeGitLab, CheckGitLab: s.checkGitLab,
		SetGitHubIdentity: s.setGitHubIdentity, View: s.view,
	}
}
