package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const testClientJSON = `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"TEST-CLIENT-SECRET","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"%s/token","redirect_uris":["http://localhost"]}}`

// fakeGoogle is a token endpoint plus a calendarList endpoint.
func fakeGoogle(t *testing.T) (*httptest.Server, *map[string]string) {
	t.Helper()
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			for k, v := range r.PostForm {
				got[k] = v[0]
			}
			if r.PostForm.Get("code") == "bad" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Bad Request"}`)
				return
			}
			switch r.PostForm.Get("refresh_token") {
			case "REVOKED":
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
				return
			case "GOOGLE-DOWN":
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"backend_error"}`)
				return
			case "CLIENT-GONE":
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":"invalid_client","error_description":"The OAuth client was not found."}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"ACCESS-1","refresh_token":"RT-1","expires_in":3600}`)
		case "/calendar/v3/users/me/calendarList":
			if r.Header.Get("Authorization") != "Bearer ACCESS-1" {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"message":"bad token"}}`)
				return
			}
			fmt.Fprint(w, `{"items":[{"id":"team@example.com","summary":"Team","accessRole":"writer"},{"id":"you@example.com","summary":"you@example.com","primary":true,"accessRole":"owner"}]}`)
		case "/revoke":
			// Tokens named so steer the answer: Google failing, or one it
			// revoked already.
			switch tok := r.FormValue("token"); tok {
			case "FAIL-REVOKE":
				w.WriteHeader(503)
			case "GONE-REVOKE":
				got["revoked"] = tok
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_token","error_description":"Token expired or revoked"}`)
			default:
				got["revoked"] = tok
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	googleAPIBase, googleTokenEndpoint, googleRevokeEndpoint = srv.URL, srv.URL+"/token", srv.URL+"/revoke"
	t.Cleanup(func() {
		googleAPIBase, googleTokenEndpoint, googleRevokeEndpoint = "https://www.googleapis.com", "https://oauth2.googleapis.com/token", "https://oauth2.googleapis.com/revoke"
	})
	return srv, &got
}

func TestParseGoogleClient(t *testing.T) {
	c, err := ParseGoogleClient([]byte(fmt.Sprintf(testClientJSON, "https://oauth2.googleapis.com")))
	if err != nil || c.Type != "installed" || c.ClientID == "" {
		t.Fatalf("parse = %+v, %v", c, err)
	}
	for _, bad := range []string{`{}`, `{"installed":{}}`, `not json`, `{"service_account":{"x":1}}`} {
		if _, err := ParseGoogleClient([]byte(bad)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}

func TestCalendarSignInFlow(t *testing.T) {
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/fam", "create_dir": true, "manifest": BrainManifest{ID: "fam", Name: "Fam"}}); w.Code != 200 {
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
	if w := cb("state=forged&code=x"); w.Code != 400 {
		t.Errorf("forged state: %d", w.Code)
	}

	// Google returns: token exchanged with the PKCE verifier, files written.
	w = cb("state=" + url.QueryEscape(q.Get("state")) + "&code=good-code")
	if w.Code != 302 || w.Header().Get("Location") != "/#/brains/fam" {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	if (*got)["code_verifier"] == "" || (*got)["code"] != "good-code" || (*got)["client_secret"] != "TEST-CLIENT-SECRET" {
		t.Errorf("token request = %v", *got)
	}
	tokPath := filepath.Join(tb.cfgs.Get().DataDir, "google_calendar_token_fam.json")
	data, err := os.ReadFile(tokPath)
	if err != nil {
		t.Fatal(err)
	}
	var tok googleToken
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
	m, _ := ReadManifest(filepath.Join(tb.home, "fam"))
	if m.Calendar == nil || m.Calendar.Account != "you@example.com" || m.Calendar.Default != "you@example.com" {
		t.Fatalf("manifest calendar = %+v", m.Calendar)
	}
	if tb.brainMgr.Get("fam").settings.Load().manifest.Calendar == nil {
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
	env := strings.Join(calendarEnv("fam", tb.brainMgr.Get("fam").settings.Load().manifest, tb.google.credsDir()), " ")
	if !strings.Contains(env, "BRIDGE_CALENDARS=you@example.com,team@example.com") || !strings.Contains(env, "BRIDGE_CALENDAR_DEFAULT=team@example.com") {
		t.Errorf("env = %s", env)
	}
	var views []brainView
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
	tb.addFolderBrain(t, BrainManifest{ID: "p", Name: "P"})
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
		tb.h.ServeHTTP(httptest.NewRecorder(), r)
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

func TestCalendarTokenGoesToCredentialsDir(t *testing.T) {
	tb := newTestBridge(t, `, "sandbox": {"credentials_dir": "~/creds"}`)
	if got, want := tb.google.tokenPath("x"), filepath.Join(tb.home, "creds", "google_calendar_token_x.json"); got != want {
		t.Errorf("token path = %s, want %s", got, want)
	}
	// An existing (possibly write-protected) credentials file is never replaced.
	_ = os.MkdirAll(filepath.Join(tb.home, "creds"), 0o700)
	keep := filepath.Join(tb.home, "creds", "google_calendar_credentials_x.json")
	_ = os.WriteFile(keep, []byte("original"), 0o600)
	_, _ = tb.google.SetClient([]byte(fmt.Sprintf(testClientJSON, "https://oauth2.googleapis.com")))
	if err := tb.google.saveToken("x", &googleToken{Token: "a", RefreshToken: "b"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(keep); string(data) != "original" {
		t.Error("an existing credentials file was overwritten")
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
		{"symlink", func(t *testing.T, own, other string) { mustSymlink(t, other, own) }, errSymlink.Error()},
		{"relative symlink", func(t *testing.T, own, other string) { mustSymlink(t, filepath.Base(other), own) }, errSymlink.Error()},
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
			if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			for _, id := range []string{"a", "b"} {
				if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/" + id, "create_dir": true, "manifest": BrainManifest{ID: id, Name: strings.ToUpper(id)}}); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}
			// Brain b's token has expired, so using it means refreshing it.
			other := tb.google.tokenPath("b")
			bTok, _ := json.MarshalIndent(googleToken{
				Token: "B-ACCESS", RefreshToken: "B-REFRESH", TokenURI: srv.URL + "/token",
				ClientID: "123-abc.apps.googleusercontent.com", ClientSecret: "TEST-CLIENT-SECRET",
				Expiry: "2020-01-01T00:00:00.000000Z",
			}, "", "  ")
			if err := os.WriteFile(other, bTok, 0o600); err != nil {
				t.Fatal(err)
			}
			own := tb.google.tokenPath("a")
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
			_, got := fakeGoogle(t)
			tb := newTestBridge(t, "")
			// The pasted client names another token endpoint too.
			if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, other.URL)}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": BrainManifest{ID: "a", Name: "A"}}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			tok, _ := json.Marshal(googleToken{
				Token: "OLD", RefreshToken: "A-REFRESH", TokenURI: other.URL + "/token",
				ClientID: tc.clientID, ClientSecret: "SECRET-THE-BRAIN-WROTE",
				Expiry: "2020-01-01T00:00:00.000000Z",
			})
			if err := os.WriteFile(tb.google.tokenPath("a"), tok, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.noClient {
				if w := tb.do("DELETE", "/api/google/client", nil); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}

			var st struct {
				Calendars []CalendarEntry `json:"calendars"`
				ListError string          `json:"list_error"`
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
		if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, other.URL)}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": BrainManifest{ID: "a", Name: "A"}}); w.Code != 200 {
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
		var tok googleToken
		data, _ := os.ReadFile(tb.google.tokenPath("a"))
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
	if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": BrainManifest{ID: "a", Name: "A"}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Valid JSON, expired, padded past the limit.
	tok, _ := json.Marshal(googleToken{
		Token: "OLD", RefreshToken: "A-REFRESH",
		ClientID: "123-abc.apps.googleusercontent.com", ClientSecret: "TEST-CLIENT-SECRET",
		Expiry: "2020-01-01T00:00:00.000000Z",
	})
	tok = append(tok, strings.Repeat(" ", maxBrainFile)...)
	if err := os.WriteFile(tb.google.tokenPath("a"), tok, 0o600); err != nil {
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
			google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			defer google.Close()
			prevAPI, prevToken := googleAPIBase, googleTokenEndpoint
			googleAPIBase, googleTokenEndpoint = google.URL, google.URL+"/token"
			defer func() { googleAPIBase, googleTokenEndpoint = prevAPI, prevToken }()

			tb := newTestBridge(t, "")
			tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
			if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, google.URL)}); w.Code != 200 {
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
			var tok googleToken
			data, _ := os.ReadFile(tb.google.tokenPath("a"))
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
			google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			defer google.Close()
			prev := googleTokenEndpoint
			googleTokenEndpoint = google.URL + "/token"
			defer func() { googleTokenEndpoint = prev }()

			tb := newTestBridge(t, "")
			tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
			if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, google.URL)}); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			data, _ := json.Marshal(googleToken{
				Token: "ACCESS-1", RefreshToken: "RT-1", ClientID: "123-abc.apps.googleusercontent.com",
				Scopes: tc.stored, Expiry: "2020-01-01T00:00:00.000000Z",
			})
			if err := os.WriteFile(tb.google.tokenPath("a"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if tok, err := tb.google.accessToken(context.Background(), "a"); err != nil || tok.Token != "ACCESS-2" {
				t.Fatalf("accessToken = %+v, %v; want it refreshed", tok, err)
			}
			var saved googleToken
			data, _ = os.ReadFile(tb.google.tokenPath("a"))
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
	data, _ := json.Marshal(googleToken{
		Token: "ACCESS-" + id, RefreshToken: refreshToken, ClientID: "123-abc.apps.googleusercontent.com",
		ClientSecret: "TEST-CLIENT-SECRET", Account: account, Expiry: "2099-01-01T00:00:00.000000Z",
	})
	if err := os.MkdirAll(tb.google.credsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.google.tokenPath(id), data, 0o600); err != nil {
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
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
			if tc.otherBrain {
				tb.addFolderBrain(t, BrainManifest{ID: "b", Name: "B"})
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
			if _, err := os.Lstat(tb.google.tokenPath("a")); !os.IsNotExist(err) {
				t.Error("a's token was not deleted")
			}
			if tc.other != "-" {
				if _, err := os.Stat(tb.google.tokenPath("b")); err != nil {
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
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
	writeCalendarToken(t, tb, "a", "you@example.com", "FAIL-REVOKE")

	w := tb.do("DELETE", "/api/brains/a/calendar", nil)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "not confirmed by Google") {
		t.Errorf("disconnect = %d %s, want 502", w.Code, w.Body.String())
	}
	if _, err := os.Stat(tb.google.tokenPath("a")); err != nil {
		t.Fatalf("the token was deleted although Google did not revoke it: %v", err)
	}
	w = tb.do("DELETE", "/api/brains/a/calendar?local_only=1", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "not revoked") {
		t.Errorf("local disconnect = %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Lstat(tb.google.tokenPath("a")); !os.IsNotExist(err) {
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

// TestCalendarTokenBrain: the brain a token file is for is read back from
// the name calendarTokenFile gives it, and from no other file there.
func TestCalendarTokenBrain(t *testing.T) {
	cases := []struct {
		name, id string
		ok       bool
	}{
		{filepath.Base(calendarTokenFile("creds", "a")), "a", true},
		{filepath.Base(calendarTokenFile("creds", "work-2")), "work-2", true},
		{"google_calendar_credentials_a.json", "", false}, // an older version's copy of the client
		{filepath.Base(calendarTokenFile("creds", "")), "", false},
		{"google_calendar_token_a.json.bak", "", false},
		{"rclone.conf", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if id, ok := calendarTokenBrain(tc.name); id != tc.id || ok != tc.ok {
				t.Errorf("calendarTokenBrain = %q, %v; want %q, %v", id, ok, tc.id, tc.ok)
			}
		})
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
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			dir := tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
			tb.google.mu.Lock()
			tb.google.lastError["a"] = "sign-in cancelled"
			tb.google.mu.Unlock()
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
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
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
			data, _ := os.ReadFile(tb.google.tokenPath("a"))
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
	if err := os.MkdirAll(tb.google.credsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.google.tokenPath("a"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCalendarRefreshKeepsTheToken: a refresh saves the new access token
// and the refresh token Google handed out with it, and keeps the fields an
// older calendar tool's library wrote.
func TestCalendarRefreshKeepsTheToken(t *testing.T) {
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
	expiredCalendarToken(t, tb, "A-REFRESH")

	var st struct {
		Calendars []CalendarEntry `json:"calendars"`
		ListError string          `json:"list_error"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/brains/a/calendar", nil).Body.Bytes(), &st)
	if st.ListError != "" || len(st.Calendars) != 2 || (*got)["refresh_token"] != "A-REFRESH" {
		t.Fatalf("status = %+v after refreshing with %v", st, *got)
	}
	data, _ := os.ReadFile(tb.google.tokenPath("a"))
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
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
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
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
	expiredCalendarToken(t, tb, "A-REFRESH")
	if err := os.Chmod(tb.google.credsDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tb.google.credsDir(), 0o700) })
	logs := captureLog(t)
	var st struct {
		Calendars []CalendarEntry `json:"calendars"`
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
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	signInCallback(t, tb, "a", gone, nil)
	if data, err := os.ReadFile(tb.google.tokenPath("a")); err != nil || !strings.Contains(string(data), "RT-1") {
		t.Errorf("token = %s, %v; want the sign-in saved", data, err)
	}
}

// TestCalendarSignInForARemovedBrain: a brain removed while it signed in
// gets no token, which a later brain of the same id would otherwise show as
// connected.
func TestCalendarSignInForARemovedBrain(t *testing.T) {
	srv, _ := fakeGoogle(t)
	tb := newTestBridge(t, "")
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
	signInCallback(t, tb, "a", context.Background(), func() {
		if w := tb.do("DELETE", "/api/brains/a", nil); w.Code != 200 {
			t.Fatalf("remove = %d %s", w.Code, w.Body.String())
		}
	})
	if _, err := os.Lstat(tb.google.tokenPath("a")); !os.IsNotExist(err) {
		t.Errorf("a token was written for the removed brain (%v)", err)
	}
	// Refused before anything is written, not taken back after the brain's
	// settings could not be saved.
	tb.google.mu.Lock()
	msg := tb.google.lastError["a"]
	tb.google.mu.Unlock()
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
			_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
			dir := tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
			if err := os.MkdirAll(tb.google.credsDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.switched {
				writeCalendarToken(t, tb, "a", "team@example.com", "RT-OLD")
			}
			if tc.readOnly == "credentials" {
				dir = tb.google.credsDir()
			}
			signInCallback(t, tb, "a", context.Background(), func() {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			})
			data, err := os.ReadFile(tb.google.tokenPath("a"))
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
	served := "http://" + tb.cfgs.Get().HTTPListen + "/"
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A"})
	if _, err := tb.cfgs.Update("", func(c *Config) error { c.HTTPListen = "127.0.0.1:1"; return nil }); err != nil {
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
