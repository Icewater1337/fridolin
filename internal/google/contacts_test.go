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
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// fakePeople is Google's token, revocation and People API endpoints for
// the Contacts tests: queries lists each search's query in order, and
// revoked the refresh tokens revoked.
type fakePeople struct {
	srv *httptest.Server

	mu      sync.Mutex
	queries []string
	revoked []string
	// scope is what the token endpoint grants; "" grants the Contacts scope.
	scope string
}

func newFakePeople(t *testing.T) *fakePeople {
	t.Helper()
	f := &fakePeople{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			f.mu.Lock()
			scope := f.scope
			f.mu.Unlock()
			if scope == "" {
				scope = ContactsScope
			}
			if r.PostForm.Get("refresh_token") == "REVOKED" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":"ACCESS-C","refresh_token":"RT-C","expires_in":3600,"scope":%q}`, scope)
		case "/revoke":
			f.mu.Lock()
			f.revoked = append(f.revoked, r.FormValue("token"))
			f.mu.Unlock()
		case "/v1/people:searchContacts":
			q := r.URL.Query()
			if q.Get("readMask") != "names,phoneNumbers" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":{"code":400,"message":"readMask is required"}}`)
				return
			}
			switch r.Header.Get("Authorization") {
			case "Bearer ACCESS-C":
			case "Bearer DISABLED":
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error":{"code":403,"message":"People API has not been used in project 123 before or it is disabled.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`)
				return
			default:
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`)
				return
			}
			f.mu.Lock()
			f.queries = append(f.queries, q.Get("query"))
			f.mu.Unlock()
			if q.Get("query") != "Alex" {
				fmt.Fprint(w, `{}`)
				return
			}
			fmt.Fprint(w, `{"results":[
				{"person":{"resourceName":"people/c1","names":[{"displayName":"Alexander Example","metadata":{"primary":false}},{"displayName":"Alex Example","metadata":{"primary":true}}],
					"phoneNumbers":[{"value":"+1 555-987-6543","canonicalForm":"+15559876543","type":"mobile","formattedType":"Mobile"},{"value":"  ","type":"home"},{"value":"555 0100","type":"work","formattedType":"Work"}]}},
				{"person":{"resourceName":"people/c2","names":[{"displayName":"Alex Other"}]}}
			]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePeople) seen() (queries, revoked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries), slices.Clone(f.revoked)
}

// contactsAuth is an Auth on f with a client, whose brains are core and,
// once removed is false, nothing else.
func contactsAuth(t *testing.T, f *fakePeople, configured func(string) bool) *Auth {
	t.Helper()
	root := t.TempDir()
	creds := filepath.Join(root, "creds")
	if err := os.MkdirAll(creds, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := googleTestOptions(Options{ClientPath: filepath.Join(root, "client.json"), RedirectURI: "http://127.0.0.1:8765/",
		CredsDir: func() string { return creds }, Configured: configured,
		UpdateManifest: func(string, func(*manifest.Manifest) error) error { return nil }}, f.srv)
	opts.Endpoints.People = f.srv.URL
	opts.Endpoints.Auth = f.srv.URL + "/authorize"
	return New(opts)
}

func writeToken(t *testing.T, path string, tok Token) {
	t.Helper()
	data, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestContactsSignIn: a brain's Contacts sign-in asks for the Contacts
// scope alone, with the client sign-ins use, and saves its own token next
// to the calendar's, which it leaves alone; a sign-in that lacks the scope,
// or whose brain went meanwhile, saves nothing and says why on the page.
func TestContactsSignIn(t *testing.T) {
	t.Parallel()
	f := newFakePeople(t)
	brains := map[string]bool{"core": true}
	var mu sync.Mutex
	g := contactsAuth(t, f, func(id string) bool { mu.Lock(); defer mu.Unlock(); return brains[id] })
	if _, err := g.StartContactsSignIn("core"); !errors.Is(err, ErrNoClient) {
		t.Fatalf("a sign-in without a client: %v", err)
	}
	if _, err := g.SetClient([]byte(fmt.Sprintf(testClientJSON, "https://ignored.example.com"))); err != nil {
		t.Fatal(err)
	}
	signIn := func() string {
		t.Helper()
		auth, err := g.StartContactsSignIn("core")
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(auth)
		q := u.Query()
		if q.Get("scope") != ContactsScope || q.Get("client_id") != "123-abc.apps.googleusercontent.com" || q.Get("access_type") != "offline" {
			t.Fatalf("authorization URL %s", auth)
		}
		back, err := g.Callback(t.Context(), url.Values{"state": {q.Get("state")}, "code": {"good"}})
		if err != nil {
			t.Fatal(err)
		}
		return back
	}

	if back := signIn(); back != "/#/brains/core" {
		t.Errorf("back to %q", back)
	}
	data, err := os.ReadFile(g.ContactsTokenPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	var tok Token
	if err := json.Unmarshal(data, &tok); err != nil || tok.RefreshToken != "RT-C" || !slices.Equal(tok.Scopes, []string{ContactsScope}) {
		t.Errorf("the token: %+v %v", tok, err)
	}
	if filepath.Base(g.ContactsTokenPath("core")) != "google_contacts_token_core.json" {
		t.Errorf("the token's name: %s", g.ContactsTokenPath("core"))
	}
	if _, err := os.Stat(g.TokenPath("core")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the calendar's token: %v", err)
	}
	if s := g.ContactsStatus(t.Context(), "core", false); !s.Connected || s.Works != nil || s.LastError != "" || s.Scope != ContactsScope {
		t.Errorf("status %+v", s)
	}

	// A consent screen with the box unticked.
	f.mu.Lock()
	f.scope = "openid"
	f.mu.Unlock()
	if err := os.Remove(g.ContactsTokenPath("core")); err != nil {
		t.Fatal(err)
	}
	signIn()
	if s := g.ContactsStatus(t.Context(), "core", false); s.Connected || !strings.Contains(s.LastError, "missing "+ContactsScope) {
		t.Errorf("a sign-in without the scope: %+v", s)
	}
	f.mu.Lock()
	f.scope = ""
	f.mu.Unlock()

	// A brain removed while it signed in.
	mu.Lock()
	brains["core"] = false
	mu.Unlock()
	signIn()
	if s := g.ContactsStatus(t.Context(), "core", false); s.Connected || !strings.Contains(s.LastError, "removed") {
		t.Errorf("a removed brain's sign-in: %+v", s)
	}
}

// TestSearchContacts: a search starts with the warm-up request, an empty
// query, then asks for names and phone numbers; each match has its primary
// name and its numbers as written, with Google's canonical form and label,
// and an empty number is left out.
func TestSearchContacts(t *testing.T) {
	t.Parallel()
	f := newFakePeople(t)
	g := contactsAuth(t, f, func(string) bool { return true })
	got, err := g.SearchContacts(t.Context(), &Token{Token: "ACCESS-C"}, "Alex", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Contact{
		{Name: "Alex Example", Phones: []ContactPhone{{Value: "+1 555-987-6543", Canonical: "+15559876543", Type: "Mobile"}, {Value: "555 0100", Type: "Work"}}},
		{Name: "Alex Other", Phones: []ContactPhone{}},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("matches %s, want %s", gotJSON, wantJSON)
	}
	if queries, _ := f.seen(); !slices.Equal(queries, []string{"", "Alex"}) {
		t.Errorf("queries %q, want the warm-up first", queries)
	}
	if got, err := g.SearchContacts(t.Context(), &Token{Token: "ACCESS-C"}, "Nobody", 3); err != nil || len(got) != 0 || got == nil {
		t.Errorf("no match: %#v %v", got, err)
	}
}

// TestContactsProblems: what a brain hears when the lookup cannot be
// made: not connected, the People API not enabled, a sign-in Google no
// longer takes.
func TestContactsProblems(t *testing.T) {
	t.Parallel()
	f := newFakePeople(t)
	g := contactsAuth(t, f, func(string) bool { return true })
	_, err := g.ContactsAccessToken(t.Context(), "core")
	if msg := ContactsProblem(err); !strings.Contains(msg, "not connected") || !strings.Contains(msg, "Connect Google Contacts") {
		t.Errorf("not connected: %q", msg)
	}
	_, err = g.SearchContacts(t.Context(), &Token{Token: "DISABLED"}, "Alex", 0)
	if msg := ContactsProblem(err); !strings.Contains(msg, "People API is not enabled") || !strings.Contains(msg, "403") {
		t.Errorf("disabled: %q", msg)
	}
	_, err = g.SearchContacts(t.Context(), &Token{Token: "OLD"}, "Alex", 0)
	if msg := ContactsProblem(err); !strings.Contains(msg, "connect Google Contacts again") {
		t.Errorf("unauthorized: %q", msg)
	}

	// An expired token whose grant Google revoked, with a client to refresh it.
	if _, err := g.SetClient([]byte(fmt.Sprintf(testClientJSON, "https://ignored.example.com"))); err != nil {
		t.Fatal(err)
	}
	writeToken(t, g.ContactsTokenPath("core"), Token{Token: "X", RefreshToken: "REVOKED", ClientID: "123-abc.apps.googleusercontent.com", Expiry: "2000-01-01T00:00:00Z"})
	_, err = g.ContactsAccessToken(t.Context(), "core")
	if msg := ContactsProblem(err); !strings.Contains(msg, "revoked or has expired") {
		t.Errorf("revoked: %q", msg)
	}
	s := g.ContactsStatus(t.Context(), "core", true)
	if s.Works == nil || *s.Works || !strings.Contains(s.CheckError, "revoked or has expired") {
		t.Errorf("the check of a revoked sign-in: %+v", s)
	}
}

// TestContactsTokenRefresh: an expired Contacts token is refreshed with the
// client and saved, as the calendar's is, and the check then works.
func TestContactsTokenRefresh(t *testing.T) {
	t.Parallel()
	f := newFakePeople(t)
	g := contactsAuth(t, f, func(string) bool { return true })
	if _, err := g.SetClient([]byte(fmt.Sprintf(testClientJSON, "https://ignored.example.com"))); err != nil {
		t.Fatal(err)
	}
	writeToken(t, g.ContactsTokenPath("core"), Token{Token: "STALE", RefreshToken: "RT-OLD", ClientID: "123-abc.apps.googleusercontent.com", Scopes: []string{ContactsScope}, Expiry: "2000-01-01T00:00:00Z"})
	s := g.ContactsStatus(t.Context(), "core", true)
	if s.Works == nil || !*s.Works || s.CheckError != "" {
		t.Fatalf("the check: %+v", s)
	}
	data, _ := os.ReadFile(g.ContactsTokenPath("core"))
	var tok Token
	_ = json.Unmarshal(data, &tok)
	if exp, err := time.Parse(time.RFC3339Nano, tok.Expiry); tok.Token != "ACCESS-C" || tok.RefreshToken != "RT-C" || err != nil || time.Until(exp) < 30*time.Minute {
		t.Errorf("the refreshed token: %+v", tok)
	}
}

// TestDisconnectContactsSharesNoGrant: a disconnect revokes the Contacts
// sign-in at Google only when no other token from the same client may
// hold its grant: a calendar token of any account (the Contacts token
// names none), or another brain's Contacts token. A calendar disconnect
// leaves a grant alone that a Contacts token may hold, in turn.
func TestDisconnectContactsSharesNoGrant(t *testing.T) {
	t.Parallel()
	f := newFakePeople(t)
	g := contactsAuth(t, f, func(string) bool { return true })
	client := "123-abc.apps.googleusercontent.com"
	contacts := Token{Token: "A", RefreshToken: "RT-CONTACTS", ClientID: client, Scopes: []string{ContactsScope}}
	calendar := Token{Token: "B", RefreshToken: "RT-CALENDAR", ClientID: client, Scopes: calendarScopes, Account: "you@example.com"}

	writeToken(t, g.ContactsTokenPath("core"), contacts)
	writeToken(t, g.TokenPath("main"), calendar)
	revoked, note, err := g.DisconnectContacts(t.Context(), "core", false)
	if err != nil || revoked || !strings.Contains(note, "brain main's calendar") {
		t.Errorf("beside a calendar of the same client: %v %q %v", revoked, note, err)
	}
	if g.ContactsConnected("core") {
		t.Error("the token is still there")
	}

	writeToken(t, g.ContactsTokenPath("core"), contacts)
	revoked, note, err = g.Disconnect(t.Context(), "main", false)
	if err != nil || revoked || !strings.Contains(note, "brain core's Google Contacts") {
		t.Errorf("a calendar beside Contacts of the same client: %v %q %v", revoked, note, err)
	}

	revoked, note, err = g.DisconnectContacts(t.Context(), "core", false)
	if err != nil || !revoked || note != "" {
		t.Errorf("alone: %v %q %v", revoked, note, err)
	}
	if _, gone := f.seen(); !slices.Equal(gone, []string{"RT-CONTACTS"}) {
		t.Errorf("revoked %q", gone)
	}
	// Nothing to disconnect is no error.
	if _, _, err := g.DisconnectContacts(t.Context(), "core", false); err != nil {
		t.Errorf("disconnecting twice: %v", err)
	}
}
