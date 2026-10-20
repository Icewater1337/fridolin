package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
)

// runInfo is whom a run's token speaks for, as the bridge started the
// run: nothing the run says can change it. The MCP server a run starts
// (fridolin mcp --run) asks the bridge with the token, so its tools are
// those of this brain, chat and source.
type runInfo struct {
	Brain string
	// ID names the run. Its MCP server is started with it, and the chat of
	// a task it delegates is named after it (delegateChat).
	ID string
	// Chat and Source are the JID and source of the message the run
	// answers.
	Chat, Source string
}

// delegated reports whether the run does a task the core brain delegated:
// such a run gets no delegation tool, whatever its brain, so delegation
// goes one level deep.
func (r runInfo) delegated() bool { return r.Source == chat.SourceDelegate }

// runTokens are the brain API tokens of the runs in progress.
type runTokens struct {
	mu sync.Mutex
	// brains maps the SHA-256 of a token to its run. The lookup goes by
	// hash, so its timing says nothing about a token's characters.
	brains map[[sha256.Size]byte]runInfo
}

// mint makes a token for one run of brain, and the function that revokes
// it once the run is over.
func (rt *runTokens) mint(brain string) (string, func()) {
	return rt.mintRun(runInfo{Brain: brain})
}

// mintRun makes a token for the run, and the function that revokes it
// once the run is over.
func (rt *runTokens) mintRun(run runInfo) (string, func()) {
	tok := randomToken(24)
	sum := sha256.Sum256([]byte(tok))
	rt.mu.Lock()
	if rt.brains == nil {
		rt.brains = map[[sha256.Size]byte]runInfo{}
	}
	rt.brains[sum] = run
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
	run, _ := rt.runOf(tok)
	return run.Brain
}

// runOf returns the run a token was minted for.
func (rt *runTokens) runOf(tok string) (runInfo, bool) {
	if tok == "" {
		return runInfo{}, false
	}
	sum := sha256.Sum256([]byte(tok))
	rt.mu.Lock()
	defer rt.mu.Unlock()
	run, ok := rt.brains[sum]
	return run, ok
}

// runAccess is what one run gets to reach the bridge: environment
// variables, and the MCP server that serves it the bridge's tools.
type runAccess struct {
	Env []string
	// MCPConfig is the file to pass Claude Code with --mcp-config, which
	// starts MCPCommand; "" without a brain API or a binary to start.
	MCPConfig string
	// MCPCommand is the server's command line, for Codex's -c settings.
	MCPCommand []string
	// Done revokes the run's token and removes its files, once the run is
	// over.
	Done func()
}

// mcpServerName is the name a run's MCP server goes by in the CLI, which
// prefixes its tools with it: mcp__fridolin__delegate.
const mcpServerName = "fridolin"

// brainRunEnv is what a run gets to use the bridge's commands: the
// binary, for "$FRIDOLIN_BIN" calendar and the like, and the brain API's
// address with the file that holds a token for this run, and the MCP
// server that serves the run the bridge's tools (see mcpTools). Prompts
// and skills name the binary by the variable, never by a path, so a brain
// folder keeps working when it or the bridge moves; FRIDOLIN_TOOLS_DIR
// does the same for the Python helpers (see invoke).
func (b *App) brainRunEnv(run runInfo) runAccess {
	brain := run.Brain
	access := runAccess{Done: func() {}}
	if b.exe != "" {
		access.Env = append(access.Env, "FRIDOLIN_BIN="+b.exe)
	}
	if b.listen == "" {
		return access
	}
	run.ID = "r" + randomToken(9)
	tok, revoke := b.runTokens.mintRun(run)
	dir := runTokenDir(b.cfgs.Boot().DataDir, brain)
	path, err := writeRunToken(dir, tok)
	if err != nil {
		revoke()
		log.Printf("brain %s: no brain API for this run: %v", brain, err)
		return access
	}
	api := "http://" + b.listen
	var files []string
	if b.exe != "" {
		cmd := []string{b.exe, "mcp", "--run", run.ID}
		cfg, err := writeRunMCPConfig(dir, run.ID, cmd, map[string]string{"FRIDOLIN_API": api, "FRIDOLIN_BRAIN_TOKEN_FILE": path})
		if err != nil {
			log.Printf("brain %s: no Fridolin tools for this run: %v", brain, err)
		} else {
			access.MCPConfig, access.MCPCommand = cfg, cmd
			files = append(files, cfg)
		}
	}
	files = append(files, path)
	access.Env = append(access.Env, "FRIDOLIN_API="+api, "FRIDOLIN_BRAIN_TOKEN_FILE="+path)
	access.Done = func() {
		revoke()
		for _, f := range files {
			if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
				log.Printf("brain %s: removing the run's %s: %v", brain, filepath.Base(f), err)
			}
		}
	}
	return access
}

// writeRunMCPConfig writes, next to the run's token, the --mcp-config file
// that has the CLI start the run's MCP server with cmd and env, and
// returns its path. It holds no secret: the token stays in its own file,
// which env names.
func writeRunMCPConfig(dir, runID string, cmd []string, env map[string]string) (string, error) {
	data, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{
		mcpServerName: map[string]any{"type": "stdio", "command": cmd[0], "args": cmd[1:], "env": env},
	}}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "run-"+runID+".mcp.json")
	if err := fsx.CreateFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	return path, nil
}

// runTokenDir is the folder of brain's brain API tokens, one file per run
// in progress. It is in the data dir, which the sandbox denies every
// brain; brain's own profile opens this folder again, for reading only.
// Absolute, since the run's tools resolve it in the brain's folder.
func runTokenDir(dataDir, brain string) string {
	return filepath.Join(AbsPath(dataDir), runTokensDirName, brain)
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
	files, _ := filepath.Glob(filepath.Join(AbsPath(dataDir), runTokensDirName, "*", "run-*.token"))
	configs, _ := filepath.Glob(filepath.Join(AbsPath(dataDir), runTokensDirName, "*", "run-*.mcp.json"))
	files = append(files, configs...)
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
func (b *App) ownsChat(br *brainruntime.Brain, chatJID string) bool {
	return b.brainMgr.BrainFor(chatJID, chat.IsGroupJID(chatJID)) == br || b.replyChatFor(br, b.cfgs.Get()) == chatJID
}
