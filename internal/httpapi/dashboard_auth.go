package httpapi

import (
	"log"
	"net/http"
	"strings"
	"time"
)

type DashboardSessionStore interface {
	Create() (string, string, time.Time, error)
	Valid(string, string) bool
	UseCode(string) bool
	Drop(string) error
	NewCode() (string, time.Time)
	NewCodeFor(time.Duration) (string, time.Time)
	DropAll() error
}
type DashboardAuthToken interface {
	Matches(string) bool
	Rotate() error
}
type DashboardAuth struct {
	Sessions   func() DashboardSessionStore
	Token      func() DashboardAuthToken
	CookieName func() string
	// TailnetURL is where the dashboard answers on the tailnet, such as
	// http://fridolin-core.example.ts.net/, or "" while it does not; nil
	// for never.
	TailnetURL func() string
}

// SessionHeader carries a session's binding, which the page keeps in its
// own storage. Without it the cookie signs nothing in (see dashsession.go).
const SessionHeader = "X-Bridge-Session"

// signedIn reports whether r carries a dashboard session that has not
// ended, in its cookie, together with that session's binding.
func (a *DashboardAuth) SignedIn(r *http.Request) bool {
	c, err := r.Cookie(a.CookieName())
	return err == nil && a.Sessions().Valid(c.Value, r.Header.Get(SessionHeader))
}

// startSession signs the browser in: a new session, in a cookie scripts on
// the page cannot read and other sites do not get sent. It returns the
// session's binding, for the page to keep and send in SessionHeader.
func (a *DashboardAuth) StartSession(w http.ResponseWriter, r *http.Request) (string, error) {
	id, binding, exp, err := a.Sessions().Create()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: a.CookieName(), Value: id, Path: "/", Expires: exp, MaxAge: int(SessionLifetime / time.Second),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r),
	})
	return binding, nil
}

// secureCookie reports whether the session cookie for r may be marked
// Secure: only when r came over TLS. The bridge serves the dashboard over
// plain HTTP on a loopback address, and not every browser keeps a Secure
// cookie that plain HTTP set, even there; a Secure cookie would leave
// those browsers unable to sign in.
func secureCookie(r *http.Request) bool { return r.TLS != nil }

// apiLogin trades a login code (from `fridolin open`) or the dashboard
// token (pasted on the connect screen) for a session. It is the one API call
// that needs neither, so it sits outside apiGuard, with its write checks.
func (a *DashboardAuth) Login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !CheckWrite(w, r) {
		return
	}
	var req struct {
		Code  string `json:"code"`
		Token string `json:"token"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The token first: a request with both does not use up a code it
	// did not need.
	ok := req.Token != "" && a.Token().Matches(req.Token)
	ok = ok || req.Code != "" && a.Sessions().UseCode(req.Code)
	if !ok {
		WriteErr(w, http.StatusUnauthorized, "that link was already used or has expired, or the token is wrong — run fridolin open for a new link")
		return
	}
	binding, err := a.StartSession(w, r)
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, "signing in: "+err.Error())
		return
	}
	// The new session replaces the one this browser's cookie named, which
	// would otherwise stay until maxSessions pushed it out: every `fridolin
	// open` signs in again.
	if c, err := r.Cookie(a.CookieName()); err == nil {
		if err := a.Sessions().Drop(c.Value); err != nil {
			log.Printf("dashboard sign-in: ending the session it replaces: %v", err)
		}
	}
	WriteJSON(w, http.StatusOK, SignInResponse{Status: "signed in", Binding: binding})
}

// apiLogout ends this browser's session.
func (a *DashboardAuth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(a.CookieName()); err == nil {
		if err := a.Sessions().Drop(c.Value); err != nil {
			WriteErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: a.CookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r)})
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "signed out"})
}

// apiLoginCode makes a login code, for `fridolin open`, which calls it with
// the token from the data dir. With ?tailnet=1 (`fridolin open --tailnet`)
// the code is for another device of the tailnet: it lasts
// RemoteLoginCodeLifetime, and the answer has the link at the
// dashboard's tailnet address, which the dashboard must have.
func (a *DashboardAuth) LoginCode(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("tailnet") == "" {
		code, exp := a.Sessions().NewCode()
		WriteJSON(w, http.StatusOK, LoginCodeResponse{Code: code, ExpiresAt: exp})
		return
	}
	base := ""
	if a.TailnetURL != nil {
		base = a.TailnetURL()
	}
	if base == "" {
		WriteErr(w, http.StatusConflict, "the dashboard is not on the tailnet: core.tailnet is off, or the core has not joined the tailnet yet (fridolin core status)")
		return
	}
	code, exp := a.Sessions().NewCodeFor(RemoteLoginCodeLifetime)
	WriteJSON(w, http.StatusOK, LoginCodeResponse{Code: code, ExpiresAt: exp, URL: TailnetLoginURL(base, code)})
}

// TailnetLoginURL is the sign-in link for code at the dashboard's tailnet
// address base: http://fridolin-core.example.ts.net/, or https:// when
// the tailnet issues certificates.
func TailnetLoginURL(base, code string) string {
	return strings.TrimSuffix(base, "/") + "/#login=" + code
}

// apiRotateToken replaces the dashboard token and ends every session: sign
// out everywhere. A browser that asked gets a new session, so the tab
// stays signed in; `fridolin rotate-token` calls with the token and has no
// use for one.
func (a *DashboardAuth) RotateToken(w http.ResponseWriter, r *http.Request) {
	resign := a.SignedIn(r)
	if err := a.Token().Rotate(); err != nil {
		WriteErr(w, http.StatusInternalServerError, "writing a new token: "+err.Error())
		return
	}
	if err := a.Sessions().DropAll(); err != nil {
		WriteErr(w, http.StatusInternalServerError, "ending the sessions: "+err.Error())
		return
	}
	log.Printf("dashboard token rotated: every dashboard was signed out, and scripts with the old token are refused")
	resp := RotateTokenResponse{Status: "rotated"}
	if resign {
		binding, err := a.StartSession(w, r)
		if err != nil {
			WriteErr(w, http.StatusInternalServerError, "signing in again: "+err.Error())
			return
		}
		resp.Binding = &binding
	}
	WriteJSON(w, http.StatusOK, resp)
}
