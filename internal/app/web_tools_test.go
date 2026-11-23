package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// TestWebTools (#405): what fridolin check says of a brain's web tools
// follows the arguments its runs get: disallowed_tools (its own, else
// config.json's) and a deny rule take one away, dangerously_skip_permissions
// gives both, and without it WebFetch takes an allow rule. Codex has one
// web search.
func TestWebTools(t *testing.T) {
	t.Parallel()
	const (
		global   = "blocked by config.json's disallowed_tools; the brain's own \"disallowed_tools\" in brain.json, [] for none, replaces that list"
		own      = "blocked by the brain's disallowed_tools in brain.json"
		approves = "without dangerously_skip_permissions nobody approves it: a permissions.allow rule in the brain's .claude/settings.json can"
	)
	empty, fetch, search := []string{}, []string{"WebFetch"}, []string{"WebSearch"}
	for _, c := range []struct {
		name        string
		provider    string
		dangerously bool
		inherited   []string  // config.json's disallowed_tools
		ownList     *[]string // the brain's own
		settings    string    // .claude/settings.json
		want        string
	}{
		{name: "skip-permissions", dangerously: true, want: "WebSearch, WebFetch"},
		{name: "locked down", want: "WebSearch; not WebFetch (" + approves + ")"},
		{name: "locked down, allow rule", settings: `{"permissions": {"allow": ["WebFetch(domain:example.com)"]}}`, want: "WebSearch, WebFetch"},
		{name: "global list", dangerously: true, inherited: []string{"WebFetch", "WebSearch"}, want: "none; not WebSearch, WebFetch (" + global + ")"},
		{name: "own list clears it", dangerously: true, inherited: []string{"WebFetch", "WebSearch"}, ownList: &empty, want: "WebSearch, WebFetch"},
		{name: "own list", dangerously: true, ownList: &fetch, want: "WebSearch; not WebFetch (" + own + ")"},
		{name: "deny rule", dangerously: true, settings: `{"permissions": {"deny": ["WebSearch", "Read(~/x/**)"]}}`, want: "WebFetch; not WebSearch (denied by a permissions.deny rule in the brain's .claude settings)"},
		{name: "two reasons", inherited: []string{"WebSearch"}, want: "none; not WebSearch (" + global + "); not WebFetch (" + approves + ")"},
		{name: "codex", provider: "codex", want: "Codex's web search"},
		{name: "antigravity", provider: "antigravity", dangerously: true, want: "agy's web search and URL fetching"},
		{name: "antigravity without permissions", provider: "antigravity", want: "agy's web search (fetching URLs needs dangerously_skip_permissions)"},
		{name: "antigravity WebSearch blocked", provider: "antigravity", dangerously: true, ownList: &search, want: "none: its runs are refused (the Antigravity CLI cannot enforce the blocked tools entry \"WebSearch\" (it can block only WebFetch); remove it from disallowed_tools or use Claude for this brain)"},
		{name: "codex blocked", provider: "codex", dangerously: true, ownList: &fetch, want: "none (Codex's web search is " + own + ")"},
		{name: "unreadable settings", settings: `{"permissions": [`, want: "cannot tell: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if c.settings != "" {
				if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(c.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tools := c.inherited
			if c.ownList != nil {
				tools = *c.ownList
			}
			s := &brain.Settings{Provider: manifest.Provider(c.provider), Dangerously: c.dangerously, DisallowedTools: tools, Manifest: manifest.Manifest{ID: "b", DisallowedTools: c.ownList}}
			if got := webTools(s, dir); got != c.want && !(strings.HasSuffix(c.want, ": ") && strings.HasPrefix(got, c.want)) {
				t.Errorf("webTools = %q, want %q", got, c.want)
			}
		})
	}
}
