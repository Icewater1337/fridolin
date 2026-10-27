package nodeagent

import (
	"slices"
	"testing"
)

// The parsers read the forms sbx printed in its versions, and say "no"
// to what they cannot read.
func TestSbxParsers(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"sbx version v0.50.0 (darwin/arm64)": "v0.50.0",
		"Version: 0.51.0-rc2\nCommit: abc":   "0.51.0-rc2",
		"no version here":                    "",
	} {
		if got := parseSbxVersion([]byte(in)); got != want {
			t.Errorf("version of %q: %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{
		`[{"name":"fridolin-a","status":"running","detached":false},{"name":"other","state":"stopped"}]`,
		`{"sandboxes":[{"name":"fridolin-a","status":"running"},{"name":"other","state":"stopped"}]}`,
	} {
		list, ok := parseSbxList([]byte(in))
		if !ok || len(list) != 2 || list[0].Name != "fridolin-a" || list[0].Status != "running" || list[1].Status != "stopped" {
			t.Errorf("ls %s: %+v %v", in, list, ok)
		}
	}
	if _, ok := parseSbxList([]byte("NAME STATUS\nfridolin-a running")); ok {
		t.Error("ls read a table as JSON")
	}
	held := []string{
		"ssh.agentForwardingEnabled  false\nbrowser.enabled  false\n",
		"ssh.agentForwardingEnabled = false\nbrowser.enabled: false",
		`{"ssh":{"agentForwardingEnabled":false},"browser":{"enabled":false}}`,
		`{"ssh.agentForwardingEnabled":"false","browser.enabled":false}`,
		`[{"key":"ssh.agentForwardingEnabled","value":false},{"key":"browser.enabled","value":"false"}]`,
	}
	for _, s := range held {
		if !sbxSettingsHold([]byte(s), []byte("Global: deny-all")) {
			t.Errorf("settings %q do not hold", s)
		}
	}
	for _, s := range []string{"ssh.agentForwardingEnabled true\nbrowser.enabled false", "browser.enabled false", "{}"} {
		if sbxSettingsHold([]byte(s), []byte("deny-all")) {
			t.Errorf("settings %q hold", s)
		}
	}
	if sbxSettingsHold([]byte(held[0]), []byte("balanced")) || sbxSettingsHold([]byte(held[0]), []byte("policy not initialized (deny-all suggested)")) {
		t.Error("a policy other than deny-all holds")
	}
	for in, want := range map[string]bool{
		`[{"service":"openai","scope":"global","value":"oai-…f00d"}]`: true,
		`[{"name":"openai"}]`: true,
		`[{"service":"openai","scope":"sandbox","sandbox":"fridolin-a"}]`: false,
		`[{"service":"anthropic","scope":"global"}]`:                      false,
		`{"secrets":[{"service":"OpenAI","scope":"global"}]}`:             true,
		`openai global oai-…f00d`:                                         false,
	} {
		if got := sbxCodexLogin([]byte(in)); got != want {
			t.Errorf("codex login in %s: %v", in, got)
		}
	}
	if !sbxSaysSignIn("Error: you are not signed in; run sbx login") || sbxSaysSignIn("cannot connect to sandboxd") {
		t.Error("sbxSaysSignIn")
	}
	if got := string(completeRunes([]byte("abü")[:3])); got != "ab" {
		t.Errorf("a cut ü: %q", got)
	}
	if got := string(completeRunes([]byte("abü"))); got != "abü" {
		t.Errorf("a whole ü: %q", got)
	}
	// sbx never inherits a credential, and runs without telemetry.
	env := sbxEnv([]string{"HOME=/Users/you", "CLAUDE_CODE_OAUTH_TOKEN=x", "OPENAI_API_KEY=y", "SBX_NO_TELEMETRY=0"})
	if !slices.Equal(env, []string{"HOME=/Users/you", "SBX_NO_TELEMETRY=1"}) {
		t.Errorf("sbx's environment: %v", env)
	}
}
