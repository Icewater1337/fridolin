package main

import (
	"slices"
	"strings"
	"testing"
)

// recordingSink is a StreamSink that keeps what parseStream tells it.
type recordingSink struct {
	logs      []string
	questions []string
	results   []RunResult
	refuse    bool // no question goes out
}

func (s *recordingSink) Log(line string) { s.logs = append(s.logs, line) }

func (s *recordingSink) Question(text string) bool {
	if s.refuse {
		return false
	}
	s.questions = append(s.questions, text)
	return true
}

func (s *recordingSink) Result(res RunResult) { s.results = append(s.results, res) }

// TestParseStreamFixtures runs the parser over whole streams as Claude Code
// prints them with --output-format stream-json --verbose, in
// testdata/stream: the events of a run at testedClaudeVersion, with every
// field the CLI sends, the system init event and the tool results the
// parser has no use for among them. Their paths, IDs and texts are made
// up. An answer after a tool call, an API error, a failed login that only
// the last text explains, and a question the model asks.
func TestParseStreamFixtures(t *testing.T) {
	cases := []struct {
		file      string
		text      string // the result's Text
		err       string // what its error says, "" for none
		asked     bool
		questions []string
		logs      []string // the activity log, in full
	}{
		{
			file: "success.jsonl",
			text: "Your dentist appointment is on Thursday at 10:00.",
			logs: []string{
				"💬 Let me look at your notes.",
				"🔧 Tool: Read",
				"💬 Your dentist appointment is on Thursday at 10:00.",
				"✅ Result: Your dentist appointment is on Thursday at 10:00.",
			},
		},
		{
			file: "api-error.jsonl",
			err:  `claude error: API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"},"request_id":"req_011CFixture0000000000000"}`,
			logs: []string{
				`💬 API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"},"request_id":"req_011CFixture0000000000000"}`,
				`❌ Error: API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"},"request_id":"req_011CFixture0000000000000"}`,
			},
		},
		{
			file: "auth-failure.jsonl",
			err:  "claude error: Invalid API key · Please run /login",
			logs: []string{
				"💬 Invalid API key · Please run /login",
				"❌ Error: Invalid API key · Please run /login",
			},
		},
		{
			file:      "ask-question.jsonl",
			text:      "I asked which day suits you. Until you answer, Thursday at 10:00 stays in your notes.",
			asked:     true,
			questions: []string{"Day: Which day suits you for the dentist?\n1. Thursday – 10:00, as in your notes\n2. Friday – any time after 14:00"},
			logs: []string{
				"🔧 Tool: AskUserQuestion",
				"💬 I asked which day suits you. Until you answer, Thursday at 10:00 stays in your notes.",
				"✅ Result: I asked which day suits you. Until you answer, Thursday at 10:00 stays in your notes.",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			stream := string(fixture(t, "stream/"+c.file))
			var sink recordingSink
			res := parseStream(strings.NewReader(stream), &sink)
			if res.Text != c.text || res.Asked != c.asked {
				t.Errorf("result = %+v, want the text %q, asked: %v", res, c.text, c.asked)
			}
			switch {
			case c.err == "" && res.Err != nil:
				t.Errorf("error %v, want none", res.Err)
			case c.err != "" && (res.Err == nil || res.Err.Error() != c.err):
				t.Errorf("error %v, want %q", res.Err, c.err)
			}
			if !slices.Equal(sink.questions, c.questions) {
				t.Errorf("questions %q, want %q", sink.questions, c.questions)
			}
			if !slices.Equal(sink.logs, c.logs) {
				t.Errorf("activity log:\n%s\nwant:\n%s", strings.Join(sink.logs, "\n"), strings.Join(c.logs, "\n"))
			}
		})
	}
}
