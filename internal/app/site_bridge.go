package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// The site bridge: where the live site reads the built-in /files and
// /wiki (docs/FILES.md), and where its modules call Fridolin's
// connections to OpenAI and ElevenLabs (docs/CONNECTIONS.md).
//
// The site's sandbox keeps module code off the bridge's own port (the
// dashboard, /send, /ingest), so the bridge answers the site on a port of
// its own: a listener on the loopback, bound on first use, that serves
// only the Files and wiki calls, with a token made at startup
// that the site gets through its secrets pipe. The site names the
// signed-in account in each request; the bridge looks it up in the site's
// accounts and lets it read the brains its role may see (admin: all),
// change them if it is an admin, and upload into the upload folders of the
// brains its role may upload to. Everything a site account reads or
// changes is audited.
//
// Module code runs in the same process as the site and can make the same
// requests, as it can read users.json and session.key: approving module
// code trusts it with what the site's roles may read (docs/SITE.md, "One
// process").
//
// A call of a connection names its module in X-Fridolin-Module. The
// bridge checks it against the live commit's manifest of that module:
// the operations and models it declares, and its monthly budget. The
// keys stay in the bridge: no module ever sees one. Module code in the one
// process could name another module; it gets that module's declaration,
// which approving the deploy allowed too.

// siteUserHeader names the signed-in site account in a request.
const siteUserHeader = "X-Fridolin-Site-User"

type siteBridge struct {
	mu    sync.Mutex
	url   string
	token string
	srv   *http.Server
}

// filesBridge starts the site bridge if it is not running and returns its
// address and token.
func (b *App) filesBridge() (string, string, error) {
	sb := &b.siteBridge
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.srv != nil {
		return sb.url, sb.token, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		ln.Close()
		return "", "", err
	}
	sb.token = hex.EncodeToString(tok)
	sb.url = "http://" + ln.Addr().String()
	routes := siteBridgeRoutes(b.newFilesAPI().SiteHandler(b.siteScope), b.conns.SiteMux(b.liveDeclarations))
	sb.srv = &http.Server{
		Handler:           siteBridgeHandler(ln.Addr().String(), sb.token, routes),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func(srv *http.Server) {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[site] the files bridge stopped: %v", err)
		}
	}(sb.srv)
	return sb.url, sb.token, nil
}

// stopSiteBridge closes the site bridge.
func (b *App) stopSiteBridge() {
	sb := &b.siteBridge
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.srv != nil {
		sb.srv.Close()
		sb.srv = nil
	}
}

// siteBridgeRoutes serves a connection's call on conns and everything
// else on files.
func siteBridgeRoutes(files http.Handler, conns *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, pattern := conns.Handler(r); pattern != "" {
			h.ServeHTTP(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// siteBody is what a call of the site bridge sends, and how much of it.
type siteBody struct {
	ctype string
	limit int64
}

// siteWrites are the calls of the site bridge that send a body: those
// that change files, and the calls of the connections, with the body
// each takes.
var siteWrites = map[string]siteBody{
	"PUT /site-api/files/text":                         {"application/json", 1 << 20},
	"POST /site-api/files/move":                        {"application/json", 1 << 20},
	"POST /site-api/files/delete":                      {"application/json", 1 << 20},
	"POST /site-api/files/upload":                      {"multipart/form-data", httpapi.MaxUploadBody},
	"POST /site-api/connections/openai/transcribe":     {"multipart/form-data", connections.SiteAudioBody},
	"POST /site-api/connections/openai/responses":      {"application/json", connections.SiteResponsesBody},
	"POST /site-api/connections/elevenlabs/tts":        {"application/json", connections.SiteSpeechBody},
	"POST /site-api/connections/elevenlabs/transcribe": {"multipart/form-data", connections.SiteAudioBody},
}

// siteBridgeHandler admits a request to next only from this machine
// (Host is the listener's address), with the token, and for GET or one
// of siteWrites with its body.
func siteBridgeHandler(addr, token string, next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(addr)
	hosts := map[string]bool{addr: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !hosts[strings.ToLower(r.Host)] {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			httpapi.WriteErr(w, http.StatusUnauthorized, "the site bridge wants its token")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			body, ok := siteWrites[r.Method+" "+r.URL.Path]
			if !ok {
				httpapi.WriteErr(w, http.StatusMethodNotAllowed, "no such change on the site bridge")
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), body.ctype) {
				httpapi.WriteErr(w, http.StatusUnsupportedMediaType, "send "+body.ctype)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, body.limit)
		}
		next.ServeHTTP(w, r)
	})
}

// siteScope is what the site account a request names may read: admin
// every brain, which it may change too, other roles the brains Site →
// Files and Wiki gives them, and uploads to those it lets them upload to.
func (b *App) siteScope(r *http.Request) (files.Scope, error) {
	id := r.Header.Get(siteUserHeader)
	if id == "" {
		return files.Scope{}, errors.New("no site account named")
	}
	for _, u := range b.site.Users() {
		if u.ID != id {
			continue
		}
		by := "site:" + u.Display()
		if u.Role == "admin" {
			return files.Scope{Private: true, All: true, Write: true, By: by}, nil
		}
		set := b.site.Settings()
		return files.Scope{Brains: set.RoleBrains[u.Role], Uploads: set.RoleUploads[u.Role], By: by}, nil
	}
	return files.Scope{}, errors.New("no such site account")
}
