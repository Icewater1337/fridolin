package claudecli

import (
	"slices"
	"strings"
)

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
	return ArgList(!s.InheritUser, settings, optionArgs(s, mcpConfig)...)
}

// LiveArgs builds the CLI arguments of a live session: the same options
// as a run's, for an interactive Claude Code that takes no prompt (no -p,
// no stream-json), and without AskUserQuestion, which would wait for a
// keyboard nobody is at. The channel flag is the caller's.
func LiveArgs(s Options, mcpConfig, settings string) []string {
	s.DisallowedTools = append(slices.Clone(s.DisallowedTools), "AskUserQuestion")
	args := optionArgs(s, mcpConfig)
	if !s.InheritUser {
		args = append(args, "--setting-sources", "project,local", "--strict-mcp-config")
		if settings != "" {
			args = append(args, "--settings", settings)
		}
	}
	return args
}

func optionArgs(s Options, mcpConfig string) []string {
	var args []string
	if !s.InheritUser && mcpConfig != "" {
		// --mcp-config takes several files: an option must follow.
		args = append(args, "--mcp-config", mcpConfig)
	}
	if s.Dangerously {
		args = append(args, "--dangerously-skip-permissions")
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
	return args
}
