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

	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// The site bridge: where the live site reads the built-in /files and
// /wiki (docs/FILES.md).
//
// The site's sandbox keeps module code off the bridge's own port (the
// dashboard, /send, /ingest), so the bridge answers the site on a port of
// its own: a listener on the loopback, bound on first use, that serves
// only the read-only Files and wiki calls, with a token made at startup
// that the site gets through its secrets pipe. The site names the
// signed-in account in each request; the bridge looks it up in the site's
// accounts and lets it read the brains its role may see (admin: all).
// Everything a site account reads is audited.
//
// Module code runs in the same process as the site and can make the same
// requests, as it can read users.json and session.key: approving module
// code trusts it with what the site's roles may read (docs/SITE.md, "One
// process").

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
	sb.srv = &http.Server{
		Handler:           siteBridgeHandler(ln.Addr().String(), sb.token, b.newFilesAPI().SiteHandler(b.siteScope)),
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

// siteBridgeHandler admits a request to next only from this machine
// (Host is the listener's address), with the token and for GET.
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
			httpapi.WriteErr(w, http.StatusMethodNotAllowed, "the site bridge only reads")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// siteScope is what the site account a request names may read: admin
// every brain, other roles the brains Site → Roles gives them.
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
			return files.Scope{Private: true, All: true, By: by}, nil
		}
		return files.Scope{Brains: b.site.Settings().RoleBrains[u.Role], By: by}, nil
	}
	return files.Scope{}, errors.New("no such site account")
}
