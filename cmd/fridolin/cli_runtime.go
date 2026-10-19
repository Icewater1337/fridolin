package main

import "path/filepath"

// cliOptionsOf projects only argument policy; paths are resolved before
// they reach the provider package.
func cliOptionsOf(s *brainSettings) cliOptions {
	return cliOptions{InheritUser: s.inheritUser, Dangerously: s.dangerously,
		Model: s.model, Effort: s.effort, DisallowedTools: s.disallowedTools}
}
func claudeArgs(s *brainSettings, mcpConfig, settings string) []string {
	return buildClaudeArgs(cliOptionsOf(s), mcpConfig, settings)
}
func codexArgs(s *brainSettings, dir string, external bool) []string {
	o := cliOptionsOf(s)
	if s.dangerously && !external {
		o.StateDir = brainStateOf(s.dataDir, s.manifest.ID).dir
		if s.mediaDir != "" {
			o.MediaInbox = mediaInboxAt(s.mediaDir).Path(s.manifest.ID)
		}
	}
	return buildCodexArgs(o, dir, external)
}

// codexLoginHint points at this brain's isolated login, never the user's
// personal Codex home. No credential contents are read by the bridge.
func codexLoginHint(dataDir, id string) string {
	return "CODEX_HOME=" + shellQuote(filepath.Join(dataDir, brainStatesDirName, id, "codex")) + " codex -c 'cli_auth_credentials_store=\"file\"' login"
}
