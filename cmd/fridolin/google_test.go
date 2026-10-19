package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/testutil"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

const testClientJSON = `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"TEST-CLIENT-SECRET","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"%s/token","redirect_uris":["http://localhost"]}}`

// fakeGoogle is a token endpoint plus a calendarList endpoint.
var fakeGoogle = testutil.FakeGoogle

// googleTestOptions supplies a private HTTP client and endpoints for a fake
// service, keeping the real authorization URL unless a test overrides it.
func googleTestOptions(options google.Options, srv *httptest.Server) google.Options {
	options.Endpoints = google.Endpoints{Token: srv.URL + "/token", Revoke: srv.URL + "/revoke", APIBase: srv.URL}
	client := *srv.Client()
	client.Timeout = 20 * time.Second
	options.HTTP = &client
	return options
}

func useGoogleServer(tb *testBridge, srv *httptest.Server) {
	tb.google = google.New(googleTestOptions(googleOptionsFor(tb.cfgs), srv))
}

func TestCalendarDashboardSignInFlow(t *testing.T) {
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/fam", "create_dir": true, "manifest": manifest.Manifest{ID: "fam", Name: "Fam"}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	// Without a client, connecting explains what to do.
	if w := tb.do("POST", "/api/brains/fam/calendar/connect", nil); w.Code != 412 {
		t.Errorf("connect without client: %d", w.Code)
	}
	w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	body := tb.do("GET", "/api/google/client", nil).Body.String()
	if strings.Contains(body, "TEST-CLIENT-SECRET") || !strings.Contains(body, `"configured":true`) {
		t.Errorf("client status leaks or is wrong: %s", body)
	}

	w = tb.do("POST", "/api/brains/fam/calendar/connect", nil)
	var start struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	u, err := url.Parse(start.AuthURL)
	if err != nil || u.Host != "accounts.google.com" {
		t.Fatalf("auth url %q", start.AuthURL)
	}
	q := u.Query()
	if q.Get("redirect_uri") != "http://127.0.0.1:"+portOf(tb)+"/" || q.Get("code_challenge_method") != "S256" ||
		q.Get("access_type") != "offline" || q.Get("scope") != strings.Join(calendarScopes, " ") || q.Get("state") == "" {
		t.Errorf("auth params = %v", q)
	}
	if q.Has("include_granted_scopes") {
		t.Error("the sign-in folds the client's other grants into the brain's token")
	}

	// A made-up state is refused and changes nothing.
	cb := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8765/?"+query, nil)
		r.Host = "127.0.0.1:8765"
		r.Header.Set("Sec-Fetch-Site", "cross-site") // Google redirecting the browser
		w := httptest.NewRecorder()
		tb.h.ServeHTTP(w, r)
		return w
	}
	if w := cb("state=forged&code=x"); w.Code != 400 || w.Body.String() != "This sign-in link expired or was already used. Start again from the dashboard.\n" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("forged state: %d %q (%s)", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}

	// Google returns: token exchanged with the PKCE verifier, files written.
	w = cb("state=" + url.QueryEscape(q.Get("state")) + "&code=good-code")
	if w.Code != 302 || w.Header().Get("Location") != "/#/brains/fam" || w.Body.String() != "<a href=\"/#/brains/fam\">Found</a>.\n\n" {
		t.Fatalf("callback: %d %s %q", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if (*got)["code_verifier"] == "" || (*got)["code"] != "good-code" || (*got)["client_secret"] != "TEST-CLIENT-SECRET" {
		t.Errorf("token request = %v", *got)
	}
	tokPath := filepath.Join(tb.cfgs.Get().DataDir, "google_calendar_token_fam.json")
	data, err := os.ReadFile(tokPath)
	if err != nil {
		t.Fatal(err)
	}
	var tok google.Token
	_ = json.Unmarshal(data, &tok)
	if tok.RefreshToken != "RT-1" || tok.ClientID == "" || tok.Account != "you@example.com" || !slices.Equal(tok.Scopes, calendarScopes) {
		t.Errorf("token file = %+v", tok)
	}
	if st, _ := os.Stat(tokPath); st.Mode().Perm() != 0o600 {
		t.Errorf("token mode %v", st.Mode().Perm())
	}
	// Older versions copied the OAuth client, secret and all, next to the
	// token for the calendar tool in the brain.
	if _, err := os.Stat(filepath.Join(tb.cfgs.Get().DataDir, "google_calendar_credentials_fam.json")); !os.IsNotExist(err) {
		t.Errorf("a copy of the OAuth client was written next to the token (%v)", err)
	}
	// The state is single-use.
	if w := cb("state=" + url.QueryEscape(q.Get("state")) + "&code=good-code"); w.Code != 400 {
		t.Errorf("replayed state: %d", w.Code)
	}

	// The manifest records the account; the primary calendar is selected.
	m, _ := manifest.Read(filepath.Join(tb.home, "fam"))
	if m.Calendar == nil || m.Calendar.Account != "you@example.com" || m.Calendar.Default != "you@example.com" {
		t.Fatalf("manifest calendar = %+v", m.Calendar)
	}
	if tb.brainMgr.Get("fam").Manifest().Calendar == nil {
		t.Error("the running brain did not pick up its calendar")
	}

	// Status lists the account's calendars, and never returns tokens.
	st := tb.do("GET", "/api/brains/fam/calendar", nil).Body.String()
	if !strings.Contains(st, "team@example.com") || strings.Contains(st, "RT-1") || strings.Contains(st, "ACCESS-1") {
		t.Errorf("status = %s", st)
	}

	// Choosing calendars updates brain.json and the environment the tool sees.
	w = tb.do("PUT", "/api/brains/fam/calendar", map[string]any{"calendars": []string{"you@example.com", "team@example.com"}, "default": "team@example.com"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	env := strings.Join(google.CalendarEnv("fam", tb.brainMgr.Get("fam").Manifest(), tb.google.CredsDir()), " ")
	if !strings.Contains(env, "BRIDGE_CALENDARS=you@example.com,team@example.com") || !strings.Contains(env, "BRIDGE_CALENDAR_DEFAULT=team@example.com") {
		t.Errorf("env = %s", env)
	}
	var views []httpapi.BrainView
	_ = json.Unmarshal(tb.do("GET", "/api/brains", nil).Body.Bytes(), &views)
	if views[0].Status != "ok" {
		t.Errorf("a dashboard change must not show as drift: %+v", views[0])
	}

	// Disconnect revokes at Google and removes the token.
	if w := tb.do("DELETE", "/api/brains/fam/calendar", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if (*got)["revoked"] != "RT-1" {
		t.Error("token not revoked")
	}
	if _, err := os.Stat(tokPath); !os.IsNotExist(err) {
		t.Error("token file not removed")
	}
}

func TestCalendarSignInErrorsAreShown(t *testing.T) {
	srv, _ := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	tb.addFolderBrain(t, manifest.Manifest{ID: "p", Name: "P"})
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	start := func() string {
		var r struct {
			AuthURL string `json:"auth_url"`
		}
		_ = json.Unmarshal(tb.do("POST", "/api/brains/p/calendar/connect", nil).Body.Bytes(), &r)
		u, _ := url.Parse(r.AuthURL)
		return u.Query().Get("state")
	}
	cb := func(query string) {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8765/?"+query, nil)
		r.Host = "127.0.0.1:8765"
		w := httptest.NewRecorder()
		tb.h.ServeHTTP(w, r)
		if w.Code != 302 || w.Header().Get("Location") != "/#/brains/p" {
			t.Fatalf("sign-in failure: %d %s %q", w.Code, w.Header().Get("Location"), w.Body.String())
		}
	}
	cb("state=" + start() + "&error=access_denied")
	if s := tb.do("GET", "/api/brains/p/calendar", nil).Body.String(); !strings.Contains(s, "cancelled") {
		t.Errorf("status after cancel = %s", s)
	}
	cb("state=" + start() + "&code=bad")
	if s := tb.do("GET", "/api/brains/p/calendar", nil).Body.String(); !strings.Contains(s, "Bad Request") {
		t.Errorf("status after bad code = %s", s)
	}
}

func portOf(tb *testBridge) string {
	return strings.TrimPrefix(tb.cfgs.Get().HTTPListen, "127.0.0.1:")
}

// TestCalendarTokenIsNotFollowed: a brain the sandbox does not hold (on
// Linux, or with the sandbox off) can write its own token file, so it can
// make it a link to another brain's token in the same folder. The
// dashboard's status page would then refresh that token and write it,
// refresh token and all, where the first brain reads it, and Disconnect
// would revoke it at Google. The bridge refuses such a token file, leaves
// the other brain's token alone, and removes only the link on disconnect.
func TestCalendarTokenIsNotFollowed(t *testing.T) {
	cases := []struct {
		name string
		link func(t *testing.T, own, other string)
		want string // in the status page's list_error
	}{
		{"symlink", func(t *testing.T, own, other string) { mustSymlink(t, other, own) }, fsx.ErrSymlink.Error()},
		{"relative symlink", func(t *testing.T, own, other string) { mustSymlink(t, filepath.Base(other), own) }, fsx.ErrSymlink.Error()},
		{"hard link", func(t *testing.T, own, other string) {
			if err := os.Link(other, own); err != nil {
				t.Fatal(err)
			}
		}, "more than one link"},
		{"FIFO", func(t *testing.T, own, _ string) {
			if err := syscall.Mkfifo(own, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := fakeGoogle(t)
			tb := newTestBridge(t, "")
			useGoogleServer(tb, srv)
			if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			for _, id := range []string{"a", "b"} {
				if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/" + id, "create_dir": true, "manifest": manifest.Manifest{ID: id, Name: strings.ToUpper(id)}}); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}
			// Brain b's token has expired, so using it means refreshing it.
			other := tb.google.TokenPath("b")
			bTok, _ := json.MarshalIndent(google.Token{
				Token: "B-ACCESS", RefreshToken: "B-REFRESH", TokenURI: srv.URL + "/token",
				ClientID: "123-abc.apps.googleusercontent.com", ClientSecret: "TEST-CLIENT-SECRET",
				Expiry: "2020-01-01T00:00:00.000000Z",
			}, "", "  ")
			if err := os.WriteFile(other, bTok, 0o600); err != nil {
				t.Fatal(err)
			}
			own := tb.google.TokenPath("a")
			tc.link(t, own, other)
			// A read that opened the FIFO would block until a writer came,
			// and hang the whole test binary rather than fail.
			do := func(method, path string) *httptest.ResponseRecorder {
				t.Helper()
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- tb.do(method, path, nil) }()
				select {
				case w := <-done:
					return w
				case <-time.After(5 * time.Second):
					t.Fatalf("%s %s hangs on a's token file", method, path)
					return nil
				}
			}

			var st struct {
				Connected bool   `json:"connected"`
				ListError string `json:"list_error"`
			}
			_ = json.Unmarshal(do("GET", "/api/brains/a/calendar").Body.Bytes(), &st)
			if !st.Connected || !strings.Contains(st.ListError, tc.want) {
				t.Errorf("status = %+v, want a's token refused for %q", st, tc.want)
			}
			if (*got)["refresh_token"] != "" {
				t.Errorf("b's token was refreshed through a's: %v", *got)
			}
			// Written back, the refresh would have put b's refresh token in a
			// file of a's own.
			otherFi, _ := os.Stat(other)
			if fi, err := os.Lstat(own); err == nil && fi.Mode().IsRegular() && !os.SameFile(fi, otherFi) {
				if data, _ := os.ReadFile(own); strings.Contains(string(data), "B-REFRESH") {
					t.Errorf("b's refresh token was copied to a's token file:\n%s", data)
				}
			}
			if w := do("DELETE", "/api/brains/a/calendar"); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if (*got)["revoked"] != "" {
				t.Errorf("revoked %q through a's token file", (*got)["revoked"])
			}
			if data, _ := os.ReadFile(other); string(data) != string(bTok) {
				t.Errorf("b's token changed:\n%s", data)
			}
			if _, err := os.Lstat(own); !os.IsNotExist(err) {
				t.Errorf("a's token file was not removed (%v)", err)
			}
		})
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestCalendarTokenRequestsGoToGoogleWithYourClient: a brain the sandbox
// does not hold can write its token file, so the bridge takes neither the
// token_uri nor the client in it. It refreshes at Google's token endpoint
// with the client you configured, and a token from another client cannot
// be refreshed. The token_uri of a pasted client file is not used either.
func TestCalendarTokenRequestsGoToGoogleWithYourClient(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		fmt.Fprint(w, `{"access_token":"ACCESS-1","expires_in":3600}`)
	}))
	t.Cleanup(other.Close)

	cases := []struct {
		name     string
		clientID string // in the token file
		noClient bool   // remove your client first
		want     string // list_error; "" for the calendars
	}{
		{"your client", "123-abc.apps.googleusercontent.com", false, ""},
		{"another client", "999-other.apps.googleusercontent.com", false, "connect it again"},
		{"no client configured", "123-abc.apps.googleusercontent.com", true, "add your Google OAuth client first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := fakeGoogle(t)
			tb := newTestBridge(t, "")
			useGoogleServer(tb, srv)
			// The pasted client names another token endpoint too.
			if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, other.URL)}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": manifest.Manifest{ID: "a", Name: "A"}}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			tok, _ := json.Marshal(google.Token{
				Token: "OLD", RefreshToken: "A-REFRESH", TokenURI: other.URL + "/token",
				ClientID: tc.clientID, ClientSecret: "SECRET-THE-BRAIN-WROTE",
				Expiry: "2020-01-01T00:00:00.000000Z",
			})
			if err := os.WriteFile(tb.google.TokenPath("a"), tok, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.noClient {
				if w := tb.do("DELETE", "/api/google/client", nil); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}

			var st struct {
				Calendars []google.CalendarEntry `json:"calendars"`
				ListError string                 `json:"list_error"`
			}
			_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar", nil).Body.Bytes(), &st)
			if n := elsewhere.Load(); n != 0 {
				t.Fatalf("%d request(s) went to the token_uri in a file", n)
			}
			if tc.want != "" {
				if !strings.Contains(st.ListError, tc.want) || (*got)["refresh_token"] != "" {
					t.Errorf("status = %+v, token request %v; want no refresh and %q", st, *got, tc.want)
				}
				return
			}
			if st.ListError != "" || len(st.Calendars) != 2 {
				t.Errorf("status = %+v, want the calendars", st)
			}
			if (*got)["refresh_token"] != "A-REFRESH" || (*got)["client_id"] != "123-abc.apps.googleusercontent.com" || (*got)["client_secret"] != "TEST-CLIENT-SECRET" {
				t.Errorf("refresh request = %v, want your client's", *got)
			}
		})
	}

	t.Run("sign-in", func(t *testing.T) {
		srv, got := fakeGoogle(t)
		tb := newTestBridge(t, "")
		useGoogleServer(tb, srv)
		if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, other.URL)}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": manifest.Manifest{ID: "a", Name: "A"}}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var start struct {
			AuthURL string `json:"auth_url"`
		}
		_ = json.Unmarshal(tb.do("POST", "/api/brains/a/calendar/connect", nil).Body.Bytes(), &start)
		u, _ := url.Parse(start.AuthURL)
		r := httptest.NewRequest("GET", "http://127.0.0.1:8765/?state="+url.QueryEscape(u.Query().Get("state"))+"&code=good-code", nil)
		r.Host = "127.0.0.1:8765"
		tb.h.ServeHTTP(httptest.NewRecorder(), r)
		if n := elsewhere.Load(); n != 0 || (*got)["code"] != "good-code" {
			t.Fatalf("code exchange went elsewhere (%d) or nowhere: %v", n, *got)
		}
		var tok google.Token
		data, _ := os.ReadFile(tb.google.TokenPath("a"))
		_ = json.Unmarshal(data, &tok)
		if tok.TokenURI != srv.URL+"/token" {
			t.Errorf("token file token_uri = %q, want Google's endpoint", tok.TokenURI)
		}
	})
}

// TestOversizedCalendarTokenIsRefused: a brain the sandbox does not hold
// can make its token file as large as it likes. The bridge refuses one
// larger than maxBrainFile rather than load it, and so neither refreshes
// it nor revokes it.
func TestOversizedCalendarTokenIsRefused(t *testing.T) {
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": manifest.Manifest{ID: "a", Name: "A"}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Valid JSON, expired, padded past the limit.
	tok, _ := json.Marshal(google.Token{
		Token: "OLD", RefreshToken: "A-REFRESH",
		ClientID: "123-abc.apps.googleusercontent.com", ClientSecret: "TEST-CLIENT-SECRET",
		Expiry: "2020-01-01T00:00:00.000000Z",
	})
	tok = append(tok, strings.Repeat(" ", fsx.MaxBrainFile)...)
	if err := os.WriteFile(tb.google.TokenPath("a"), tok, 0o600); err != nil {
		t.Fatal(err)
	}

	var st struct {
		ListError string `json:"list_error"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar", nil).Body.Bytes(), &st)
	if !strings.Contains(st.ListError, "larger than 256 KiB") {
		t.Errorf("list_error = %q, want the token refused as too large", st.ListError)
	}
	if w := tb.do("DELETE", "/api/brains/a/calendar", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if (*got)["refresh_token"] != "" || (*got)["revoked"] != "" {
		t.Errorf("the token was used: %v", *got)
	}
}

// TestCalendarSignInScopes: the bridge asks for the narrow scopes the
// calendar commands need and records what Google granted. A grant that
// lacks one of them fails the sign-in, since the commands that need it
// would fail later with less to go on; a broader one, such as the full
// calendar scope a brain got from an older version, is shown on the
// brain's page so you can narrow it.
func TestCalendarSignInScopes(t *testing.T) {
	const full = "https://www.googleapis.com/auth/calendar"
	narrow := strings.Join(calendarScopes, " ")
	cases := []struct {
		name      string
		granted   string // the token response's scope field
		connected bool
		broad     bool
		lastError string
	}{
		{"no scope field", "", true, false, ""},
		{"what was asked", narrow, true, false, ""},
		{"in another order", strings.Join([]string{calendarScopes[2], calendarScopes[0], calendarScopes[1]}, " "), true, false, ""},
		{"one unticked", calendarScopes[0] + " " + calendarScopes[2], false, false, "missing " + calendarScopes[1]},
		{"more than asked", narrow + " " + full, true, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			googleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					scope, _ := json.Marshal(tc.granted)
					fmt.Fprintf(w, `{"access_token":"ACCESS-1","refresh_token":"RT-1","expires_in":3600,"scope":%s}`, scope)
				case "/calendar/v3/users/me/calendarList":
					fmt.Fprint(w, `{"items":[{"id":"you@example.com","summary":"you@example.com","primary":true,"accessRole":"owner"}]}`)
				default:
					w.WriteHeader(404)
				}
			}))
			defer googleServer.Close()
			tb := newTestBridge(t, "")
			useGoogleServer(tb, googleServer)
			tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
			if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, googleServer.URL)}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			var start struct {
				AuthURL string `json:"auth_url"`
			}
			_ = json.Unmarshal(tb.do("POST", "/api/brains/a/calendar/connect", nil).Body.Bytes(), &start)
			u, _ := url.Parse(start.AuthURL)
			r := httptest.NewRequest("GET", "http://127.0.0.1:8765/?state="+url.QueryEscape(u.Query().Get("state"))+"&code=good-code", nil)
			r.Host = "127.0.0.1:8765"
			tb.h.ServeHTTP(httptest.NewRecorder(), r)

			var st struct {
				Connected   bool   `json:"connected"`
				BroadAccess bool   `json:"broad_access"`
				LastError   string `json:"last_error"`
			}
			_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar?list=0", nil).Body.Bytes(), &st)
			if st.Connected != tc.connected || st.BroadAccess != tc.broad || !strings.Contains(st.LastError, tc.lastError) {
				t.Errorf("status = %+v, want connected %v, broad_access %v, last_error with %q", st, tc.connected, tc.broad, tc.lastError)
			}
			if !tc.connected {
				return
			}
			var tok google.Token
			data, _ := os.ReadFile(tb.google.TokenPath("a"))
			_ = json.Unmarshal(data, &tok)
			want := strings.Fields(tc.granted)
			if tc.granted == "" {
				want = calendarScopes
			}
			if !slices.Equal(tok.Scopes, want) {
				t.Errorf("token scopes = %q, want %q", tok.Scopes, want)
			}
		})
	}
}

// TestCalendarRefreshRecordsGrantedScopes: a refresh stores the scopes
// Google says the token has, as the sign-in does, since the brain's page
// warns about broad access by them. An older version recorded the full
// calendar scope whatever was granted. An answer without a scope field
// leaves the stored ones as they were.
func TestCalendarRefreshRecordsGrantedScopes(t *testing.T) {
	const full = "https://www.googleapis.com/auth/calendar"
	narrow := strings.Join(calendarScopes, " ")
	cases := []struct {
		name    string
		stored  []string // in the token file before the refresh
		granted string   // the refresh answer's scope field
		want    []string
		broad   bool
	}{
		{"Google grants less than stored", []string{full}, narrow, calendarScopes, false},
		{"Google grants more than stored", calendarScopes, narrow + " " + full, append(slices.Clone(calendarScopes), full), true},
		{"no scope field", []string{full}, "", []string{full}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			googleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/token" {
					w.WriteHeader(404)
					return
				}
				answer := map[string]any{"access_token": "ACCESS-2", "expires_in": 3600}
				if tc.granted != "" {
					answer["scope"] = tc.granted
				}
				_ = json.NewEncoder(w).Encode(answer)
			}))
			defer googleServer.Close()
			tb := newTestBridge(t, "")
			useGoogleServer(tb, googleServer)
			tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
			if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, googleServer.URL)}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			data, _ := json.Marshal(google.Token{
				Token: "ACCESS-1", RefreshToken: "RT-1", ClientID: "123-abc.apps.googleusercontent.com",
				Scopes: tc.stored, Expiry: "2020-01-01T00:00:00.000000Z",
			})
			if err := os.WriteFile(tb.google.TokenPath("a"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if tok, err := tb.google.AccessToken(context.Background(), "a"); err != nil || tok.Token != "ACCESS-2" {
				t.Fatalf("accessToken = %+v, %v; want it refreshed", tok, err)
			}
			var saved google.Token
			data, _ = os.ReadFile(tb.google.TokenPath("a"))
			if err := json.Unmarshal(data, &saved); err != nil || saved.Token != "ACCESS-2" || !slices.Equal(saved.Scopes, tc.want) {
				t.Errorf("token file has %q with scopes %q (%v); want ACCESS-2 with %q", saved.Token, saved.Scopes, err, tc.want)
			}
			var st struct {
				BroadAccess bool `json:"broad_access"`
			}
			_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar?list=0", nil).Body.Bytes(), &st)
			if st.BroadAccess != tc.broad {
				t.Errorf("broad_access = %v, want %v", st.BroadAccess, tc.broad)
			}
		})
	}
}

// writeCalendarToken gives brain id a token file of the client in
// testClientJSON, signed in to account with refreshToken.
func writeCalendarToken(t *testing.T, tb *testBridge, id, account, refreshToken string) {
	t.Helper()
	data, _ := json.Marshal(google.Token{
		Token: "ACCESS-" + id, RefreshToken: refreshToken, ClientID: "123-abc.apps.googleusercontent.com",
		ClientSecret: "TEST-CLIENT-SECRET", Account: account, Expiry: "2099-01-01T00:00:00.000000Z",
	})
	if err := os.MkdirAll(tb.google.CredsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.google.TokenPath(id), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCalendarDisconnectRevokesOnlyAnUnsharedGrant: Google revokes an app's
// whole grant for an account, so Disconnect revokes only when no other
// token from the same client is signed in to the same account, or may be.
// Either way this brain's token goes, and the answer says why access stayed:
// another brain, or a token no configured brain uses, which can be deleted.
func TestCalendarDisconnectRevokesOnlyAnUnsharedGrant(t *testing.T) {
	const shared, leftover = "brain b signs in", "a token for b, which is not a configured brain"
	cases := []struct {
		name           string
		account, other string // the brain's and the other token's account; "-" for no other token
		otherBrain     bool   // the other token is a configured brain's
		revoked        bool
		note           string // what the note says when not revoked
	}{
		{"no other brain", "you@example.com", "-", false, true, ""},
		{"another brain, same account", "you@example.com", "you@example.com", true, false, shared},
		{"another brain, same account in other case", "you@example.com", "YOU@example.com", true, false, shared},
		{"another brain, another account", "you@example.com", "team@example.com", true, true, ""},
		{"another brain, account unknown", "you@example.com", "", true, false, shared},
		{"this brain's account unknown", "", "team@example.com", true, false, shared},
		{"a token left by a removed brain, same account", "you@example.com", "you@example.com", false, false, leftover},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := fakeGoogle(t)
			tb := newTestBridge(t, "")
			useGoogleServer(tb, srv)
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
			if tc.otherBrain {
				tb.addFolderBrain(t, manifest.Manifest{ID: "b", Name: "B"})
			}
			writeCalendarToken(t, tb, "a", tc.account, "RT-A")
			if tc.other != "-" {
				writeCalendarToken(t, tb, "b", tc.other, "RT-B")
			}
			w := tb.do("DELETE", "/api/brains/a/calendar", nil)
			if w.Code != 200 {
				t.Fatalf("disconnect = %d %s", w.Code, w.Body.String())
			}
			var resp struct {
				Revoked bool   `json:"revoked"`
				Note    string `json:"note"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			if resp.Revoked != tc.revoked || ((*got)["revoked"] == "RT-A") != tc.revoked {
				t.Errorf("revoked = %v (at Google: %q), want %v", resp.Revoked, (*got)["revoked"], tc.revoked)
			}
			if !tc.revoked && !strings.Contains(resp.Note, tc.note) {
				t.Errorf("note = %q, want %q", resp.Note, tc.note)
			}
			if _, err := os.Lstat(tb.google.TokenPath("a")); !os.IsNotExist(err) {
				t.Error("a's token was not deleted")
			}
			if tc.other != "-" {
				if _, err := os.Stat(tb.google.TokenPath("b")); err != nil {
					t.Errorf("b's token: %v", err)
				}
			}
		})
	}
}

// TestCalendarDisconnectWhenGoogleDoesNotRevoke: a revocation Google does
// not confirm keeps the token, so that it can be tried again, and says so;
// local_only then deletes it without asking Google. One Google calls
// invalid is revoked already.
func TestCalendarDisconnectWhenGoogleDoesNotRevoke(t *testing.T) {
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	writeCalendarToken(t, tb, "a", "you@example.com", "FAIL-REVOKE")

	w := tb.do("DELETE", "/api/brains/a/calendar", nil)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "not confirmed by Google") {
		t.Errorf("disconnect = %d %s, want 502", w.Code, w.Body.String())
	}
	if _, err := os.Stat(tb.google.TokenPath("a")); err != nil {
		t.Fatalf("the token was deleted although Google did not revoke it: %v", err)
	}
	w = tb.do("DELETE", "/api/brains/a/calendar?local_only=1", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "not revoked") {
		t.Errorf("local disconnect = %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Lstat(tb.google.TokenPath("a")); !os.IsNotExist(err) {
		t.Error("local_only kept the token")
	}

	writeCalendarToken(t, tb, "a", "you@example.com", "GONE-REVOKE")
	if w := tb.do("DELETE", "/api/brains/a/calendar", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"revoked":true`) {
		t.Errorf("disconnect of a token revoked already = %d %s", w.Code, w.Body.String())
	}
	if (*got)["revoked"] != "GONE-REVOKE" {
		t.Errorf("revoked %q", (*got)["revoked"])
	}
}

// TestCalendarDisconnectEndsWhatTheBrainShows: a disconnect takes the
// brain's last sign-in error with the token, and one whose brain settings
// cannot be cleared says so rather than "disconnected".
func TestCalendarDisconnectEndsWhatTheBrainShows(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only folders")
	}
	cases := []struct {
		name     string
		readOnly bool // the brain's folder, so its brain.json cannot be written
		code     int
		want     string // in the answer
	}{
		{"a sign-in error before", false, 200, `"status":"disconnected"`},
		{"settings that cannot be saved", true, 500, "saving the brain's calendar settings failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeGoogle(t)
			tb := newTestBridge(t, `, "sandbox": {"credentials_dir": "~/creds"}`)
			useGoogleServer(tb, srv)
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			dir := tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
			auth, err := tb.google.StartSignIn("a")
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(auth)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tb.google.Callback(t.Context(), url.Values{"state": {u.Query().Get("state")}, "error": {"access_denied"}}); err != nil {
				t.Fatal(err)
			}
			writeCalendarToken(t, tb, "a", "you@example.com", "RT-A")
			if tc.readOnly {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			w := tb.do("DELETE", "/api/brains/a/calendar", nil)
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("disconnect = %d %s, want %d with %q", w.Code, w.Body.String(), tc.code, tc.want)
			}
			if s := tb.do("GET", "/api/brains/a/calendar", nil).Body.String(); strings.Contains(s, "last_error") {
				t.Errorf("status after disconnect = %s, want no last_error", s)
			}
		})
	}
}

// TestCalendarSwitchRevokesTheOldAccount: signing a brain in to another
// account revokes the grant of the one it leaves, which nobody can use any
// more, unless another brain shares it. Signing in to the same account
// again revokes nothing: the new token belongs to the same grant.
func TestCalendarSwitchRevokesTheOldAccount(t *testing.T) {
	cases := []struct {
		name, old string
		shared    bool
		revoked   bool
	}{
		{"another account", "team@example.com", false, true},
		{"another account another brain uses", "team@example.com", true, false},
		{"the same account", "you@example.com", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := fakeGoogle(t)
			tb := newTestBridge(t, "")
			useGoogleServer(tb, srv)
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
			writeCalendarToken(t, tb, "a", tc.old, "RT-OLD")
			if tc.shared {
				writeCalendarToken(t, tb, "b", tc.old, "RT-B")
			}
			var start struct {
				AuthURL string `json:"auth_url"`
			}
			_ = json.Unmarshal(tb.do("POST", "/api/brains/a/calendar/connect", nil).Body.Bytes(), &start)
			u, _ := url.Parse(start.AuthURL)
			r := httptest.NewRequest("GET", "http://127.0.0.1:8765/?state="+url.QueryEscape(u.Query().Get("state"))+"&code=good-code", nil)
			r.Host = "127.0.0.1:8765"
			tb.h.ServeHTTP(httptest.NewRecorder(), r)
			if ((*got)["revoked"] == "RT-OLD") != tc.revoked {
				t.Errorf("revoked %q, want the old token revoked: %v", (*got)["revoked"], tc.revoked)
			}
			data, _ := os.ReadFile(tb.google.TokenPath("a"))
			if !strings.Contains(string(data), "RT-1") {
				t.Errorf("token file = %s, want the new sign-in", data)
			}
		})
	}
}

// expiredCalendarToken writes brain a's token file as the Google library of
// an older version's calendar tool left it: expired, with a field of the
// library's own.
func expiredCalendarToken(t *testing.T, tb *testBridge, refreshToken string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{
		"token": "OLD", "refresh_token": refreshToken, "token_uri": "https://oauth2.googleapis.com/token",
		"client_id": "123-abc.apps.googleusercontent.com", "client_secret": "TEST-CLIENT-SECRET",
		"scopes": calendarScopes, "universe_domain": "googleapis.com",
		"account": "you@example.com", "expiry": "2020-01-01T00:00:00.000000Z",
	})
	if err := os.MkdirAll(tb.google.CredsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.google.TokenPath("a"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCalendarRefreshKeepsTheToken: a refresh saves the new access token
// and the refresh token Google handed out with it, and keeps the fields an
// older calendar tool's library wrote.
func TestCalendarRefreshKeepsTheToken(t *testing.T) {
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	expiredCalendarToken(t, tb, "A-REFRESH")

	var st struct {
		Calendars []google.CalendarEntry `json:"calendars"`
		ListError string                 `json:"list_error"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar", nil).Body.Bytes(), &st)
	if st.ListError != "" || len(st.Calendars) != 2 || (*got)["refresh_token"] != "A-REFRESH" {
		t.Fatalf("status = %+v after refreshing with %v", st, *got)
	}
	data, _ := os.ReadFile(tb.google.TokenPath("a"))
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	exp, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(saved["expiry"]))
	if saved["token"] != "ACCESS-1" || saved["refresh_token"] != "RT-1" || saved["universe_domain"] != "googleapis.com" ||
		saved["account"] != "you@example.com" || time.Until(exp) < 30*time.Minute {
		t.Errorf("saved token = %s", data)
	}
}

// TestCalendarRevokedSignInAsksToReconnect: a refresh Google refuses with
// invalid_grant tells the brain's card to say that the sign-in no longer
// works; one Google could not answer just now does not, nor one refusing
// the OAuth client, which signing in again does not mend.
func TestCalendarRevokedSignInAsksToReconnect(t *testing.T) {
	cases := []struct {
		name, refreshToken string
		want               string // in list_error
		reconnect          bool
	}{
		{"revoked", "REVOKED", "Token has been expired or revoked.", true},
		{"Google down", "GOOGLE-DOWN", "backend_error", false},
		{"client deleted", "CLIENT-GONE", "The OAuth client was not found.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeGoogle(t)
			tb := newTestBridge(t, "")
			useGoogleServer(tb, srv)
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
			expiredCalendarToken(t, tb, tc.refreshToken)
			var st struct {
				ListError       string `json:"list_error"`
				ReconnectNeeded bool   `json:"reconnect_needed"`
			}
			_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar", nil).Body.Bytes(), &st)
			if st.ReconnectNeeded != tc.reconnect || !strings.Contains(st.ListError, tc.want) {
				t.Errorf("status = %+v, want reconnect_needed %v and %q", st, tc.reconnect, tc.want)
			}
		})
	}
}

// TestCalendarRefreshThatCannotBeSavedIsLogged: the refreshed access token
// still lists the calendars, and the failed save is logged.
func TestCalendarRefreshThatCannotBeSavedIsLogged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only folders")
	}
	srv, _ := fakeGoogle(t)
	tb := newTestBridge(t, `, "sandbox": {"credentials_dir": "~/creds"}`)
	useGoogleServer(tb, srv)
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	expiredCalendarToken(t, tb, "A-REFRESH")
	if err := os.Chmod(tb.google.CredsDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tb.google.CredsDir(), 0o700) })
	logs := captureLog(t)
	var st struct {
		Calendars []google.CalendarEntry `json:"calendars"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar", nil).Body.Bytes(), &st)
	if len(st.Calendars) != 2 {
		t.Errorf("calendars = %v, want them listed with the new access token", st.Calendars)
	}
	if all := strings.Join(logs(), "\n"); !strings.Contains(all, "saving the refreshed calendar token failed") {
		t.Errorf("log = %s, want the failed save", all)
	}
}

// signInCallback starts brain id's sign-in and has Google come back with a
// good code, through a request whose context is ctx.
func signInCallback(t *testing.T, tb *testBridge, id string, ctx context.Context, before func()) *httptest.ResponseRecorder {
	t.Helper()
	var start struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(tb.do("POST", "/api/brains/"+id+"/calendar/connect", nil).Body.Bytes(), &start)
	u, err := url.Parse(start.AuthURL)
	if err != nil || u.Query().Get("state") == "" {
		t.Fatalf("auth url %q", start.AuthURL)
	}
	if before != nil {
		before()
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:8765/?state="+url.QueryEscape(u.Query().Get("state"))+"&code=good-code", nil).WithContext(ctx)
	r.Host = "127.0.0.1:8765"
	w := httptest.NewRecorder()
	tb.h.ServeHTTP(w, r)
	return w
}

// TestCalendarSignInSurvivesTheBrowserLeaving: the code is spent once it
// reaches Google, so the sign-in finishes even when the browser's request
// is gone by then.
func TestCalendarSignInSurvivesTheBrowserLeaving(t *testing.T) {
	srv, _ := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	signInCallback(t, tb, "a", gone, nil)
	if data, err := os.ReadFile(tb.google.TokenPath("a")); err != nil || !strings.Contains(string(data), "RT-1") {
		t.Errorf("token = %s, %v; want the sign-in saved", data, err)
	}
}

// TestCalendarSignInForARemovedBrain: a brain removed while it signed in
// gets no token, which a later brain of the same id would otherwise show as
// connected.
func TestCalendarSignInForARemovedBrain(t *testing.T) {
	srv, _ := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	signInCallback(t, tb, "a", context.Background(), func() {
		if w := tb.do("DELETE", "/api/brains/a", nil); w.Code != 200 {
			t.Fatalf("remove = %d %s", w.Code, w.Body.String())
		}
	})
	if _, err := os.Lstat(tb.google.TokenPath("a")); !os.IsNotExist(err) {
		t.Errorf("a token was written for the removed brain (%v)", err)
	}
	// Refused before anything is written, not taken back after the brain's
	// settings could not be saved.
	msg := tb.google.CalendarStatus(t.Context(), "a", nil, false).LastError
	if !strings.Contains(msg, "removed while it signed in") {
		t.Errorf("sign-in error = %q, want it to say the brain was removed", msg)
	}
}

// TestCalendarSignInThatCannotBeSaved: a sign-in whose token or brain
// settings cannot be saved takes back what it wrote, puts back the token
// it replaced, and revokes nothing, so that the brain keeps the access it
// had.
func TestCalendarSignInThatCannotBeSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only folders")
	}
	cases := []struct {
		name     string
		switched bool   // the brain had a token for another account
		readOnly string // the folder that cannot be written: "credentials" or "brain"
	}{
		{"the token, first sign-in", false, "credentials"},
		{"the token, another account", true, "credentials"},
		{"the settings, first sign-in", false, "brain"},
		{"the settings, another account", true, "brain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := fakeGoogle(t)
			tb := newTestBridge(t, `, "sandbox": {"credentials_dir": "~/creds"}`)
			useGoogleServer(tb, srv)
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			dir := tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
			if err := os.MkdirAll(tb.google.CredsDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.switched {
				writeCalendarToken(t, tb, "a", "team@example.com", "RT-OLD")
			}
			if tc.readOnly == "credentials" {
				dir = tb.google.CredsDir()
			}
			signInCallback(t, tb, "a", context.Background(), func() {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			})
			data, err := os.ReadFile(tb.google.TokenPath("a"))
			if tc.switched && !strings.Contains(string(data), "RT-OLD") {
				t.Errorf("token = %s, %v; want the old one", data, err)
			}
			if !tc.switched && !os.IsNotExist(err) {
				t.Errorf("token = %s, %v; want none", data, err)
			}
			if (*got)["revoked"] != "" {
				t.Errorf("revoked %q although the sign-in failed", (*got)["revoked"])
			}
		})
	}
}

// TestCalendarRedirectIsTheAddressServed: Google sends the browser back to
// the address the dashboard was started on, also after http_listen was
// changed for the next start.
func TestCalendarRedirectIsTheAddressServed(t *testing.T) {
	srv, _ := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	served := "http://" + tb.cfgs.Get().HTTPListen + "/"
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	if _, err := tb.cfgs.Update("", func(c *config.File) error { c.HTTPListen = "127.0.0.1:1"; return nil }); err != nil {
		t.Fatal(err)
	}
	var start struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(tb.do("POST", "/api/brains/a/calendar/connect", nil).Body.Bytes(), &start)
	u, _ := url.Parse(start.AuthURL)
	if got := u.Query().Get("redirect_uri"); got != served {
		t.Errorf("redirect_uri = %q, want %q", got, served)
	}
}

// gatedGoogle is a Google whose token endpoint holds every refresh until
// release is called, answering each with a new access token, and grants a
// sign-in's code at once.
type gatedGoogle struct {
	srv      *httptest.Server
	refresh  atomic.Int32  // refresh requests
	entered  chan struct{} // one value as each refresh arrives
	release  func()
	released chan struct{}
	revoked  atomic.Value // the last token revoked, a string
}

func newGatedGoogle(t *testing.T) *gatedGoogle {
	t.Helper()
	g := &gatedGoogle{entered: make(chan struct{}, 10), released: make(chan struct{})}
	var once sync.Once
	g.release = func() { once.Do(func() { close(g.released) }) }
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") != "refresh_token" {
				fmt.Fprint(w, `{"access_token":"ACCESS-SIGNIN","refresh_token":"RT-NEW","expires_in":3600}`)
				return
			}
			n := g.refresh.Add(1)
			g.entered <- struct{}{}
			select {
			case <-g.released:
			case <-r.Context().Done():
				return
			}
			fmt.Fprintf(w, `{"access_token":"ACCESS-%d","expires_in":3600}`, n)
		case "/calendar/v3/users/me/calendarList":
			fmt.Fprint(w, `{"items":[{"id":"you@example.com","summary":"you@example.com","primary":true,"accessRole":"owner"}]}`)
		case "/revoke":
			g.revoked.Store(r.FormValue("token"))
		default:
			w.WriteHeader(404)
		}
	}))
	// Released first: Close waits for the requests it holds.
	t.Cleanup(g.srv.Close)
	t.Cleanup(g.release)
	return g
}

// waitEntered waits for a refresh to reach the token endpoint.
func (g *gatedGoogle) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no refresh reached Google's token endpoint")
	}
}

// gatedCalendarBrain sets up brain a, with its calendars chosen, and an
// expired token signed in to account with refresh token RT-A, so that
// using it means a refresh.
func gatedCalendarBrain(t *testing.T, g *gatedGoogle, account string) *testBridge {
	t.Helper()
	tb := newTestBridge(t, "")
	useGoogleServer(tb, g.srv)
	if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, g.srv.URL)}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	if w := tb.do("PUT", "/api/brains/a/calendar", map[string]any{"account": account, "calendars": []string{account}, "default": account}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	data, _ := json.Marshal(google.Token{
		Token: "ACCESS-OLD", RefreshToken: "RT-A", ClientID: "123-abc.apps.googleusercontent.com",
		Scopes: calendarScopes, Account: account, Expiry: "2020-01-01T00:00:00.000000Z",
	})
	if err := os.WriteFile(tb.google.TokenPath("a"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return tb
}

// within waits up to d for done, which a goroutine closes when it returns,
// and reports whether it did. A call that should wait for another is given
// d to return early, as it would without the wait.
func within(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestRefreshDoesNotResurrectDisconnectedToken: a refresh that read the
// brain's token before a disconnect removed it, and was waiting for Google
// meanwhile, wrote the token back once Google answered. A disconnect here
// only, or one that leaves a grant another brain shares, revokes nothing,
// so the brain was connected again, and, with no calendars chosen any
// more, used its primary one. The disconnect now waits for the refresh.
func TestRefreshDoesNotResurrectDisconnectedToken(t *testing.T) {
	g := newGatedGoogle(t)
	tb := gatedCalendarBrain(t, g, "you@example.com")

	refreshed := make(chan struct{})
	var refreshErr error
	go func() {
		defer close(refreshed)
		_, refreshErr = tb.google.AccessToken(context.Background(), "a")
	}()
	g.waitEntered(t)

	disconnected := make(chan struct{})
	var w *httptest.ResponseRecorder
	go func() {
		defer close(disconnected)
		w = tb.do("DELETE", "/api/brains/a/calendar?local_only=1", nil)
	}()
	if within(disconnected, 200*time.Millisecond) {
		t.Log("the disconnect did not wait for the refresh")
	}
	g.release()
	if !within(refreshed, 5*time.Second) || !within(disconnected, 5*time.Second) {
		t.Fatal("the refresh or the disconnect hangs")
	}
	if refreshErr != nil {
		t.Errorf("refresh: %v", refreshErr)
	}
	if w.Code != 200 {
		t.Fatalf("disconnect = %d %s", w.Code, w.Body.String())
	}

	if data, err := os.ReadFile(tb.google.TokenPath("a")); !os.IsNotExist(err) {
		t.Errorf("the token is back after the disconnect (%v):\n%s", err, data)
	}
	var st struct {
		Connected bool `json:"connected"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar?list=0", nil).Body.Bytes(), &st)
	if st.Connected {
		t.Error("the brain shows as connected after the disconnect")
	}
	token, revoke := tb.runTokens.mint("a")
	t.Cleanup(revoke)
	call := tb.do("GET", "/brain/calendar/calendars", nil, func(r *http.Request) {
		r.Header.Del("X-Bridge-Token")
		r.Header.Set("Authorization", "Bearer "+token)
	})
	if call.Code != http.StatusPreconditionFailed || !strings.Contains(call.Body.String(), "not connected for this brain") {
		t.Errorf("a calendar call after the disconnect = %d %s, want it refused as not connected", call.Code, call.Body.String())
	}
}

// TestDisconnectClearsTheChosenCalendars: Disconnect itself clears the
// brain's chosen calendars, while it still holds the token's lock. The
// dashboard's handler cleared them after Disconnect had released it, so a
// sign-in waiting for the lock could save its token and its calendars in
// between, which the clearing then undid: the brain was signed in with no
// calendars chosen, and used its primary one.
func TestDisconnectClearsTheChosenCalendars(t *testing.T) {
	for _, localOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("local_only=%v", localOnly), func(t *testing.T) {
			g := newGatedGoogle(t)
			tb := gatedCalendarBrain(t, g, "you@example.com")
			if m := tb.brainMgr.Get("a").Manifest(); m.Calendar == nil {
				t.Fatal("no calendars chosen to begin with")
			}
			if _, _, err := tb.google.Disconnect(context.Background(), "a", localOnly); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(tb.google.TokenPath("a")); !os.IsNotExist(err) {
				t.Errorf("the token after the disconnect: %v", err)
			}
			if m := tb.brainMgr.Get("a").Manifest(); m.Calendar != nil {
				t.Errorf("calendar settings after the disconnect = %+v, want none", m.Calendar)
			}
		})
	}
}

// TestConcurrentRefreshesPostOnce: calls that find the brain's token
// expired at the same time refresh it once, and get the same access
// token: the second waits for the first and reads the token it saved.
// Each asked Google before, and the last answer to be saved won.
func TestConcurrentRefreshesPostOnce(t *testing.T) {
	g := newGatedGoogle(t)
	tb := gatedCalendarBrain(t, g, "you@example.com")

	const calls = 2
	var wg sync.WaitGroup
	tokens := make([]string, calls)
	errs := make([]error, calls)
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := tb.google.AccessToken(context.Background(), "a")
			if tok != nil {
				tokens[i] = tok.Token
			}
			errs[i] = err
		}()
	}
	g.waitEntered(t)
	// Time for the other call to reach Google too, as it would unserialized.
	time.Sleep(200 * time.Millisecond)
	g.release()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	if n := g.refresh.Load(); n != 1 {
		t.Errorf("%d refresh requests, want 1", n)
	}
	if tokens[0] != "ACCESS-1" || tokens[1] != tokens[0] {
		t.Errorf("access tokens = %q, want ACCESS-1 for both", tokens)
	}
	var saved google.Token
	data, _ := os.ReadFile(tb.google.TokenPath("a"))
	if err := json.Unmarshal(data, &saved); err != nil || saved.Token != "ACCESS-1" {
		t.Errorf("saved token = %s (%v), want ACCESS-1", data, err)
	}
}

// TestWaitingForTheTokenLockEndsWithCtx: a disconnect or a calendar call
// that waits for a refresh in progress gives up when its context ends,
// and changes nothing, rather than hang with the refresh.
func TestWaitingForTheTokenLockEndsWithCtx(t *testing.T) {
	cases := []struct {
		name string
		call func(ctx context.Context, g *google.Auth) error
	}{
		{"disconnect", func(ctx context.Context, g *google.Auth) error {
			_, _, err := g.Disconnect(ctx, "a", false)
			return err
		}},
		{"calendar call", func(ctx context.Context, g *google.Auth) error {
			_, err := g.AccessToken(ctx, "a")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGatedGoogle(t)
			tb := gatedCalendarBrain(t, g, "you@example.com")
			refreshed := make(chan struct{})
			go func() {
				defer close(refreshed)
				_, _ = tb.google.AccessToken(context.Background(), "a")
			}()
			defer func() { g.release(); <-refreshed }()
			g.waitEntered(t)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			var err error
			go func() {
				defer close(done)
				err = tc.call(ctx, tb.google)
			}()
			if within(done, 200*time.Millisecond) {
				t.Fatalf("returned %v before the refresh it should wait for", err)
			}
			cancel()
			if !within(done, 5*time.Second) {
				t.Fatal("still waiting after its context ended")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want the context's", err)
			}
			if _, err := os.Stat(tb.google.TokenPath("a")); err != nil {
				t.Errorf("the token: %v", err)
			}
			if n := g.refresh.Load(); n != 1 {
				t.Errorf("%d refresh requests, want only the first one's", n)
			}
			if r, _ := g.revoked.Load().(string); r != "" {
				t.Errorf("revoked %q", r)
			}
		})
	}
}

// TestSignInIsNotUndoneByARefresh: a sign-in to another account that
// finishes while a refresh of the old token is waiting for Google is not
// overwritten by it: the refresh wrote the old account's token back over
// the new one, which the brain's settings named.
func TestSignInIsNotUndoneByARefresh(t *testing.T) {
	g := newGatedGoogle(t)
	tb := gatedCalendarBrain(t, g, "team@example.com")

	refreshed := make(chan struct{})
	go func() {
		defer close(refreshed)
		_, _ = tb.google.AccessToken(context.Background(), "a")
	}()
	g.waitEntered(t)

	var start struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(tb.do("POST", "/api/brains/a/calendar/connect", nil).Body.Bytes(), &start)
	u, err := url.Parse(start.AuthURL)
	if err != nil || u.Query().Get("state") == "" {
		t.Fatalf("auth url %q", start.AuthURL)
	}
	signedIn := make(chan struct{})
	go func() {
		defer close(signedIn)
		r := httptest.NewRequest("GET", "http://127.0.0.1:8765/?state="+url.QueryEscape(u.Query().Get("state"))+"&code=good-code", nil)
		r.Host = "127.0.0.1:8765"
		tb.h.ServeHTTP(httptest.NewRecorder(), r)
	}()
	if within(signedIn, 200*time.Millisecond) {
		t.Log("the sign-in did not wait for the refresh")
	}
	g.release()
	if !within(refreshed, 5*time.Second) || !within(signedIn, 5*time.Second) {
		t.Fatal("the refresh or the sign-in hangs")
	}

	var tok google.Token
	data, _ := os.ReadFile(tb.google.TokenPath("a"))
	if err := json.Unmarshal(data, &tok); err != nil || tok.RefreshToken != "RT-NEW" || tok.Account != "you@example.com" {
		t.Errorf("token = %s (%v), want the new sign-in's", data, err)
	}
	m := tb.brainMgr.Get("a").Manifest()
	if m.Calendar == nil || m.Calendar.Account != "you@example.com" {
		t.Errorf("calendar settings = %+v, want the new account", m.Calendar)
	}
}

// TestLeftoverClientCopiesAreReported: older versions copied the OAuth
// client, secret and all, next to each brain's token as
// google_calendar_credentials_<brain>.json, where a brain the sandbox does
// not hold can read it. The bridge names the files of that name that hold
// a secret, in one warning when it starts and on every brain's page, for
// you to delete, a hard link to a file kept elsewhere included. It deletes
// none itself, a Disconnect included: a file of that name may be one of
// yours that something else uses.
func TestLeftoverClientCopiesAreReported(t *testing.T) {
	// In place of a file's content: a FIFO, or a hard link or a symlink to
	// a file outside the folder with the content that follows.
	const fifo, hardLink, symlink = "\x00fifo", "\x00hard link to ", "\x00symlink to "
	linkedContent := func(content string) string {
		content = strings.TrimPrefix(content, hardLink)
		return strings.TrimPrefix(content, symlink)
	}
	desktop := fmt.Sprintf(testClientJSON, "https://oauth2.googleapis.com")
	web := `{"web":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"TEST-CLIENT-SECRET"}}`
	cases := []struct {
		name  string
		files map[string]string // in the credentials dir, beside brain a's token
		want  []string
	}{
		{"none", nil, nil},
		{"a copy of a Desktop app client", map[string]string{"google_calendar_credentials_a.json": desktop}, []string{"google_calendar_credentials_a.json"}},
		{"copies for two brains", map[string]string{"google_calendar_credentials_old.json": web, "google_calendar_credentials_a.json": desktop},
			[]string{"google_calendar_credentials_a.json", "google_calendar_credentials_old.json"}},
		{"a copy without a secret", map[string]string{"google_calendar_credentials_a.json": `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":""}}`}, nil},
		{"a copy that is not JSON", map[string]string{"google_calendar_credentials_a.json": "original"}, nil},
		{"another brain's token", map[string]string{"google_calendar_token_b.json": `{"refresh_token":"RT-B","client_id":"123-abc.apps.googleusercontent.com","client_secret":"TEST-CLIENT-SECRET"}`}, nil},
		{"another name", map[string]string{"google_calendar_credentials_a.json.bak": desktop}, nil},
		{"a FIFO", map[string]string{"google_calendar_credentials_a.json": fifo}, nil},
		{"a hard link to a copy", map[string]string{"google_calendar_credentials_a.json": hardLink + desktop}, []string{"google_calendar_credentials_a.json"}},
		{"a symlink to a copy", map[string]string{"google_calendar_credentials_a.json": symlink + desktop}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeGoogle(t)
			tb := newTestBridge(t, `, "sandbox": {"credentials_dir": "~/creds"}`)
			useGoogleServer(tb, srv)
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
			writeCalendarToken(t, tb, "a", "you@example.com", "RT-A")
			dir := tb.google.CredsDir()
			for name, content := range tc.files {
				path := filepath.Join(dir, name)
				var err error
				switch {
				case content == fifo:
					err = syscall.Mkfifo(path, 0o600)
				case strings.HasPrefix(content, hardLink), strings.HasPrefix(content, symlink):
					elsewhere := filepath.Join(t.TempDir(), name)
					if err := os.WriteFile(elsewhere, []byte(linkedContent(content)), 0o600); err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(content, hardLink) {
						err = os.Link(elsewhere, path)
					} else {
						err = os.Symlink(elsewhere, path)
					}
				default:
					err = os.WriteFile(path, []byte(content), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}

			// At start: one warning, naming the files but not the folder.
			logs := captureLog(t)
			started := make(chan struct{})
			go func() { defer close(started); google.New(googleOptionsFor(tb.cfgs)) }()
			if !within(started, 5*time.Second) {
				t.Fatal("looking for copies hangs")
			}
			var warnings []string
			for _, l := range logs() {
				if strings.Contains(l, "copies of the Google OAuth client") {
					warnings = append(warnings, l)
				}
			}
			if len(warnings) != min(len(tc.want), 1) {
				t.Errorf("warnings at start = %q, want one naming %q", warnings, tc.want)
			}
			for _, w := range warnings {
				for _, name := range tc.want {
					if !strings.Contains(w, name) {
						t.Errorf("the warning does not name %s: %s", name, w)
					}
				}
				if strings.Contains(w, dir) {
					t.Errorf("the warning names the folder: %s", w)
				}
				// It knows a name and a secret, not that nothing else uses the file.
				if !strings.Contains(w, "files named like the copies") || strings.Contains(w, "Nothing reads them") {
					t.Errorf("the warning claims more than the bridge knows: %s", w)
				}
			}

			var st struct {
				Copies []string `json:"leftover_client_copies"`
			}
			_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar?list=0", nil).Body.Bytes(), &st)
			if st.Copies == nil || !slices.Equal(st.Copies, tc.want) {
				t.Errorf("leftover_client_copies = %#v, want %q", st.Copies, tc.want)
			}

			if w := tb.do("DELETE", "/api/brains/a/calendar", nil); w.Code != 200 {
				t.Fatalf("disconnect = %d %s", w.Code, w.Body.String())
			}
			for name, content := range tc.files {
				fi, err := os.Lstat(filepath.Join(dir, name))
				if err != nil {
					t.Errorf("%s is gone after the disconnect: %v", name, err)
					continue
				}
				wantMode := os.FileMode(0)
				switch {
				case content == fifo:
					continue // a read would wait for a writer
				case strings.HasPrefix(content, symlink):
					wantMode = os.ModeSymlink
				}
				if data, _ := os.ReadFile(filepath.Join(dir, name)); fi.Mode().Type() != wantMode || string(data) != linkedContent(content) {
					t.Errorf("%s changed:\n%s", name, data)
				}
			}
		})
	}
}

// TestCalendarEnv: a run is told its brain and its calendars, for a
// brain's own scripts, but no longer the credentials folder, where older
// versions left copies of the OAuth client a brain the sandbox does not
// hold can read.

// Expected OAuth grants and guidance in the command integration contract.
var calendarScopes = []string{
	"https://www.googleapis.com/auth/calendar.events",                // list, create and delete events
	"https://www.googleapis.com/auth/calendar.events.freebusy",       // free: the busy times of the brain's calendars
	"https://www.googleapis.com/auth/calendar.calendarlist.readonly", // the account's calendars, for the brain's page and `calendars`
}

const calendarReconnectHint = "Ask the owner to connect the calendar again in the dashboard: Brains → this brain → Calendar → Sign in again."
const calendarCheckHint = "If it stays so, ask the owner to check that the calendar still exists and is shared with the brain's Google account, and which calendars the brain has: Brains → this brain → Calendar."
