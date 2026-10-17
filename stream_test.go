package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractAskQuestion(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"no input", ``, ""},
		{"not JSON", `{"questions":`, ""},
		{"no questions", `{"questions":[]}`, ""},
		{"one question", `{"questions":[{"question":"Which day suits you?"}]}`, "Which day suits you?"},
		{"header in front", `{"questions":[{"header":"Meeting","question":"Which day suits you?"}]}`, "Meeting: Which day suits you?"},
		{"header that repeats the question", `{"questions":[{"header":"Which day?","question":"Which day?"}]}`, "Which day?"},
		{"one line per question", `{"questions":[{"question":"Which day?"},{"header":"Time","question":"Morning or afternoon?"}]}`, "Which day?\nTime: Morning or afternoon?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractAskQuestion(json.RawMessage(c.input)); got != c.want {
				t.Errorf("extractAskQuestion(%s) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// A brain that asks with AskUserQuestion gets the question to the chat while
// it runs, and the final result, which restates it, is not sent again.
func TestParseStreamForwardsAskUserQuestion(t *testing.T) {
	br := bareBrain(t)
	var sent []string
	br.hooks = &BrainHooks{Intermediate: func(jid, text string) { sent = append(sent, jid+" "+text) }}
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"header":"Meeting","question":"Which day suits you?"}]}}]}}
{"type":"result","result":"I asked which day suits you."}
`
	res := br.parseStream("chat", strings.NewReader(stream))
	if res.Err != nil || !res.AlreadySent || res.Text != "" {
		t.Errorf("result = %+v, want AlreadySent and no text", res)
	}
	if want := []string{"chat Meeting: Which day suits you?"}; strings.Join(sent, "|") != strings.Join(want, "|") {
		t.Errorf("sent %q mid-run, want %q", sent, want)
	}
}
