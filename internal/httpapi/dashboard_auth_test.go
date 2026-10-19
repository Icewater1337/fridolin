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
