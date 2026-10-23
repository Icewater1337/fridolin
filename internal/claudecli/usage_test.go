package claudecli

import (
	"strings"
	"testing"
)

// TestResultUsage: what the result says a run cost reaches the Result,
// for Claude (dollars and tokens, cached ones counted as input) and
// Codex (tokens).
func TestResultUsage(t *testing.T) {
	var sink recordingSink
	res := Parse(strings.NewReader(`{"type":"result","result":"ok","total_cost_usd":0.0123,"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":5,"output_tokens":42}}`+"\n"), &sink)
	if res.Usage != (Usage{CostUSD: 0.0123, InputTokens: 105, OutputTokens: 42}) {
		t.Errorf("claude usage %+v", res.Usage)
	}
	res = ParseProvider(strings.NewReader(`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`+"\n"+`{"type":"turn.completed","usage":{"input_tokens":300,"cached_input_tokens":200,"output_tokens":7}}`+"\n"), &sink, "codex")
	if res.Text != "done" || res.Usage != (Usage{InputTokens: 300, OutputTokens: 7}) {
		t.Errorf("codex %q usage %+v", res.Text, res.Usage)
	}
	res = Parse(strings.NewReader(`{"type":"result","is_error":true,"result":"quota","total_cost_usd":0.5}`+"\n"), &sink)
	if res.Err == nil || res.Usage.CostUSD != 0.5 {
		t.Errorf("an error result keeps its cost: %+v", res)
	}
}
