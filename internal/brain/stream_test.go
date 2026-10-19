package brain

import (
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// readTestStream is what a run of br for m makes of the CLI output stream,
// its activity log and questions included, without a CLI that writes it.
func readTestStream(br *Brain, m Inbound, stream string) claudecli.Result {
	return br.readStream(m, strings.NewReader(stream), "── Test run ──", nil)
}

// A brain that asks with AskUserQuestion gets the question to the chat
// while it runs, with its options. Nobody answers the call in a -p run,
// so the model goes on, and what it ends with reaches the chat too: an
// answer it settled on itself must not be lost. A run that only asked has
// no reply to send, and that is no error. A silent run asks nobody.
func TestParseStreamQuestions(t *testing.T) {
	const ask = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"header":"Day","question":"Which day?","options":[{"label":"Monday"},{"label":"Tuesday"}]}]}}]}}`
	const unanswered = `{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"Answer questions?"}]}}`
	cases := []struct {
		name    string
		stream  []string
		silent  bool
		refused bool // the hook did not put the question on its way
		sent    []string
		want    claudecli.Result
		err     bool
	}{
		{
			name:   "an answer of its own after asking",
			stream: []string{ask, unanswered, `{"type":"assistant","message":{"content":[{"type":"text","text":"I booked Monday."}]}}`, `{"type":"result","result":"I booked Monday."}`},
			sent:   []string{"Day: Which day?\n1. Monday\n2. Tuesday"},
			want:   claudecli.Result{Text: "I booked Monday.", Asked: true},
		},
		{
			name:   "only the question",
			stream: []string{ask, unanswered, `{"type":"result","subtype":"success","result":""}`},
			sent:   []string{"Day: Which day?\n1. Monday\n2. Tuesday"},
			want:   claudecli.Result{Asked: true},
		},
		{
			name:   "a question with nothing to ask",
			stream: []string{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{},{"question":" "}]}}]}}`, `{"type":"result","result":"Done."}`},
			want:   claudecli.Result{Text: "Done."},
		},
		{
			name:   "an error after asking",
			stream: []string{ask, `{"type":"result","is_error":true,"result":"quota"}`},
			sent:   []string{"Day: Which day?\n1. Monday\n2. Tuesday"},
			want:   claudecli.Result{Asked: true},
			err:    true,
		},
		{
			name:   "no question",
			stream: []string{`{"type":"result","result":"Hello."}`},
			want:   claudecli.Result{Text: "Hello."},
		},
		{
			name:   "a silent run's question",
			stream: []string{ask, unanswered, `{"type":"result","result":"I booked Monday."}`},
			silent: true,
			want:   claudecli.Result{Text: "I booked Monday."},
		},
		{
			// One bridge.db refused: it asked nobody.
			name:    "a question the chat did not get",
			stream:  []string{ask, unanswered, `{"type":"result","result":"I booked Monday."}`},
			refused: true,
			want:    claudecli.Result{Text: "I booked Monday."},
		},
		{
			name:    "only a question the chat did not get",
			stream:  []string{ask, unanswered, `{"type":"result","result":""}`},
			refused: true,
			err:     true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bareBrain(t)
			var sent []string
			br.sink = sinkForTest(&testSink{Ask: func(_ *Brain, jid, text string) bool {
				if jid != "chat" || c.silent {
					t.Errorf("question for %q from a run silent: %v, want only the run's chat, of an addressed run", jid, c.silent)
				}
				if c.refused {
					return false
				}
				sent = append(sent, text)
				return true
			}})
			res := readTestStream(br, Inbound{Inbound: chat.Inbound{JID: "chat", Silent: c.silent}}, strings.Join(c.stream, "\n")+"\n")
			if (res.Err != nil) != c.err {
				t.Errorf("error = %v, want one: %v", res.Err, c.err)
			}
			res.Err = nil
			if res != c.want {
				t.Errorf("result = %+v, want %+v", res, c.want)
			}
			if !slices.Equal(sent, c.sent) {
				t.Errorf("sent %q mid-run, want %q", sent, c.sent)
			}
		})
	}
}
