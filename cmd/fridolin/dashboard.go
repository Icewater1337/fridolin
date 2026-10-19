package main

import (
	"fmt"

	"net/http"

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
		mux.HandleFunc("POST /api/login", b.newDashboardAuth().Login)
		mux.Handle("/api/", httpapi.APIGuard(b.token, b.requiresToken, b.newDashboardAuth().SignedIn, b.apiMux()))
	}

	return httpapi.SecurityHeaders(httpapi.HostGuard(hosts, httpapi.OriginGuard(hosts, mux)))
}
