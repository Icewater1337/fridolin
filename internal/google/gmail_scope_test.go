package google

import (
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// TestGmailSignInScopes: a calendar sign-in never asks for Gmail and a token
// without it still reports calendar as fine; the Gmail sign-in asks for the
// calendar scopes and GmailScope together, without include_granted_scopes,
// and the resulting token reports Gmail as granted without counting it as
// the broad calendar access of older versions.
func TestGmailSignInScopes(t *testing.T) {
	t.Parallel()
	srv, _ := fakeGoogle(t)
	root := t.TempDir()
	current := manifest.Manifest{ID: "a"}
	options := googleTestOptions(Options{
		ClientPath: filepath.Join(root, "client.json"), RedirectURI: "http://127.0.0.1:8765/",
		CredsDir:   func() string { return root },
		Configured: func(id string) bool { return id == "a" },
		UpdateManifest: func(id string, update func(*manifest.Manifest) error) error {
			return update(&current)
		},
	}, srv)
	options.Endpoints.Auth = srv.URL + "/authorize"
	g := New(options)
	if _, err := g.SetClient([]byte(fmt.Sprintf(testClientJSON, srv.URL))); err != nil {
		t.Fatal(err)
	}
	connect := func(start func(string) (string, error)) url.Values {
		auth, err := start("a")
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(auth)
		q := u.Query()
		if _, err := g.Callback(t.Context(), url.Values{"state": {q.Get("state")}, "code": {"good-code"}}); err != nil {
			t.Fatal(err)
		}
		return q
	}

	q := connect(g.StartSignIn)
	if strings.Contains(q.Get("scope"), GmailScope) {
		t.Fatalf("a calendar sign-in asked for Gmail: %s", q.Get("scope"))
	}
	if st := g.CalendarStatus(t.Context(), "a", current.Calendar, false); !st.Connected || st.Gmail || st.LastError != "" {
		t.Fatalf("calendar-only token: %+v", st)
	}

	q = connect(g.StartGmailSignIn)
	scopes := strings.Fields(q.Get("scope"))
	for _, want := range append(CalendarScopes(), GmailScope) {
		if !slices.Contains(scopes, want) {
			t.Fatalf("Gmail sign-in did not ask for %s: %v", want, scopes)
		}
	}
	if q.Has("include_granted_scopes") {
		t.Fatal("include_granted_scopes would fold other grants into the brain's token")
	}
	if st := g.CalendarStatus(t.Context(), "a", current.Calendar, false); !st.Gmail || st.BroadAccess || st.LastError != "" {
		t.Fatalf("Gmail token: %+v", st)
	}
}
