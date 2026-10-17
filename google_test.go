package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
			fmt.Fprint(w, `{"access_token":"ACCESS-1","refresh_token":"RT-1","expires_in":3600}`)
		case "/calendar/v3/users/me/calendarList":
			if r.Header.Get("Authorization") != "Bearer ACCESS-1" {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"message":"bad token"}}`)
				return
			}
			fmt.Fprint(w, `{"items":[{"id":"team@example.com","summary":"Team","accessRole":"writer"},{"id":"you@example.com","summary":"you@example.com","primary":true,"accessRole":"owner"}]}`)
		case "/revoke":
			got["revoked"] = r.FormValue("token")
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
		q.Get("access_type") != "offline" || q.Get("scope") != calendarScope || q.Get("state") == "" {
		t.Errorf("auth params = %v", q)
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
	if tok.RefreshToken != "RT-1" || tok.ClientID == "" || tok.Account != "you@example.com" || len(tok.Scopes) != 1 {
		t.Errorf("token file = %+v", tok)
	}
	if st, _ := os.Stat(tokPath); st.Mode().Perm() != 0o600 {
		t.Errorf("token mode %v", st.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(tb.cfgs.Get().DataDir, "google_calendar_credentials_fam.json")); err != nil {
		t.Error("credentials copy for the tool not written")
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

// TestCalendarTokenIsNotFollowed: each brain's sandbox lets it write its
// own token file, so a brain can make it a link to another brain's token
// in the same folder. The dashboard's status page would then refresh that
// token and write it, refresh token and all, where the first brain reads
// it, and Disconnect would revoke it at Google. The bridge refuses such a
// token file, leaves the other brain's token alone, and removes only the
// link on disconnect.
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

// TestCalendarTokenRequestsGoToGoogleWithYourClient: the brain can write
// its token file, so the bridge takes neither the token_uri nor the client
// in it. It refreshes at Google's token endpoint with the client you
// configured, and a token from another client cannot be refreshed. The
// token_uri of a pasted client file is not used either.
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

// TestOversizedCalendarTokenIsRefused: a brain can make its token file as
// large as it likes. The bridge refuses one larger than maxBrainFile
// rather than load it, and so neither refreshes it nor revokes it.
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
