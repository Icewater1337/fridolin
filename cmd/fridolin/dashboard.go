package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/web"
)

// The dashboard and its API are served by the bridge on http_listen, which is
// always a loopback address. Loopback is not enough on its own:
//
//   - Web pages in your browser can send requests to localhost (CSRF) and can
//     reach it under a hostname they control (DNS rebinding). The Host check,
//     the Origin / Sec-Fetch-Site check and the custom header on writes stop
//     both.
//   - Brains are local programs that can run curl. The dashboard can change
//     which chats reach which brain and whether the sandbox is on, so every
//     API call needs the token in <data_dir>/dashboard.token. That keeps
//     brains out only while they cannot read the data dir, which takes the
//     macOS sandbox: on Linux, or with the sandbox off, a brain with all
//     permissions can read the token, and the status page says so.
//
// /health and /ready need no token. /send and /ingest get the same Host and
// Origin checks, and capability tokens of their own (scripttoken.go).

// sessionHeader carries a session's binding, which the page keeps in its
// own storage. Without it the cookie signs nothing in (see dashsession.go).
const sessionHeader = "X-Bridge-Session"

// signedIn reports whether r carries a dashboard session that has not
// ended, in its cookie, together with that session's binding.
func (b *Bridge) signedIn(r *http.Request) bool {
	c, err := r.Cookie(b.sessionCookie)
	return err == nil && b.sessions.valid(c.Value, r.Header.Get(sessionHeader))
}

// startSession signs the browser in: a new session, in a cookie scripts on
// the page cannot read and other sites do not get sent. It returns the
// session's binding, for the page to keep and send in sessionHeader.
func (b *Bridge) startSession(w http.ResponseWriter, r *http.Request) (string, error) {
	id, binding, exp, err := b.sessions.create()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: b.sessionCookie, Value: id, Path: "/", Expires: exp, MaxAge: int(sessionLifetime / time.Second),
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
func (b *Bridge) apiLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !httpapi.CheckWrite(w, r) {
		return
	}
	var req struct {
		Code  string `json:"code"`
		Token string `json:"token"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The token first: a request with both does not use up a code it
	// did not need.
	ok := req.Token != "" && b.token.Matches(req.Token)
	ok = ok || req.Code != "" && b.sessions.useCode(req.Code)
	if !ok {
		httpapi.WriteErr(w, http.StatusUnauthorized, "that link was already used or has expired, or the token is wrong — run fridolin open for a new link")
		return
	}
	binding, err := b.startSession(w, r)
	if err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, "signing in: "+err.Error())
		return
	}
	// The new session replaces the one this browser's cookie named, which
	// would otherwise stay until maxSessions pushed it out: every `fridolin
	// open` signs in again.
	if c, err := r.Cookie(b.sessionCookie); err == nil {
		if err := b.sessions.drop(c.Value); err != nil {
			log.Printf("dashboard sign-in: ending the session it replaces: %v", err)
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "signed in", "binding": binding})
}

// apiLogout ends this browser's session.
func (b *Bridge) apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(b.sessionCookie); err == nil {
		if err := b.sessions.drop(c.Value); err != nil {
			httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: b.sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r)})
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

// apiLoginCode makes a login code, for `fridolin open`, which calls it with
// the token from the data dir.
func (b *Bridge) apiLoginCode(w http.ResponseWriter, r *http.Request) {
	code, exp := b.sessions.newCode()
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"code": code, "expires_at": exp})
}

// apiRotateToken replaces the dashboard token and ends every session: sign
// out everywhere. A browser that asked gets a new session, so the tab
// stays signed in; `fridolin rotate-token` calls with the token and has no
// use for one.
func (b *Bridge) apiRotateToken(w http.ResponseWriter, r *http.Request) {
	resign := b.signedIn(r)
	if err := b.token.Rotate(); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, "writing a new token: "+err.Error())
		return
	}
	if err := b.sessions.dropAll(); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, "ending the sessions: "+err.Error())
		return
	}
	log.Printf("dashboard token rotated: every dashboard was signed out, and scripts with the old token are refused")
	resp := map[string]string{"status": "rotated"}
	if resign {
		binding, err := b.startSession(w, r)
		if err != nil {
			httpapi.WriteErr(w, http.StatusInternalServerError, "signing in again: "+err.Error())
			return
		}
		resp["binding"] = binding
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// requiresToken reports whether API calls need the token. Turning
// dashboard.require_token off waits for a restart, while the dashboard
// warns about it: one saved setting must not drop the only thing keeping
// other programs on this computer out of the API at once. Turning it on
// applies at once.
func (b *Bridge) requiresToken() bool {
	return b.tokenAtStart || b.cfgs.Get().DashboardRequiresToken()
}

// newHTTPHandler builds the whole HTTP surface.
func (b *Bridge) newHTTPHandler(listen string) http.Handler {
	hosts := httpapi.AllowedHosts(listen)
	b.sessionCookie = httpapi.SessionCookieName(listen)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /ready", b.httpReady)
	mux.HandleFunc("POST /send", b.newScriptAPI().Send)
	mux.HandleFunc("POST /ingest", b.newScriptAPI().Ingest)
	// The site builder's MCP servers; each brain's own token (site_builder.go).
	mux.Handle("/builder/", b.builderHandler())
	// The tools brains run; each run's own token (brainapi.go).
	mux.Handle("/brain/", b.brainAPIHandler())

	if !b.cfgs.Get().Dashboard.Disabled {
		static := httpapi.StaticHandler(web.FS)
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			// Google sign-in returns here (the redirect URI is the dashboard
			// itself). The one-time state ties it to a sign-in the dashboard
			// started, so it needs no token. Google sends the browser back
			// with a GET; a HEAD, say from a link prefetcher, must not use
			// up the state.
			if r.Method == http.MethodGet && r.URL.Path == "/" && httpapi.IsOAuthCallback(r) {
				b.newGoogleAPI().Callback(w, r)
				return
			}
			static.ServeHTTP(w, r)
		}))
		mux.HandleFunc("POST /api/login", b.apiLogin)
		mux.Handle("/api/", httpapi.APIGuard(b.token, b.requiresToken, b.signedIn, b.apiMux()))
	}

	return httpapi.SecurityHeaders(httpapi.HostGuard(hosts, httpapi.OriginGuard(hosts, mux)))
}
