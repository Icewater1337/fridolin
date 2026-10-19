package brain

import (
	"strings"
	"testing"
)

func TestCodexStream(t *testing.T) {
	const partial = `{"type":"item.completed","item":{"type":"agent_message","text":"partial"}}` + "\n"
	const final = `{"type":"item.completed","item":{"type":"agent_message","phase":"final_answer","text":"Hello!"}}` + "\n"
	const done = `{"type":"turn.completed","usage":{"input_tokens":20}}` + "\n"
	for _, c := range []struct {
		name, stream, want string
		fail               bool
	}{
		{"final only", partial + final + done, "Hello!", false},
		{"tool progress", `{"type":"item.completed","item":{"type":"command_execution"}}` + "\n" + final + done, "Hello!", false},
		{"truncated", partial, "", true},
		{"failure", partial + `{"type":"turn.failed","error":{"message":"Not logged in"}}`, "", true},
		{"error", `{"type":"error","message":"quota exceeded"}`, "", true},
		{"retry succeeds", `{"type":"error","message":"Reconnecting"}` + "\n" + final + done, "Hello!", false},
		{"empty", done, "", true},
		{"commentary only", `{"type":"item.completed","item":{"type":"agent_message","phase":"commentary","text":"Working"}}` + "\n" + done, "", true},
		{"junk", "bad json\n{\n" + final + done, "Hello!", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			br := bareBrain(t)
			res := br.readAgentStream(dm("hi"), strings.NewReader(c.stream), "test", nil, "codex")
			if (res.Err != nil) != c.fail || res.Text != c.want {
				t.Fatalf("got %+v", res)
			}
		})
	}
}
