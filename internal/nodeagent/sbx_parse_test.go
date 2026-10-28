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
		// sbx v0.50.0's `settings ls --json`.
		`[{"key":"browser.enabled","value":false,"type":"bool","source":"override"},{"key":"ssh.agentForwardingEnabled","value":false,"requires_restart":true}]`,
		"ssh.agentForwardingEnabled  false  bool  override  yes\nbrowser.enabled  false  bool\n",
		`{"ssh":{"agentForwardingEnabled":false},"browser":{"enabled":false}}`,
	}
	for _, s := range held {
		if !sbxSettingsHold([]byte(s), []byte(fakeDenyAll)) {
			t.Errorf("settings %q do not hold", s)
		}
	}
	for _, s := range []string{`[{"key":"browser.enabled","value":false},{"key":"ssh.agentForwardingEnabled","value":true}]`, "browser.enabled false", "{}"} {
		if sbxSettingsHold([]byte(s), []byte(fakeDenyAll)) {
			t.Errorf("settings %q hold", s)
		}
	}
	if got := sbxSettingsDrift([]byte(`[{"key":"browser.enabled","value":true}]`)); !slices.Equal(got, []string{"browser.enabled", "ssh.agentForwardingEnabled"}) {
		t.Errorf("drift %v", got)
	}
	for _, p := range []string{fakeBalanced, `{"rules":[]}`, "default-deny-all local all network: 1 deny", `{"rules":[{"scope":"global","resource_type":"network","decision":"deny","resources":["**"],"status":"inactive"}]}`} {
		if sbxSettingsHold([]byte(held[0]), []byte(p)) {
			t.Errorf("policy %s holds", p)
		}
	}
	if !sbxPolicyUnset([]byte("error: global network policy has not been initialized\n  try: sbx policy init")) {
		t.Error("sbxPolicyUnset")
	}
	for in, want := range map[string]bool{
		`{"secrets":[{"name":"openai","scope":"(global)","secret":"oai-…f00d","type":"oauth"}],"custom_secrets":[]}`: true,
		`{"secrets":[],"custom_secrets":[{"name":"openai"}]}`:                                                        false,
		`{"secrets":[{"name":"anthropic","scope":"(global)"}]}`:                                                      false,
		`[{"name":"openai"}]`:     false,
		`openai global oai-…f00d`: false,
	} {
		if got := sbxCodexLogin([]byte(in)); got != want {
			t.Errorf("codex login in %s: %v", in, got)
		}
	}
	if !sbxSaysSignIn("Error: you are not signed in; run sbx login") || sbxSaysSignIn("cannot connect to sandboxd") {
		t.Error("sbxSaysSignIn")
	}
	// sbx v0.50.0's `diagnose --json`, shortened.
	diag := `{"version":"1.0","checks":[{"name":"CLI binary","status":"pass"},{"name":"Daemon","status":"pass","message":"healthy"},` +
		`{"name":"Authentication","status":"fail","message":"not authenticated"}],"summary":{"pass":2,"fail":1}}`
	if d, ok := parseSbxDiagnose([]byte(diag)); !ok || !d.Daemon || d.SignedIn {
		t.Errorf("diagnose: %+v %v", d, ok)
	}
	if _, ok := parseSbxDiagnose([]byte(`{"checks":[{"name":"Daemon","status":"pass"}]}`)); ok {
		t.Error("diagnose without Authentication read")
	}
	tpls := `[{"id":"4a94","repository":"docker.io/docker/sandbox-templates","tag":"claude-code-docker"},{"id":"6179","repository":"docker.io/library/fridolin-brain","tag":"0123abcd"}]`
	if !sbxListsTemplate([]byte(tpls), "fridolin-brain:0123abcd") || sbxListsTemplate([]byte(tpls), "fridolin-brain:v1") || sbxListsTemplate([]byte("[]"), "x:y") {
		t.Error("sbxListsTemplate")
	}
	// sbx never inherits a credential, and runs without telemetry.
	env := sbxEnv([]string{"HOME=/Users/you", "CLAUDE_CODE_OAUTH_TOKEN=x", "OPENAI_API_KEY=y", "SBX_NO_TELEMETRY=0"})
	if !slices.Equal(env, []string{"HOME=/Users/you", "SBX_NO_TELEMETRY=1"}) {
		t.Errorf("sbx's environment: %v", env)
	}
}
