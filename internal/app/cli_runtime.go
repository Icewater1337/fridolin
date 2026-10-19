package app

import (
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// cliOptionsOf projects only argument policy; paths are resolved before
// they reach the provider package.
func cliOptionsOf(s *brain.Settings) claudecli.Options {
	return claudecli.Options{InheritUser: s.InheritUser, Dangerously: s.Dangerously,
		Model: s.Model, Effort: s.Effort, DisallowedTools: s.DisallowedTools}
}

func claudeArgs(s *brain.Settings, mcpConfig, settings string) []string {
	return claudecli.Args(cliOptionsOf(s), mcpConfig, settings)
}

func codexArgs(s *brain.Settings, dir string, external bool) []string {
	o := cliOptionsOf(s)
	if s.Dangerously && !external {
		o.StateDir = brainStateOf(s.DataDir, s.Manifest.ID).dir
		if s.MediaDir != "" {
			o.MediaInbox = mediaInboxAt(s.MediaDir).Path(s.Manifest.ID)
		}
	}
	return claudecli.CodexArgs(o, dir, external)
}

// codexLoginHint points at this brain's isolated login, never the user's
// personal Codex home. No credential contents are read by the bridge.
func codexLoginHint(dataDir, id string) string {
	return "CODEX_HOME=" + shellQuote(filepath.Join(dataDir, brainStatesDirName, id, "codex")) + " codex -c 'cli_auth_credentials_store=\"file\"' login"
}
