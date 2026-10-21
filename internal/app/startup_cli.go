package app

import (
	"fmt"
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
// configured path, which setting changes. A found CLI is named by the
// absolute path runs start, with the configured name beside it when that
// was looked up on the PATH.
func logCLI(name, setting, path string, st claudecli.Status) {
	where := st.Path
	if st.Found && path != st.Path {
		where += fmt.Sprintf(" (configured as %q)", path)
	}
	switch {
	case !st.Found:
		log.Printf("WARNING: the %s (%s) was not found, so brains cannot run. Set its path under %s.", name, path, setting)
	case st.Version == "":
		log.Printf("%s: %s (`--version` gave no answer)", name, where)
	default:
		log.Printf("%s: %s, %s", name, where, st.Version)
	}
}
