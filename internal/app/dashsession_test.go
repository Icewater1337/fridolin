package app

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/controlcli"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// login posts to /api/login the way the dashboard page does.
func (tb *testBridge) login(body any, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	return tb.do("POST", "/api/login", body, append([]func(*http.Request){func(r *http.Request) { r.Header.Del("X-Bridge-Token") }}, mutate...)...)
}

// signIn is a session as the browser holds it: the cookie, and the binding
// the page keeps.
type signIn struct {
	cookie  *http.Cookie
	binding string
}

// sessionOf is the session w started (its cookie and the binding in its
// answer), or nil.
func sessionOf(w *httptest.ResponseRecorder) *signIn {
	var body struct{ Binding string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	for _, c := range w.Result().Cookies() {
		if c.Name == "fridolin_session_8765" && c.Value != "" {
			return &signIn{cookie: c, binding: body.Binding}
		}
	}
	return nil
}

// withSession sends the request as the page does with session s, instead
// of the token: the cookie, and the binding in its header.
func withSession(s *signIn) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Del("X-Bridge-Token")
		r.AddCookie(s.cookie)
		r.Header.Set(httpapi.SessionHeader, s.binding)
	}
}

// The browser signs in with a login code or the token and gets a session
// cookie that scripts on the page cannot read; the code works once.
func TestDashboardSignIn(t *testing.T) {
	tb := newTestBridge(t, "")
	now := time.Now
	var err error
	tb.sessions, err = httpapi.LoadDashboardSessions(tb.cfgs.Get().DataDir, func() time.Time { return now() })
	if err != nil {
		t.Fatal(err)
	}
	code, _ := tb.sessions.NewCode()

	w := tb.login(map[string]string{"code": code})
	c := sessionOf(w)
	if w.Code != 200 || c == nil || c.binding == "" {
		t.Fatalf("sign in with a code: %d %s, session %v", w.Code, w.Body.String(), c)
	}
	if k := c.cookie; !k.HttpOnly || k.SameSite != http.SameSiteStrictMode || k.Path != "/" || k.MaxAge <= 0 {
		t.Errorf("cookie %+v, want HttpOnly, SameSite=Strict, Path=/ and an expiry", k)
	}
	if c.binding == c.cookie.Value {
		t.Error("the binding is the session id")
	}
	if w := tb.do("GET", "/api/status", nil, withSession(c)); w.Code != 200 {
		t.Fatalf("API with the session: %d", w.Code)
	}

	cases := []struct {
		name   string
		body   any
		mutate func(*http.Request)
		want   int
	}{
		{"the code again", map[string]string{"code": code}, nil, 401},
		{"a made-up code", map[string]string{"code": "nope"}, nil, 401},
		{"a wrong token", map[string]string{"token": "nope"}, nil, 401},
		{"nothing", map[string]string{}, nil, 401},
		{"the token", map[string]string{"token": tb.token.Value()}, nil, 200},
		{"no dashboard header", map[string]string{"token": tb.token.Value()}, func(r *http.Request) { r.Header.Del("X-Bridge-Dashboard") }, 403},
		{"from another site", map[string]string{"token": tb.token.Value()}, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }, 403},
		{"a form", map[string]string{"token": tb.token.Value()}, func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, 415},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var muts []func(*http.Request)
			if tc.mutate != nil {
				muts = append(muts, tc.mutate)
			}
			w := tb.login(tc.body, muts...)
			if w.Code != tc.want || (tc.want == 200) != (sessionOf(w) != nil) {
				t.Fatalf("%d %s (cookie %v), want %d", w.Code, w.Body.String(), sessionOf(w), tc.want)
			}
		})
	}

	// A request with the token and a code signs in with the token and
	// leaves the code for the link it came from.
	spare, _ := tb.sessions.NewCode()
	if w := tb.login(map[string]string{"token": tb.token.Value(), "code": spare}); w.Code != 200 {
		t.Fatalf("the token and a code: %d %s", w.Code, w.Body.String())
	}
	if w := tb.login(map[string]string{"code": spare}); w.Code != 200 {
		t.Errorf("the code after a sign-in with the token and it: %d %s", w.Code, w.Body.String())
	}

	// A code that waited too long is refused.
	late, _ := tb.sessions.NewCode()
	now = func() time.Time { return time.Now().Add(httpapi.LoginCodeLifetime + time.Second) }
	if w := tb.login(map[string]string{"code": late}); w.Code != 401 {
		t.Errorf("an expired code: %d", w.Code)
	}
	// And so is a session past its lifetime.
	now = func() time.Time { return time.Now().Add(httpapi.SessionLifetime + time.Hour) }
	if w := tb.do("GET", "/api/status", nil, withSession(c)); w.Code != 401 {
		t.Errorf("an ended session: %d", w.Code)
	}
}

// Signing in again, as every `fridolin open` does, ends the session the
// browser's cookie named, rather than leaving it for maxSessions to push
// out; a refused sign-in leaves it alone.
func TestSignInAgainReplacesTheSession(t *testing.T) {
	tb := newTestBridge(t, "")
	first := sessionOf(tb.login(map[string]string{"token": tb.token.Value()}))
	other := sessionOf(tb.login(map[string]string{"token": tb.token.Value()})) // another browser's
	if first == nil || other == nil {
		t.Fatal("no sessions")
	}
	withCookie := func(s *signIn) func(*http.Request) { return func(r *http.Request) { r.AddCookie(s.cookie) } }
	if w := tb.login(map[string]string{"code": "nope"}, withCookie(first)); w.Code != 401 {
		t.Fatalf("a made-up code: %d", w.Code)
	}
	if w := tb.do("GET", "/api/status", nil, withSession(first)); w.Code != 200 {
		t.Fatalf("a refused sign-in ended the session: %d", w.Code)
	}
	code, _ := tb.sessions.NewCode()
	second := sessionOf(tb.login(map[string]string{"code": code}, withCookie(first)))
	if second == nil {
		t.Fatal("no new session")
	}
	cases := []struct {
		name string
		s    *signIn
		want int
	}{
		{"the session the cookie named", first, 401},
		{"the new one", second, 200},
		{"another browser's", other, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := tb.do("GET", "/api/status", nil, withSession(c.s)); w.Code != c.want {
				t.Errorf("%d, want %d", w.Code, c.want)
			}
		})
	}
}

// A session signs a request in only with its cookie and its binding
// together. The cookie reaches every server on 127.0.0.1, whatever its
// port (a site builder preview runs a brain's code on one), and the
// binding lives in the browser's storage on disk; neither may be enough
// on its own.
func TestSessionNeedsCookieAndBinding(t *testing.T) {
	tb := newTestBridge(t, "")
	a := sessionOf(tb.login(map[string]string{"token": tb.token.Value()}))
	b := sessionOf(tb.login(map[string]string{"token": tb.token.Value()}))
	if a == nil || b == nil {
		t.Fatal("no sessions")
	}
	cases := []struct {
		name    string
		cookie  *http.Cookie
		binding string
		want    int
	}{
		{"cookie and binding", a.cookie, a.binding, 200},
		{"the cookie alone", a.cookie, "", 401},
		{"the binding alone", nil, a.binding, 401},
		{"the cookie with another session's binding", a.cookie, b.binding, 401},
		{"the cookie with its id as the binding", a.cookie, a.cookie.Value, 401},
	}
	for _, c := range cases {
		for _, method := range []string{"GET", "POST"} {
			t.Run(c.name+" "+method, func(t *testing.T) {
				path := "/api/status"
				if method == "POST" {
					path = "/api/login/code" // a write that leaves the session as it is
				}
				w := tb.do(method, path, nil, func(r *http.Request) {
					r.Header.Del("X-Bridge-Token")
					if c.cookie != nil {
						r.AddCookie(c.cookie)
					}
					if c.binding != "" {
						r.Header.Set(httpapi.SessionHeader, c.binding)
					}
				})
				if w.Code != c.want {
					t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), c.want)
				}
			})
		}
	}
}

// Sessions survive a restart, are stored only as hashes, and end on sign
// out; rotating the token signs out every browser and the old token.
func TestDashboardSessionsLastAndEnd(t *testing.T) {
	tb := newTestBridge(t, "")
	a := sessionOf(tb.login(map[string]string{"token": tb.token.Value()}))
	b := sessionOf(tb.login(map[string]string{"token": tb.token.Value()}))
	if a == nil || b == nil {
		t.Fatal("no sessions")
	}
	file := filepath.Join(tb.cfgs.Get().DataDir, "dashboard-sessions.json")
	data, err := os.ReadFile(file)
	for _, secret := range []string{a.cookie.Value, a.binding, b.cookie.Value, b.binding} {
		if err != nil || strings.Contains(string(data), secret) {
			t.Fatalf("sessions file %q (%v) holds a session id or binding", data, err)
		}
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("sessions file mode %v", st.Mode().Perm())
	}
	again, err := httpapi.LoadDashboardSessions(tb.cfgs.Get().DataDir, time.Now)
	if err != nil || !again.Valid(a.cookie.Value, a.binding) || !again.Valid(b.cookie.Value, b.binding) {
		t.Fatalf("after a restart the sessions are gone (%v)", err)
	}
	if again.Valid(a.cookie.Value, "") || again.Valid(a.cookie.Value, b.binding) {
		t.Fatal("after a restart a session signs in without its binding")
	}

	if w := tb.do("POST", "/api/logout", nil, withSession(a)); w.Code != 200 {
		t.Fatalf("sign out: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("GET", "/api/status", nil, withSession(a)); w.Code != 401 {
		t.Errorf("a signed-out session: %d", w.Code)
	}
	if w := tb.do("GET", "/api/status", nil, withSession(b)); w.Code != 200 {
		t.Errorf("another session after a sign out: %d", w.Code)
	}

	old := tb.token.Value()
	w := tb.do("POST", "/api/token/rotate", nil, withSession(b))
	fresh := sessionOf(w)
	if w.Code != 200 || fresh == nil || fresh.binding == "" {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	if tb.token.Value() == old {
		t.Fatal("the token did not change")
	}
	if onDisk, _ := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "dashboard.token")); strings.TrimSpace(string(onDisk)) != tb.token.Value() {
		t.Error("the token file was not replaced")
	}
	for name, check := range map[string]struct {
		mutate func(*http.Request)
		want   int
	}{
		"the old token":          {func(r *http.Request) { r.Header.Set("X-Bridge-Token", old) }, 401},
		"the session that asked": {withSession(b), 401},
		"its new session":        {withSession(fresh), 200},
		"the new token":          {func(r *http.Request) { r.Header.Set("X-Bridge-Token", tb.token.Value()) }, 200},
	} {
		if w := tb.do("GET", "/api/status", nil, check.mutate); w.Code != check.want {
			t.Errorf("%s: %d, want %d", name, w.Code, check.want)
		}
	}
}

// Rotating forgets the login codes not used yet. A caller that sent the
// token, as `fridolin rotate-token` does, is no browser and gets no
// session, which would only sit in the sessions file for a month.
func TestRotateForgetsLoginCodes(t *testing.T) {
	tb := newTestBridge(t, "")
	code, _ := tb.sessions.NewCode()
	w := tb.do("POST", "/api/token/rotate", nil)
	if w.Code != 200 {
		t.Fatalf("rotate: %d", w.Code)
	}
	if s := sessionOf(w); s != nil || strings.Contains(w.Body.String(), "binding") {
		t.Errorf("rotating with the token started a session: %v %s", s, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "dashboard-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if n := len(persisted.Sessions); n != 0 {
		t.Errorf("%d sessions after rotating with the token, want 0", n)
	}
	if w := tb.login(map[string]string{"code": code}); w.Code != 401 {
		t.Errorf("a code from before the rotation: %d", w.Code)
	}
}

func TestSessionCookieNamePerPort(t *testing.T) {
	if a, b := httpapi.SessionCookieName("127.0.0.1:8765"), httpapi.SessionCookieName("127.0.0.1:9999"); a == b || a != "fridolin_session_8765" {
		t.Fatalf("%q, %q", a, b)
	}
}

// The session cookie is Secure on a TLS connection only: over the plain
// HTTP the bridge serves, a Secure cookie would not be kept by every
// browser.
func TestSessionCookieSecureOnlyOverTLS(t *testing.T) {
	overTLS := func(r *http.Request) { r.TLS = &tls.ConnectionState{} }
	cases := []struct {
		name, path string
		mutate     []func(*http.Request)
		want       bool
	}{
		{"sign in", "/api/login", nil, false},
		{"sign in over TLS", "/api/login", []func(*http.Request){overTLS}, true},
		{"sign out", "/api/logout", nil, false},
		{"sign out over TLS", "/api/logout", []func(*http.Request){overTLS}, true},
		{"rotate the token", "/api/token/rotate", nil, false},
		{"rotate the token over TLS", "/api/token/rotate", []func(*http.Request){overTLS}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			s := sessionOf(tb.login(map[string]string{"token": tb.token.Value()}))
			if s == nil {
				t.Fatal("no session")
			}
			var w *httptest.ResponseRecorder
			if c.path == "/api/login" {
				w = tb.login(map[string]string{"token": tb.token.Value()}, c.mutate...)
			} else {
				w = tb.do("POST", c.path, nil, append([]func(*http.Request){withSession(s)}, c.mutate...)...)
			}
			cookies := w.Result().Cookies()
			if w.Code != 200 || len(cookies) != 1 {
				t.Fatalf("%d %s, cookies %v", w.Code, w.Body.String(), cookies)
			}
			if cookies[0].Secure != c.want {
				t.Errorf("cookie %+v, want Secure=%v", cookies[0], c.want)
			}
		})
	}
}

// serveBridge serves tb on a real loopback port, as the bridge does, and
// returns its address.
func serveBridge(t *testing.T, tb *testBridge) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	listen := srv.Listener.Addr().String()
	srv.Config.Handler = tb.newHTTPHandler(listen)
	srv.Start()
	t.Cleanup(srv.Close)
	return listen
}

// closedPort is a loopback address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// fridolin open asks the running bridge for a login code: the link it
// opens holds that, not the token.
func TestOpenAsksForALoginCode(t *testing.T) {
	tb := newTestBridge(t, "")
	listen := serveBridge(t, tb)
	code, err := controlcli.AskLoginCode(listen, tb.token.Value())
	if err != nil || code == "" || code == tb.token.Value() {
		t.Fatalf("code %q, %v", code, err)
	}
	if url := httpapi.LoginURL(listen, code); strings.Contains(url, tb.token.Value()) || !strings.HasSuffix(url, "/#login="+code) {
		t.Errorf("url %q", url)
	}
	if !tb.sessions.UseCode(code) {
		t.Error("the code does not sign in")
	}
	if _, err := controlcli.AskLoginCode(listen, "wrong"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a wrong token: %v", err)
	}
	if _, err := controlcli.AskLoginCode(closedPort(t), tb.token.Value()); !errors.Is(err, controlcli.ErrBridgeDown) {
		t.Errorf("no bridge: %v", err)
	}
}

// stubBridge answers every request with status and body, as a bridge of
// another build might, and returns its address.
func stubBridge(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// fridolin open against bridges of other builds: one older than login
// codes answers 404, and the command says to restart it rather than
// putting the token in the link, as that build's own `open` did.
func TestDashboardLink(t *testing.T) {
	const token = "0123456789abcdef"
	cases := []struct {
		name, body string
		status     int
		link       string // "" for an error
		older      bool
		errHas     string
	}{
		{"a login code", `{"code":"c0de"}`, http.StatusOK, "/#login=c0de", false, ""},
		{"a bridge older than login codes", `{"error":"no such API endpoint"}`, http.StatusNotFound, "", true, "Restart it on this build"},
		{"another 404", "404 page not found\n", http.StatusNotFound, "", true, "has no POST /api/login/code"},
		{"a wrong token", `{"error":"invalid token"}`, http.StatusUnauthorized, "", false, "refused: invalid token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.File{DataDir: t.TempDir(), HTTPListen: stubBridge(t, c.status, c.body)}
			if err := os.WriteFile(filepath.Join(cfg.DataDir, "dashboard.token"), []byte(token+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			link, err := controlcli.DashboardLink(resolvedSettings(t, cfg))
			if c.link != "" {
				if err != nil || link != "http://"+cfg.HTTPListen+c.link {
					t.Fatalf("link %q, %v", link, err)
				}
				return
			}
			if err == nil || errors.Is(err, controlcli.ErrOlderBridge) != c.older || !strings.Contains(err.Error(), c.errHas) {
				t.Fatalf("link %q, err %v", link, err)
			}
			if link != "" || strings.Contains(err.Error(), token) {
				t.Errorf("the token is in what it gives: %q, %v", link, err)
			}
		})
	}
}

// fridolin rotate-token goes through the running bridge, or, when none
// runs, changes the files itself; a bridge that runs without answering is
// left alone.
func TestRotateTokenCommand(t *testing.T) {
	t.Run("through the bridge", func(t *testing.T) {
		tb := newTestBridge(t, "")
		cfg := *tb.cfgs.Get()
		cfg.HTTPListen = serveBridge(t, tb)
		old := tb.token.Value()
		if err := controlcli.RotateToken(&cfg); err != nil {
			t.Fatal(err)
		}
		if tb.token.Value() == old {
			t.Error("the running bridge kept its token")
		}
	})
	t.Run("an older bridge", func(t *testing.T) {
		tb := newTestBridge(t, "")
		cfg := *tb.cfgs.Get()
		cfg.HTTPListen = stubBridge(t, http.StatusNotFound, `{"error":"no such API endpoint"}`)
		old := tb.token.Value()
		if err := controlcli.RotateToken(&cfg); !errors.Is(err, controlcli.ErrOlderBridge) || !strings.Contains(err.Error(), "Restart it on this build") {
			t.Fatalf("got %v", err)
		}
		if data, _ := os.ReadFile(filepath.Join(cfg.DataDir, "dashboard.token")); strings.TrimSpace(string(data)) != old {
			t.Error("the token file changed under a running bridge")
		}
	})
	// With the dashboard off, the bridge has no /api/ and answers the
	// mux's 404, which is no sign of an older build.
	t.Run("a bridge with the dashboard off", func(t *testing.T) {
		tb := newTestBridge(t, `, "dashboard": {"disabled": true}`)
		cfg := *tb.cfgs.Get()
		cfg.HTTPListen = serveBridge(t, tb)
		old := tb.token.Value()
		if err := controlcli.RotateToken(&cfg); err == nil || errors.Is(err, controlcli.ErrOlderBridge) || !strings.Contains(err.Error(), "dashboard is turned off") {
			t.Fatalf("got %v", err)
		}
		if data, _ := os.ReadFile(filepath.Join(cfg.DataDir, "dashboard.token")); strings.TrimSpace(string(data)) != old || tb.token.Value() != old {
			t.Error("the token changed under a running bridge")
		}
	})
	t.Run("a bridge that does not answer", func(t *testing.T) {
		tb := newTestBridge(t, "")
		cfg := *tb.cfgs.Get()
		cfg.HTTPListen = closedPort(t)
		old := tb.token.Value()
		if err := controlcli.RotateToken(&cfg); err == nil || !strings.Contains(err.Error(), "does not answer") {
			t.Fatalf("got %v", err)
		}
		if data, _ := os.ReadFile(filepath.Join(cfg.DataDir, "dashboard.token")); strings.TrimSpace(string(data)) != old {
			t.Error("the token file changed under a running bridge")
		}
	})
	// Without a bridge the files change, whether the dashboard is on or not.
	for _, c := range []struct {
		name string
		off  bool
	}{{"no bridge", false}, {"no bridge, the dashboard off", true}} {
		t.Run(c.name, func(t *testing.T) {
			dataDir := t.TempDir()
			tok, err := httpapi.LoadDashboardToken(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			sessions, _ := httpapi.LoadDashboardSessions(dataDir, time.Now)
			id, binding, _, _ := sessions.Create()
			cfg := &config.File{DataDir: dataDir, HTTPListen: closedPort(t)}
			cfg.Dashboard.Disabled = c.off
			if err := controlcli.RotateToken(resolvedSettings(t, cfg)); err != nil {
				t.Fatal(err)
			}
			after, err := httpapi.LoadDashboardToken(dataDir)
			if err != nil || after.Value() == tok.Value() {
				t.Errorf("token %q → %q (%v)", tok.Value(), after.Value(), err)
			}
			if again, _ := httpapi.LoadDashboardSessions(dataDir, time.Now); again.Valid(id, binding) {
				t.Error("a session survived the rotation")
			}
		})
	}
}

// The login endpoint's answers carry no secret either.
func TestLoginAnswersCarryNoToken(t *testing.T) {
	tb := newTestBridge(t, "")
	for _, w := range []*httptest.ResponseRecorder{
		tb.login(map[string]string{"token": tb.token.Value()}),
		tb.do("POST", "/api/login/code", nil),
	} {
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || strings.Contains(w.Body.String(), tb.token.Value()) || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%d %s (%v), Cache-Control %q", w.Code, w.Body.String(), err, w.Header().Get("Cache-Control"))
		}
	}
}
