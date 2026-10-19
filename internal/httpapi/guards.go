package httpapi

import (
	"net"
	"net/http"
	"strings"
)

// LoginURL is the address to open, with a login code in the fragment
// (which browsers never send to the server or put in logs). The code works
// once, within a minute (see dashsession.go), so the browser history keeps
// nothing that signs in.
func LoginURL(listen, code string) string {
	return "http://" + listen + "/#login=" + code
}

// SessionCookieName is the cookie that holds a dashboard session. Cookies
// belong to a host, not a port, so the port is in the name: a second bridge
// on this computer, in dev mode say, keeps its own.
func SessionCookieName(listen string) string {
	_, port, _ := net.SplitHostPort(listen)
	return "fridolin_session_" + port
}

// AllowedHosts returns the Host header values that may reach the server.
// listen has a port: validate refuses an http_listen that isLoopbackAddr
// cannot split.
func AllowedHosts(listen string) map[string]bool {
	_, port, _ := net.SplitHostPort(listen)
	return map[string]bool{
		listen:              true,
		"127.0.0.1:" + port: true,
		"localhost:" + port: true,
		"[::1]:" + port:     true,
	}
}

// HostGuard rejects requests whose Host is not this machine (DNS rebinding).
func HostGuard(allowed map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[strings.ToLower(r.Host)] {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OriginGuard rejects state-changing requests that a browser sent on behalf
// of another site. Scripts (curl, Python, Node) send no Origin and pass.
func OriginGuard(allowed map[string]bool, next http.Handler) http.Handler {
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

// APIGuard requires the dashboard token or a dashboard session on every API
// call, plus a custom header and a JSON body on writes (which a cross-site
// form cannot send).
func APIGuard(token *Token, requiresToken func() bool, signedIn func(*http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First, so the refusals below are not cached either.
		w.Header().Set("Cache-Control", "no-store")
		// compat: scripts that call the API send the token under this name;
		// the dashboard itself signs in with a session (see dashsession.go).
		if requiresToken() && !token.Matches(r.Header.Get("X-Bridge-Token")) && !signedIn(r) {
			WriteErr(w, http.StatusUnauthorized, "missing or wrong dashboard token")
			return
		}
		if CheckWrite(w, r) {
			next.ServeHTTP(w, r)
		}
	})
}

// CheckWrite holds a write to what APIGuard asks of one, the dashboard
// header and a JSON body of at most 1 MiB, and answers it when it falls
// short. It reports whether the request may go on.
func CheckWrite(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if r.Header.Get("X-Bridge-Dashboard") != "1" {
		WriteErr(w, http.StatusForbidden, "missing X-Bridge-Dashboard header")
		return false
	}
	if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		WriteErr(w, http.StatusUnsupportedMediaType, "send JSON")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	return true
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

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", dashboardCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// A site that opened the dashboard in a window gets no handle on
		// it, and no other site may load its files.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
