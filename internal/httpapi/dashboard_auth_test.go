package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type authSessionsFake struct {
	calls                 *[]string
	createErr, dropAllErr error
	valid                 bool
}

func (s *authSessionsFake) Create() (string, string, time.Time, error) {
	*s.calls = append(*s.calls, "create")
	return "session-id", "binding", time.Unix(2000000000, 0), s.createErr
}
func (s *authSessionsFake) Valid(string, string) bool    { return s.valid }
func (s *authSessionsFake) UseCode(string) bool          { *s.calls = append(*s.calls, "code"); return true }
func (s *authSessionsFake) Drop(string) error            { *s.calls = append(*s.calls, "drop"); return nil }
func (s *authSessionsFake) NewCode() (string, time.Time) { return "code", time.Unix(2000000000, 0) }
func (s *authSessionsFake) NewCodeFor(ttl time.Duration) (string, time.Time) {
	return "code-" + ttl.String(), time.Unix(2000000000, 0)
}
func (s *authSessionsFake) DropAll() error {
	*s.calls = append(*s.calls, "drop-all")
	return s.dropAllErr
}

type authTokenFake struct {
	calls     *[]string
	rotateErr error
}

func (t *authTokenFake) Matches(string) bool { *t.calls = append(*t.calls, "token"); return true }
func (t *authTokenFake) Rotate() error       { *t.calls = append(*t.calls, "rotate"); return t.rotateErr }

func TestDashboardLoginPrefersTokenAndReplacesSessionAfterCreation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		calls := []string{}
		s := &authSessionsFake{calls: &calls}
		token := &authTokenFake{calls: &calls}
		if fail {
			s.createErr = errors.New("write failed")
		}
		a := DashboardAuth{Sessions: func() DashboardSessionStore { return s }, Token: func() DashboardAuthToken { return token }, CookieName: func() string { return "session" }}
		r := httptest.NewRequest("POST", "https://localhost/api/login", strings.NewReader(`{"token":"test-token","code":"test-code"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Bridge-Dashboard", "1")
		r.AddCookie(&http.Cookie{Name: "session", Value: "old"})
		w := httptest.NewRecorder()
		a.Login(w, r)
		want := "token,create,drop"
		status := 200
		if fail {
			want = "token,create"
			status = 500
		}
		if strings.Join(calls, ",") != want || w.Code != status {
			t.Fatalf("fail=%v: %v status=%d", fail, calls, w.Code)
		}
		cookies := w.Result().Cookies()
		if fail {
			if len(cookies) != 0 {
				t.Fatal("cookie set after failed persistence")
			}
			continue
		}
		if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Fatalf("cookie safeguards: %+v", cookies)
		}
	}
}

// ?tailnet=1 makes a code for another device, with the link at the
// tailnet address, and refuses while the dashboard is not on the tailnet.
func TestLoginCodeForTheTailnet(t *testing.T) {
	calls := []string{}
	s := &authSessionsFake{calls: &calls}
	url := ""
	a := DashboardAuth{Sessions: func() DashboardSessionStore { return s }, TailnetURL: func() string { return url }}
	ask := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.LoginCode(w, httptest.NewRequest("POST", path, nil))
		return w
	}
	if w := ask("/api/login/code"); w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"code"`) || !strings.Contains(w.Body.String(), `"url":""`) {
		t.Errorf("a local code: %d %s", w.Code, w.Body.String())
	}
	if w := ask("/api/login/code?tailnet=1"); w.Code != http.StatusConflict {
		t.Errorf("off the tailnet: %d %s", w.Code, w.Body.String())
	}
	url = "http://fridolin-core.example.ts.net/"
	if w := ask("/api/login/code?tailnet=1"); w.Code != 200 || !strings.Contains(w.Body.String(), `"url":"http://fridolin-core.example.ts.net/#login=code-10m0s"`) {
		t.Errorf("a tailnet code: %d %s", w.Code, w.Body.String())
	}
}

// A code for another device lasts RemoteLoginCodeLifetime, once.
func TestRemoteLoginCodeLifetime(t *testing.T) {
	now := time.Now()
	s, err := LoadDashboardSessions(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	early, _ := s.NewCodeFor(RemoteLoginCodeLifetime)
	late, _ := s.NewCodeFor(RemoteLoginCodeLifetime)
	now = now.Add(RemoteLoginCodeLifetime - time.Second)
	if !s.UseCode(early) || s.UseCode(early) {
		t.Error("a code within its lifetime works once")
	}
	now = now.Add(2 * time.Second)
	if s.UseCode(late) {
		t.Error("an expired code signed in")
	}
}

func TestDashboardRotateStopsOnPersistenceError(t *testing.T) {
	for _, stage := range []string{"ok", "rotate", "drop-all", "create"} {
		calls := []string{}
		s := &authSessionsFake{calls: &calls, valid: true}
		token := &authTokenFake{calls: &calls}
		failure := errors.New("write failed")
		want := "rotate,drop-all,create"
		status := 200
		switch stage {
		case "rotate":
			token.rotateErr = failure
			want = "rotate"
			status = 500
		case "drop-all":
			s.dropAllErr = failure
			want = "rotate,drop-all"
			status = 500
		case "create":
			s.createErr = failure
			status = 500
		}
		a := DashboardAuth{Sessions: func() DashboardSessionStore { return s }, Token: func() DashboardAuthToken { return token }, CookieName: func() string { return "session" }}
		r := httptest.NewRequest("POST", "/api/rotate-token", nil)
		r.AddCookie(&http.Cookie{Name: "session", Value: "old"})
		w := httptest.NewRecorder()
		a.RotateToken(w, r)
		if strings.Join(calls, ",") != want || w.Code != status {
			t.Fatalf("%s: %v status=%d", stage, calls, w.Code)
		}
		if stage == "ok" && w.Body.String() != "{\"binding\":\"binding\",\"status\":\"rotated\"}\n" {
			t.Fatal(w.Body.String())
		}
	}
}
