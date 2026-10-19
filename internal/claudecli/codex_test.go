package claudecli

import (
	"errors"
	"strings"
	"testing"
)

// The provider adapter shares the terminal outcome with its sink, drains
// after it, and applies the same line bound as Claude's parser.
func TestCodexTerminalResult(t *testing.T) {
	const answer = `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}` + "\n" + `{"type":"turn.completed"}` + "\n"
	const failure = `{"type":"turn.failed","error":{"message":"quota exceeded"}}` + "\n"
	for _, tc := range []struct{ name, stream, text, err string }{
		{"answer then error", answer + failure, "done", ""},
		{"error then answer", failure + answer, "", "codex error: quota exceeded"},
		{"empty then answer", `{"type":"turn.completed"}` + "\n" + answer, "", ErrNoResult.Error()},
		{"oversized before answer", strings.Repeat("x", MaxStreamLine+1) + "\n" + answer, "done", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sink recordingSink
			reader := strings.NewReader(tc.stream)
			res := ParseProvider(reader, &sink, "codex")
			if reader.Len() != 0 {
				t.Error("left output undrained")
			}
			errText := func(r Result) string {
				if r.Err == nil {
					return ""
				}
				return r.Err.Error()
			}
			if res.Text != tc.text || errText(res) != tc.err {
				t.Fatalf("result = %+v, want %q, %q", res, tc.text, tc.err)
			}
			if len(sink.results) != 1 || sink.results[0].Text != res.Text || errText(sink.results[0]) != errText(res) {
				t.Fatalf("sink = %+v, result = %+v", sink.results, res)
			}
			var cliErr *Error
			if errors.As(res.Err, &cliErr) && cliErr.Provider != "codex" {
				t.Errorf("wrong provider: %q", cliErr.Provider)
			}
		})
	}
}
