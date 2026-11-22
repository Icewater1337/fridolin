package claudecli

import (
	"cmp"
	"encoding/json"
	"strings"
)

// antigravityStream adapts agy's stream-json (init, step_update and
// result events, https://antigravity.google/docs/cli/headless) to the run
// reader's common events. The text of an agent_response step arrives in
// deltas; it is one message once the step is DONE. Events of a kind it
// does not know carry nothing.
type antigravityStream struct {
	text  map[int]*strings.Builder // agent_response text by step_index
	tools map[int]bool             // tool steps already logged
}

type antigravityEvent struct {
	Event string `json:"event"`
	Init  *struct {
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
	} `json:"init"`
	Step *struct {
		Index     int    `json:"step_index"`
		State     string `json:"state"`
		Type      string `json:"step_type"`
		ToolName  string `json:"tool_name"`
		TextDelta string `json:"text_delta"`
	} `json:"step_update"`
	Result *struct {
		Status         string `json:"status"`
		Response       string `json:"response"`
		Error          string `json:"error"`
		FinishedReason string `json:"finished_reason"`
		Usage          *struct {
			Input    int64 `json:"input_tokens"`
			Output   int64 `json:"output_tokens"`
			Thinking int64 `json:"thinking_tokens"`
		} `json:"usage"`
	} `json:"result"`
}

func (s *antigravityStream) event(line []byte) streamEvent {
	var e antigravityEvent
	if json.Unmarshal(line, &e) != nil {
		return streamEvent{}
	}
	switch {
	case e.Event == "step_update" && e.Step != nil:
		return s.step(e)
	case e.Event == "result" && e.Result != nil:
		r := e.Result
		ev := streamEvent{Type: "result", Result: r.Response}
		if u := r.Usage; u != nil {
			// Thinking is billed as output.
			ev.Usage = &streamUsage{InputTokens: u.Input, OutputTokens: u.Output + u.Thinking}
		}
		if r.Status != "SUCCESS" {
			ev.IsError = true
			ev.Error = antigravityError(r.Status, r.Error, r.FinishedReason)
		}
		return ev
	case e.Event == "init":
		// The session's model and permission mode: nothing the run says.
		return streamEvent{Type: "system"}
	}
	return streamEvent{}
}

// step handles one step_update: the model's text once its step is done,
// and a tool step the first time it names its tool.
func (s *antigravityStream) step(e antigravityEvent) streamEvent {
	st := e.Step
	switch st.Type {
	case "agent_response":
		if s.text == nil {
			s.text = map[int]*strings.Builder{}
		}
		b := s.text[st.Index]
		if b == nil {
			b = &strings.Builder{}
			s.text[st.Index] = b
		}
		b.WriteString(st.TextDelta)
		if st.State != "DONE" {
			return streamEvent{}
		}
		delete(s.text, st.Index)
		return streamEvent{Type: "assistant", Message: &streamMsg{Content: []streamContent{{Type: "text", Text: b.String()}}}}
	case "tool":
		if st.ToolName == "" || s.tools[st.Index] {
			return streamEvent{}
		}
		if s.tools == nil {
			s.tools = map[int]bool{}
		}
		s.tools[st.Index] = true
		return streamEvent{Type: "assistant", Message: &streamMsg{Content: []streamContent{{Type: "tool_use", Name: st.ToolName}}}}
	}
	return streamEvent{}
}

// antigravityError is the error of a result that did not succeed. A
// missing login says so in words the chat and the dashboard recognise.
func antigravityError(status, msg, reason string) string {
	msg = cmp.Or(strings.TrimSpace(msg), reason, "the run ended with status "+status)
	low := strings.ToLower(msg)
	if strings.Contains(low, "authenticat") || strings.Contains(low, "not logged in") || strings.Contains(low, "log in") {
		return AntigravityNotSignedIn + " (" + msg + ")"
	}
	return msg
}
