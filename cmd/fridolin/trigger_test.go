package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTriggerListJSONRoundTrip(t *testing.T) {
	var tl TriggerList
	in := `["@claude", {"text":"@c","match":"start"}, {"text":"bot","match":"word"}, {"text":"x"}]`
	if err := json.Unmarshal([]byte(in), &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl) != 4 || tl[0].Match != MatchAnywhere || tl[1].Match != MatchStart || tl[3].Match != MatchAnywhere {
		t.Fatalf("parsed %+v", tl)
	}
	out, _ := json.Marshal(tl)
	want := `["@claude",{"text":"@c","match":"start"},{"text":"bot","match":"word"},"x"]`
	if string(out) != want {
		t.Errorf("marshal = %s, want %s", out, want)
	}
	if err := json.Unmarshal([]byte(`[{"text":"a","match":"sometimes"}]`), &tl); err == nil {
		t.Error("an unknown match mode must be rejected")
	}
}

func TestTriggerStartMatch(t *testing.T) {
	tl := TriggerList{{Text: "@c", Match: MatchStart}, {Text: "©", Match: MatchStart}}
	cases := []struct {
		text string
		want bool
	}{
		{"@c hi", true},
		{"  @C hi", true},
		{"@c", true},
		{"@c, can you", true},
		{"@carla are you coming", false}, // a name that starts with the shorthand
		{"hi @c", false},                 // only at the start
		{"©hi", true},                    // a symbol trigger needs no word boundary
		{"x ©", false},
	}
	for _, c := range cases {
		if got := tl.Matches(c.text); got != c.want {
			t.Errorf("Matches(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	if got := tl.Strip("@c remind me @c later"); got != "remind me @c later" {
		t.Errorf("start trigger must only be stripped from the start, got %q", got)
	}
}

func TestTriggerWordMatch(t *testing.T) {
	tl := TriggerList{{Text: "bot", Match: MatchWord}}
	if !tl.Matches("hey bot, help") || !tl.Matches("BOT") {
		t.Error("word trigger should match the whole word")
	}
	if tl.Matches("robotics") || tl.Matches("bots") {
		t.Error("word trigger must not match inside another word")
	}
}

func TestTriggerStripKeepsByteOffsetsWithUnicode(t *testing.T) {
	// "İ" lowercases to a longer byte sequence; offsets must come from the
	// original text, not a lowercased copy.
	tl := TriggersFromStrings([]string{"@claude"})
	if got := tl.Strip("İİ @CLAUDE ok"); got != "İİ  ok" {
		t.Errorf("Strip = %q", got)
	}
}

// FuzzTriggerListJSON: triggers come from config.json and the dashboard.
// Whatever decodes is a list of triggers with a known match mode, and it
// encodes and decodes back to itself, so a save never changes them.
func FuzzTriggerListJSON(f *testing.F) {
	for _, s := range []string{
		`["@claude", {"text":"@c","match":"start"}, {"text":"bot","match":"word"}, {"text":"x"}]`,
		`[{"text":"a","match":"sometimes"}]`,
		`[{"TEXT":"a","Match":"word"}, {"text":"b","match":"anywhere"}, {"match":"start"}]`,
		`[null, "", {}, {"text":null}]`,
		`[{"text":"a","text":"b","match":"start","match":""}]`,
		`["\u00e9\ud800", "<&>", "\u2028"]`,
		`[]`, `null`, `"x"`, `[1]`, `{}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var tl TriggerList
		if err := json.Unmarshal(data, &tl); err != nil {
			return
		}
		for _, tr := range tl {
			switch tr.Match {
			case MatchAnywhere, MatchStart, MatchWord:
			default:
				t.Errorf("trigger %q decoded with match %q", tr.Text, tr.Match)
			}
		}
		out, err := json.Marshal(tl)
		if err != nil {
			t.Fatalf("encoding %+v: %v", tl, err)
		}
		var back TriggerList
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("decoding %s again: %v", out, err)
		}
		if !reflect.DeepEqual(back, tl) {
			t.Errorf("%s decoded as %+v, encoded as %s, decoded again as %+v", data, tl, out, back)
		}
	})
}

// matchModes are the match modes a fuzzed byte picks from.
var matchModes = []TriggerMatch{MatchAnywhere, MatchStart, MatchWord}

// FuzzTriggerStrip: group messages are anyone's text, and a trigger is any
// text the operator picked. Strip only removes whole characters and never
// makes the message longer than it was trimmed; it leaves a message no
// trigger matches as it was, trimmed; a trigger that may match anywhere in
// the message is gone from it afterwards; and ASCII letter case changes
// nothing.
func FuzzTriggerStrip(f *testing.F) {
	seeds := []struct {
		text, t1 string
		m1       uint8
		t2       string
		m2       uint8
	}{
		{"@claude hello @claude again", "@claude", 0, "hey bot", 0},
		{"@c remind me @c later", "@c", 1, "©", 1},
		{"  @C hi", "@c", 1, "", 0},
		{"hey bot, help robotics bots", "bot", 2, "BOT", 2},
		{"aaaa", "a", 0, "aa", 2},
		{"İİ @CLAUDE ok", "@claude", 0, "i\u0307", 0},
		{"\u212a k K", "k", 2, "K", 1},
		{"x\u00a0@c\u00a0y", "\u00a0", 0, "@c", 1},
		{"\xffbot\xe2\x82", "\xff", 0, "bot", 2},
		{"\uFFFD\uFFFD", "\xe2\x82", 0, "", 0},
	}
	for _, s := range seeds {
		f.Add(s.text, s.t1, s.m1, s.t2, s.m2)
	}
	f.Fuzz(func(t *testing.T, text, t1 string, m1 uint8, t2 string, m2 uint8) {
		tl := TriggerList{{Text: t1, Match: matchModes[int(m1)%len(matchModes)]}, {Text: t2, Match: matchModes[int(m2)%len(matchModes)]}}
		got := tl.Strip(text)
		trimmed := strings.TrimSpace(text)
		if len(got) > len(trimmed) {
			t.Errorf("Strip(%q) = %q, longer than the message trimmed", text, got)
		}
		if got != strings.TrimSpace(got) {
			t.Errorf("Strip(%q) = %q, not trimmed", text, got)
		}
		if utf8.ValidString(text) && !utf8.ValidString(got) {
			t.Errorf("Strip(%q) = %q, which splits a character", text, got)
		}
		matches := tl.Matches(text)
		if !matches && got != trimmed {
			t.Errorf("Strip(%q) = %q, but no trigger matches it", text, got)
		}
		if one := (TriggerList{tl[0]}.Matches(text) || TriggerList{tl[1]}.Matches(text)); one != matches {
			t.Errorf("Matches(%q) = %v, but %v for the triggers one by one", text, matches, one)
		}
		swapped := swapASCIICase(text)
		if tl.Matches(swapped) != matches {
			t.Errorf("Matches(%q) = %v, but not for %q", text, matches, swapped)
		}
		if s := tl.Strip(swapped); s != swapASCIICase(got) {
			t.Errorf("Strip(%q) = %q, but Strip(%q) = %q", text, got, swapped, s)
		}
		for _, tr := range tl {
			if tr.Match == MatchStart {
				continue
			}
			one := TriggerList{tr}
			if s := one.Strip(text); one.Matches(s) {
				t.Errorf("%+v still matches %q, what Strip(%q) left", tr, s, text)
			}
		}
	})
}
