package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/config"
	"io/fs"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Dashboard sign-ins.
//
// The dashboard token is long-lived and opens the whole API, so it is kept
// out of URLs and browser storage: a browser keeps both in files under your
// home folder (history, localStorage), and the sandbox denies brains only
// some browsers' profiles. Instead:
//
//   - `fridolin open` and the startup line hand the browser a login code in
//     the URL fragment, which works once, within a minute;
//   - the page trades it, or a token you paste, for a session: an HttpOnly,
//     SameSite=Strict cookie that page scripts cannot read and Chromium
//     browsers encrypt at rest, and a binding the page keeps in its own
//     storage and sends in sessionHeader with every call. Only the two
//     together sign a request in;
//   - sessions are kept in <data_dir>, which brains cannot read, so they
//     survive restarts, and end after SessionLifetime, on sign-out, or for
//     every browser at once when the token is rotated.
//
// The cookie alone is not enough because cookies are not kept apart by
// port (RFC 6265 §8.5), and SameSite does not help either: every server on
// 127.0.0.1, whatever its port, is the same site. So the browser sends the
// cookie to every local server it visits, a site builder preview among
// them (the Build page frames one), which runs a brain's code on a port of
// its own. Web storage, on the other hand, belongs to the origin, port
// included, so that brain's code never sees the binding.
//
// The binding is in the browser's files on disk as well, though, so the
// pair is out of a brain's reach only while the browser's profile is one
// the sandbox denies (sandboxHomeReadDenies() and browserProfilePrefixes:
// Safari, Chrome, Chromium, Brave, Arc, Edge, Vivaldi, Opera, Firefox), or,
// in another Chromium browser, while nobody opens a server a brain runs on
// 127.0.0.1 in it: Chromium encrypts cookies at rest with a Keychain key a
// brain cannot use without a prompt. Firefox forks (Zen, LibreWolf,
// Waterfox, Floorp) keep cookies unencrypted, so a brain reads both halves
// there from disk.

const (
	SessionLifetime   = 30 * 24 * time.Hour
	LoginCodeLifetime = time.Minute
	// RemoteLoginCodeLifetime is a login code's for another machine (`fridolin
	// open --tailnet`, the !dashboard chat command): long enough to move it
	// there and open it.
	RemoteLoginCodeLifetime = 10 * time.Minute
	// maxSessions caps the sign-ins kept; the oldest goes first.
	maxSessions = 32
)

// DashboardSessions holds the sign-ins and the login codes not used yet. Both
// are kept as hashes, so the sessions file holds nothing that signs in.
type DashboardSessions struct {
	path string // <data_dir>/dashboard-sessions.json
	now  func() time.Time

	mu       sync.Mutex
	sessions map[string]session   // hash of a session id → the session
	codes    map[string]time.Time // hash of a login code → when it expires
}

// session is one sign-in.
type session struct {
	expires time.Time
	binding string // hash of its binding (see the top of this file)
}

type sessionFile struct {
	Sessions []sessionRecord `json:"sessions"`
}

type sessionRecord struct {
	Hash    string    `json:"hash"`
	Binding string    `json:"binding"`
	Expires time.Time `json:"expires"`
}

// LoadDashboardSessions reads the sessions in dataDir. A missing file is no
// sessions; one that cannot be parsed is logged and dropped, which only
// means signing in again.
func LoadDashboardSessions(dataDir string, now func() time.Time) (*DashboardSessions, error) {
	s := &DashboardSessions{
		path:     config.Layout{Root: dataDir}.DashboardSessions(),
		now:      now,
		sessions: map[string]session{},
		codes:    map[string]time.Time{},
	}
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("reading dashboard sessions: %w", err)
	}
	var f sessionFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("dashboard sessions %s: %v; every dashboard signs in again", s.path, err)
		return s, nil
	}
	for _, r := range f.Sessions {
		// One without a binding could be used with its cookie alone.
		if r.Binding != "" {
			s.sessions[r.Hash] = session{expires: r.Expires, binding: r.Binding}
		}
	}
	return s, nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// NewCode makes a login code that works once, within LoginCodeLifetime.
func (s *DashboardSessions) NewCode() (string, time.Time) { return s.NewCodeFor(LoginCodeLifetime) }

// NewCodeFor makes a login code that works once, within ttl.
func (s *DashboardSessions) NewCodeFor(ttl time.Duration) (string, time.Time) {
	code := dashboardRandomToken(24)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for h, exp := range s.codes {
		if !now.Before(exp) {
			delete(s.codes, h)
		}
	}
	exp := now.Add(ttl)
	s.codes[hashSecret(code)] = exp
	return code, exp
}

// useCode reports whether code is a login code that has not expired, and
// uses it up.
func (s *DashboardSessions) UseCode(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := hashSecret(code)
	exp, ok := s.codes[h]
	delete(s.codes, h)
	return ok && s.now().Before(exp)
}

// create starts a session and saves it. It returns the session id, for the
// cookie, and its binding, for the page.
func (s *DashboardSessions) Create() (id, binding string, expires time.Time, err error) {
	id, binding = dashboardRandomToken(32), dashboardRandomToken(32)
	s.mu.Lock()
	defer s.mu.Unlock()
	expires = s.now().Add(SessionLifetime)
	s.sessions[hashSecret(id)] = session{expires: expires, binding: hashSecret(binding)}
	if err := s.saveLocked(); err != nil {
		delete(s.sessions, hashSecret(id))
		return "", "", time.Time{}, err
	}
	return id, binding, expires, nil
}

// valid reports whether id is a session that has not ended and binding is
// its binding.
func (s *DashboardSessions) Valid(id, binding string) bool {
	if id == "" || binding == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.sessions[hashSecret(id)]
	return ok && s.now().Before(ses.expires) &&
		subtle.ConstantTimeCompare([]byte(hashSecret(binding)), []byte(ses.binding)) == 1
}

// drop ends session id.
func (s *DashboardSessions) Drop(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[hashSecret(id)]; !ok {
		return nil
	}
	delete(s.sessions, hashSecret(id))
	return s.saveLocked()
}

// dropAll ends every session and forgets every login code.
func (s *DashboardSessions) DropAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]session{}
	s.codes = map[string]time.Time{}
	return s.saveLocked()
}

// saveLocked writes the sessions that have not ended, at most maxSessions
// of them, the newest.
func (s *DashboardSessions) saveLocked() error {
	now := s.now()
	var f sessionFile
	for h, ses := range s.sessions {
		if now.Before(ses.expires) {
			f.Sessions = append(f.Sessions, sessionRecord{Hash: h, Binding: ses.binding, Expires: ses.expires})
		} else {
			delete(s.sessions, h)
		}
	}
	sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].Expires.After(f.Sessions[j].Expires) })
	if len(f.Sessions) > maxSessions {
		for _, r := range f.Sessions[maxSessions:] {
			delete(s.sessions, r.Hash)
		}
		f.Sessions = f.Sessions[:maxSessions]
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(s.path, append(data, '\n'), 0o600, false)
}

func dashboardRandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
