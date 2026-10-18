package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/chat"
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
		{"a paragraph per question", `{"questions":[{"question":"Which day?"},{"header":"Time","question":"Morning or afternoon?"}]}`, "Which day?\n\nTime: Morning or afternoon?"},
		{"only blank questions", `{"questions":[{},{"header":"Time","question":" "}]}`, ""},
		{"blank question left out", `{"questions":[{"question":""},{"header":" ","question":" Which day? "}]}`, "Which day?"},
		{"options numbered", `{"questions":[{"header":"Day","question":"Which day?","options":[{"label":"Monday"},{"label":"Tuesday","description":"the afternoon is free"}]}]}`,
			"Day: Which day?\n1. Monday\n2. Tuesday – the afternoon is free"},
		{"blank options left out", `{"questions":[{"question":"Which day?","options":[{"label":" "},{"description":"no label"},{"label":" Friday "}]}]}`, "Which day?\n1. Friday"},
		{"several to pick", `{"questions":[{"question":"Which topics?","multiSelect":true,"options":[{"label":"Budget"},{"label":"Roadmap"}]}]}`,
			"Which topics?\n1. Budget\n2. Roadmap\n(You can pick more than one.)"},
		{"options of each question", `{"questions":[{"question":"Which day?","options":[{"label":"Monday"},{"label":"Tuesday"}]},{"question":"Where?","options":[{"label":"Office"},{"label":"Online"}]}]}`,
			"Which day?\n1. Monday\n2. Tuesday\n\nWhere?\n1. Office\n2. Online"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractAskQuestion(json.RawMessage(c.input)); got != c.want {
				t.Errorf("extractAskQuestion(%s) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// readTestStream is what a run of br for m makes of the CLI output stream,
// its activity log and questions included, without a CLI that writes it.
func readTestStream(br *Brain, m Inbound, stream string) RunResult {
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
		want    RunResult
		err     bool
	}{
		{
			name:   "an answer of its own after asking",
			stream: []string{ask, unanswered, `{"type":"assistant","message":{"content":[{"type":"text","text":"I booked Monday."}]}}`, `{"type":"result","result":"I booked Monday."}`},
			sent:   []string{"❓ Day: Which day?\n1. Monday\n2. Tuesday"},
			want:   RunResult{Text: "I booked Monday.", Asked: true},
		},
		{
			name:   "only the question",
			stream: []string{ask, unanswered, `{"type":"result","subtype":"success","result":""}`},
			sent:   []string{"❓ Day: Which day?\n1. Monday\n2. Tuesday"},
			want:   RunResult{Asked: true},
		},
		{
			name:   "a question with nothing to ask",
			stream: []string{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{},{"question":" "}]}}]}}`, `{"type":"result","result":"Done."}`},
			want:   RunResult{Text: "Done."},
		},
		{
			name:   "an error after asking",
			stream: []string{ask, `{"type":"result","is_error":true,"result":"quota"}`},
			sent:   []string{"❓ Day: Which day?\n1. Monday\n2. Tuesday"},
			want:   RunResult{Asked: true},
			err:    true,
		},
		{
			name:   "no question",
			stream: []string{`{"type":"result","result":"Hello."}`},
			want:   RunResult{Text: "Hello."},
		},
		{
			name:   "a silent run's question",
			stream: []string{ask, unanswered, `{"type":"result","result":"I booked Monday."}`},
			silent: true,
			want:   RunResult{Text: "I booked Monday."},
		},
		{
			// One bridge.db refused: it asked nobody.
			name:    "a question the chat did not get",
			stream:  []string{ask, unanswered, `{"type":"result","result":"I booked Monday."}`},
			refused: true,
			want:    RunResult{Text: "I booked Monday."},
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
			br.hooks = &BrainHooks{Question: func(_ *Brain, jid, text string) bool {
				if jid != "chat" || c.silent {
					t.Errorf("question for %q from a run silent: %v, want only the run's chat, of an addressed run", jid, c.silent)
				}
				if c.refused {
					return false
				}
				sent = append(sent, text)
				return true
			}}
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

// FuzzExtractAskQuestion: the AskUserQuestion input is the model's. What
// comes out goes to the chat as it is, so it is valid UTF-8, never blank,
// and holds every question that was asked and every option offered.
func FuzzExtractAskQuestion(f *testing.F) {
	for _, s := range []string{
		``, `{"questions":`, `{"questions":[]}`, `null`, `[]`, `{"questions":null}`,
		`{"questions":[{"question":"Which day suits you?"}]}`,
		`{"questions":[{"header":"Meeting","question":"Which day suits you?"}]}`,
		`{"questions":[{"header":"Which day?","question":"Which day?"}]}`,
		`{"questions":[{"question":"Which day?"},{"header":"Time","question":"Morning or afternoon?","options":[{"label":"Morning"}]}]}`,
		`{"questions":[{"question":"caf\u00e9 \ud83d"}]}`,
		`{"questions":[{"header":"Day","question":"Which day?","multiSelect":true,"options":[{"label":"Monday","description":"busy"},{"label":" "},{}]}]}`,
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
			s := strings.TrimSpace(q.Question)
			if s == "" {
				continue
			}
			if !strings.Contains(got, s) {
				t.Errorf("extractAskQuestion(%s) = %q, without the question %q", raw, got, s)
			}
			for _, o := range q.Options {
				if l := strings.TrimSpace(o.Label); l != "" && !strings.Contains(got, l) {
					t.Errorf("extractAskQuestion(%s) = %q, without the option %q", raw, got, l)
				}
			}
		}
	})
}

// FuzzParseStream: the stream is the CLI's, but most of what is in it the
// model wrote. Whatever it holds, a run ends either in an error or with
// something said: a reply, a question sent mid-run, or both, and never
// with an error and a reply. It reports a question exactly when one was
// sent, and what is sent mid-run is never blank. Lines that are not JSON
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
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"question":"Which day?","options":[{"label":"Monday"}]}]}}]}}
{"type":"result","is_error":true,"result":"quota"}`, ""},
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
	run := func(stream string) (RunResult, []string) {
		var sink recordingSink
		res := parseStream(strings.NewReader(stream), &sink)
		return res, sink.questions
	}
	f.Fuzz(func(t *testing.T, stream, junk string) {
		res, sent := run(stream)
		if res.Err == nil && res.Text == "" && !res.Asked {
			t.Errorf("result %+v has neither an error nor anything said", res)
		}
		if res.Err != nil && res.Text != "" {
			t.Errorf("result %+v has both an error and a reply", res)
		}
		if res.Asked != (len(sent) > 0) {
			t.Errorf("result %+v, with the questions %q sent", res, sent)
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

// A question a brain asks mid-run reaches the chat marked as a question,
// and the working notice as it is, whatever either starts with: the
// bridge used to tell the two apart by a leading ⏳.
func TestMidRunMessagesReachTheChatMarked(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
	br := tb.brainMgr.Get("a")
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"question":"⏳ Wait for the delivery?"}]}}]}}
{"type":"result","result":"asked"}
`
	readTestStream(br, Inbound{Inbound: chat.Inbound{JID: dm}}, stream)
	br.hooks.Notice(dm, workingNotice)
	// The question goes through the outbox, after the notice.
	waitFor(t, 5*time.Second, "both messages", func() bool { return len(tb.sentTo(dm)) == 2 })
	sent := tb.sentTo(dm)
	slices.Sort(sent)
	if want := []string{"⏳ Working on it...", "❓ ⏳ Wait for the delivery?"}; !slices.Equal(sent, want) {
		t.Errorf("sent %q, want %q", sent, want)
	}
}
