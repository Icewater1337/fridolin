package google

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// fakeGmailGoogle is Google's token and revocation endpoints and Gmail's
// profile for the Gmail sign-in tests: revoked lists the refresh tokens
// revoked.
type fakeGmailGoogle struct {
	srv *httptest.Server

	mu      sync.Mutex
	revoked []string
	// scope is what the token endpoint grants; "" grants GmailScope.
	scope string
}

func newFakeGmailGoogle(t *testing.T) *fakeGmailGoogle {
	t.Helper()
	f := &fakeGmailGoogle{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			f.mu.Lock()
			scope := f.scope
			f.mu.Unlock()
			if scope == "" {
				scope = GmailScope
			}
			if r.PostForm.Get("refresh_token") == "REVOKED" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":"ACCESS-G","refresh_token":"RT-G","expires_in":3600,"scope":%q}`, scope)
		case "/revoke":
			f.mu.Lock()
			f.revoked = append(f.revoked, r.FormValue("token"))
			f.mu.Unlock()
		case "/gmail/v1/users/me/profile":
			switch r.Header.Get("Authorization") {
			case "Bearer ACCESS-G":
				fmt.Fprint(w, `{"emailAddress":"you@example.com","messagesTotal":3}`)
			case "Bearer DISABLED":
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error":{"code":403,"message":"Gmail API has not been used in project 123 before or it is disabled."}}`)
			default:
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGmailGoogle) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.revoked)
}

func gmailAuth(t *testing.T, f *fakeGmailGoogle, configured func(string) bool) *Auth {
	t.Helper()
	root := t.TempDir()
	creds := filepath.Join(root, "creds")
	if err := os.MkdirAll(creds, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := googleTestOptions(Options{ClientPath: filepath.Join(root, "client.json"), RedirectURI: "http://127.0.0.1:8765/",
		CredsDir: func() string { return creds }, Configured: configured,
		UpdateManifest: func(string, func(*manifest.Manifest) error) error { return nil }}, f.srv)
	opts.Endpoints.Auth = f.srv.URL + "/authorize"
	return New(opts)
}

// TestGmailSignIn: a brain's Gmail sign-in asks for GmailScope alone, saves
// its own token with the mailbox's address, and leaves the calendar's token
// alone; the calendar's sign-in never asks for Gmail. A sign-in that lacks
// the scope, or whose brain went meanwhile, saves nothing and says why.
func TestGmailSignIn(t *testing.T) {
	t.Parallel()
	f := newFakeGmailGoogle(t)
	brains := map[string]bool{"a": true}
	var mu sync.Mutex
	g := gmailAuth(t, f, func(id string) bool { mu.Lock(); defer mu.Unlock(); return brains[id] })
	if _, err := g.StartGmailSignIn("a"); !errors.Is(err, ErrNoClient) {
		t.Fatalf("a sign-in without a client: %v", err)
	}
	if _, err := g.SetClient([]byte(fmt.Sprintf(testClientJSON, "https://ignored.example.com"))); err != nil {
		t.Fatal(err)
	}
	calendar, err := g.StartSignIn("a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(calendar, url.QueryEscape(GmailScope)) {
		t.Fatalf("the calendar's sign-in asked for Gmail: %s", calendar)
	}
	signIn := func() string {
		t.Helper()
		auth, err := g.StartGmailSignIn("a")
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(auth)
		q := u.Query()
		if q.Get("scope") != GmailScope || q.Has("include_granted_scopes") || q.Get("access_type") != "offline" {
			t.Fatalf("authorization URL %s", auth)
		}
		back, err := g.Callback(t.Context(), url.Values{"state": {q.Get("state")}, "code": {"good"}})
		if err != nil {
			t.Fatal(err)
		}
		return back
	}

	if back := signIn(); back != "/#/brains/a" {
		t.Errorf("back to %q", back)
	}
	data, err := os.ReadFile(g.GmailTokenPath("a"))
	if err != nil {
		t.Fatal(err)
	}
	var tok Token
	if err := json.Unmarshal(data, &tok); err != nil || tok.RefreshToken != "RT-G" || !slices.Equal(tok.Scopes, []string{GmailScope}) || tok.Account != "you@example.com" {
		t.Errorf("the token: %+v %v", tok, err)
	}
	if filepath.Base(g.GmailTokenPath("a")) != "google_gmail_token_a.json" {
		t.Errorf("the token's name: %s", g.GmailTokenPath("a"))
	}
	if _, err := os.Stat(g.TokenPath("a")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the calendar's token: %v", err)
	}
	if s := g.GmailStatus(t.Context(), "a", true); !s.Connected || s.Works == nil || !*s.Works || s.Account != "you@example.com" || s.LastError != "" || s.Scope != GmailScope {
		t.Errorf("status %+v", s)
	}

	// A consent screen with the box unticked.
	f.mu.Lock()
	f.scope = "openid"
	f.mu.Unlock()
	if err := os.Remove(g.GmailTokenPath("a")); err != nil {
		t.Fatal(err)
	}
	signIn()
	if s := g.GmailStatus(t.Context(), "a", false); s.Connected || !strings.Contains(s.LastError, "missing "+GmailScope) {
		t.Errorf("a sign-in without the scope: %+v", s)
	}
	f.mu.Lock()
	f.scope = ""
	f.mu.Unlock()

	// A brain removed while it signed in.
	mu.Lock()
	brains["a"] = false
	mu.Unlock()
	signIn()
	if s := g.GmailStatus(t.Context(), "a", false); s.Connected || !strings.Contains(s.LastError, "removed") {
		t.Errorf("a removed brain's sign-in: %+v", s)
	}
}

// TestGmailProblems: what a brain hears when a Gmail call cannot be made:
// not connected, the Gmail API not enabled, a sign-in Google no longer
// takes. None of it sends the owner to the calendar.
func TestGmailProblems(t *testing.T) {
	t.Parallel()
	f := newFakeGmailGoogle(t)
	g := gmailAuth(t, f, func(string) bool { return true })
	_, err := g.GmailAccessToken(t.Context(), "a")
	if _, msg := GmailProblem(err); !strings.Contains(msg, "Gmail is not connected") || !strings.Contains(msg, "Connect Gmail") {
		t.Errorf("not connected: %q", msg)
	}
	_, err = g.gmailAddress(t.Context(), &Token{Token: "DISABLED", Scopes: []string{GmailScope}})
	if _, msg := GmailProblem(err); !strings.Contains(msg, "Gmail API is not enabled") || !strings.Contains(msg, "403") {
		t.Errorf("disabled: %q", msg)
	}
	_, err = g.gmailAddress(t.Context(), &Token{Token: "OLD", Scopes: []string{GmailScope}})
	if _, msg := GmailProblem(err); !strings.Contains(msg, "connect Gmail again") || strings.Contains(msg, "alendar") {
		t.Errorf("unauthorized: %q", msg)
	}
	_, err = g.gmailAddress(t.Context(), &Token{Token: "ACCESS-G", Scopes: calendarScopes})
	if status, msg := GmailProblem(err); status != http.StatusPreconditionFailed || !errors.Is(err, ErrNoGmail) || !strings.Contains(msg, "connect Gmail again") {
		t.Errorf("a token without the scope: %d %q", status, msg)
	}

	if _, err := g.SetClient([]byte(fmt.Sprintf(testClientJSON, "https://ignored.example.com"))); err != nil {
		t.Fatal(err)
	}
	writeToken(t, g.GmailTokenPath("a"), Token{Token: "X", RefreshToken: "REVOKED", ClientID: "123-abc.apps.googleusercontent.com", Scopes: []string{GmailScope}, Expiry: "2000-01-01T00:00:00Z"})
	_, err = g.GmailAccessToken(t.Context(), "a")
	if _, msg := GmailProblem(err); !strings.Contains(msg, "revoked or has expired") || !strings.Contains(msg, "Gmail") {
		t.Errorf("revoked: %q", msg)
	}
	s := g.GmailStatus(t.Context(), "a", true)
	if s.Works == nil || *s.Works || !strings.Contains(s.CheckError, "revoked or has expired") {
		t.Errorf("the check of a revoked sign-in: %+v", s)
	}
}

// TestDisconnectGmailSharesNoGrant: a Gmail disconnect revokes at Google
// only when no other token from the same client may hold its grant: the
// brain's own calendar or contacts sign-in, or another brain's Gmail for
// the same account. The calendar's and the contacts' disconnects leave a
// grant alone that a Gmail token may hold, in turn.
func TestDisconnectGmailSharesNoGrant(t *testing.T) {
	t.Parallel()
	f := newFakeGmailGoogle(t)
	g := gmailAuth(t, f, func(string) bool { return true })
	client := "123-abc.apps.googleusercontent.com"
	gmail := Token{Token: "A", RefreshToken: "RT-GMAIL", ClientID: client, Scopes: []string{GmailScope}, Account: "you@example.com"}
	calendar := Token{Token: "B", RefreshToken: "RT-CALENDAR", ClientID: client, Scopes: calendarScopes, Account: "you@example.com"}
	contacts := Token{Token: "C", RefreshToken: "RT-CONTACTS", ClientID: client, Scopes: []string{ContactsScope}}

	writeToken(t, g.GmailTokenPath("a"), gmail)
	writeToken(t, g.TokenPath("a"), calendar)
	revoked, note, err := g.DisconnectGmail(t.Context(), "a", false)
	if err != nil || revoked || !strings.Contains(note, "brain a's calendar") {
		t.Errorf("beside the brain's calendar: %v %q %v", revoked, note, err)
	}
	if g.GmailConnected("a") {
		t.Error("the token is still there")
	}

	writeToken(t, g.GmailTokenPath("a"), gmail)
	revoked, note, err = g.Disconnect(t.Context(), "a", false)
	if err != nil || revoked || !strings.Contains(note, "brain a's Gmail") {
		t.Errorf("a calendar beside Gmail: %v %q %v", revoked, note, err)
	}
	writeToken(t, g.ContactsTokenPath("core"), contacts)
	revoked, note, err = g.DisconnectContacts(t.Context(), "core", false)
	if err != nil || revoked || !strings.Contains(note, "brain a's Gmail") {
		t.Errorf("contacts beside Gmail: %v %q %v", revoked, note, err)
	}

	// Another brain's Gmail for another account shares nothing.
	writeToken(t, g.GmailTokenPath("b"), Token{Token: "D", RefreshToken: "RT-OTHER", ClientID: client, Scopes: []string{GmailScope}, Account: "other@example.com"})
	revoked, note, err = g.DisconnectGmail(t.Context(), "a", false)
	if err != nil || !revoked || note != "" {
		t.Errorf("alone: %v %q %v", revoked, note, err)
	}
	if gone := f.revokedTokens(); !slices.Equal(gone, []string{"RT-GMAIL"}) {
		t.Errorf("revoked %q", gone)
	}
	if !g.GmailConnected("b") {
		t.Error("the other brain's Gmail went")
	}
	if _, _, err := g.DisconnectGmail(t.Context(), "a", false); err != nil {
		t.Errorf("disconnecting twice: %v", err)
	}
}
