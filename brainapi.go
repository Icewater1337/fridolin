package main

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// The brain API, /brain/, is what the tools a brain runs ask the bridge
// for: poll results and calendar calls. The bridge answers them with what
// a brain's sandbox keeps from it (bridge.db, the Google tokens) and
// decides itself what the brain may see, so a brain cannot widen that by
// changing its environment or writing its own client.
//
// Each run gets a token of its own, bound to the brain, and the token
// stops working when the run's CLI has exited. It opens nothing but
// /brain/: the dashboard API needs the dashboard token, which brains
// cannot read. The token is in a file that only its brain's sandbox
// profile can read, and the run's environment names the file
// (FRIDOLIN_BRAIN_TOKEN_FILE). The token itself stays out of the
// environment: macOS shows a process's environment to every process of
// the same user (sysctl KERN_PROCARGS2, as `ps eww` reads it), sandboxed
// or not, so a brain could read the token of another brain's run in
// progress and call /brain/ as that brain.

// runTokens are the brain API tokens of the runs in progress.
type runTokens struct {
	mu sync.Mutex
	// brains maps the SHA-256 of a token to its brain. The lookup goes by
	// hash, so its timing says nothing about a token's characters.
	brains map[[sha256.Size]byte]string
}

// mint makes a token for one run of brain, and the function that revokes
// it once the run is over.
func (rt *runTokens) mint(brain string) (string, func()) {
	tok := randomToken(24)
	sum := sha256.Sum256([]byte(tok))
	rt.mu.Lock()
	if rt.brains == nil {
		rt.brains = map[[sha256.Size]byte]string{}
	}
	rt.brains[sum] = brain
	rt.mu.Unlock()
	var once sync.Once
	return tok, func() {
		once.Do(func() {
			rt.mu.Lock()
			delete(rt.brains, sum)
			rt.mu.Unlock()
		})
	}
}

// brainOf returns the brain a token was minted for, or "".
func (rt *runTokens) brainOf(tok string) string {
	if tok == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tok))
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.brains[sum]
}

// brainRunEnv is what a run of brain needs to reach the brain API: its
// address and the file that holds a token for this run. The second result
// revokes the token and deletes its file.
func (b *Bridge) brainRunEnv(brain string) ([]string, func()) {
	if b.listen == "" {
		return nil, func() {}
	}
	tok, revoke := b.runTokens.mint(brain)
	path, err := writeRunToken(runTokenDir(b.cfgs.Get().DataDir, brain), tok)
	if err != nil {
		revoke()
		log.Printf("brain %s: no brain API for this run: %v", brain, err)
		return nil, func() {}
	}
	return []string{"FRIDOLIN_API=http://" + b.listen, "FRIDOLIN_BRAIN_TOKEN_FILE=" + path}, func() {
		revoke()
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("brain %s: removing the run's brain API token: %v", brain, err)
		}
	}
}

// runTokenDir is the folder of brain's brain API tokens, one file per run
// in progress. It is in the data dir, which the sandbox denies every
// brain; brain's own profile opens this folder again, for reading only.
// Absolute, since the run's tools resolve it in the brain's folder.
func runTokenDir(dataDir, brain string) string {
	return filepath.Join(absPath(dataDir), runTokensDirName, brain)
}

// runTokensDirName is the data dir's folder of run tokens.
const runTokensDirName = "run-tokens"

// writeRunToken writes tok to a new file in dir, readable by the bridge's
// user only, and returns its path. Each run gets a file of its own: one
// brain's runs can overlap while an interrupted one winds down.
func writeRunToken(dir, tok string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "run-*.token")
	if err != nil {
		return "", fmt.Errorf("creating the token file: %w", err)
	}
	_, err = f.WriteString(tok)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing the token file: %w", err)
	}
	return f.Name(), nil
}

// pruneRunTokens deletes the token files a bridge that stopped without
// cleaning up left in dataDir. Their tokens died with it; only the files
// are left.
func pruneRunTokens(dataDir string) {
	files, _ := filepath.Glob(filepath.Join(absPath(dataDir), runTokensDirName, "*", "run-*.token"))
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("warning: removing a stale brain API token: %v", err)
		}
	}
}

// brainAPIHandler serves /brain/.
func (b *Bridge) brainAPIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /brain/polls", b.brainCall(b.brainPolls))
	mux.HandleFunc("/brain/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such brain API call: "+r.Method+" "+r.URL.Path)
	})
	return mux
}

// brainCall authenticates a brain API call by the run's token and hands
// the brain it belongs to on to fn.
func (b *Bridge) brainCall(fn func(w http.ResponseWriter, r *http.Request, br *Brain)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		br := b.brainMgr.Get(b.runTokens.brainOf(tok))
		if br == nil {
			writeErr(w, http.StatusUnauthorized, "the bridge does not know this run's token (FRIDOLIN_BRAIN_TOKEN_FILE): it works only inside the run it was made for")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		fn(w, r, br)
	}
}

// ownsChat reports whether chat is one of br's: a chat that routes to it,
// or the one it replies in. A brain sees only the polls of those, since a
// poll's voters are the people of the chat it was sent to.
func (b *Bridge) ownsChat(br *Brain, chat string) bool {
	return b.brainMgr.BrainFor(chat, strings.HasSuffix(chat, "@g.us")) == br || b.replyChatFor(br, b.cfgs.Get()) == chat
}

// brainPolls answers GET /brain/polls: ?id=MSG_ID for one poll, else the
// ?recent=N (default 10) newest, of ?chat=JID when given.
func (b *Bridge) brainPolls(w http.ResponseWriter, r *http.Request, br *Brain) {
	q := r.URL.Query()
	owns := func(chat string) bool { return b.ownsChat(br, chat) }
	if id := q.Get("id"); id != "" {
		p, err := b.store.Poll(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !owns(p.ChatJID)) {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("poll %q not found in this brain's chats", id))
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "reading the poll: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)
		return
	}
	recent := 10
	if s := q.Get("recent"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			writeErr(w, http.StatusBadRequest, "recent must be a number from 1 to 100")
			return
		}
		recent = n
	}
	chat := q.Get("chat")
	if chat != "" && !owns(chat) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("chat %q is not one of this brain's chats", chat))
		return
	}
	polls, err := b.store.RecentPolls(r.Context(), chat, recent, owns)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "reading the polls: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"polls": polls, "count": len(polls)})
}
