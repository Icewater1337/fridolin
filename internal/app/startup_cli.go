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
	switch {
	case !st.Found:
		log.Printf("WARNING: the Claude Code CLI (%s) was not found, so brains cannot run. Set its path under Settings → Claude.", path)
	case st.Version == "":
		log.Printf("Claude Code CLI: %s (`--version` gave no answer)", st.Path)
	default:
		log.Printf("Claude Code CLI: %s, %s", st.Path, st.Version)
	}
	if w := claudecli.VersionWarning(st); w != "" {
		log.Printf("WARNING: %s", w)
	}
}
