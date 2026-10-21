package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/manifest"

	mediafiles "github.com/Icewater1337/fridolin/internal/media"
)

// childScope says which of the programs the bridge starts get a variable.
type childScope uint8

const (
	// anyChild is any program the bridge starts with childEnv.
	anyChild childScope = iota
	// claudeChild is a Claude CLI run: a brain, and the scripts and tools it
	// runs, which inherit the CLI's environment.
	claudeChild
	// desktopChild is a helper that opens your browser for a sign-in:
	// `plaud login` (see withDesktop).
	desktopChild
	codexChild
)

// childEnvVar names one variable of the bridge's environment that a program
// of scope gets, or, ending in "*", every variable with that prefix.
type childEnvVar struct {
	name  string
	scope childScope
}

// childEnvAllowlist is what the programs the bridge starts with childEnv
// see of its own environment, along with the names in
// child_env_passthrough. The rest stays with the bridge: whatever the
// service file or the shell that started it holds would otherwise reach
// every brain, where anyone who may message one can ask it to run `env`.
// That keeps a variable from being handed over unasked, no more: a brain
// that goes looking can still read the bridge's environment (see "What
// reaches brains" in docs/CONFIGURATION.md). The bridge adds its own
// variables on top (see childEnv).
var childEnvAllowlist = []childEnvVar{
	// What any program expects to find.
	{"HOME", anyChild},
	{"USER", anyChild},
	{"LOGNAME", anyChild},
	{"PATH", anyChild},
	{"SHELL", anyChild},
	{"TMPDIR", anyChild},
	{"LANG", anyChild},
	{"LC_*", anyChild},
	{"TERM", anyChild},
	{"TZ", anyChild},

	// Programs behind a proxy, or a firewall that inspects TLS.
	{"HTTPS_PROXY", anyChild},
	{"https_proxy", anyChild},
	{"HTTP_PROXY", anyChild},
	{"http_proxy", anyChild},
	{"NO_PROXY", anyChild},
	{"no_proxy", anyChild},
	{"NODE_EXTRA_CA_CERTS", anyChild},
	{"SSL_CERT_FILE", anyChild},
	{"SSL_CERT_DIR", anyChild},

	{"DO_NOT_TRACK", anyChild},
	{"TMUX_TMPDIR", anyChild},

	// The Claude CLI's own settings.
	//
	// CLAUDE_CODE_OAUTH_TOKEN logs it in; CLAUDE_CODE_ENTRYPOINT,
	// CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST and CLAUDE_CODE_USE_BEDROCK say
	// how it runs and which provider it uses.
	{"CLAUDE_CODE_*", claudeChild},
	// ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN log it in; ANTHROPIC_BASE_URL
	// points it at a gateway.
	{"ANTHROPIC_*", claudeChild},
	{"CLAUDE_CONFIG_DIR", claudeChild},
	{"DISABLE_AUTOUPDATER", claudeChild},
	{"DISABLE_TELEMETRY", claudeChild},
	{"DISABLE_ERROR_REPORTING", claudeChild},

	// Your desktop session, for a sign-in that opens your browser: on Linux
	// rclone and the Plaud CLI call xdg-open, which finds the session
	// through these. macOS's open needs none of them.
	{"DISPLAY", desktopChild},
	{"WAYLAND_DISPLAY", desktopChild},
	{"XAUTHORITY", desktopChild},
	{"XDG_RUNTIME_DIR", desktopChild},
	{"DBUS_SESSION_BUS_ADDRESS", desktopChild},
	{"XDG_CURRENT_DESKTOP", desktopChild},
	{"KDE_SESSION_VERSION", desktopChild},
	{"XDG_DATA_DIRS", desktopChild},
	{"XDG_CONFIG_DIRS", desktopChild},
	{"BROWSER", desktopChild},

	// Settings of the tools brains run (docs/CONFIGURATION.md).
	{"CALENDAR_TZ", claudeChild},
	{"TRANSCRIBE_MODEL", claudeChild},
	{"TRANSCRIBE_LANGUAGE", claudeChild},
	{"CODEX_API_KEY", codexChild},
	{"CALENDAR_TZ", codexChild},
	{"TRANSCRIBE_MODEL", codexChild},
	{"TRANSCRIBE_LANGUAGE", codexChild},
}

// childEnv is the environment for a program of scope: the variables of the
// bridge's environment that childEnvAllowlist gives it or passthrough
// (child_env_passthrough) names, then extra (NAME=value), which the bridge
// sets on purpose and which replace a variable of the same name.
func childEnv(scope childScope, passthrough []string, extra ...string) []string {
	set := make(map[string]bool, len(extra))
	for _, kv := range extra {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !set[name] && (childGets(scope, name) || slices.Contains(passthrough, name)) {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// desktopKey marks a context whose helper runs open your browser.
type desktopKey struct{}

// withDesktop marks ctx as a sign-in that opens your browser: the helper
// programs run on it get desktopChild's variables (see helperScope).
func withDesktop(ctx context.Context) context.Context {
	return context.WithValue(ctx, desktopKey{}, true)
}

// helperScope is the scope of a helper program, such as rclone, run on ctx.
func helperScope(ctx context.Context) childScope {
	if ctx.Value(desktopKey{}) != nil {
		return desktopChild
	}
	return anyChild
}

// childGets reports whether childEnvAllowlist gives a program of scope the
// variable name.
func childGets(scope childScope, name string) bool {
	for _, v := range childEnvAllowlist {
		if v.scope != anyChild && v.scope != scope {
			continue
		}
		if prefix, ok := strings.CutSuffix(v.name, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if name == v.name {
			return true
		}
	}
	return false
}

// brainStatesDirName is the folder of the data dir that holds every brain's
// own state, one folder per brain id (see brainStateOf).
const brainStatesDirName = "brains"

// brainState is where the bridge keeps one brain's own state, the only
// places outside its folder and inbox its sandbox lets it write: its Claude
// Code config dir (CLAUDE_CONFIG_DIR: settings, transcripts, todos, the
// CLI's own .claude.json), its TMPDIR and its caches. A brain therefore
// shares none of them with your own Claude Code sessions or with other
// brains.
type brainState struct {
	dir                       string // <data_dir>/brains/<id>, which holds the rest
	claude, codex, tmp, cache string
}

// brainStateOf is the state of brain id in the data dir dataDir.
func brainStateOf(dataDir, id string) brainState {
	dir := filepath.Join(dataDir, brainStatesDirName, id)
	return brainState{
		dir:    dir,
		claude: filepath.Join(dir, "claude"),
		codex:  filepath.Join(dir, "codex"),
		tmp:    filepath.Join(dir, "tmp"),
		cache:  filepath.Join(dir, "cache"),
	}
}

// folders are the folders of the state, which the sandbox pins.
func (s brainState) folders() []string { return []string{s.claude, s.codex, s.tmp, s.cache} }

// prepareBrainState creates brain id's media inbox in mediaDir (when set)
// and its state in dataDir, which its sandbox profile pins and so must
// exist as real folders before the brain runs, and returns what a
// sandboxed run adds to its environment to use them:
//
//   - CLAUDE_CONFIG_DIR, the CLI's settings, transcripts and .claude.json,
//     unless inheritUser (inherit_claude_user_config) shares yours;
//   - TMPDIR and CLAUDE_CODE_TMPDIR, where the CLI (its Bash tool's output
//     among others) and every other program keep temporary files, and
//     TMPPREFIX for zsh, the shell the Bash tool usually runs, which
//     ignores TMPDIR and failed every here-document in /tmp;
//   - XDG_CACHE_HOME and npm_config_cache, the caches of the tools brains
//     run, such as transcribe.py's model and the site builder's npm.
//
// A brain may write nowhere else outside its folder and inbox, and shares
// none of these with your own sessions or another brain. Programs that
// ignore TMPDIR, macOS's mktemp without a template among them, fail to
// write their temp files (see "Temporary files" in docs/CONFIGURATION.md).
func prepareBrainState(dataDir, mediaDir, id string, inheritUser bool) ([]string, error) {
	if dataDir == "" {
		return nil, errors.New("the sandbox needs the data folder, which holds each brain's own state")
	}
	if !manifest.ValidID(id) {
		return nil, fmt.Errorf("brain id %q cannot name a folder", id)
	}
	if mediaDir != "" {
		// The profile opens the inbox by its path, so what is there and is
		// no inbox refuses the run. Any other failure leaves the run without
		// an inbox: the files sent to it stay where they are (see
		// claimMedia), and the brain runs, as it used to fail every run.
		switch inbox, err := mediaInboxAt(mediaDir).Open(id); {
		case errors.Is(err, mediafiles.ErrNotTheInbox):
			return nil, err
		case err != nil:
			log.Printf("brain %s: no media inbox for this run: %v", id, err)
		default:
			inbox.Close()
		}
	}
	st := brainStateOf(dataDir, id)
	for _, dir := range st.folders() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating the state of brain %s: %w", id, err)
		}
		// The data dir is the bridge's alone, but check anyway: the
		// profile allows these folders by name, and a symlink there would
		// open whatever it points to.
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("the state folder %s of brain %s is not a folder", dir, id)
		}
	}
	env := []string{
		"TMPDIR=" + st.tmp,
		"CLAUDE_CODE_TMPDIR=" + st.tmp,
		"TMPPREFIX=" + filepath.Join(st.tmp, "zsh"),
		"XDG_CACHE_HOME=" + st.cache,
		"npm_config_cache=" + filepath.Join(st.cache, "npm"),
	}
	if !inheritUser {
		env = append(env, "CLAUDE_CONFIG_DIR="+st.claude)
	}
	return env, nil
}
