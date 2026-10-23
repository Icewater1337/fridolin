package httpapi

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
)

// GitHub (docs/GITHUB.md): the dashboard registers a GitHub App of the
// bridge's own through GitHub's manifest flow, and runs get short-lived
// tokens of it. Three of the routes are pages the browser is sent to, not
// API calls: the hand-off that posts the manifest to GitHub, and the two
// addresses GitHub sends the browser back to. They need no token: each is
// tied to a one-time state that an API call made (or, for the install's
// return, does nothing but send the browser on).

// ErrGitHubState is a hand-off or a return whose state is unknown, used
// up or too old.
var ErrGitHubState = errors.New("this GitHub sign-up is unknown or expired; start again from the dashboard (Integrations → GitHub)")

// ErrGitHubNotConnected is a call that needs the app before there is one.
var ErrGitHubNotConnected = errors.New("GitHub is not connected: Integrations → GitHub → Connect GitHub")

// GitHubService is the GitHub App's side the handlers use.
type GitHubService interface {
	Status(ctx context.Context) GitHubStatus
	// Start makes a one-time state for a new app, whose redirects go to
	// origin (the dashboard's scheme and host), and returns the path of
	// the hand-off page.
	Start(origin, org string, pullRequests bool) (string, error)
	Handoff(state string) (GitHubHandoff, error)
	// Created exchanges GitHub's code and returns where the browser goes
	// next: the app's install page.
	Created(ctx context.Context, code, state string) (string, error)
	// Installed forgets the installations read before.
	Installed()
	Test(ctx context.Context, repo string) (GitHubTestResult, error)
	Disconnect() (GitHubDisconnected, error)
}

// GitHubStatus is GET /api/github.
type GitHubStatus struct {
	Connected bool           `json:"connected"`
	App       *GitHubAppInfo `json:"app,omitempty"`
	// Where names the store that holds the app's key.
	Where         string               `json:"where,omitempty"`
	Installations []GitHubInstallation `json:"installations"`
	InstallURL    string               `json:"install_url,omitempty"`
	SettingsURL   string               `json:"settings_url,omitempty"`
	DeleteURL     string               `json:"delete_url,omitempty"`
	// TypedToken: a typed token is there, which runs get when no app is.
	TypedToken bool   `json:"typed_token"`
	Error      string `json:"error,omitempty"`
}

// GitHubAppInfo is what the dashboard shows of the app.
type GitHubAppInfo struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	HTMLURL string `json:"html_url"`
}

// GitHubInstallation is the app installed on one account.
type GitHubInstallation struct {
	ID                  int64             `json:"id"`
	Account             string            `json:"account"`
	HTMLURL             string            `json:"html_url"`
	RepositorySelection string            `json:"repository_selection"`
	Repos               []string          `json:"repos"`
	Permissions         map[string]string `json:"permissions"`
	Suspended           bool              `json:"suspended"`
}

// GitHubHandoff is the form the hand-off page posts to GitHub.
type GitHubHandoff struct {
	Action   string // GitHub's new-app page, with the state
	Manifest string // the manifest's JSON
	Name     string
}

// GitHubTestResult is POST /api/github/test.
type GitHubTestResult struct {
	Repo        string            `json:"repo"`
	Permissions map[string]string `json:"permissions"`
	Issue       *GitHubIssue      `json:"issue"`
	Revoked     bool              `json:"revoked"`
}

// GitHubIssue is the issue the test read.
type GitHubIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// GitHubDisconnected is POST /api/github/disconnect.
type GitHubDisconnected struct {
	Status string `json:"status"`
	// DeleteURL is where the app itself is deleted on GitHub; disconnecting
	// only forgets its key here.
	DeleteURL string `json:"delete_url,omitempty"`
}

type GitHubAPI struct {
	GitHub func() GitHubService
}

func (a *GitHubAPI) Status(w http.ResponseWriter, r *http.Request) {
	WriteJSONIfChanged(w, r, a.GitHub().Status(r.Context()))
}

// origin is the scheme and host the dashboard was opened at, which the
// host guard has checked: GitHub sends the browser back there.
func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (a *GitHubAPI) Connect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Org          string `json:"org"`
		PullRequests bool   `json:"pull_requests"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	path, err := a.GitHub().Start(origin(r), req.Org, req.PullRequests)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, struct {
		URL string `json:"url"`
	}{path})
}

func (a *GitHubAPI) Test(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo string `json:"repo"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := a.GitHub().Test(r.Context(), req.Repo)
	switch {
	case errors.Is(err, ErrGitHubNotConnected):
		WriteErr(w, http.StatusConflict, err.Error())
	case err != nil:
		WriteErr(w, http.StatusBadGateway, err.Error())
	default:
		WriteJSON(w, http.StatusOK, res)
	}
}

func (a *GitHubAPI) Disconnect(w http.ResponseWriter, r *http.Request) {
	res, err := a.GitHub().Disconnect()
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

// handoffPage posts the manifest to GitHub when the owner clicks: the
// manifest flow wants a form POST, which the dashboard's own CSP
// (form-action 'none') does not allow. It has no script.
var handoffPage = template.Must(template.New("handoff").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <meta name="referrer" content="no-referrer">
  <title>Connect GitHub · Fridolin</title>
  <link rel="icon" href="/favicon.svg" type="image/svg+xml">
  <link rel="stylesheet" href="/assets/app.css">
</head>
<body>
  <main class="handoff">
    <h1>Connect GitHub</h1>
    <p>GitHub will show a new private app, <strong>{{.Name}}</strong>, with the permissions Fridolin asks for and no webhook. Create it there; GitHub then asks which repositories it may use.</p>
    <form method="post" action="{{.Action}}">
      <input type="hidden" name="manifest" value="{{.Manifest}}">
      <button type="submit" class="btn primary">Continue to GitHub</button>
    </form>
    <p><a href="/#/integrations">Back to the dashboard</a></p>
  </main>
</body>
</html>
`))

// handoffCSP is the hand-off page's policy: no script at all, styles from
// the bridge, and the one form may go to GitHub only.
func handoffCSP(action string) string {
	target := "https://github.com"
	if u, err := url.Parse(action); err == nil && u.Scheme != "" && u.Host != "" {
		target = u.Scheme + "://" + u.Host
	}
	return "default-src 'none'; style-src 'self'; img-src 'self'; form-action " + target +
		"; frame-ancestors 'none'; base-uri 'none'"
}

// Handoff serves GET /github/connect?state=…
func (a *GitHubAPI) Handoff(w http.ResponseWriter, r *http.Request) {
	h, err := a.GitHub().Handoff(r.URL.Query().Get("state"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Security-Policy", handoffCSP(h.Action))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = handoffPage.Execute(w, h)
}

// Created serves GET /github/created?code=…&state=…, where GitHub sends
// the browser once the app is made.
func (a *GitHubAPI) Created(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	next, err := a.GitHub().Created(r.Context(), r.URL.Query().Get("code"), r.URL.Query().Get("state"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// Installed serves GET /github/installed, the app's setup URL, where
// GitHub sends the browser after an install or a change of its
// repositories. What GitHub appends is not trusted: the dashboard reads
// the installations from GitHub itself.
func (a *GitHubAPI) Installed(w http.ResponseWriter, r *http.Request) {
	a.GitHub().Installed()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/#/integrations", http.StatusSeeOther)
}
