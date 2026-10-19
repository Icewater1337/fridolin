package main

import (
	"encoding/json"
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
