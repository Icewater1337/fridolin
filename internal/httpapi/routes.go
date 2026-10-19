package httpapi

import (
	"fmt"
	"io/fs"
	"net/http"
)

type Deps struct {
	Listen            string
	Ready             http.HandlerFunc
	Send              http.HandlerFunc
	Ingest            http.HandlerFunc
	Builder           func() http.Handler
	Brain             func() http.Handler
	DashboardDisabled func() bool
	Static            fs.FS
	GoogleCallback    http.HandlerFunc
	Login             http.HandlerFunc
	Token             *Token
	RequiresToken     func() bool
	SignedIn          func(*http.Request) bool
	API               func() http.Handler
}

// newHTTPHandler builds the whole HTTP surface.
func New(d Deps) http.Handler {
	hosts := AllowedHosts(d.Listen)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /ready", d.Ready)
	mux.HandleFunc("POST /send", d.Send)
	mux.HandleFunc("POST /ingest", d.Ingest)
	// The site builder's MCP servers; each brain's own token (site_builder.go).
	mux.Handle("/builder/", d.Builder())
	// The tools brains run; each run's own token (brainapi.go).
	mux.Handle("/brain/", d.Brain())

	if !d.DashboardDisabled() {
		static := StaticHandler(d.Static)
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
			if r.Method == http.MethodGet && r.URL.Path == "/" && IsOAuthCallback(r) {
				d.GoogleCallback(w, r)
				return
			}
			static.ServeHTTP(w, r)
		}))
		mux.HandleFunc("POST /api/login", d.Login)
		mux.Handle("/api/", APIGuard(d.Token, d.RequiresToken, d.SignedIn, d.API()))
	}

	return SecurityHeaders(HostGuard(hosts, OriginGuard(hosts, mux)))
}
