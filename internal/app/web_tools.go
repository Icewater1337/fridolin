package app

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// The web tools of a brain's runs (#405), as fridolin check says them. In
// Claude mode they are WebSearch and WebFetch:
//
//   - disallowed_tools takes either away: the brain's own list in its
//     brain.json, else config.json's, which a brain without a list of its
//     own inherits;
//   - so does a permissions.deny rule naming it in the brain folder's
//     .claude/settings.json or settings.local.json;
//   - with dangerously_skip_permissions the brain has both: it has a
//     shell, and the sandbox leaves the network open, so WebFetch opens
//     no way out it did not have;
//   - without it, nobody is there to approve a tool: the bridge allows
//     WebSearch, which picks no address (claudecli.Args), and WebFetch
//     only comes from a permissions.allow rule in those settings (#410).
//
// Codex has one web search, off when disallowed_tools names either.
// Antigravity has a web search nothing turns off, and URL fetching, which
// WebFetch in disallowed_tools denies, as does running without
// dangerously_skip_permissions.
// What your own settings allow, with inherit_claude_user_config, is not
// looked at.

// webTools says which web tools brain s, in folder dir, has and why one
// it has not is off.
func webTools(s *brain.Settings, dir string) string {
	blockedBy := "blocked by config.json's disallowed_tools; the brain's own \"disallowed_tools\" in brain.json, [] for none, replaces that list"
	if s.Manifest.DisallowedTools != nil {
		blockedBy = "blocked by the brain's disallowed_tools in brain.json"
	}
	blocked := func(tool string) bool { return slices.Contains(s.DisallowedTools, tool) }
	switch s.Provider {
	case manifest.ProviderCodex:
		if blocked("WebSearch") || blocked("WebFetch") {
			return "none (Codex's web search is " + blockedBy + ")"
		}
		return "Codex's web search"
	case manifest.ProviderAntigravity:
		return antigravityWebTools(s, blocked, blockedBy)
	case manifest.ProviderClaude:
	}
	allow, deny, err := claudePermissions(dir)
	if err != nil {
		return "cannot tell: " + err.Error()
	}
	var on []string
	off := map[string][]string{} // why → the tools it keeps off
	var whys []string
	for _, tool := range []string{"WebSearch", "WebFetch"} {
		var why string
		switch {
		case blocked(tool):
			why = blockedBy
		case slices.Contains(deny, tool):
			why = "denied by a permissions.deny rule in the brain's .claude settings"
		case s.Dangerously, tool == "WebSearch", ruleNames(allow, tool):
			on = append(on, tool)
			continue
		default:
			why = "without dangerously_skip_permissions nobody approves it: a permissions.allow rule in the brain's .claude/settings.json can"
		}
		if off[why] == nil {
			whys = append(whys, why)
		}
		off[why] = append(off[why], tool)
	}
	out := strings.Join(on, ", ")
	if out == "" {
		out = "none"
	}
	for _, why := range whys {
		out += "; not " + strings.Join(off[why], ", ") + " (" + why + ")"
	}
	return out
}

// ruleNames reports whether one of rules is for tool: the tool's name
// alone, or with a specifier, such as WebFetch(domain:example.com).
func ruleNames(rules []string, tool string) bool {
	for _, r := range rules {
		if r == tool || strings.HasPrefix(r, tool+"(") {
			return true
		}
	}
	return false
}

// claudePermissions are the permissions.allow and permissions.deny rules
// of the brain folder dir's .claude/settings.json and settings.local.json,
// which Claude Code reads for the brain's runs.
func claudePermissions(dir string) (allow, deny []string, err error) {
	for _, name := range []string{"settings.json", "settings.local.json"} {
		rel := filepath.Join(".claude", name)
		data, err := fsx.ReadBrainFile(dir, rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var settings struct {
			Permissions struct {
				Allow []string `json:"allow"`
				Deny  []string `json:"deny"`
			} `json:"permissions"`
		}
		if err == nil {
			err = json.Unmarshal(data, &settings)
		}
		if err != nil {
			return nil, nil, errors.New(rel + ": " + err.Error())
		}
		allow = append(allow, settings.Permissions.Allow...)
		deny = append(deny, settings.Permissions.Deny...)
	}
	return allow, deny, nil
}

// antigravityWebTools is webTools for a brain on Antigravity.
func antigravityWebTools(s *brain.Settings, blocked func(string) bool, blockedBy string) string {
	if err := claudecli.ValidateAntigravityTools(s.DisallowedTools); err != nil {
		return "none: its runs are refused (" + err.Error() + ")"
	}
	switch {
	case blocked("WebFetch"):
		return "agy's web search (fetching URLs is " + blockedBy + ")"
	case !s.Dangerously:
		return "agy's web search (fetching URLs needs dangerously_skip_permissions)"
	}
	return "agy's web search and URL fetching"
}
