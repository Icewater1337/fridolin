package nodeagent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// fakeAgent stands in for `claude -p --output-format stream-json`: its
// first turn looks around and asks which colour (or, asked to PUSHIT, is
// refused git push by the permission rules); a resumed turn writes the
// answer to colour.txt (or, allowed, pushed.txt). FAKE_AGENT_LOG gets each
// turn's arguments and whether a login token came with it.
func fakeAgent() int {
	in, _ := io.ReadAll(os.Stdin)
	prompt := string(in)
	args := os.Args[1:]
	if log := os.Getenv("FAKE_AGENT_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%s token=%v\n", strings.Join(args, " "), os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "")
			f.Close()
		}
	}
	emit := func(v any) {
		data, _ := json.Marshal(v)
		fmt.Println(string(data))
	}
	text := func(t string) map[string]any {
		return map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": t}}}}
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": "fake-1"})
	if !slices.Contains(args, "--resume") {
		emit(text("Looking at the folder."))
		emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "README"}}}}})
		if strings.Contains(prompt, "PUSHIT") {
			emit(map[string]any{"type": "result", "subtype": "success", "result": "I could not push.", "session_id": "fake-1",
				"permission_denials": []any{map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "git push origin main"}}}})
			return 0
		}
		emit(map[string]any{"type": "result", "subtype": "success", "result": "I need one thing.\nQUESTION: Which colour?", "session_id": "fake-1"})
		return 0
	}
	if strings.Contains(prompt, "allowed it") {
		_ = os.WriteFile("pushed.txt", []byte("pushed"), 0o600)
		emit(map[string]any{"type": "result", "subtype": "success", "result": "Pushed.", "session_id": "fake-1"})
		return 0
	}
	words := strings.Fields(prompt)
	answer := words[len(words)-1]
	if err := os.WriteFile("colour.txt", []byte(answer), 0o600); err != nil {
		emit(map[string]any{"type": "result", "subtype": "error", "is_error": true, "result": err.Error()})
		return 1
	}
	emit(text("Writing colour.txt."))
	emit(map[string]any{"type": "result", "subtype": "success", "result": "Wrote " + answer + " to colour.txt.", "session_id": "fake-1"})
	return 0
}
