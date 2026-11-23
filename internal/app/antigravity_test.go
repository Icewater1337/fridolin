package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// fakeAgy is an Antigravity CLI that writes down how it was run, in its
// HOME, and answers in agy's stream-json; FRIDOLIN_FAKE_AGY=unauthenticated
// has it fail as agy does without a login.
func fakeAgy(t *testing.T) string {
	t.Helper()
	path := filepath.Join(fsx.Canonical(t.TempDir()), "agy")
	script := `#!/bin/sh
if [ "$1" = --version ]; then echo '1.5.14'; exit 0; fi
printf '%s\n' "$@" > "$HOME/args"
printf '%s\n' "${CLAUDE_CODE_OAUTH_TOKEN-unset}" "${GEMINI_API_KEY-unset}" "${AGY_CLI_DISABLE_AUTO_UPDATE-unset}" "${FRIDOLIN_RUN_ID:+run}" > "$HOME/env"
cat > "$HOME/prompt"
if [ "$FRIDOLIN_FAKE_AGY" = unauthenticated ]; then
  echo '{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"authentication failed or timed out","finished_reason":"error"}}'
  echo "Error: authentication required. Run 'agy' to log in, then retry." >&2
  exit 1
fi
echo '{"event":"init","conversation_id":"c1","init":{"cwd":".","permission_mode":"always-proceed"}}'
echo '{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"tool","tool_name":"view_file"}}'
echo '{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"DONE","step_type":"agent_response","text_delta":"Gemini reply"}}'
echo '{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"Gemini reply","num_turns":1}}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// A brain whose brain.json says antigravity runs agy while the bridge
// stays on Claude: its own HOME, the prompt on stdin, the settings and
// MCP servers the bridge writes, and the reply from agy's result.
func TestAntigravityBrainRuns(t *testing.T) {
	t.Parallel()
	cfg, _ := json.Marshal(map[string]any{"antigravity_path": fakeAgy(t), "antigravity_model": "gemini-test", "disallowed_tools": []string{"WebFetch"}, "dangerously_skip_permissions": true})
	h := newHarness(t, string(cfg))
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Provider: manifest.ProviderAntigravity, Model: "opus"})
	setenv(t, "CLAUDE_CODE_OAUTH_TOKEN", "test-claude-token")
	setenv(t, "GEMINI_API_KEY", "test-gemini-key")
	h.Inject(inbound{JID: harnessDM, Text: "hello Gemini"})
	h.WaitSent(harnessDM, "Gemini reply")

	home := antigravityHome(h.DataDir(), "main")
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	args := read("args")
	for _, want := range []string{"--model\ngemini-test\n", "--input-format\nstream-json\n", "--dangerously-skip-permissions\n", `"modelProvider":"gemini"`, `"read_url(*)"`, "\n-p=\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "opus") {
		t.Error("the brain's Claude model reached agy")
	}
	if env := read("env"); !strings.HasPrefix(env, "unset\ntest-gemini-key\ntrue\n") {
		t.Errorf("environment = %q", env)
	}
	var prompt struct {
		Event   string
		Message struct{ Content string }
	}
	if err := json.Unmarshal([]byte(read("prompt")), &prompt); err != nil || prompt.Event != "user" || !strings.Contains(prompt.Message.Content, "hello Gemini") {
		t.Errorf("prompt = %q (%v)", read("prompt"), err)
	}
	if s := read(antigravitySettingsFile); !strings.Contains(s, `"disableSlashCommands": true`) {
		t.Errorf("settings.json = %s", s)
	}
	var mcp struct{ MCPServers map[string]json.RawMessage }
	if err := json.Unmarshal([]byte(read(antigravityMCPFile)), &mcp); err != nil {
		t.Fatalf("mcp_config.json: %v", err)
	}
	if raw, ok := mcp.MCPServers[mcpServerName]; ok {
		var srv struct{ Args []string }
		if err := json.Unmarshal(raw, &srv); err != nil || len(srv.Args) != 1 || srv.Args[0] != "mcp" || !strings.HasSuffix(read("env"), "run\n") {
			t.Errorf("the bridge's server = %s, env %q", raw, read("env"))
		}
	}
}

// Without a login the chat is told so, and the Overview names the command
// that signs that brain in.
func TestAntigravityNotSignedIn(t *testing.T) {
	t.Parallel()
	cfg, _ := json.Marshal(map[string]any{"agent_provider": "antigravity", "antigravity_path": fakeAgy(t)})
	h := newHarness(t, string(cfg))
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	setenv(t, "FRIDOLIN_FAKE_AGY", "unauthenticated")
	h.Inject(inbound{JID: harnessDM, Text: "hello"})
	h.WaitSent(harnessDM, outboundmsg.ChatErrAntigravityNotSignedIn)
	hint := antigravityLoginHint(h.DataDir(), "main")
	h.WaitFor("the sign-in warning", func() bool {
		return strings.Contains(h.Do("GET", "/api/status", nil).Body.String(), "Sign Antigravity in for brain main")
	})
	if body := h.Do("GET", "/api/status", nil).Body.String(); !strings.Contains(body, `"antigravity":{`) || !strings.Contains(body, strings.ReplaceAll(hint, `'`, `\u0027`)) && !strings.Contains(body, hint) {
		t.Errorf("status lacks agy or %q: %s", hint, body)
	}
}

// An entry of disallowed_tools agy cannot enforce refuses the run.
func TestAntigravityRefusesWebSearch(t *testing.T) {
	t.Parallel()
	cli := fakeAgy(t)
	mgr, d, home := queueTestManager(t, 0, `,"agent_provider":"antigravity","antigravity_path":`+string(mustJSON(t, cli))+`,"disallowed_tools":["WebSearch"]`)
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "refusal", func() bool { return d.count() == 1 })
	if err := d.all()[0].res.Err; err == nil || !strings.Contains(err.Error(), `"WebSearch"`) {
		t.Fatalf("got %v", err)
	}
	if fsx.Exists(filepath.Join(antigravityHome(filepath.Join(home, "data"), "a"), "args")) {
		t.Fatal("started agy despite a tool it cannot block")
	}
}
