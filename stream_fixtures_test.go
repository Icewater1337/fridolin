package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
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

// skippedLog is the activity log's line for a line of size bytes that
// parseStream skipped.
func skippedLog(size int) string {
	return fmt.Sprintf("⚠️ Skipped a line of %d bytes, longer than the 4 MiB the bridge reads", size)
}

// fillLine is what oversized-line.jsonl holds in place of the 5 MiB tool
// result of the run it records, which is too large to commit.
const fillLine = "@@FILL@@"

// TestParseStreamFixtures runs the parser over whole streams as Claude Code
// prints them with --output-format stream-json --verbose, in
// testdata/stream: the events of a run at testedClaudeVersion, with every
// field the CLI sends, the system init event and the tool results the
// parser has no use for among them. Their paths, IDs and texts are made
// up. An answer after a tool call, an API error, a failed login that only
// the last text explains, a question the model asks, and a tool result
// longer than the parser reads of a line.
func TestParseStreamFixtures(t *testing.T) {
	const fill = 5 << 20
	oversized := 0 // the length of the line with the tool result
	for _, l := range strings.Split(string(fixture(t, "stream/oversized-line.jsonl")), "\n") {
		if strings.Contains(l, fillLine) {
			oversized = len(l) - len(fillLine) + fill
		}
	}
	cases := []struct {
		file      string
		fill      int    // the bytes in place of fillLine
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
		{
			file: "oversized-line.jsonl",
			fill: fill,
			text: "The export lists 48213 rows.",
			logs: []string{
				"🔧 Tool: Read",
				skippedLog(oversized),
				"💬 The export lists 48213 rows.",
				"✅ Result: The export lists 48213 rows.",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			stream := string(fixture(t, "stream/"+c.file))
			if c.fill > 0 {
				stream = strings.Replace(stream, fillLine, strings.Repeat("x", c.fill), 1)
			}
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
			// The result, an error one too, as soon as it came: each
			// stream has one.
			if len(sink.results) != 1 || sink.results[0].Text != res.Text || sink.results[0].Err != res.Err {
				t.Errorf("results told %+v, want the one of %+v", sink.results, res)
			}
		})
	}
}

// assistantTextLine is an assistant event of exactly size bytes, its text x's.
func assistantTextLine(size int) string {
	const head, end = `{"type":"assistant","message":{"content":[{"type":"text","text":"`, `"}]}}`
	return head + strings.Repeat("x", size-len(head)-len(end)) + end
}

// TestParseStreamLongLines: a line longer than maxStreamLine is skipped,
// wherever it is, and the activity log says so; the rest of the stream
// counts as ever, so the run keeps its answer. The bridge failed such a
// run with "token too long" and lost its result. A line just as long as
// that is read.
func TestParseStreamLongLines(t *testing.T) {
	const result = `{"type":"result","result":"done"}`
	long := "💬 " + strings.Repeat("x", 2000) + "…" // a long text, as logged
	cases := []struct {
		name   string
		stream string
		logs   []string
	}{
		{"as long as one may be", assistantTextLine(maxStreamLine) + "\n" + result + "\n",
			[]string{long, "✅ Result: done"}},
		{"a byte longer", assistantTextLine(maxStreamLine+1) + "\n" + result + "\n",
			[]string{skippedLog(maxStreamLine + 1), "✅ Result: done"}},
		{"the last line, without a newline", result + "\n" + assistantTextLine(maxStreamLine+1),
			[]string{"✅ Result: done", skippedLog(maxStreamLine + 1)}},
		{"one after the other", assistantTextLine(5<<20) + "\n" + assistantTextLine(9<<20) + "\n" + result + "\n",
			[]string{skippedLog(5 << 20), skippedLog(9 << 20), "✅ Result: done"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sink recordingSink
			res := parseStream(strings.NewReader(c.stream), &sink)
			if res.Err != nil || res.Text != "done" {
				t.Errorf("result = %+v, want the text done", res)
			}
			if !slices.Equal(sink.logs, c.logs) {
				t.Errorf("activity log %.80q, want %.80q", sink.logs, c.logs)
			}
		})
	}
}

// failingReader yields what data holds, then err.
type failingReader struct {
	data *strings.Reader
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.data.Len() > 0 {
		return r.data.Read(p)
	}
	return 0, r.err
}

// TestParseStreamReadErrors: output that can no longer be read because the
// pipe was closed, as stopping a run does, is an interrupted run; any other
// failure to read it is the run's error.
func TestParseStreamReadErrors(t *testing.T) {
	closed := &fs.PathError{Op: "read", Path: "|0", Err: os.ErrClosed}
	cases := []struct {
		name string
		err  error
		want func(error) bool
	}{
		{"pipe closed", closed, func(err error) bool { return errors.Is(err, errBrainInterrupted) }},
		{"another error", errors.New("boom"), func(err error) bool {
			return err != nil && err.Error() == "reading claude output: boom"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &failingReader{data: strings.NewReader(`{"type":"assistant","message":{"content":[{"type":"text","text":"Working"}]}}` + "\n"), err: c.err}
			if res := parseStream(r, &recordingSink{}); !c.want(res.Err) || res.Text != "" {
				t.Errorf("result = %+v", res)
			}
		})
	}
}

// TestParseStreamErrorResults: a result event with is_error, or an error,
// fails the run with the CLI's own words: its error, its result, or else
// the last text the model wrote, where the CLI puts some failures, or a
// note that it gave none.
func TestParseStreamErrorResults(t *testing.T) {
	const said = `{"type":"assistant","message":{"content":[{"type":"text","text":"Credit balance is too low"}]}}`
	cases := []struct {
		name, stream, want string
	}{
		{"the error", `{"type":"result","is_error":true,"error":"quota","result":"ignored"}`, "quota"},
		{"an error without is_error", `{"type":"result","error":"quota"}`, "quota"},
		{"the result", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Execution error"}`, "Execution error"},
		{"the last text", said + "\n" + `{"type":"result","is_error":true}`, "Credit balance is too low"},
		{"the last text, cut", strings.Replace(said, "Credit balance is too low", strings.Repeat("é", 400), 1) + "\n" + `{"type":"result","is_error":true}`,
			strings.Repeat("é", 150) + "…"},
		{"no message at all", `{"type":"result","is_error":true}`, "(claude exited with an error but no message)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sink recordingSink
			res := parseStream(strings.NewReader(c.stream+"\n"), &sink)
			var cliErr *cliError
			if !errors.As(res.Err, &cliErr) || cliErr.msg != c.want || res.Text != "" {
				t.Fatalf("result = %+v, want the error %q", res, c.want)
			}
			if last := sink.logs[len(sink.logs)-1]; last != "❌ Error: "+c.want {
				t.Errorf("logged %q last, want the error", last)
			}
		})
	}
}

// TestParseStreamDrainsAfterAnError: after an error result the parser still
// reads the output to its end, though nothing in it counts any more. It
// stopped at the error, and a CLI, or a command it started, still writing
// to the pipe then blocked for good.
func TestParseStreamDrainsAfterAnError(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { r.Close() })
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, `{"type":"assistant","message":{"content":[{"type":"text","text":"Rate limited"}]}}`+"\n"+
			`{"type":"result","is_error":true,"result":"API Error: 429"}`+"\n")
		// More than a pipe holds, with what would count before the error.
		for i := 0; err == nil && i < 2000; i++ {
			_, err = io.WriteString(w, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"question":"Retry?"}]}}]}}`+"\n"+
				`{"type":"result","result":"late"}`+"\n")
		}
		w.Close()
		written <- err
	}()
	var sink recordingSink
	res := parseStream(r, &sink)
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the parser left the rest of the output unread")
	}
	var cliErr *cliError
	if !errors.As(res.Err, &cliErr) || cliErr.msg != "API Error: 429" || res.Text != "" || res.Asked {
		t.Errorf("result = %+v, want the error result", res)
	}
	if len(sink.questions) > 0 || len(sink.results) != 1 || len(sink.logs) != 2 {
		t.Errorf("after the error: questions %q, results %+v, log %q; want nothing", sink.questions, sink.results, sink.logs)
	}
}
