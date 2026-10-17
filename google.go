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
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
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
// Google allows for Desktop app clients. The resulting token is written where
// the calendar tool and the sandbox expect it:
//
//	<credentials dir>/google_calendar_token_<brain>.json
//
// (credentials dir = sandbox.credentials_dir, else the data dir), in the
// standard "authorized user" format the Google libraries read. Only that
// brain's sandbox profile can read it.

const calendarScope = "https://www.googleapis.com/auth/calendar"

// Endpoints, variables so tests can point them at a fake server. Token
// requests always go to googleTokenEndpoint, never to a token_uri from a
// file: a brain can write its own token file.
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

type oauthPending struct {
	brainID  string
	verifier string
	created  time.Time
}

// GoogleAuth manages the client, sign-ins and per-brain tokens.
type GoogleAuth struct {
	cfgs *ConfigStore

	mu        sync.Mutex
	pending   map[string]oauthPending // state → sign-in in progress
	lastError map[string]string       // brain id → last sign-in error
}

// NewGoogleAuth creates the manager.
func NewGoogleAuth(cfgs *ConfigStore) *GoogleAuth {
	return &GoogleAuth{cfgs: cfgs, pending: map[string]oauthPending{}, lastError: map[string]string{}}
}

func (g *GoogleAuth) clientPath() string {
	return filepath.Join(g.cfgs.Get().DataDir, "google", "oauth_client.json")
}

// credsDir is where per-brain calendar files live.
func (g *GoogleAuth) credsDir() string { return credentialsDirOf(g.cfgs.Get()) }

func (g *GoogleAuth) tokenPath(brainID string) string {
	return filepath.Join(g.credsDir(), "google_calendar_token_"+brainID+".json")
}

func (g *GoogleAuth) credentialsPath(brainID string) string {
	return filepath.Join(g.credsDir(), "google_calendar_credentials_"+brainID+".json")
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
	if err := writeFileAtomic(g.clientPath(), data, 0o600, false); err != nil {
		return nil, err
	}
	return c, nil
}

// redirectURI is where Google sends the browser back: the dashboard itself.
func (g *GoogleAuth) redirectURI() string {
	return "http://" + g.cfgs.Get().HTTPListen + "/"
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
	g.pending[state] = oauthPending{brainID: brainID, verifier: verifier, created: time.Now()}
	delete(g.lastError, brainID)
	g.mu.Unlock()

	q := url.Values{
		"client_id":              {c.ClientID},
		"redirect_uri":           {g.redirectURI()},
		"response_type":          {"code"},
		"scope":                  {calendarScope},
		"access_type":            {"offline"},
		"prompt":                 {"consent select_account"},
		"include_granted_scopes": {"true"},
		"state":                  {state},
		"code_challenge":         {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method":  {"S256"},
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tok, err := g.exchange(ctx, q.Get("code"), p.verifier)
	if err != nil {
		fail(err.Error())
		return
	}
	if tok.RefreshToken == "" {
		fail("Google did not return a refresh token — remove the bridge's access at myaccount.google.com/permissions and connect again")
		return
	}
	account, err := g.primaryAccount(ctx, tok)
	if err != nil {
		fail("signed in, but reading the calendar list failed: " + err.Error())
		return
	}
	tok.Account = account
	if err := g.saveToken(p.brainID, tok); err != nil {
		fail("saving the token failed: " + err.Error())
		return
	}
	if err := g.cfgs.UpdateManifest(p.brainID, func(m *BrainManifest) error {
		if m.Calendar == nil || m.Calendar.Account != account {
			m.Calendar = &BrainCalendar{Account: account, Calendars: []string{account}, Default: account}
		}
		return nil
	}); err != nil {
		fail("saving the brain's calendar settings failed: " + err.Error())
		return
	}
	log.Printf("brain %s: Google Calendar connected (%s)", p.brainID, maskEmail(account))
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
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

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
		return nil, err
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
		return nil, fmt.Errorf("Google token request failed: %s", msg)
	}
	return &tr, nil
}

func (g *GoogleAuth) exchange(ctx context.Context, code, verifier string) (*googleToken, error) {
	c := g.Client()
	if c == nil {
		return nil, errors.New("the Google OAuth client was removed")
	}
	tr, err := g.postToken(ctx, c, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {g.redirectURI()},
	})
	if err != nil {
		return nil, err
	}
	return &googleToken{
		Token: tr.AccessToken, RefreshToken: tr.RefreshToken, TokenURI: googleTokenEndpoint,
		ClientID: c.ClientID, ClientSecret: c.ClientSecret, Scopes: []string{calendarScope},
		Expiry: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Format("2006-01-02T15:04:05.000000Z"),
	}, nil
}

// saveToken writes the brain's token, plus a copy of the client as its
// credentials file when there is none (tools fall back to it to sign in again).
// Neither write follows a symlink: writeFileAtomic renames a new file over
// the token, replacing whatever is there, and O_EXCL creates the
// credentials file only where nothing is.
func (g *GoogleAuth) saveToken(brainID string, tok *googleToken) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(g.tokenPath(brainID), data, 0o600, false); err != nil {
		return err
	}
	if c := g.Client(); c != nil {
		f, err := os.OpenFile(g.credentialsPath(brainID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = f.Write(c.raw)
			f.Close()
		}
	}
	return nil
}

// readCredentialFile reads a brain's file in the credentials dir without
// following a symlink. Each brain's sandbox lets it write its own token
// file, so a brain can put a symlink there to another brain's token: the
// bridge, outside the sandbox, would refresh that token and write it back
// where this brain reads it, or revoke it on disconnect. The dir itself is
// not brain-writable, so the last path component is the one to check. A
// hard link would do the same; the sandbox refuses those, and a file with
// more than one link is refused here as well. The open does not block, so
// a FIFO cannot hang the dashboard.
func readCredentialFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s: %w", path, errSymlink)
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
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
		return nil, fmt.Errorf("%s: it has more than one link, which the bridge does not trust", path)
	}
	return io.ReadAll(f)
}

func (g *GoogleAuth) loadToken(brainID string) (*googleToken, error) {
	data, err := readCredentialFile(g.tokenPath(brainID))
	if err != nil {
		return nil, err
	}
	var t googleToken
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("unreadable token file: %w", err)
	}
	return &t, nil
}

// accessToken returns a valid access token, refreshing (and saving) it when
// it has expired.
func (g *GoogleAuth) accessToken(ctx context.Context, brainID string) (*googleToken, error) {
	t, err := g.loadToken(brainID)
	if err != nil {
		return nil, err
	}
	exp, _ := time.Parse(time.RFC3339Nano, t.Expiry)
	if t.Token != "" && time.Until(exp) > time.Minute {
		return t, nil
	}
	if t.RefreshToken == "" {
		return nil, errors.New("the token has no refresh token — connect again")
	}
	// The brain can write its token file, so the client in it is not
	// trusted: the refresh uses yours, at Google's fixed endpoint. A
	// refresh token works only with the client that issued it, so a token
	// from another client cannot be refreshed here.
	c := g.Client()
	if c == nil {
		return nil, errors.New("add your Google OAuth client first (Settings → Google sign-in)")
	}
	if t.ClientID != c.ClientID {
		return nil, errors.New("this brain signed in with another Google OAuth client than the one in Settings → Google sign-in — connect it again")
	}
	tr, err := g.postToken(ctx, c, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}})
	if err != nil {
		return nil, err
	}
	t.Token = tr.AccessToken
	t.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Format("2006-01-02T15:04:05.000000Z")
	if data, err := json.MarshalIndent(t, "", "  "); err == nil {
		// A rename: a symlink planted since the read is replaced, not followed.
		_ = writeFileAtomic(g.tokenPath(brainID), data, 0o600, false)
	}
	return t, nil
}

// CalendarEntry is one calendar of the signed-in account.
type CalendarEntry struct {
	ID         string `json:"id"`
	Summary    string `json:"summary"`
	Primary    bool   `json:"primary"`
	AccessRole string `json:"access_role"`
	Color      string `json:"color,omitempty"`
}

func (g *GoogleAuth) calendarList(ctx context.Context, tok *googleToken) ([]CalendarEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleAPIBase+"/calendar/v3/users/me/calendarList?maxResults=250", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
		if e.Error.Message == "" {
			e.Error.Message = resp.Status
		}
		return nil, errors.New(e.Error.Message)
	}
	var body struct {
		Items []struct {
			ID              string `json:"id"`
			Summary         string `json:"summary"`
			SummaryOverride string `json:"summaryOverride"`
			Primary         bool   `json:"primary"`
			AccessRole      string `json:"accessRole"`
			BackgroundColor string `json:"backgroundColor"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]CalendarEntry, 0, len(body.Items))
	for _, it := range body.Items {
		name := it.SummaryOverride
		if name == "" {
			name = it.Summary
		}
		out = append(out, CalendarEntry{ID: it.ID, Summary: name, Primary: it.Primary, AccessRole: it.AccessRole, Color: it.BackgroundColor})
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

// Disconnect revokes the token at Google and deletes the token file. A
// token file that is a symlink is not read, so nothing is revoked through
// it, and the remove takes the symlink away, not what it points to.
func (g *GoogleAuth) Disconnect(ctx context.Context, brainID string) error {
	if t, err := g.loadToken(brainID); err == nil && t.RefreshToken != "" {
		form := url.Values{"token": {t.RefreshToken}}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, googleRevokeEndpoint, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if resp, err := googleHTTP.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	if err := os.Remove(g.tokenPath(brainID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
	writeJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiSetGoogleClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSON string `json:"json"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := b.google.SetClient([]byte(strings.TrimSpace(req.JSON)))
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	note := ""
	if c.Type == "web" {
		note = "This is a Web application client: add " + b.google.redirectURI() + " to its Authorized redirect URIs in Google Cloud Console, or create a Desktop app client instead."
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "type": c.Type, "note": note})
}

func (b *Bridge) apiDeleteGoogleClient(w http.ResponseWriter, r *http.Request) {
	if err := os.Remove(b.google.clientPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (b *Bridge) calendarBrain(w http.ResponseWriter, r *http.Request) *Brain {
	br := b.brainMgr.Get(r.PathValue("id"))
	if br == nil {
		writeErr(w, http.StatusNotFound, "no such brain")
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
		writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"auth_url": u})
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
	}
	if m.Calendar != nil {
		resp["account"], resp["selected"], resp["default"] = m.Calendar.Account, nz(m.Calendar.Calendars), m.Calendar.Default
	}
	g.mu.Lock()
	if e := g.lastError[br.ID()]; e != "" {
		resp["last_error"] = e
	}
	g.mu.Unlock()
	// Lstat: a symlink there shows as connected, with the reason it is not
	// used in list_error.
	if _, err := os.Lstat(g.tokenPath(br.ID())); err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["connected"] = true
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
		}
	}
	writeJSON(w, http.StatusOK, resp)
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
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Calendars) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "choose at least one calendar (or disconnect)")
		return
	}
	if req.Default == "" || !contains(req.Calendars, req.Default) {
		req.Default = req.Calendars[0]
	}
	err := b.cfgs.UpdateManifest(br.ID(), func(m *BrainManifest) error {
		account := req.Account
		if account == "" && m.Calendar != nil {
			account = m.Calendar.Account
		}
		m.Calendar = &BrainCalendar{Account: account, Calendars: req.Calendars, Default: req.Default}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (b *Bridge) apiCalendarDisconnect(w http.ResponseWriter, r *http.Request) {
	br := b.calendarBrain(w, r)
	if br == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := b.google.Disconnect(ctx, br.ID()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = b.cfgs.UpdateManifest(br.ID(), func(m *BrainManifest) error { m.Calendar = nil; return nil })
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

// calendarEnv tells the calendar tool which brain is running and which
// calendars it may use.
func calendarEnv(id string, m BrainManifest, credsDir string) []string {
	env := []string{"BRIDGE_BRAIN_ID=" + id, "BRIDGE_CREDENTIALS_DIR=" + credsDir}
	if m.Calendar != nil && len(m.Calendar.Calendars) > 0 {
		env = append(env, "BRIDGE_CALENDARS="+strings.Join(m.Calendar.Calendars, ","), "BRIDGE_CALENDAR_DEFAULT="+m.Calendar.Default)
	}
	return env
}
