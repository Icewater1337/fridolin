package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"fridolin/internal/fsx"
	"fridolin/web"
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

// DashboardToken is a secret kept in a file of the data dir: the
// dashboard's, or the one scripts send to /send and /ingest.
type DashboardToken struct {
	path string
	v    atomic.Value // string
}

// minTokenLen is the shortest token loadTokenFile accepts; Rotate writes 48
// hex digits.
const minTokenLen = 32

// LoadDashboardToken reads <dataDir>/dashboard.token, creating it on first
// run (see loadTokenFile).
func LoadDashboardToken(dataDir string) (*DashboardToken, error) {
	return loadTokenFile(filepath.Join(dataDir, "dashboard.token"))
}

// loadTokenFile reads the token in path, creating the file when there is
// none. Only a missing file makes a new token: one that cannot be read or
// holds something too short to be a token is an error, since quietly
// replacing it would lock out every dashboard and script that uses it, and
// hide whatever broke the file.
func loadTokenFile(path string) (*DashboardToken, error) {
	t := &DashboardToken{path: path}
	data, err := os.ReadFile(t.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := t.Rotate(); err != nil {
			return nil, fmt.Errorf("creating token %s: %w", path, err)
		}
		return t, nil
	case err != nil:
		return nil, fmt.Errorf("reading token: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if len(tok) < minTokenLen || strings.ContainsAny(tok, " \t\r\n") {
		return nil, fmt.Errorf("%s is not a token (delete the file and the bridge makes a new one)", t.path)
	}
	t.v.Store(tok)
	return t, nil
}

// Value returns the token.
func (t *DashboardToken) Value() string { return t.v.Load().(string) }

// Rotate replaces the token and its file: whoever sends the old one is
// refused from then on. apiRotateToken also ends every dashboard session.
func (t *DashboardToken) Rotate() error {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	tok := hex.EncodeToString(buf)
	if err := fsx.WriteFileAtomic(t.path, []byte(tok+"\n"), 0o600, false); err != nil {
		return err
	}
	t.v.Store(tok)
	return nil
}

// Matches compares in constant time.
func (t *DashboardToken) Matches(s string) bool {
	return subtle.ConstantTimeCompare([]byte(s), []byte(t.Value())) == 1
}

// loginURL is the address to open, with a login code in the fragment
// (which browsers never send to the server or put in logs). The code works
// once, within a minute (see dashsession.go), so the browser history keeps
// nothing that signs in.
func loginURL(listen, code string) string {
	return "http://" + listen + "/#login=" + code
}

// sessionCookieName is the cookie that holds a dashboard session. Cookies
// belong to a host, not a port, so the port is in the name: a second bridge
// on this computer, in dev mode say, keeps its own.
func sessionCookieName(listen string) string {
	_, port, _ := net.SplitHostPort(listen)
	return "fridolin_session_" + port
}

// allowedHosts returns the Host header values that may reach the server.
// listen has a port: validate refuses an http_listen that isLoopbackAddr
// cannot split.
func allowedHosts(listen string) map[string]bool {
	_, port, _ := net.SplitHostPort(listen)
	return map[string]bool{
		listen:              true,
		"127.0.0.1:" + port: true,
		"localhost:" + port: true,
		"[::1]:" + port:     true,
	}
}

// hostGuard rejects requests whose Host is not this machine (DNS rebinding).
func hostGuard(allowed map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[strings.ToLower(r.Host)] {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originGuard rejects state-changing requests that a browser sent on behalf
// of another site. Scripts (curl, Python, Node) send no Origin and pass.
func originGuard(allowed map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := strings.ToLower(r.Header.Get("Origin")); o != "" {
				host, ok := strings.CutPrefix(o, "http://")
				if !ok || !allowed[host] {
					http.Error(w, "cross-site request refused", http.StatusForbidden)
					return
				}
			}
			switch r.Header.Get("Sec-Fetch-Site") {
			case "", "same-origin", "none":
			default:
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// apiGuard requires the dashboard token or a dashboard session on every API
// call, plus a custom header and a JSON body on writes (which a cross-site
// form cannot send).
func (b *Bridge) apiGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First, so the refusals below are not cached either.
		w.Header().Set("Cache-Control", "no-store")
		// compat: scripts that call the API send the token under this name;
		// the dashboard itself signs in with a session (see dashsession.go).
		if b.requiresToken() && !b.token.Matches(r.Header.Get("X-Bridge-Token")) && !b.signedIn(r) {
			writeErr(w, http.StatusUnauthorized, "missing or wrong dashboard token")
			return
		}
		if checkWrite(w, r) {
			next.ServeHTTP(w, r)
		}
	})
}

// checkWrite holds a write to what apiGuard asks of one, the dashboard
// header and a JSON body of at most 1 MiB, and answers it when it falls
// short. It reports whether the request may go on.
func checkWrite(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if r.Header.Get("X-Bridge-Dashboard") != "1" {
		writeErr(w, http.StatusForbidden, "missing X-Bridge-Dashboard header")
		return false
	}
	if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeErr(w, http.StatusUnsupportedMediaType, "send JSON")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	return true
}

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
	if !checkWrite(w, r) {
		return
	}
	var req struct {
		Code  string `json:"code"`
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The token first: a request with both does not use up a code it
	// did not need.
	ok := req.Token != "" && b.token.Matches(req.Token)
	ok = ok || req.Code != "" && b.sessions.useCode(req.Code)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "that link was already used or has expired, or the token is wrong — run fridolin open for a new link")
		return
	}
	binding, err := b.startSession(w, r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "signing in: "+err.Error())
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed in", "binding": binding})
}

// apiLogout ends this browser's session.
func (b *Bridge) apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(b.sessionCookie); err == nil {
		if err := b.sessions.drop(c.Value); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: b.sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r)})
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

// apiLoginCode makes a login code, for `fridolin open`, which calls it with
// the token from the data dir.
func (b *Bridge) apiLoginCode(w http.ResponseWriter, r *http.Request) {
	code, exp := b.sessions.newCode()
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "expires_at": exp})
}

// apiRotateToken replaces the dashboard token and ends every session: sign
// out everywhere. A browser that asked gets a new session, so the tab
// stays signed in; `fridolin rotate-token` calls with the token and has no
// use for one.
func (b *Bridge) apiRotateToken(w http.ResponseWriter, r *http.Request) {
	resign := b.signedIn(r)
	if err := b.token.Rotate(); err != nil {
		writeErr(w, http.StatusInternalServerError, "writing a new token: "+err.Error())
		return
	}
	if err := b.sessions.dropAll(); err != nil {
		writeErr(w, http.StatusInternalServerError, "ending the sessions: "+err.Error())
		return
	}
	log.Printf("dashboard token rotated: every dashboard was signed out, and scripts with the old token are refused")
	resp := map[string]string{"status": "rotated"}
	if resign {
		binding, err := b.startSession(w, r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "signing in again: "+err.Error())
			return
		}
		resp["binding"] = binding
	}
	writeJSON(w, http.StatusOK, resp)
}

// requiresToken reports whether API calls need the token. Turning
// dashboard.require_token off waits for a restart, while the dashboard
// warns about it: one saved setting must not drop the only thing keeping
// other programs on this computer out of the API at once. Turning it on
// applies at once.
func (b *Bridge) requiresToken() bool {
	return b.tokenAtStart || b.cfgs.Get().DashboardRequiresToken()
}

// dashboardCSP is the page's Content-Security-Policy. Its scripts and
// styles come from the bridge alone, never inline. Trusted Types with no
// policy at all keep it that way for HTML: the page builds every element
// with lib/dom.js and never parses a string into the DOM, so a string
// that reaches innerHTML, document.write or the like is refused, not
// run. No plugin content either.
const dashboardCSP = "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; connect-src 'self'; " +
	"frame-src http://127.0.0.1:* http://localhost:*; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; " +
	"object-src 'none'; require-trusted-types-for 'script'; trusted-types 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", dashboardCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		// A site that opened the dashboard in a window gets no handle on
		// it, and no other site may load its files.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// staticHandler serves the page's files from root. http.FileServer alone
// lists the files of a folder (GET /assets/), and embedded files have no
// modification time, so it sends no validator and the browser downloads
// every module again on each visit. Here a folder is not found, and each
// file gets an ETag from its content with Cache-Control: no-cache: the
// browser asks each time, which keeps an upgraded bridge's files from
// being mixed with cached old ones, and the answer is a 304 for every
// file the upgrade did not change.
func staticHandler(root fs.FS) http.Handler {
	etags := map[string]string{}
	err := fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		etags["/"+name] = `"` + hex.EncodeToString(sum[:12]) + `"`
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("embedded dashboard: %v", err))
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(r.URL.Path)
		if name == "/" {
			name = "/index.html"
		}
		etag, ok := etags[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// The file server answers /index.html and a file's name with a
		// slash after it with a redirect to the file's own address, which
		// the file's validator does not describe.
		if r.URL.Path == "/" || (r.URL.Path == name && name != "/index.html") {
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// newHTTPHandler builds the whole HTTP surface.
func (b *Bridge) newHTTPHandler(listen string) http.Handler {
	hosts := allowedHosts(listen)
	b.sessionCookie = sessionCookieName(listen)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /ready", b.httpReady)
	mux.HandleFunc("POST /send", b.httpSend)
	mux.HandleFunc("POST /ingest", b.httpIngest)
	// The site builder's MCP servers; each brain's own token (site_builder.go).
	mux.Handle("/builder/", b.builderHandler())
	// The tools brains run; each run's own token (brainapi.go).
	mux.Handle("/brain/", b.brainAPIHandler())

	if !b.cfgs.Get().Dashboard.Disabled {
		static := staticHandler(web.FS)
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
			if r.Method == http.MethodGet && r.URL.Path == "/" && isOAuthCallback(r) {
				b.google.HandleCallback(w, r)
				return
			}
			static.ServeHTTP(w, r)
		}))
		mux.HandleFunc("POST /api/login", b.apiLogin)
		mux.Handle("/api/", b.apiGuard(b.apiMux()))
	}

	return securityHeaders(hostGuard(hosts, originGuard(hosts, mux)))
}
