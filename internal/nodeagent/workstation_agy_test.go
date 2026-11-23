package nodeagent

import (
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func TestAgyTurnArgs(t *testing.T) {
	t.Parallel()
	s := &agentSession{a: &Agent{Logf: t.Logf}, id: "s1", provider: nodeproto.ProviderAntigravity, sandbox: nodeproto.SandboxBwrap, model: "gemini-x", resume: "c-1"}
	args := s.turnArgs()
	for _, want := range []string{"--input-format", "--dangerously-skip-permissions", "--conversation", "c-1", "--model", "gemini-x"} {
		if !slices.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if args[len(args)-1] != "-p=" {
		t.Errorf("args end in %q", args[len(args)-1])
	}
	s.sandbox = nodeproto.SandboxNone
	if slices.Contains(s.turnArgs(), "--dangerously-skip-permissions") {
		t.Error("approves every tool without a sandbox")
	}
	var m struct {
		Event   string
		Message struct{ Content string }
	}
	if err := json.Unmarshal([]byte(agyPrompt("hi\nthere")), &m); err != nil || m.Event != "user" || m.Message.Content != "hi\nthere" {
		t.Errorf("prompt %+v, %v", m, err)
	}
}

func TestAgyParse(t *testing.T) {
	t.Parallel()
	stream := strings.Join([]string{
		`{"event":"init","conversation_id":"c-9","init":{"cwd":"/w"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c-9","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"Lo"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c-9","step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"oking."}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c-9","step_index":2,"state":"DONE","step_type":"tool","tool_name":"run_command"}}`,
		`{"event":"result","result":{"conversation_id":"c-9","status":"SUCCESS","response":"Fixed it.","denied_actions":["command(rm -rf x)"]}}`,
	}, "\n") + "\n"
	s := &agentSession{a: &Agent{Logf: t.Logf}, id: "s1", provider: nodeproto.ProviderAntigravity}
	res := s.parse(strings.NewReader(stream))
	if !res.finished || res.text != "Fixed it." || res.resume != "c-9" || res.errMsg != "" || !slices.Equal(res.denied, []string{"command(rm -rf x)"}) {
		t.Errorf("turn = %+v", res)
	}
	if len(s.events) != 2 || s.events[0].Text != "Looking." || s.events[1].Text != "run_command" {
		t.Errorf("events = %+v", s.events)
	}
	failed := `{"event":"result","result":{"status":"ERROR","error":"authentication failed or timed out"}}` + "\n"
	if res := s.parse(strings.NewReader(failed)); res.errMsg != "authentication failed or timed out" {
		t.Errorf("failed turn = %+v", res)
	}
}

// The node offers agy when it finds it, by its program's name.
func TestAgyIsFoundAsAgy(t *testing.T) {
	t.Parallel()
	a := &Agent{AgentPaths: map[string]string{nodeproto.ProviderAntigravity: "/usr/local/bin/agy"}}
	if p, err := a.agentPath(nodeproto.ProviderAntigravity); err != nil || p != "/usr/local/bin/agy" {
		t.Errorf("agentPath = %q, %v", p, err)
	}
	if agentCLIName(nodeproto.ProviderAntigravity) != "agy" || agentCLIName(nodeproto.ProviderCodex) != "codex" {
		t.Error("agentCLIName")
	}
	if _, err := (&Agent{AgentPaths: map[string]string{}}).agentPath(nodeproto.ProviderAntigravity); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("missing agy: %v", err)
	}
}
