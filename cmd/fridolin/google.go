package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// Google sign-in for calendars.
//
// Google only lets an app ask for calendar access through an OAuth client
// registered in a Google Cloud project. The bridge does not ship one: you
// create a "Desktop app" client once (docs/CALENDAR.md) and paste its JSON
// into the dashboard. After that every brain connects its own Google account
// with a normal sign-in in the browser — no API keys, no files to copy.
//
// The sign-in returns to the bridge itself (http://127.0.0.1:<port>/), which
// Google allows for Desktop app clients. The resulting token is written to
//
//	<credentials dir>/google_calendar_token_<brain>.json
//
// (credentials dir = sandbox.credentials_dir, else the data dir), in the
// standard "authorized user" format the Google libraries read. The bridge
// alone uses it: brains make their calendar calls through it
// (google_calendar.go), and the sandbox denies them the folder.

// calendarScopes are what a brain's sign-in asks Google for: the narrowest
// grants the calendar commands work with. None of them lets a brain delete
// a calendar or change who it is shared with, which the full calendar
// scope older versions asked for does.
var calendarScopes = []string{
	"https://www.googleapis.com/auth/calendar.events",                // list, create and delete events
	"https://www.googleapis.com/auth/calendar.events.freebusy",       // free: the busy times of the brain's calendars
	"https://www.googleapis.com/auth/calendar.calendarlist.readonly", // the account's calendars, for the brain's page and `calendars`
}

// missingScopes are the calendarScopes granted lacks. Google's consent
// screen lets the person untick some of them.
func missingScopes(granted []string) []string {
	var out []string
	for _, s := range calendarScopes {
		if !slices.Contains(granted, s) {
			out = append(out, s)
		}
	}
	return out
}

// extraScopes are the scopes in granted beyond calendarScopes: the full
// calendar scope of a brain that connected with an older version, say.
func extraScopes(granted []string) []string {
	var out []string
	for _, s := range granted {
		if !slices.Contains(calendarScopes, s) {
			out = append(out, s)
		}
	}
	return out
}

// Endpoints, variables so tests can point them at a fake server. Token
// requests always go to googleTokenEndpoint, never to a token_uri from a
// file: a brain the sandbox does not hold can write its token file.
var (
	googleAuthEndpoint   = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint  = "https://oauth2.googleapis.com/token"
	googleRevokeEndpoint = "https://oauth2.googleapis.com/revoke"
	googleAPIBase        = "https://www.googleapis.com"
	googleHTTP           = &http.Client{Timeout: 20 * time.Second}
)

// GoogleClient is an OAuth client from a Google Cloud project.
type GoogleClient struct {
	Type         string // installed or web
	ClientID     string
	ClientSecret string
	raw          []byte
}

// ParseGoogleClient reads the JSON Google Cloud Console offers for download.
// Its token_uri is not used; see googleTokenEndpoint.
func ParseGoogleClient(data []byte) (*GoogleClient, error) {
	var f map[string]struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("not a Google OAuth client file: %w", err)
	}
	for _, typ := range []string{"installed", "web"} {
		c, ok := f[typ]
		if !ok {
			continue
		}
		if c.ClientID == "" || c.ClientSecret == "" {
			return nil, errors.New("the client file has no client_id or client_secret")
		}
		return &GoogleClient{Type: typ, ClientID: c.ClientID, ClientSecret: c.ClientSecret, raw: data}, nil
	}
	return nil, errors.New(`expected a Google OAuth client file with an "installed" (Desktop app) or "web" section`)
}

// googleToken is the "authorized user" file the Google libraries read.
type googleToken struct {
	Token        string   `json:"token"`
	RefreshToken string   `json:"refresh_token"`
	TokenURI     string   `json:"token_uri"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	Account      string   `json:"account,omitempty"`
	Expiry       string   `json:"expiry,omitempty"`
}

// oauthPending is a sign-in waiting for Google to send the browser back.
// It keeps the client and redirect URI the sign-in started with: the code
// Google returns can be exchanged only with those, whatever was changed in
// Settings in the meantime.
type oauthPending struct {
	brainID     string
	verifier    string
	created     time.Time
	client      *GoogleClient
	redirectURI string
}

// GoogleAuth manages the client, sign-ins and per-brain tokens.
type GoogleAuth struct {
	cfgs *ConfigStore

	mu         sync.Mutex
	pending    map[string]oauthPending  // state → sign-in in progress
	lastError  map[string]string        // brain id → last sign-in error
	tokenLocks map[string]chan struct{} // brain id → its token's lock; see lockToken
}

// NewGoogleAuth creates the manager, and logs the files in the credentials
// dir that look like the copies of the OAuth client older versions left.
func NewGoogleAuth(cfgs *ConfigStore) *GoogleAuth {
	g := &GoogleAuth{cfgs: cfgs, pending: map[string]oauthPending{}, lastError: map[string]string{}, tokenLocks: map[string]chan struct{}{}}
	if copies := g.leftoverClientCopies(); len(copies) > 0 {
		log.Printf("WARNING: the credentials folder holds files named like the copies of the Google OAuth client that older versions wrote for the calendar tool, "+
			"each holding a client secret: %s. The bridge no longer reads them, and a brain the sandbox does not hold (on Linux, or with the sandbox off) can: "+
			"delete them by hand, unless something else of yours uses them.", strings.Join(copies, ", "))
	}
	return g
}

// lockToken takes brainID's token lock, which a refresh, a disconnect and
// a sign-in hold from reading the brain's token to writing or removing it.
// Without it a refresh that read the token before a disconnect removed it
// would write it back once Google answered: the brain would show as
// connected again, and with no calendars chosen any more it may use its
// primary one (calendarsOf). The lock is a channel of one, so that waiting
// for it ends with ctx; unlock releases it.
func (g *GoogleAuth) lockToken(ctx context.Context, brainID string) (unlock func(), err error) {
	g.mu.Lock()
	l := g.tokenLocks[brainID]
	if l == nil {
		l = make(chan struct{}, 1)
		g.tokenLocks[brainID] = l
	}
	g.mu.Unlock()
	select {
	case l <- struct{}{}:
		return func() { <-l }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for brain %s's calendar token, which another request is using: %w", brainID, ctx.Err())
	}
}

func (g *GoogleAuth) clientPath() string {
	return filepath.Join(g.cfgs.Get().DataDir, "google", "oauth_client.json")
}

// credsDir is where per-brain calendar files live.
func (g *GoogleAuth) credsDir() string { return credentialsDirOf(g.cfgs.Get()) }

func (g *GoogleAuth) tokenPath(brainID string) string {
	return calendarTokenFile(g.credsDir(), brainID)
}

// calendarTokenFile is a brain's calendar token in dir, the credentials
// dir. Only the bridge reads and writes it; the sandbox denies brains the
// folder.
func calendarTokenFile(dir, brainID string) string {
	return filepath.Join(dir, calendarTokenPrefix+brainID+calendarTokenSuffix)
}

// calendarTokenPrefix and calendarTokenSuffix make up a token file's name
// around the brain id, for calendarTokenFile and calendarTokenBrain alike.
const calendarTokenPrefix, calendarTokenSuffix = "google_calendar_token_", ".json"

// calendarClientCopyPrefix starts the name of the copy of the OAuth client
// that older versions wrote next to a brain's token for the calendar tool
// in the brain, google_calendar_credentials_<brain>.json.
const calendarClientCopyPrefix = "google_calendar_credentials_"

// leftoverClientCopies names the files in the credentials dir that are
// named like those copies and hold a client secret. The bridge no longer
// reads them, but a brain the sandbox does not hold (on Linux, or with the
// sandbox off) can, so they are reported for you to delete. Going by name
// and secret only, the bridge cannot tell such a copy from a file of
// yours that something else uses, so it deletes none; one may be
// write-protected as well. A file is read only when it is a regular one,
// so a FIFO cannot hang the bridge's start, but unlike a token it may
// have more than one link: a hard link to a file kept elsewhere exposes
// the secret all the same. A symlink is not followed, so one is not
// reported. The names are returned without the folder.
func (g *GoogleAuth) leftoverClientCopies() []string {
	dir := g.credsDir()
	entries, _ := os.ReadDir(dir) // sorted by name
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, calendarClientCopyPrefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		if data, err := readRegularFile(filepath.Join(dir, name), false); err == nil && holdsClientSecret(data) {
			out = append(out, name)
		}
	}
	return out
}

// holdsClientSecret reports whether data, a JSON object, has a client
// secret: in a section of an OAuth client file ("installed" or "web"), or
// at the top, as a token has it.
func holdsClientSecret(data []byte) bool {
	has := func(raw []byte) bool {
		var o struct {
			ClientSecret string `json:"client_secret"`
		}
		return json.Unmarshal(raw, &o) == nil && o.ClientSecret != ""
	}
	var sections map[string]json.RawMessage
	if json.Unmarshal(data, &sections) != nil {
		return false
	}
	if has(data) {
		return true
	}
	for _, s := range sections {
		if has(s) {
			return true
		}
	}
	return false
}

// calendarTokenBrain turns the name of a token file back into its brain's
// id, the inverse of calendarTokenFile; ok is false for any other name.
func calendarTokenBrain(name string) (brainID string, ok bool) {
	id, ok := strings.CutPrefix(name, calendarTokenPrefix)
	if ok {
		id, ok = strings.CutSuffix(id, calendarTokenSuffix)
	}
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// Client returns the configured OAuth client, or nil.
func (g *GoogleAuth) Client() *GoogleClient {
	data, err := os.ReadFile(g.clientPath())
	if err != nil {
		return nil
	}
	c, err := ParseGoogleClient(data)
	if err != nil {
		return nil
	}
	return c
}

// SetClient validates and stores the OAuth client file.
func (g *GoogleAuth) SetClient(data []byte) (*GoogleClient, error) {
	c, err := ParseGoogleClient(data)
	if err != nil {
		return nil, err
	}
	if err := fsx.WriteFileAtomic(g.clientPath(), data, 0o600, false); err != nil {
		return nil, err
	}
	return c, nil
}

// redirectURI is where Google sends the browser back: the dashboard
// itself, at the address it was started on. http_listen changes only with a
// restart, and a sign-in started before it must come back here.
func (g *GoogleAuth) redirectURI() string {
	return "http://" + g.cfgs.Startup().HTTPListen + "/"
}

// tokenLifetime is how long an access token Google issued lasts. An
// expires_in that is missing or not positive gets Google's usual hour,
// rather than a token that counts as expired at once.
func tokenLifetime(expiresIn int) time.Duration {
	if expiresIn <= 0 {
		return time.Hour
	}
	return time.Duration(expiresIn) * time.Second
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// StartSignIn returns the Google URL that signs a brain in.
func (g *GoogleAuth) StartSignIn(brainID string) (string, error) {
	c := g.Client()
	if c == nil {
		return "", errors.New("add your Google OAuth client first (Settings → Google sign-in)")
	}
	verifier := randomString(48)
	sum := sha256.Sum256([]byte(verifier))
	state := randomString(24)

	g.mu.Lock()
	for s, p := range g.pending {
		if time.Since(p.created) > 15*time.Minute {
			delete(g.pending, s)
		}
	}
	redirect := g.redirectURI()
	g.pending[state] = oauthPending{brainID: brainID, verifier: verifier, created: time.Now(), client: c, redirectURI: redirect}
	delete(g.lastError, brainID)
	g.mu.Unlock()

	// No include_granted_scopes: it would fold whatever else this client
	// was ever granted into the brain's token.
	q := url.Values{
		"client_id":             {c.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {strings.Join(calendarScopes, " ")},
		"access_type":           {"offline"},
		"prompt":                {"consent select_account"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	return googleAuthEndpoint + "?" + q.Encode(), nil
}

// isOAuthCallback reports whether a request to / is Google returning.
func isOAuthCallback(r *http.Request) bool {
	q := r.URL.Query()
	return q.Get("state") != "" && (q.Get("code") != "" || q.Get("error") != "")
}

// HandleCallback finishes a sign-in and redirects to the brain's page.
func (g *GoogleAuth) HandleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	g.mu.Lock()
	p, ok := g.pending[q.Get("state")]
	delete(g.pending, q.Get("state"))
	g.mu.Unlock()
	if !ok || time.Since(p.created) > 15*time.Minute {
		http.Error(w, "This sign-in link expired or was already used. Start again from the dashboard.", http.StatusBadRequest)
		return
	}
	back := "/#/brains/" + url.PathEscape(p.brainID)
	fail := func(msg string) {
		g.mu.Lock()
		g.lastError[p.brainID] = msg
		g.mu.Unlock()
		log.Printf("brain %s: Google sign-in failed: %s", p.brainID, msg)
		http.Redirect(w, r, back, http.StatusFound)
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			fail("Google sign-in was cancelled")
		} else {
			fail("Google returned an error: " + e)
		}
		return
	}
	// Not the request's context: the code is spent once Google has it, and a
	// browser that goes away meanwhile must not throw the sign-in away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	tok, err := g.exchange(ctx, p, q.Get("code"))
	if err != nil {
		fail(err.Error())
		return
	}
	if tok.RefreshToken == "" {
		fail("Google did not return a refresh token — remove the bridge's access at myaccount.google.com/permissions and connect again")
		return
	}
	if missing := missingScopes(tok.Scopes); len(missing) > 0 {
		fail("Google granted only part of the calendar access the brain needs (missing " + strings.Join(missing, ", ") + ") — connect again and leave every box ticked")
		return
	}
	if extra := extraScopes(tok.Scopes); len(extra) > 0 {
		log.Printf("brain %s: Google granted more than the bridge asked for (%s); to narrow it, disconnect the brain's calendar, and every other brain signed in to the same account, and connect it again", p.brainID, strings.Join(extra, ", "))
	}
	account, err := g.primaryAccount(ctx, tok)
	if err != nil {
		fail("signed in, but reading the calendar list failed: " + err.Error())
		return
	}
	tok.Account = account
	// A brain removed while it signed in gets no token: one left behind
	// would count as connected for a later brain of the same id.
	if !g.configured(p.brainID) {
		fail("the brain was removed while it signed in")
		return
	}
	// Held to the end, so that a refresh in progress cannot write the
	// token this replaces back over the new one, nor a disconnect come
	// between reading the old token and saving the new (lockToken).
	unlock, err := g.lockToken(ctx, p.brainID)
	if err != nil {
		fail(err.Error())
		return
	}
	defer unlock()
	// A switch to another account leaves the old one's grant behind, of use
	// to nobody: revoke it, unless another brain signs in to that account
	// too. Signing in to the same account again keeps its grant, which the
	// new token belongs to as well, so revoking the old token would revoke
	// the new one with it. The old token is read now, before the new one
	// replaces it, and revoked last, once the new one is saved.
	var revokeOld string
	oldData, oldErr := readCredentialFile(g.tokenPath(p.brainID))
	if old, err := g.loadToken(p.brainID); err == nil && old.RefreshToken != "" && old.Account != "" &&
		!strings.EqualFold(old.Account, account) && g.sharingGrant(p.brainID, old) == "" {
		revokeOld = old.RefreshToken
	}
	if err := g.saveToken(p.brainID, tok); err != nil {
		fail("saving the token failed: " + err.Error())
		return
	}
	if err := g.cfgs.UpdateManifest(p.brainID, func(m *manifest.Manifest) error {
		if m.Calendar == nil || m.Calendar.Account != account {
			m.Calendar = &manifest.Calendar{Account: account, Calendars: []string{account}, Default: account}
		}
		return nil
	}); err != nil {
		// The brain went in the meantime, say: take back what this sign-in
		// wrote, and only that. The token it replaced goes back.
		if oldErr == nil {
			_ = fsx.WriteFileAtomic(g.tokenPath(p.brainID), oldData, 0o600, false)
		} else {
			_ = os.Remove(g.tokenPath(p.brainID))
		}
		fail("saving the brain's calendar settings failed: " + err.Error())
		return
	}
	log.Printf("brain %s: Google Calendar connected (%s)", p.brainID, maskEmail(account))
	// Only now: a sign-in that failed above leaves the brain with the old
	// token, which must still work.
	if revokeOld != "" {
		if err := g.revoke(ctx, revokeOld); err != nil {
			log.Printf("brain %s: could not revoke the access of the Google account it switched from: %v", p.brainID, err)
		}
	}
	http.Redirect(w, r, back, http.StatusFound)
}

func maskEmail(e string) string {
	at := strings.Index(e, "@")
	if at <= 1 {
		return "…"
	}
	return e[:1] + "…" + e[at:]
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"` // what Google granted, space-separated
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// oauthError is a refusal from Google's token endpoint.
type oauthError struct {
	status  int
	code    string // the OAuth error (invalid_grant, invalid_client, …), "" when Google gave none
	message string
}

func (e *oauthError) Error() string { return "token request to Google failed: " + e.message }

// temporary reports whether Google may well take the same request in a
// minute: it was overloaded or limited the rate, rather than refusing the
// grant.
func (e *oauthError) temporary() bool {
	return e.status == http.StatusTooManyRequests || e.status >= 500
}

// revoked reports whether Google refused the grant itself (invalid_grant):
// the sign-in was revoked, expired (an app in Testing), or the password
// changed. Only signing in again helps.
func (e *oauthError) revoked() bool { return e.code == "invalid_grant" }

func (g *GoogleAuth) postToken(ctx context.Context, c *GoogleClient, form url.Values) (*tokenResponse, error) {
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Google (%w); this is usually temporary, try again in a minute", err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		msg := tr.ErrorDesc
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, &oauthError{status: resp.StatusCode, code: tr.Error, message: msg}
	}
	return &tr, nil
}

// exchange trades the code Google returned for sign-in p for a token, with
// the client and redirect URI p started with.
func (g *GoogleAuth) exchange(ctx context.Context, p oauthPending, code string) (*googleToken, error) {
	c := p.client
	tr, err := g.postToken(ctx, c, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {p.verifier},
		"redirect_uri":  {p.redirectURI},
	})
	if err != nil {
		return nil, err
	}
	return &googleToken{
		Token: tr.AccessToken, RefreshToken: tr.RefreshToken, TokenURI: googleTokenEndpoint,
		ClientID: c.ClientID, ClientSecret: c.ClientSecret, Scopes: tr.grantedScopes(calendarScopes),
		Expiry: time.Now().Add(tokenLifetime(tr.ExpiresIn)).UTC().Format("2006-01-02T15:04:05.000000Z"),
	}, nil
}

// grantedScopes are the scopes Google says it granted, else asked: an
// answer without a scope field grants what was requested.
func (tr *tokenResponse) grantedScopes(asked []string) []string {
	if granted := strings.Fields(tr.Scope); len(granted) > 0 {
		return granted
	}
	return slices.Clone(asked)
}

// saveToken writes the brain's token. It does not follow a symlink:
// fsx.WriteFileAtomic renames a new file over the token, replacing
// whatever is there. Older versions also copied the OAuth client, secret
// and all, next to it as google_calendar_credentials_<brain>.json, for the
// calendar tool in the brain; nothing reads that copy any more. The caller
// holds the brain's token lock (lockToken).
func (g *GoogleAuth) saveToken(brainID string, tok *googleToken) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(g.tokenPath(brainID), data, 0o600, false)
}

// configured reports whether brainID is one of the configured brains.
func (g *GoogleAuth) configured(brainID string) bool {
	for _, ref := range g.cfgs.Get().BrainDirs {
		if ref.Approved != nil && ref.Approved.ID == brainID {
			return true
		}
	}
	return false
}

// readCredentialFile reads a brain's file in the credentials dir without
// following a symlink. A brain the sandbox does not hold (on Linux, or with
// sandbox.enabled off) can write there, so it can put a symlink to another
// brain's token in place of its own: the bridge would refresh that token
// and write it back where this brain reads it, or revoke it on disconnect.
// Older versions let each sandboxed brain write its own token file too. The
// last path component is the one to check. A hard link would do the same;
// the sandbox refuses those, and a file with more than one link is refused
// here as well. The open does not block, so a FIFO cannot hang the
// dashboard, and a file larger than maxBrainFile is refused rather than
// loaded.
func readCredentialFile(path string) ([]byte, error) { return readRegularFile(path, true) }

// readRegularFile is readCredentialFile's read. singleLink refuses a file
// with more than one link, which matters only for a file the bridge acts
// on, not for one it only reports.
func readRegularFile(path string, singleLink bool) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s: %w", path, fsx.ErrSymlink)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && singleLink && st.Nlink != 1 {
		return nil, fmt.Errorf("%s: it has more than one link, which the bridge does not trust", path)
	}
	data, err := fsx.ReadAtMost(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}

func (g *GoogleAuth) loadToken(brainID string) (*googleToken, error) {
	t, _, err := g.loadTokenFields(brainID)
	return t, err
}

// loadTokenFields is loadToken with every field of the file as it is, for
// writing the file back: the Google library that older versions' calendar
// tool used added fields of its own (universe_domain), which googleToken
// does not know.
func (g *GoogleAuth) loadTokenFields(brainID string) (*googleToken, map[string]json.RawMessage, error) {
	data, err := readCredentialFile(g.tokenPath(brainID))
	if err != nil {
		return nil, nil, err
	}
	var t googleToken
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, nil, fmt.Errorf("unreadable token file: %w", err)
	}
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, nil, fmt.Errorf("unreadable token file: not a JSON object")
	}
	return &t, fields, nil
}

// signInError is a brain's Google token the bridge itself will not use,
// before Google is asked: a token file it cannot read or trust, one with
// no refresh token or from another OAuth client, or no client to refresh
// it with. Connecting the brain again writes a new token, so a brain is
// told to ask for that (calendarCallProblem); other errors are not this.
type signInError struct{ err error }

func (e *signInError) Error() string { return e.err.Error() }
func (e *signInError) Unwrap() error { return e.err }

// accessToken returns a valid access token, refreshing (and saving) it when
// it has expired. It holds the brain's token lock from reading the token
// to writing it back (lockToken), so that calls which find it expired at
// the same time refresh it once: the others then read what the first one
// saved. The refresh is a single request, bounded by ctx and googleHTTP's
// timeout, so a disconnect waiting for the lock waits that long at most.
func (g *GoogleAuth) accessToken(ctx context.Context, brainID string) (*googleToken, error) {
	unlock, err := g.lockToken(ctx, brainID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	t, fields, err := g.loadTokenFields(brainID)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err // not connected
	}
	if err != nil {
		return nil, &signInError{err}
	}
	exp, _ := time.Parse(time.RFC3339Nano, t.Expiry)
	if t.Token != "" && time.Until(exp) > time.Minute {
		return t, nil
	}
	if t.RefreshToken == "" {
		return nil, &signInError{errors.New("the token has no refresh token — connect again")}
	}
	// A brain the sandbox does not hold can write its token file, so the
	// client in it is not trusted: the refresh uses yours, at Google's
	// fixed endpoint. A refresh token works only with the client that
	// issued it, so a token from another client cannot be refreshed here.
	c := g.Client()
	if c == nil {
		return nil, &signInError{errors.New("add your Google OAuth client first (Settings → Google sign-in)")}
	}
	if t.ClientID != c.ClientID {
		return nil, &signInError{errors.New("this brain signed in with another Google OAuth client than the one in Settings → Google sign-in — connect it again")}
	}
	tr, err := g.postToken(ctx, c, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}})
	if err != nil {
		return nil, err
	}
	t.Token = tr.AccessToken
	t.Scopes = tr.grantedScopes(t.Scopes)
	t.Expiry = time.Now().Add(tokenLifetime(tr.ExpiresIn)).UTC().Format("2006-01-02T15:04:05.000000Z")
	// Google may hand out a new refresh token with a refresh, and the old
	// one may stop working once it did.
	if tr.RefreshToken != "" {
		t.RefreshToken = tr.RefreshToken
	}
	// The access token is good for this call either way; a token not saved
	// is only refreshed again next time.
	if err := writeTokenFields(g.tokenPath(brainID), fields, t); err != nil {
		log.Printf("brain %s: saving the refreshed calendar token failed: %v", brainID, err)
	}
	return t, nil
}

// writeTokenFields writes t's refreshed fields over fields, the token file
// as it was read, so that what an older calendar tool's library added
// stays.
func writeTokenFields(path string, fields map[string]json.RawMessage, t *googleToken) error {
	for k, v := range map[string]any{"token": t.Token, "refresh_token": t.RefreshToken, "expiry": t.Expiry, "scopes": t.Scopes} {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		fields[k] = raw
	}
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	// A rename: a symlink planted since the read is replaced, not followed.
	return fsx.WriteFileAtomic(path, data, 0o600, false)
}

// CalendarEntry is one calendar of the signed-in account.
type CalendarEntry struct {
	ID         string `json:"id"`
	Summary    string `json:"summary"`
	Primary    bool   `json:"primary"`
	AccessRole string `json:"access_role"`
	Color      string `json:"color,omitempty"`
}

// calendarList returns the account's calendars, primary first. An account
// can have more than fit in one page, and a calendar on the second would
// look as if it did not exist.
func (g *GoogleAuth) calendarList(ctx context.Context, tok *googleToken) ([]CalendarEntry, error) {
	q := url.Values{"maxResults": {"250"}} // the API's largest page
	var out []CalendarEntry
	for range maxGooglePages {
		var page struct {
			Items []struct {
				ID              string `json:"id"`
				Summary         string `json:"summary"`
				SummaryOverride string `json:"summaryOverride"`
				Primary         bool   `json:"primary"`
				AccessRole      string `json:"accessRole"`
				BackgroundColor string `json:"backgroundColor"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := g.calendarAPI(ctx, tok, http.MethodGet, "/users/me/calendarList", q, nil, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			name := it.SummaryOverride
			if name == "" {
				name = it.Summary
			}
			out = append(out, CalendarEntry{ID: it.ID, Summary: name, Primary: it.Primary, AccessRole: it.AccessRole, Color: it.BackgroundColor})
		}
		if page.NextPageToken == "" {
			break
		}
		q.Set("pageToken", page.NextPageToken)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Primary != out[j].Primary {
			return out[i].Primary
		}
		return strings.ToLower(out[i].Summary) < strings.ToLower(out[j].Summary)
	})
	return out, nil
}

func (g *GoogleAuth) primaryAccount(ctx context.Context, tok *googleToken) (string, error) {
	cals, err := g.calendarList(ctx, tok)
	if err != nil {
		return "", err
	}
	for _, c := range cals {
		if c.Primary {
			return c.ID, nil
		}
	}
	return "", errors.New("the account has no primary calendar")
}

// errNotRevoked is Disconnect's error when Google did not confirm that it
// revoked the access. The token stays, so that Disconnect can be tried
// again, or told to remove it here only.
var errNotRevoked = errors.New("the revocation was not confirmed by Google")

// Disconnect ends a brain's calendar access, deletes its token file and
// clears its chosen calendars. It revokes the refresh token at Google
// first, but not while another token in the credentials dir is from the
// same client for the same account, or may be: Google revokes the app's
// whole grant for the account, so every brain signed in to it would lose
// its access too. note then says why it did not revoke. When Google does
// not confirm the revocation, the token stays and the error wraps
// errNotRevoked, unless localOnly asks to delete it anyway.
//
// A token file that is a symlink is not read, so nothing is revoked
// through it, and the remove takes the symlink away, not what it points to.
//
// It holds the brain's token lock from the read to the remove, so that a
// refresh in progress cannot write the token back after it (lockToken),
// and returns ctx's error when ctx ends while it waits for the lock. The
// brain's chosen calendars are cleared with the lock still held: cleared
// after it, they could undo the choice of a sign-in that was waiting for
// the lock and saved its token in between, which would leave the brain
// signed in with no calendars chosen, so on its primary one
// (calendarsOf).
func (g *GoogleAuth) Disconnect(ctx context.Context, brainID string, localOnly bool) (revoked bool, note string, err error) {
	unlock, err := g.lockToken(ctx, brainID)
	if err != nil {
		return false, "", err
	}
	defer unlock()
	t, lerr := g.loadToken(brainID)
	switch {
	case lerr != nil || t.RefreshToken == "":
		// Nothing that could be revoked.
	case localOnly:
		note = "the access was not revoked at Google: remove it at myaccount.google.com/permissions"
	default:
		if other := g.sharingGrant(brainID, t); other != "" {
			note = fmt.Sprintf("the access was not revoked at Google, since brain %s signs in to the same Google account with the same client "+
				"and would lose its access too; once neither needs it, remove it at myaccount.google.com/permissions", other)
			if !g.configured(other) {
				// Left by a brain removed without a disconnect, say: it
				// keeps the grant from being revoked until it goes.
				note = fmt.Sprintf("the access was not revoked at Google, since the credentials folder holds a token for %s, "+
					"which is not a configured brain, from the same client for the same Google account (%s); "+
					"if nothing uses that file, delete it and remove the access at myaccount.google.com/permissions",
					other, filepath.Base(g.tokenPath(other)))
			}
			break
		}
		if err := g.revoke(ctx, t.RefreshToken); err != nil {
			return false, "", err
		}
		revoked = true
	}
	if err := os.Remove(g.tokenPath(brainID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return revoked, note, err
	}
	g.mu.Lock()
	delete(g.lastError, brainID)
	g.mu.Unlock()
	if err := g.cfgs.UpdateManifest(brainID, func(m *manifest.Manifest) error { m.Calendar = nil; return nil }); err != nil {
		return revoked, note, fmt.Errorf("the token was deleted, but saving the brain's calendar settings failed: %w", err)
	}
	return revoked, note, nil
}

// revoke revokes a refresh token, and with it the grant it belongs to. A
// token Google calls invalid is revoked already.
func (g *GoogleAuth) revoke(ctx context.Context, refreshToken string) error {
	form := url.Values{"token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleRevokeEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", errNotRevoked, err)
	}
	defer resp.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusBadRequest && body.Error == "invalid_token":
		return nil
	case body.Error != "":
		return fmt.Errorf("%w: %s (%s)", errNotRevoked, body.Error, resp.Status)
	}
	return fmt.Errorf("%w: %s", errNotRevoked, resp.Status)
}

// sharingGrant names another holder of the grant t belongs to: a token in
// the credentials dir, other than brainID's, from the same OAuth client for
// the same Google account. Without an account in both, it cannot tell, and
// counts the other token as sharing it. It returns "" for none, else the
// brain that token is for, which need not be a configured one: a token
// file outlives a brain removed without a disconnect.
func (g *GoogleAuth) sharingGrant(brainID string, t *googleToken) string {
	entries, _ := os.ReadDir(g.credsDir()) // sorted by name
	for _, e := range entries {
		other, ok := calendarTokenBrain(e.Name())
		if !ok || other == brainID {
			continue
		}
		data, err := readCredentialFile(filepath.Join(g.credsDir(), e.Name()))
		if err != nil {
			continue
		}
		var o googleToken
		if json.Unmarshal(data, &o) != nil || o.RefreshToken == "" || o.ClientID != t.ClientID {
			continue
		}
		if o.Account == "" || t.Account == "" || strings.EqualFold(o.Account, t.Account) {
			return other
		}
	}
	return ""
}

// ── API ──────────────────────────────────────────────────────────────────

func (b *Bridge) apiGoogleClient(w http.ResponseWriter, r *http.Request) {
	g := b.google
	resp := map[string]any{"configured": false, "redirect_uri": g.redirectURI()}
	if c := g.Client(); c != nil {
		hint := c.ClientID
		if i := strings.Index(hint, "-"); i > 0 {
			hint = hint[:i] + "-…"
		}
		resp["configured"], resp["type"], resp["client_id_hint"] = true, c.Type, hint
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiSetGoogleClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSON string `json:"json"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := b.google.SetClient([]byte(strings.TrimSpace(req.JSON)))
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	note := ""
	if c.Type == "web" {
		note = "This is a Web application client: add " + b.google.redirectURI() + " to its Authorized redirect URIs in Google Cloud Console, or create a Desktop app client instead."
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved", "type": c.Type, "note": note})
}

func (b *Bridge) apiDeleteGoogleClient(w http.ResponseWriter, r *http.Request) {
	if err := os.Remove(b.google.clientPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (b *Bridge) calendarBrain(w http.ResponseWriter, r *http.Request) *Brain {
	br := b.brainMgr.Get(r.PathValue("id"))
	if br == nil {
		httpapi.WriteErr(w, http.StatusNotFound, "no such brain")
	}
	return br
}

func (b *Bridge) apiCalendarConnect(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	u, err := b.google.StartSignIn(br.ID())
	if err != nil {
		httpapi.WriteErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"auth_url": u})
}

func (b *Bridge) apiCalendarStatus(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	g := b.google
	m := br.settings.Load().manifest
	resp := map[string]any{
		"client_configured": g.Client() != nil,
		"connected":         false,
		"token_path":        g.tokenPath(br.ID()),
		"calendars":         []CalendarEntry{},
		"selected":          []string{},
		// Of every brain, not only this one: any brain the sandbox does not
		// hold can read them.
		"leftover_client_copies": nonNil(g.leftoverClientCopies()),
	}
	if m.Calendar != nil {
		resp["account"], resp["selected"], resp["default"] = m.Calendar.Account, nonNil(m.Calendar.Calendars), m.Calendar.Default
	}
	g.mu.Lock()
	if e := g.lastError[br.ID()]; e != "" {
		resp["last_error"] = e
	}
	g.mu.Unlock()
	// Lstat: a symlink there shows as connected, with the reason it is not
	// used in list_error.
	if _, err := os.Lstat(g.tokenPath(br.ID())); err != nil {
		httpapi.WriteJSON(w, http.StatusOK, resp)
		return
	}
	resp["connected"] = true
	if t, err := g.loadToken(br.ID()); err == nil && len(extraScopes(t.Scopes)) > 0 {
		// Connected with an older version, which asked for every calendar
		// right there is. Signing in again alone keeps that grant alive at
		// Google; Disconnect revokes it, and the next sign-in asks for
		// calendarScopes only.
		resp["broad_access"] = true
	}
	if r.URL.Query().Get("list") != "0" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		tok, err := g.accessToken(ctx, br.ID())
		if err == nil {
			var cals []CalendarEntry
			if cals, err = g.calendarList(ctx, tok); err == nil {
				resp["calendars"] = cals
				if m.Calendar == nil {
					for _, c := range cals {
						if c.Primary {
							resp["account"] = c.ID
						}
					}
				}
			}
		}
		if err != nil {
			resp["list_error"] = err.Error()
			var oe *oauthError
			resp["reconnect_needed"] = errors.As(err, &oe) && oe.revoked()
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiCalendarSelect(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	var req struct {
		Account   string   `json:"account"`
		Calendars []string `json:"calendars"`
		Default   string   `json:"default"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Calendars) == 0 {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, "choose at least one calendar (or disconnect)")
		return
	}
	if req.Default == "" || !slices.Contains(req.Calendars, req.Default) {
		req.Default = req.Calendars[0]
	}
	err := b.cfgs.UpdateManifest(br.ID(), func(m *manifest.Manifest) error {
		account := req.Account
		if account == "" && m.Calendar != nil {
			account = m.Calendar.Account
		}
		m.Calendar = &manifest.Calendar{Account: account, Calendars: req.Calendars, Default: req.Default}
		return nil
	})
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// apiCalendarDisconnect ends a brain's calendar access. ?local_only=1
// deletes the token without revoking it, for when Google would not confirm
// the revocation.
func (b *Bridge) apiCalendarDisconnect(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	revoked, note, err := b.google.Disconnect(ctx, br.ID(), r.URL.Query().Get("local_only") == "1")
	switch {
	case errors.Is(err, errNotRevoked):
		httpapi.WriteErr(w, http.StatusBadGateway, err.Error()+"; the token was kept, so try again, or remove it here only")
		return
	case err != nil:
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "disconnected", "revoked": revoked, "note": note})
}

// calendarEnv tells a run which brain it is and which calendars it may use.
//
// compat: calendar_tool.py read these to find the brain's token and keep to
// its calendars; nothing in tools/ reads them now that the bridge makes the
// calls, but a brain's own scripts may. Kept for one release, then removed.
// BRIDGE_CREDENTIALS_DIR is no longer set: it told every run where the
// tokens are, and with them the client copies older versions left
// (leftoverClientCopies), which a brain the sandbox does not hold can
// read. The last argument, that folder, is not used any more; it stays
// until the caller in brain.go stops passing it.
func calendarEnv(id string, m manifest.Manifest, _ string) []string {
	env := []string{"BRIDGE_BRAIN_ID=" + id}
	if m.Calendar != nil && len(m.Calendar.Calendars) > 0 {
		env = append(env, "BRIDGE_CALENDARS="+strings.Join(m.Calendar.Calendars, ","), "BRIDGE_CALENDAR_DEFAULT="+m.Calendar.Default)
	}
	return env
}
