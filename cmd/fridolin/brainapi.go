package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// The brain API, /brain/, is what the tools a brain runs ask the bridge
// for: poll results and calendar calls (google_calendar.go). The bridge
// answers them with what a brain's sandbox keeps from it (bridge.db, the
// Google tokens) and decides itself what the brain may see, so a brain
// cannot widen that by changing its environment or writing its own client.
//
// Each run gets a token of its own, bound to the brain, and the token
// stops working when the run's CLI has exited. It opens /brain/, and
// /send and /ingest for the brain's own chats and itself (scripttoken.go),
// and nothing else: the dashboard API needs the dashboard token, which
// brains cannot read. The token is in a file that only its brain's sandbox
// profile can read, and the run's environment names the file
// (FRIDOLIN_BRAIN_TOKEN_FILE). The token itself stays out of the
// environment: macOS shows a process's environment to every process of
// the same user (sysctl KERN_PROCARGS2, as `ps eww` reads it), sandboxed
// or not, so a brain could read the token of another brain's run in
// progress and call the bridge as that brain.

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

// brainRunEnv is what a run of brain gets to use the bridge's commands: the
// binary, for "$FRIDOLIN_BIN" calendar and the like, and the brain API's
// address with the file that holds a token for this run. Prompts and
// skills name the binary by the variable, never by a path, so a brain
// folder keeps working when it or the bridge moves; FRIDOLIN_TOOLS_DIR
// does the same for the Python helpers (see invoke). The second result
// revokes the token and deletes its file.
func (b *Bridge) brainRunEnv(brain string) ([]string, func()) {
	var env []string
	if b.exe != "" {
		env = append(env, "FRIDOLIN_BIN="+b.exe)
	}
	if b.listen == "" {
		return env, func() {}
	}
	tok, revoke := b.runTokens.mint(brain)
	path, err := writeRunToken(runTokenDir(b.cfgs.Boot().DataDir, brain), tok)
	if err != nil {
		revoke()
		log.Printf("brain %s: no brain API for this run: %v", brain, err)
		return env, func() {}
	}
	return append(env, "FRIDOLIN_API=http://"+b.listen, "FRIDOLIN_BRAIN_TOKEN_FILE="+path), func() {
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

// bridgeBinary is the running binary, which brains run as $FRIDOLIN_BIN:
// the copy `fridolin service install` made, or a build. Without it, or
// without the Python helpers next to it, the log says what brains lack.
func bridgeBinary() string {
	if ownLayout().Tools == "" {
		log.Printf("warning: the bridge has no tools/ next to its binary: brains get no FRIDOLIN_TOOLS_DIR, so tools/transcribe.py is not there for them")
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("warning: cannot tell where the bridge's binary is (%v): brains get no FRIDOLIN_BIN, so fridolin calendar, polls and history fail in them", err)
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe
}

// ownsChat reports whether chat is one of br's: a chat that routes to it,
// or the one it replies in. A brain sees only the polls of those, since a
// poll's voters are the people of the chat it was sent to.
func (b *Bridge) ownsChat(br *brainruntime.Brain, chatJID string) bool {
	return b.brainMgr.BrainFor(chatJID, chat.IsGroupJID(chatJID)) == br || b.replyChatFor(br, b.cfgs.Get()) == chatJID
}

var sendUpdatesValues = []string{"all", "externalOnly", "none"}
