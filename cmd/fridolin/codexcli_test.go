package main

import (
	"encoding/json"
	"github.com/Icewater1337/fridolin/internal/pipeline"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestCodexDefaultsAndValidation(t *testing.T) {
	cfg, err := config.Resolve(&config.Config{}, configPaths(), runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentProvider != "claude" || cfg.CodexPath != "codex" || cfg.CodexModel != "gpt-6.1-sol" || cfg.CodexEffort != "high" {
		t.Fatalf("wrong agent defaults: %s %s %s %s", cfg.AgentProvider, cfg.CodexPath, cfg.CodexModel, cfg.CodexEffort)
	}
	for _, c := range []*config.Config{{AgentProvider: "other"}, {CodexEffort: "minimal"}} {
		if _, err := config.Resolve(c, configPaths(), runtime.GOOS); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	cfg.BrainModel, cfg.BrainEffort = "sonnet", "low"
	sp := manifest.Spec{Manifest: manifest.Manifest{ID: "a", Model: "opus", Effort: "max"}}
	cfg.AgentProvider = "codex"
	s := settingsFor(cfg, sp)
	if s.Model != claudecli.DefaultCodexModel || s.Effort != "high" {
		t.Fatalf("Claude overrides reached Codex: %+v", s)
	}
	cfg.AgentProvider = "claude"
	s = settingsFor(cfg, sp)
	if s.Model != "opus" || s.Effort != "max" {
		t.Fatalf("Claude overrides lost: %+v", s)
	}
}

func TestCodexArgsPermissions(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, tools := range []bool{false, true} {
			s := &brain.Settings{Dangerously: tools, DataDir: "/data", MediaDir: "/media", Manifest: manifest.Manifest{ID: "a"}}
			args := codexArgs(s, "/brain", external)
			if args[len(args)-1] != "-" || !slices.Contains(args, claudecli.DefaultCodexModel) || !slices.Contains(args, `model_reasoning_effort="high"`) {
				t.Fatalf("missing stdin/defaults: %q", args)
			}
			if slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") != (tools && external) {
				t.Fatalf("wrong bypass: %q", args)
			}
			if !tools && !slices.Contains(args, "read-only") {
				t.Fatalf("tools enabled unasked: %q", args)
			}
			if !external && tools && !slices.Contains(args, "workspace-write") {
				t.Fatalf("no built-in sandbox: %q", args)
			}
			for _, flag := range []string{"--ignore-user-config", "--ignore-rules", "--ephemeral", "project_doc_max_bytes=0"} {
				if !slices.Contains(args, flag) {
					t.Errorf("missing isolation flag %q", flag)
				}
			}
		}
	}
}

func TestCodexWebToolRestrictions(t *testing.T) {
	for _, blocked := range [][]string{nil, {"WebFetch"}, {"WebSearch"}, {"WebFetch", "WebSearch"}} {
		if err := claudecli.ValidateCodexTools(blocked); err != nil {
			t.Fatal(err)
		}
		args := codexArgs(&brain.Settings{DisallowedTools: blocked}, "/brain", false)
		if slices.Contains(args, `web_search="disabled"`) != (len(blocked) > 0) {
			t.Fatalf("web restriction %v not applied: %v", blocked, args)
		}
	}
	for _, blocked := range [][]string{{"Bash"}, {"WebFetch", "Bash(rm *)"}, {"WebSearch(domain:example.com)"}} {
		if err := claudecli.ValidateCodexTools(blocked); err == nil {
			t.Fatalf("accepted unsupported restrictions: %v", blocked)
		}
	}
}

func fakeCodex(t *testing.T) string {
	t.Helper()
	path := filepath.Join(fsx.Canonical(t.TempDir()), "codex")
	script := `#!/bin/sh
if [ "$1" = --version ]; then echo 'codex-cli 0.163.0'; exit 0; fi
printf '%s\n' "$@" > "$CODEX_HOME/args"
printf '%s\n' "${CLAUDE_CODE_OAUTH_TOKEN-unset}" "${CODEX_API_KEY-unset}" > "$CODEX_HOME/env"
cat > "$CODEX_HOME/prompt"
sleep 0.3
echo '{"type":"item.completed","item":{"type":"agent_message","text":"Codex reply"}}'
echo '{"type":"turn.completed"}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexDashboardSwitch(t *testing.T) {
	cli := fakeCodex(t)
	cfg, _ := json.Marshal(map[string]any{"codex_path": cli, "brain_model": "sonnet", "brain_effort": "low", "disallowed_tools": []string{"WebFetch", "WebSearch"}})
	h := newHarness(t, string(cfg))
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Model: "opus", Effort: "max"})
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-claude-token")
	t.Setenv("CODEX_API_KEY", "test-codex-key")
	change := func(provider string) {
		v := h.Config()
		var raw map[string]any
		if err := json.Unmarshal(v.Config, &raw); err != nil {
			t.Fatal(err)
		}
		raw["agent_provider"] = provider
		w := h.Do("PUT", "/api/config", map[string]any{"etag": v.ETag, "config": raw})
		if w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
	}
	change("codex")
	h.Inject(inbound{JID: harnessDM, Text: "hello Codex"})
	st := brainStateOf(h.DataDir(), "main")
	h.WaitFor("Codex started", func() bool { return fsx.Exists(filepath.Join(st.codex, "prompt")) })
	change("claude") // a running Codex turn keeps its original output parser
	h.WaitSent(harnessDM, "Codex reply")
	args, err := os.ReadFile(filepath.Join(st.codex, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "gpt-6.1-sol") || !strings.Contains(string(args), `model_reasoning_effort="high"`) || strings.Contains(string(args), "opus") {
		t.Fatalf("wrong args: %s", args)
	}
	if !strings.Contains(string(args), `web_search="disabled"`) {
		t.Fatal("web tools were not disabled")
	}
	env, err := os.ReadFile(filepath.Join(st.codex, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(env) != "unset\ntest-codex-key\n" {
		t.Fatalf("wrong environment: %q", env)
	}
	prompt, err := os.ReadFile(filepath.Join(st.codex, "prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "hello Codex") {
		t.Fatal("prompt did not reach stdin")
	}
	h.Inject(inbound{JID: harnessDM, Text: "hello Claude"})
	h.WaitSent(harnessDM, "says: hello Claude")
	var saved config.Config
	if err := json.Unmarshal(h.Config().Config, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.BrainModel != "sonnet" || saved.BrainEffort != "low" {
		t.Fatal("lost Claude settings")
	}
}

func TestCodexRefusesBlockedTools(t *testing.T) {
	cli := fakeCodex(t)
	mgr, d, home := queueTestManager(t, 0, `,"agent_provider":"codex","codex_path":`+string(mustJSON(t, cli))+`,"disallowed_tools":["Bash"]`)
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "refusal", func() bool { return d.count() == 1 })
	if err := d.all()[0].res.Err; err == nil || !strings.Contains(err.Error(), "blocked tools") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(brainStateOf(filepath.Join(home, "data"), "a").codex, "args")); !os.IsNotExist(err) {
		t.Fatal("started the CLI despite blocked tools")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCodexChatErrorsNameTheRightProvider(t *testing.T) {
	for _, msg := range []string{"Not logged in", "401 Unauthorized", "quota exceeded", "something failed"} {
		text := pipeline.ChatErrorText(&claudecli.Error{Message: msg, Provider: "codex"}, true)
		if !strings.Contains(text, "Codex") || strings.Contains(text, "Claude") {
			t.Fatalf("%q -> %q", msg, text)
		}
	}
}
