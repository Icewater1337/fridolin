package main

import "strings"

// cliOptions is the provider argument policy projected from runtime settings.
type cliOptions struct {
	InheritUser, Dangerously bool
	Model, Effort            string
	DisallowedTools          []string
	StateDir, MediaInbox     string
}

// claudeArgs builds the CLI arguments for one run. mcpConfig is what
// mcpConfigOf returned for the brain's folder, or "" for none; settings is
// the brain's excludeInstructionsAbove. They end in -p: the prompt goes on
// stdin (see startClaude).
func buildClaudeArgs(s cliOptions, mcpConfig, settings string) []string {
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
	return claudeArgList(!s.InheritUser, settings, args...)
}
