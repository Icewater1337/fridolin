package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// A brain on Antigravity runs agy with HOME set to its own folder in the
// data dir (brainState.antigravity): everything agy keeps, its login when
// not in a keyring, its settings, its MCP servers and its conversations,
// is under $HOME/.gemini, and agy has no other way to move it. The
// programs agy starts get that HOME too: the bridge's tools find their
// files through FRIDOLIN_* variables, not ~, but a git or gh run by the
// brain sees no user config of yours there. Your own ~/.gemini is never
// read or copied.

// siteMCPServerName is the site builder's MCP server, as Codex names it
// too.
const siteMCPServerName = "fridolin-site"

// antigravityMCPFile and antigravitySettingsFile are where agy reads its
// MCP servers and settings, below its HOME.
const (
	antigravityMCPFile      = ".gemini/config/mcp_config.json"
	antigravitySettingsFile = ".gemini/antigravity-cli/settings.json"
)

// antigravitySettings is what the bridge writes into a brain's
// settings.json before each run, in place of whatever the brain wrote
// there: the run's own policy goes in --settings (AntigravitySettings),
// which holds for that run only, so that two runs of a brain do not race
// over the file.
var antigravitySettings = map[string]any{"disableSlashCommands": true, "notifications": false, "showTips": false, "showFeedbackSurvey": false}

// writeAntigravityHome writes the brain's mcp_config.json, with the
// bridge's tools (cmd, the run's `fridolin mcp` without --run: the run's
// FRIDOLIN_RUN_ID names it) and the site builder's server, and its
// settings.json. Both are the same for every run of the brain, so a run of
// the other lane that starts meanwhile reads the same files. A server the
// brain added itself is gone again.
func writeAntigravityHome(home string, servers map[string]any) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	for name, v := range map[string]any{antigravityMCPFile: map[string]any{"mcpServers": servers}, antigravitySettingsFile: antigravitySettings} {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		if err := fsx.WriteInRoot(root, name, bytes.NewReader(append(data, '\n')), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

// antigravityMCPServer is the entry of mcp_config.json that starts
// command (and args) as a stdio MCP server, which gets agy's environment
// and env. A delegated task can take minutes: the timeout leaves it
// that.
func antigravityMCPServer(command string, args []string, env map[string]string) map[string]any {
	if env == nil {
		env = map[string]string{}
	}
	return map[string]any{"command": command, "args": args, "env": env, "disabled": false,
		"timeoutSeconds": int((maxDelegateWait + time.Minute) / time.Second)}
}

// antigravityRunMCP is the bridge's MCP server for a run's access, and
// the variable that names the run to it; nil without one.
func antigravityRunMCP(access runAccess) (server map[string]any, env []string) {
	// [exe mcp --run ID], as brainRunEnv makes it.
	if len(access.MCPCommand) < 4 {
		return nil, nil
	}
	return antigravityMCPServer(access.MCPCommand[0], []string{"mcp"}, nil), []string{"FRIDOLIN_RUN_ID=" + access.MCPCommand[3]}
}

func antigravityArgs(s *brain.Settings, external, apiKey, bridgeTools bool) []string {
	o := cliOptionsOf(s)
	if s.MediaDir != "" {
		o.MediaInbox = mediaInboxAt(s.MediaDir).Path(s.Manifest.ID)
	}
	a := claudecli.AntigravityOptions{ExternalSandbox: external, APIKey: apiKey}
	if bridgeTools {
		a.MCPServer = mcpServerName
	}
	return claudecli.AntigravityArgs(o, a)
}

// antigravityHome is the HOME brain id's agy runs with.
func antigravityHome(dataDir, id string) string { return brainStateOf(dataDir, id).antigravity }

// antigravityLoginHint is the command that signs agy in for brain id: an
// interactive agy with the brain's HOME. In the core's container it runs
// there, through docker exec.
func antigravityLoginHint(dataDir, id string) string {
	home := antigravityHome(AbsPath(dataDir), id)
	if InContainer() {
		return "docker exec -it fridolin-core env HOME=" + shellQuote(home) + " agy"
	}
	return "HOME=" + shellQuote(home) + " agy"
}

// brainsByProvider are the running brains by the provider each runs on.
func (b *App) brainsByProvider(cfg *config.Settings) map[manifest.Provider][]*brain.Brain {
	out := map[manifest.Provider][]*brain.Brain{}
	for _, br := range b.brainMgr.Brains() {
		p := manifest.ProviderOf(cfg.AgentProvider, br.Manifest())
		out[p] = append(out[p], br)
	}
	return out
}

// antigravityWarnings are the Overview's warnings about agy, found as st,
// for the brains that run on it: a CLI that is missing, and the sign-in
// command of each brain whose last run failed for want of a login. The
// bridge cannot tell a login from outside: agy keeps it in the keyring,
// or in a file of its own format.
func antigravityWarnings(cfg *config.Settings, st claudecli.Status, brains []*brain.Brain) []string {
	var out []string
	if !st.Found {
		out = append(out, "The Antigravity CLI (agy) was not found. Install it (curl -fsSL https://antigravity.google/cli/install.sh | bash) or set its path under Settings → AI mode.")
	}
	for _, br := range brains {
		if last := br.Snapshot().Last; last != nil && strings.Contains(last.Error, claudecli.AntigravityNotSignedIn) {
			out = append(out, "Sign Antigravity in for brain "+br.ID()+" from Terminal, then send the message again: "+antigravityLoginHint(cfg.DataDir, br.ID()))
		}
	}
	return out
}
