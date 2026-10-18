package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
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
//     API call needs the token in <data_dir>/dashboard.token — a directory the
//     sandbox denies to brains.
//
// /send, /ingest, /health and /ready keep their token-free contract for
// local scripts, but get the same Host and Origin checks.

// DashboardToken is the shared secret between the bridge and your browser.
type DashboardToken struct {
	path string
	v    atomic.Value // string
}

// LoadDashboardToken reads <dataDir>/dashboard.token, creating it on first run.
func LoadDashboardToken(dataDir string) (*DashboardToken, error) {
	t := &DashboardToken{path: filepath.Join(dataDir, "dashboard.token")}
	data, err := os.ReadFile(t.path)
	if err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		t.v.Store(strings.TrimSpace(string(data)))
		return t, nil
	}
	if err := t.Rotate(); err != nil {
		return nil, fmt.Errorf("creating dashboard token: %w", err)
	}
	return t, nil
}

// Value returns the token.
func (t *DashboardToken) Value() string { return t.v.Load().(string) }

// Rotate replaces the token; open dashboards must log in again.
func (t *DashboardToken) Rotate() error {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	tok := hex.EncodeToString(buf)
	if err := writeFileAtomic(t.path, []byte(tok+"\n"), 0o600, false); err != nil {
		return err
	}
	t.v.Store(tok)
	return nil
}

// Matches compares in constant time.
func (t *DashboardToken) Matches(s string) bool {
	return subtle.ConstantTimeCompare([]byte(s), []byte(t.Value())) == 1
}

// dashboardURL is the address to open, with the token in the fragment (which
// browsers never send to the server or put in logs).
func dashboardURL(listen, token string) string {
	return "http://" + listen + "/#token=" + token
}

// allowedHosts returns the Host header values that may reach the server.
func allowedHosts(listen string) map[string]bool {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return map[string]bool{listen: true}
	}
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
			if o := r.Header.Get("Origin"); o != "" {
				host := strings.TrimPrefix(strings.ToLower(o), "http://")
				if !strings.HasPrefix(strings.ToLower(o), "http://") || !allowed[host] {
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

// apiGuard requires the dashboard token on every API call, plus a custom
// header and a JSON body on writes (which a cross-site form cannot send).
func (b *Bridge) apiGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// compat: the dashboard, and any script that calls the API, sends
		// the token under this name.
		if b.cfgs.Get().DashboardRequiresToken() && !b.token.Matches(r.Header.Get("X-Bridge-Token")) {
			writeErr(w, http.StatusUnauthorized, "missing or wrong dashboard token")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Bridge-Dashboard") != "1" {
				writeErr(w, http.StatusForbidden, "missing X-Bridge-Dashboard header")
				return
			}
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType, "send JSON")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// webRoot is the page's files, web/ without its prefix.
func webRoot() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(fmt.Sprintf("embedded dashboard: %v", err)) // only a bad constant path fails
	}
	return sub
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
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /ready", b.httpReady)
	mux.HandleFunc("POST /send", b.httpSend)
	mux.HandleFunc("POST /ingest", b.httpIngest)
	// The site builder's MCP servers; each brain's own token (site_builder.go).
	mux.Handle("/builder/", b.builderHandler())

	if !b.cfgs.Get().Dashboard.Disabled {
		static := staticHandler(webRoot())
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			// Google sign-in returns here (the redirect URI is the dashboard
			// itself). The one-time state ties it to a sign-in the dashboard
			// started, so it needs no token.
			if r.URL.Path == "/" && isOAuthCallback(r) {
				b.google.HandleCallback(w, r)
				return
			}
			static.ServeHTTP(w, r)
		}))
		mux.Handle("/api/", b.apiGuard(b.apiMux()))
	}

	return securityHeaders(hostGuard(hosts, originGuard(hosts, mux)))
}
