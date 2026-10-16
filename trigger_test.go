package main

import (
	"encoding/json"
	"testing"
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
