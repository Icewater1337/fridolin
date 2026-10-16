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
	"testing"
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
	googleAPIBase, googleRevokeEndpoint = srv.URL, srv.URL+"/revoke"
	t.Cleanup(func() {
		googleAPIBase, googleRevokeEndpoint = "https://www.googleapis.com", "https://oauth2.googleapis.com/revoke"
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
	tb := newTestBridge(t, `, "brains": [{"name": "p", "work_dir": "~/p"}]`)
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
