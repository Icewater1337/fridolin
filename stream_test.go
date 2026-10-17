package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
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
		{"only blank questions", `{"questions":[{},{"header":"Time","question":" "}]}`, ""},
		{"blank question left out", `{"questions":[{"question":""},{"header":" ","question":" Which day? "}]}`, "Which day?"},
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

// An AskUserQuestion with nothing to ask sends nothing, and the reply still
// goes out.
func TestParseStreamSkipsBlankQuestions(t *testing.T) {
	br := bareBrain(t)
	var sent []string
	br.hooks = &BrainHooks{Intermediate: func(jid, text string) { sent = append(sent, text) }}
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{},{"question":" "}]}}]}}
{"type":"result","result":"Done."}
`
	res := br.parseStream("chat", strings.NewReader(stream))
	if res.Err != nil || res.AlreadySent || res.Text != "Done." {
		t.Errorf("result = %+v, want the reply", res)
	}
	if len(sent) != 0 {
		t.Errorf("sent %q mid-run, want nothing", sent)
	}
}

// FuzzExtractAskQuestion: the AskUserQuestion input is the model's. What
// comes out goes to the chat as it is, and once something has, the run's
// final reply is not sent (see parseStream). So it is valid UTF-8, never
// blank, and holds every question that was asked.
func FuzzExtractAskQuestion(f *testing.F) {
	for _, s := range []string{
		``, `{"questions":`, `{"questions":[]}`, `null`, `[]`, `{"questions":null}`,
		`{"questions":[{"question":"Which day suits you?"}]}`,
		`{"questions":[{"header":"Meeting","question":"Which day suits you?"}]}`,
		`{"questions":[{"header":"Which day?","question":"Which day?"}]}`,
		`{"questions":[{"question":"Which day?"},{"header":"Time","question":"Morning or afternoon?","options":[{"label":"Morning"}]}]}`,
		`{"questions":[{"question":"caf\u00e9 \ud83d"}]}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		got := extractAskQuestion(raw)
		if !utf8.ValidString(got) {
			t.Errorf("extractAskQuestion(%s) = %q, not valid UTF-8", raw, got)
		}
		if got != "" && strings.TrimSpace(got) == "" {
			t.Errorf("extractAskQuestion(%s) = %q, a blank message sent in place of the reply", raw, got)
		}
		var ask askInput
		if err := json.Unmarshal(raw, &ask); err != nil {
			if got != "" {
				t.Errorf("extractAskQuestion(%s) = %q from input that does not decode", raw, got)
			}
			return
		}
		for _, q := range ask.Questions {
			if s := strings.TrimSpace(q.Question); s != "" && !strings.Contains(got, s) {
				t.Errorf("extractAskQuestion(%s) = %q, without the question %q", raw, got, s)
			}
		}
	})
}

// FuzzParseStream: the stream is the CLI's, but most of what is in it the
// model wrote. Whatever it holds, a run ends in exactly one way: an error,
// a reply, or a question already sent, and never both a question and the
// reply. What is sent mid-run is never blank. Lines that are not JSON
// objects, like the CLI's own warnings, change nothing.
func FuzzParseStream(f *testing.F) {
	for _, s := range []struct{ stream, junk string }{
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"header":"Meeting","question":"Which day suits you?"}]}}]}}
{"type":"result","result":"I asked which day suits you."}
`, "Warning: something"},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"Working on it"}]}}
{"type":"result","subtype":"success","result":"Done."}`, ""},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"Invalid API key"}]}}
{"type":"result","is_error":true}`, "  "},
		{`{"type":"result","error":"quota"}`, "{"},
		{`{"type":"result","result":""}`, "\r"},
		{`{"type":"assistant","message":null}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":"x"}]}}
{"type":"result","result":"a"}
{"type":"result","result":"b"}`, "[]"},
		{"", "plain text"},
		{"{not json\n{\"type\":\"result\",\"result\":\"\xff\"}", "\x00"},
	} {
		f.Add(s.stream, s.junk)
	}
	// One brain for every input: a fuzz worker runs them one at a time, and
	// a new folder for each would make most of the work file system calls.
	br := bareBrain(f)
	run := func(stream string) (RunResult, []string) {
		var sent []string
		br.hooks = &BrainHooks{Intermediate: func(_, text string) { sent = append(sent, text) }}
		return br.parseStream("chat", strings.NewReader(stream)), sent
	}
	f.Fuzz(func(t *testing.T, stream, junk string) {
		res, sent := run(stream)
		ends := 0
		for _, ended := range []bool{res.Err != nil, res.Text != "", res.AlreadySent} {
			if ended {
				ends++
			}
		}
		if ends != 1 {
			t.Errorf("result %+v is not exactly one of an error, a reply and a question sent", res)
		}
		if res.AlreadySent && len(sent) == 0 {
			t.Errorf("result %+v says a question went out, but none did", res)
		}
		if len(sent) > 0 && res.Text != "" {
			t.Errorf("questions %q were sent, and the reply %q too", sent, res.Text)
		}
		for _, q := range sent {
			if strings.TrimSpace(q) == "" {
				t.Errorf("sent the blank question %q", q)
			}
		}

		junk = strings.ReplaceAll(junk, "\n", " ")
		if strings.HasPrefix(strings.TrimSpace(junk), "{") {
			junk = "x" + junk
		}
		res2, sent2 := run(junk + "\n" + stream + "\n" + junk)
		if fmt.Sprint(res2) != fmt.Sprint(res) || fmt.Sprint(sent2) != fmt.Sprint(sent) {
			t.Errorf("the line %q around the stream changed the result from %+v, %q to %+v, %q", junk, res, sent, res2, sent2)
		}
	})
}
