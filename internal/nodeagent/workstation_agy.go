package nodeagent

import (
	"cmp"
	"encoding/json"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// agyArgsLocked are an Antigravity turn's arguments: the prompt as one
// stream-json message on stdin (agyPrompt), the stream-json output, and
// the session's conversation resumed by its ID. Inside the node's sandbox
// it approves every tool, as Claude Code does there; without one (Windows)
// edits in the folder go through and agy's own rules hold the rest.
func (s *agentSession) agyArgsLocked() []string {
	args := []string{"--output-format", "stream-json", "--input-format", "stream-json", "--disable-slash-commands", "--mode", "accept-edits"}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.resume != "" {
		args = append(args, "--conversation", s.resume)
	}
	if s.sandbox != nodeproto.SandboxNone {
		args = append(args, "--dangerously-skip-permissions")
	}
	return append(args, "-p=")
}

// agyPrompt is text as the stream-json message agy reads on stdin.
func agyPrompt(text string) string {
	data, _ := json.Marshal(map[string]any{"event": "user", "message": map[string]any{"content": text}})
	return string(data) + "\n"
}

// agyLine is what an Antigravity turn's event says that a session needs.
type agyLine struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	Step           *struct {
		ConversationID string `json:"conversation_id"`
		Index          int    `json:"step_index"`
		State          string `json:"state"`
		Type           string `json:"step_type"`
		ToolName       string `json:"tool_name"`
		TextDelta      string `json:"text_delta"`
	} `json:"step_update"`
	Result *struct {
		ConversationID string   `json:"conversation_id"`
		Status         string   `json:"status"`
		Response       string   `json:"response"`
		Error          string   `json:"error"`
		DeniedActions  []string `json:"denied_actions"`
	} `json:"result"`
}

// agyEvent reads one line of an Antigravity turn: the conversation to
// resume, the model's text once its step is done, the tools it ran, and
// the result. Lines of a kind it does not know are skipped.
func (s *agentSession) agyEvent(line []byte, res *turnResult) {
	var ev agyLine
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case ev.Event == "init" && ev.ConversationID != "":
		res.resume = ev.ConversationID
	case ev.Event == "step_update" && ev.Step != nil:
		st := ev.Step
		if st.ConversationID != "" {
			res.resume = st.ConversationID
		}
		switch {
		case st.Type == "agent_response":
			s.agyText += st.TextDelta
			if st.State == "DONE" {
				if strings.TrimSpace(s.agyText) != "" {
					res.text = s.agyText
					s.addEventLocked("text", s.agyText)
				}
				s.agyText = ""
			}
		case st.Type == "tool" && st.State == "DONE" && st.ToolName != "":
			s.addEventLocked("tool", st.ToolName)
		}
	case ev.Event == "result" && ev.Result != nil:
		r := ev.Result
		res.finished = true
		if r.ConversationID != "" {
			res.resume = r.ConversationID
		}
		if r.Status != "SUCCESS" {
			res.errMsg = cmp.Or(r.Error, "the session ended with status "+r.Status)
			return
		}
		if strings.TrimSpace(r.Response) != "" {
			res.text = r.Response
		}
		res.denied = append(res.denied, r.DeniedActions...)
	}
}
