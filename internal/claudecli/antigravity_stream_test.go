package claudecli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Constructed in the shape of agy 1.5.14's events (testdata/agy-*.jsonl
// are real runs); real ones carry more fields, which the parser ignores.
const (
	agyInit      = `{"event":"init","conversation_id":"c1","init":{"cwd":"/b","tools":["run_command"],"permission_mode":"always-proceed","model":"gemini-x"}}`
	agyUser      = `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":0,"state":"DONE","step_type":"user_input"}}`
	agyThinking1 = `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"Let me "}}`
	agyThinking2 = `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"look."}}`
	agyToolA     = `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"run_command"}}`
	agyToolD     = `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","output":"ok"}}}`
	agyAnswer    = `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":3,"state":"DONE","step_type":"agent_response","text_delta":"All done."}}`
	agySuccess   = `{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"All done.","duration_seconds":6.8,"num_turns":1,"usage":{"input_tokens":100,"output_tokens":20,"thinking_tokens":5,"cache_read_tokens":40,"total_tokens":165}}}`
	agyFailed    = `{"event":"result","result":{"conversation_id":"c1","status":"ERROR","response":"partial","error":"quota exhausted","finished_reason":"error"}}`
	agyUnknown   = `{"event":"something_new","x":1}`
)

func agyStream(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func TestAntigravityStream(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stream, text, err string
		logs                    []string
		in, out                 int64
	}{
		{name: "answer", stream: agyStream(agyInit, agyUser, agyThinking1, agyThinking2, agyToolA, agyToolD, agyAnswer, agyUnknown, agySuccess),
			text: "All done.", logs: []string{"💬 Let me look.", "🔧 Tool: run_command", "💬 All done."}, in: 100, out: 25},
		{name: "error result", stream: agyStream(agyInit, agyAnswer, agyFailed), err: "antigravity error: quota exhausted"},
		{name: "empty response takes the last text", stream: agyStream(agyAnswer, strings.Replace(agySuccess, `"response":"All done."`, `"response":""`, 1)), text: "All done."},
		{name: "no result", stream: agyStream(agyInit, agyAnswer), err: ErrNoResult.Error()},
		{name: "answer then error", stream: agyStream(agySuccess, agyFailed), text: "All done."},
		{name: "not JSON", stream: "warning: something\n" + agyStream(agySuccess), text: "All done."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sink recordingSink
			res := ParseProvider(strings.NewReader(tc.stream), &sink, ProviderAntigravity)
			errText := ""
			if res.Err != nil {
				errText = res.Err.Error()
			}
			if res.Text != tc.text || errText != tc.err {
				t.Fatalf("result = %q, %q; want %q, %q", res.Text, errText, tc.text, tc.err)
			}
			for _, want := range tc.logs {
				if !slices.Contains(sink.logs, want) {
					t.Errorf("log %q lacks %q", sink.logs, want)
				}
			}
			if tc.in != 0 && (res.Usage.InputTokens != tc.in || res.Usage.OutputTokens != tc.out) {
				t.Errorf("usage = %+v", res.Usage)
			}
			if n := slices.IndexFunc(sink.logs, func(l string) bool { return strings.Contains(l, "Tool: run_command") }); n >= 0 &&
				slices.IndexFunc(sink.logs[n+1:], func(l string) bool { return strings.Contains(l, "Tool: run_command") }) >= 0 {
				t.Errorf("tool logged twice: %q", sink.logs)
			}
		})
	}
}

// TestAntigravityRealRuns reads the stream-json of real agy 1.5.14 turns,
// signed in, through a pipe: a plain answer, and one that ran a command.
func TestAntigravityRealRuns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file, text, log string
		in, out         int64
	}{
		{file: "agy-answer.jsonl", text: "pong", in: 9360, out: 46},
		{file: "agy-tool.jsonl", text: "marker-7731", log: "🔧 Tool: run_command"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			var sink recordingSink
			res := ParseProvider(f, &sink, ProviderAntigravity)
			if res.Err != nil || !strings.Contains(res.Text, tc.text) {
				t.Fatalf("result = %q, %v; want it to contain %q", res.Text, res.Err, tc.text)
			}
			if tc.log != "" && !slices.Contains(sink.logs, tc.log) {
				t.Errorf("log %q lacks %q", sink.logs, tc.log)
			}
			if tc.in != 0 && (res.Usage.InputTokens != tc.in || res.Usage.OutputTokens != tc.out) {
				t.Errorf("usage = %+v, want %d in, %d out", res.Usage, tc.in, tc.out)
			}
		})
	}
}

func TestAntigravityAuthErrors(t *testing.T) {
	t.Parallel()
	for _, msg := range []string{"authentication failed or timed out", "Authentication expired. Please log back in."} {
		if got := antigravityError("ERROR", msg, ""); !strings.HasPrefix(got, AntigravityNotSignedIn) {
			t.Errorf("%q -> %q", msg, got)
		}
	}
	if got := antigravityError("CANCELED", "", ""); got != "the run ended with status CANCELED" {
		t.Errorf("empty error -> %q", got)
	}
}
