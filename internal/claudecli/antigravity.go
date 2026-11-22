package claudecli

import (
	"encoding/json"
	"fmt"
	"slices"
)

// The providers by the names the bridge's settings give them.
const (
	ProviderClaude      = "claude"
	ProviderCodex       = "codex"
	ProviderAntigravity = "antigravity"
)

// AntigravityNotSignedIn starts the error of an Antigravity run that has no
// login. The dashboard looks for it in a brain's last run.
const AntigravityNotSignedIn = "Antigravity is not signed in for this brain"

// AntigravityOptions are what an Antigravity run needs beyond Options.
type AntigravityOptions struct {
	// ExternalSandbox says Fridolin's sandbox wraps the run. agy's own
	// terminal sandbox stays off inside it: macOS cannot nest
	// sandbox-exec, and the outer one holds the CLI and every child.
	ExternalSandbox bool
	// APIKey says the run logs in with GEMINI_API_KEY, which agy reads
	// only with modelProvider "gemini".
	APIKey bool
	// MCPServer is the bridge's MCP server, whose tools need no approval
	// ("" for none).
	MCPServer string
}

// The permission actions of agy that reach the web: read_url fetches a
// page, execute_url opens one in the browser subagent, download_url saves
// a file.
var antigravityWebActions = []string{"read_url(*)", "execute_url(*)", "download_url(*)"}

// ValidateAntigravityTools refuses disallowed_tools entries agy cannot
// enforce. WebFetch maps onto its URL actions; nothing denies its web
// search, so WebSearch, like every other Claude tool name, refuses the
// run rather than being dropped.
func ValidateAntigravityTools(blocked []string) error {
	for _, tool := range blocked {
		if tool != "WebFetch" {
			return fmt.Errorf("the Antigravity CLI cannot enforce the blocked tools entry %q (it can block only WebFetch); remove it from disallowed_tools or use Claude for this brain", tool)
		}
	}
	return nil
}

// AntigravitySettings is the --settings of one run: settings.json keys for
// this session only, never saved. Deny rules hold in every mode,
// --dangerously-skip-permissions included.
func AntigravitySettings(s Options, a AntigravityOptions) string {
	var deny, allow []string
	if !s.Dangerously {
		// Nobody approves anything in a headless run, but say so: no
		// commands, no writes, and no fetching a URL the model picks, an
		// outbound channel for what the brain reads (as for Claude, #410).
		deny = append(deny, "command(*)", "write_file(*)")
		deny = append(deny, antigravityWebActions...)
	} else if slices.Contains(s.DisallowedTools, "WebFetch") {
		deny = append(deny, antigravityWebActions...)
	}
	if a.MCPServer != "" {
		allow = append(allow, "mcp("+a.MCPServer+"/*)")
	}
	settings := map[string]any{
		"disableSlashCommands":  true,
		"enableTerminalSandbox": !a.ExternalSandbox && s.Dangerously,
		"permissions":           map[string]any{"deny": nonNilStrings(deny), "allow": nonNilStrings(allow)},
	}
	if !s.Dangerously {
		// Default mode: edits and commands wait for an approval nobody
		// gives, besides the deny rules.
		settings["agentMode"] = ""
	}
	if a.APIKey {
		settings["modelProvider"] = "gemini"
	}
	data, _ := json.Marshal(settings)
	return string(data)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// AntigravityArgs start a fresh headless turn. The prompt is one
// stream-json message on stdin (AntigravityPrompt): agy's -p takes its
// prompt only from argv, which ARG_MAX bounds and process listings show,
// and with --input-format stream-json it reads stdin instead; the empty
// -p= at the end turns on print mode.
func AntigravityArgs(s Options, a AntigravityOptions) []string {
	args := []string{"--output-format", "stream-json", "--input-format", "stream-json", "--disable-slash-commands"}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	if s.Dangerously {
		args = append(args, "--dangerously-skip-permissions", "--mode", "accept-edits")
		if !a.ExternalSandbox {
			args = append(args, "--sandbox")
		}
	}
	if s.MediaInbox != "" {
		args = append(args, "--add-dir", s.MediaInbox)
	}
	args = append(args, "--settings", AntigravitySettings(s, a))
	return append(args, "-p=")
}

// AntigravityPrompt is prompt as the one stream-json message agy reads on
// stdin, a line of its own.
func AntigravityPrompt(prompt string) string {
	data, _ := json.Marshal(map[string]any{"event": "user", "message": map[string]any{"content": prompt}})
	return string(data) + "\n"
}
