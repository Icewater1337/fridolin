package app

import (
	"log"

	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// LogClaudeCLI says at startup which CLI brains run and warns when it is
// missing or too old, so a log excerpt shows it next to the bridge's own
// version.
func LogClaudeCLI(path string, passthrough []string) {
	st := ClaudeInfo(path, passthrough)
	logCLI("Claude Code CLI", "Settings → Claude", path, st)
	if w := claudecli.VersionWarning(st); w != "" {
		log.Printf("WARNING: %s", w)
	}
}

// LogCodexCLI is LogClaudeCLI for Codex mode.
func LogCodexCLI(path string, passthrough []string) {
	logCLI("Codex CLI", "Settings → AI mode", path, CodexInfo(path, passthrough))
}

// logCLI writes the line for the CLI called name, found as st for the
// configured path, which setting changes.
func logCLI(name, setting, path string, st claudecli.Status) {
	switch {
	case !st.Found:
		log.Printf("WARNING: the %s (%s) was not found, so brains cannot run. Set its path under %s.", name, path, setting)
	case st.Version == "":
		log.Printf("%s: %s (`--version` gave no answer)", name, st.Path)
	default:
		log.Printf("%s: %s, %s", name, st.Path, st.Version)
	}
}
