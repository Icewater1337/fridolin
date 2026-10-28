package nodes

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Joining natively (docs/NODES.md, "Joining a machine"): the install
// scripts of the !join reply (container/node.sh, node.ps1) fetch the
// fridolin they install from the node port, the build of the core's
// version for their platform that the core keeps for updates (update.go):
//
//	GET /join/<code>/fridolin.sha256?os=OS&arch=ARCH   "<sha256> <size> <version>\n"
//	GET /join/<code>/fridolin?os=OS&arch=ARCH          the binary, unpacked
//
// The join code is the key: it must be valid and unused, and the download
// does not use it up (node join spends it). Nothing lists codes, a host
// asks joinPerHost times in joinWindow at most and all hosts joinPerWindow
// times, and joinStreams binaries go out at once. The machine reaches the
// port on the tailnet, which WireGuard encrypts; the script checks the
// binary against the SHA-256 the core sent with it.

// joinPrefix is where the route begins on the node port.
const joinPrefix = "/join/"

// The limits of the route.
const (
	joinWindow    = 10 * time.Minute
	joinPerHost   = 20
	joinPerWindow = 100
	joinStreams   = 4
	// joinSendTimeout bounds sending one binary, so that a client that
	// stops reading frees its stream.
	joinSendTimeout = 15 * time.Minute
)

// joinPlatforms are the platforms a joining machine may ask for: those
// the core image builds (scripts/build-node-binaries.sh).
var joinPlatforms = map[string]bool{
	"darwin/arm64": true, "darwin/amd64": true,
	"linux/amd64": true, "linux/arm64": true,
	"windows/amd64": true, "windows/arm64": true,
}

// joinCodeRE is the shape of a join code (Registry.NewJoinCodeFrom): 15
// random bytes in base32.
var joinCodeRE = regexp.MustCompile(`^fj-[a-z2-7]{24}$`)

// joinLimiter counts the route's requests by host, and the binaries
// being sent.
type joinLimiter struct {
	mu      sync.Mutex
	hosts   map[string][]time.Time
	all     []time.Time
	streams int
}

// allow records a request of host at now, and reports whether it is
// within the limits.
func (l *joinLimiter) allow(host string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	since := now.Add(-joinWindow)
	recent := func(ts []time.Time) []time.Time {
		i := 0
		for i < len(ts) && !ts[i].After(since) {
			i++
		}
		return ts[i:]
	}
	if l.hosts == nil {
		l.hosts = map[string][]time.Time{}
	}
	for h, ts := range l.hosts {
		if ts = recent(ts); len(ts) == 0 {
			delete(l.hosts, h)
		} else {
			l.hosts[h] = ts
		}
	}
	l.all = recent(l.all)
	if len(l.all) >= joinPerWindow || len(l.hosts[host]) >= joinPerHost {
		return false
	}
	l.all = append(l.all, now)
	l.hosts[host] = append(l.hosts[host], now)
	return true
}

// startStream takes one of the streams, false when all are taken;
// endStream gives it back.
func (l *joinLimiter) startStream() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.streams >= joinStreams {
		return false
	}
	l.streams++
	return true
}

func (l *joinLimiter) endStream() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.streams--
}

// serveJoin answers the route.
func (s *Service) serveJoin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "only GET", http.StatusMethodNotAllowed)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.joins.allow(host, s.reg.now()) {
		w.Header().Set("Retry-After", strconv.Itoa(int(joinWindow/time.Second)))
		http.Error(w, "too many requests for the node binary: wait ten minutes and try again", http.StatusTooManyRequests)
		return
	}
	code, file, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, joinPrefix), "/")
	if !ok || (file != "fridolin" && file != "fridolin.sha256") {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	goos, goarch := q.Get("os"), q.Get("arch")
	if !joinPlatforms[goos+"/"+goarch] {
		http.Error(w, "no node binary for that platform: os is darwin, linux or windows, arch amd64 or arm64", http.StatusBadRequest)
		return
	}
	valid := false
	if joinCodeRE.MatchString(code) {
		if valid, err = s.reg.JoinCodeValid(code); err != nil {
			s.logf("nodes: checking a join code for a node binary: %v", err)
			http.Error(w, "the core could not read its node registry", http.StatusInternalServerError)
			return
		}
	}
	if !valid {
		http.Error(w, ErrBadJoinCode.Error()+": send !join again for a new one", http.StatusForbidden)
		return
	}
	b, path, err := s.Binary(goos, goarch)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrNoBinary) {
			status = http.StatusServiceUnavailable
		}
		s.logf("nodes: a node binary for %s/%s to join with: %v", goos, goarch, err)
		http.Error(w, err.Error(), status)
		return
	}
	if file == "fridolin.sha256" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s %d %s\n", b.SHA256, b.Size, s.version)
		return
	}
	s.sendJoinBinary(w, r, b, path, host)
}

// sendJoinBinary sends the binary b, gzip'd at path, unpacked.
func (s *Service) sendJoinBinary(w http.ResponseWriter, r *http.Request, b Binary, path, host string) {
	f, err := os.Open(path)
	if err != nil {
		s.logf("nodes: a node binary to join with: %v", err)
		http.Error(w, "the core could not read its node binary", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		s.logf("nodes: a node binary to join with: %s: %v", path, err)
		http.Error(w, "the core's node binary is damaged", http.StatusInternalServerError)
		return
	}
	defer zr.Close()
	if r.Method == http.MethodGet {
		if !s.joins.startStream() {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "the core sends as many node binaries as it does at once: try again in a minute", http.StatusTooManyRequests)
			return
		}
		defer s.joins.endStream()
	}
	name := "fridolin"
	if b.OS == "windows" {
		name += ".exe"
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Content-Length", strconv.FormatInt(b.Size, 10))
	h.Set("X-Fridolin-Sha256", b.SHA256)
	h.Set("X-Fridolin-Version", s.version)
	if r.Method == http.MethodHead {
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(joinSendTimeout))
	if _, err := io.CopyN(w, zr, b.Size); err != nil {
		s.logf("nodes: sending the %s/%s node binary to %s to join with: %v", b.OS, b.Arch, host, err)
		return
	}
	s.logf("nodes: sent the %s/%s node binary of %s to %s, which holds a join code", b.OS, b.Arch, s.version, host)
}
