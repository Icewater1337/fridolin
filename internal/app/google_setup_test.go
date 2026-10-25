package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// builtinTestClient stands in for a client baked into the build.
var builtinTestClient = google.Client{ClientID: "999-builtin.apps.googleusercontent.com", ClientSecret: "BUILTIN-SECRET"}

// useBuiltinGoogle is useGoogleServer for a build with a built-in client.
func useBuiltinGoogle(tb *testBridge, options google.Options) {
	options.Builtin = &builtinTestClient
	tb.google = google.New(options)
}

// connectBrain runs a brain's sign-in through to Google's return, with the
// code the fake Google exchanges.
func connectBrain(t *testing.T, tb *testBridge, id string) {
	t.Helper()
	w := tb.do("POST", "/api/brains/"+id+"/calendar/connect", nil)
	var start struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	u, err := url.Parse(start.AuthURL)
	if err != nil || start.AuthURL == "" {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	if c := signInCallbackQuery(tb, u.Query().Get("state")); c != 302 {
		t.Fatalf("callback: %d", c)
	}
}

func signInCallbackQuery(tb *testBridge, state string) int {
	w := tb.do("POST", "/api/google/callback", map[string]string{"url": "http://127.0.0.1:8765/?state=" + url.QueryEscape(state) + "&code=good-code&scope=x"})
	if w.Code == 200 {
		return 302
	}
	return w.Code
}

// TestBuiltinGoogleClient: a build with a built-in client signs brains in
// with it until a client is pasted under Settings, which then wins; a
// brain that signed in with the built-in one keeps refreshing with it.
// Neither secret is ever shown.
func TestBuiltinGoogleClient(t *testing.T) {
	t.Parallel()
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useBuiltinGoogle(tb, googleTestOptions(googleOptionsFor(tb.cfgs), srv))
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})

	var c struct {
		Configured bool     `json:"configured"`
		Builtin    bool     `json:"builtin"`
		Source     string   `json:"source"`
		Scopes     []string `json:"scopes"`
	}
	body := tb.do("GET", "/api/google/client", nil).Body.String()
	_ = json.Unmarshal([]byte(body), &c)
	if !c.Configured || !c.Builtin || c.Source != google.SourceBuiltin || len(c.Scopes) != 4 || strings.Contains(body, "BUILTIN-SECRET") {
		t.Fatalf("client = %s", body)
	}

	connectBrain(t, tb, "a")
	if (*got)["client_id"] != builtinTestClient.ClientID || (*got)["client_secret"] != "BUILTIN-SECRET" {
		t.Errorf("the sign-in used %v, not the built-in client", *got)
	}

	// A pasted client wins over the built-in one.
	if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_ = json.Unmarshal(tb.do("GET", "/api/google/client", nil).Body.Bytes(), &c)
	if c.Source != google.SourceOwn || !c.Builtin {
		t.Errorf("after the paste: %+v", c)
	}

	// The brain's token is from the built-in client: it refreshes with that.
	tok, err := os.ReadFile(tb.google.TokenPath("a"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	_ = json.Unmarshal(tok, &fields)
	fields["expiry"] = "2020-01-01T00:00:00.000000Z"
	tok, _ = json.Marshal(fields)
	if err := os.WriteFile(tb.google.TokenPath("a"), tok, 0o600); err != nil {
		t.Fatal(err)
	}
	*got = map[string]string{}
	st := tb.do("GET", "/api/brains/a/calendar", nil).Body.String()
	if strings.Contains(st, "list_error") || (*got)["client_secret"] != "BUILTIN-SECRET" || (*got)["grant_type"] != "refresh_token" {
		t.Errorf("refresh: status %s, token request %v", st, *got)
	}
}

// TestGoogleClientCheck: the check after a paste says what is wrong with a
// client, from Google's answer to a code no sign-in returns, and never
// shows the secret.
func TestGoogleClientCheck(t *testing.T) {
	t.Parallel()
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	if w := tb.do("POST", "/api/google/client/check", nil); w.Code != 412 {
		t.Errorf("without a client: %d %s", w.Code, w.Body.String())
	}
	cases := []struct {
		name, json string
		status     string
		item, want string
	}{
		{"good", `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"GOOD-SECRET"}}`, "ok", "Known to Google", "recognised"},
		{"wrong secret", `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"WRONG-SECRET"}}`, "bad", "Known to Google", "does not accept"},
		{"web", `{"web":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"GOOD-SECRET"}}`, "warn", "Redirect", "http://127.0.0.1:8765/"},
		{"not an id", `{"installed":{"client_id":"my-project","client_secret":"GOOD-SECRET"}}`, "bad", "Client ID", "does not look like"},
	}
	for _, tc := range cases {
		if w := tb.do("PUT", "/api/google/client", map[string]string{"json": tc.json}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		w := tb.do("POST", "/api/google/client/check", nil)
		var res google.ClientCheck
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if w.Code != 200 || res.Status != tc.status || strings.Contains(w.Body.String(), "SECRET") {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body.String())
			continue
		}
		found := false
		for _, it := range res.Items {
			found = found || (it.Name == tc.item && strings.Contains(it.Detail, tc.want))
		}
		if !found {
			t.Errorf("%s: no item %q saying %q: %+v", tc.name, tc.item, tc.want, res.Items)
		}
		if (*got)["code"] != "fridolin-client-check" || (*got)["grant_type"] != "authorization_code" {
			t.Errorf("%s: the probe sent %v", tc.name, *got)
		}
	}
}

// TestPastedGoogleCallback: a dashboard opened from another machine sends
// the browser back to a loopback address there, where nothing answers;
// pasting that address finishes the sign-in once, and nothing else does.
func TestPastedGoogleCallback(t *testing.T) {
	t.Parallel()
	srv, _ := fakeGoogle(t)
	tb := newTestBridge(t, "")
	useGoogleServer(tb, srv)
	_ = tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)})
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})

	for _, bad := range []string{"", "hello", "http://127.0.0.1:8765/?code=x", "http://127.0.0.1:8765/?state=forged&code=x"} {
		if w := tb.do("POST", "/api/google/callback", map[string]string{"url": bad}); w.Code != 422 {
			t.Errorf("%q: %d %s", bad, w.Code, w.Body.String())
		}
	}
	w := tb.do("POST", "/api/brains/a/calendar/connect", nil)
	var start struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	u, _ := url.Parse(start.AuthURL)
	pasted := "  http://127.0.0.1:8765/?state=" + url.QueryEscape(u.Query().Get("state")) + "&code=good-code\n"
	w = tb.do("POST", "/api/google/callback", map[string]string{"url": pasted})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"redirect":"/#/brains/a"`) {
		t.Fatalf("pasted: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(tb.google.TokenPath("a")); err != nil {
		t.Errorf("no token after the pasted sign-in: %v", err)
	}
	if w := tb.do("POST", "/api/google/callback", map[string]string{"url": pasted}); w.Code != 422 {
		t.Errorf("replayed: %d", w.Code)
	}
}
