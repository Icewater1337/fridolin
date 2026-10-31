package claudecli

import "strings"

// Options is the provider argument policy projected from runtime settings.
type Options struct {
	InheritUser, Dangerously bool
	Model, Effort            string
	DisallowedTools          []string
	StateDir, MediaInbox     string
}

// claudeArgs builds the CLI arguments for one run. mcpConfig is what
// mcpConfigOf returned for the brain's folder, or "" for none; settings is
// the brain's excludeInstructionsAbove. They end in -p: the prompt goes on
// stdin (see Start).
func Args(s Options, mcpConfig, settings string) []string {
	var args []string
	if !s.InheritUser && mcpConfig != "" {
		// --mcp-config takes several files: an option must follow.
		args = append(args, "--mcp-config", mcpConfig)
	}
	if s.Dangerously {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		// Nobody can approve a prompt in -p, so without this web search is
		// refused. WebFetch is not allowed so: it fetches any URL the model
		// picks, an outbound channel for what the brain reads (#410); a
		// brain that is to have it gets it from a permissions.allow rule of
		// its own folder's .claude/settings.json. DisallowedTools still wins.
		args = append(args, "--allowed-tools", "WebSearch")
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	if len(s.DisallowedTools) > 0 {
		args = append(args, "--disallowed-tools", strings.Join(s.DisallowedTools, ","))
	}
	return ArgList(!s.InheritUser, settings, args...)
}
