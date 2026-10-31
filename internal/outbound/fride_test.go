package outbound

import (
	"strings"
	"testing"
)

func TestFrideShort(t *testing.T) {
	words := strings.Repeat("word ", 130)                                                                         // 650 runes, no sentence end
	sentences := strings.Repeat("A short sentence here. ", 20) + "And the last one" + strings.Repeat(" more", 40) // a sentence end past the half
	for _, tc := range []struct{ in, want string }{
		{"one\ntwo", "one\ntwo"},
		{"one\n\n\ntwo\nthree", "one\ntwo\nthree"},
		{"1\n2\n3\n4\n5\n6", "1\n2\n3\n4\n5\n" + FrideMore},
		{strings.Repeat("x", 601), strings.Repeat("x", 600) + "\n" + FrideMore},
		// Never inside a word: at the last space before the bound.
		{words, strings.TrimSpace(words[:600]) + "\n" + FrideMore},
		// At the last sentence end when there is one in the second half.
		{sentences, strings.TrimSpace(sentences[:strings.LastIndex(sentences[:600], ".")+1]) + "\n" + FrideMore},
	} {
		if got := frideShort(tc.in); got != tc.want {
			t.Errorf("frideShort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A question comes whole, however many lines, up to a bound only a
// runaway text reaches; a failure keeps its reason at length; other news
// is short.
func TestFrideFor(t *testing.T) {
	question := "May it use the development-channels flag with a fridolin-owned MCP server, or should it wait for an allowlisted channel?\n" +
		strings.TrimSpace(strings.Repeat("Some context the owner needs to decide. ", 20)) + "\nOption one\nOption two\nOption three\nOption four\nOption five"
	if got := frideFor("question", question); got != question {
		t.Errorf("a question was cut:\n%s", got)
	}
	if got := frideFor("question", strings.Repeat("why ", 1100)); !strings.HasSuffix(got, FrideMore) || len([]rune(got)) > frideMaxQuestionRunes+len([]rune(FrideMore))+1 {
		t.Errorf("a runaway question: %d runes", len([]rune(got)))
	}
	failed := strings.Repeat("the build broke because ", 50) // 1200 runes
	if got := frideFor("failed", failed); got != strings.TrimSpace(failed) {
		t.Errorf("a failure was cut:\n%s", got)
	}
	if got := frideFor("update", question); !strings.HasSuffix(got, FrideMore) {
		t.Errorf("news was not shortened:\n%s", got)
	}
}

func TestFrideBookkeepingAndAsks(t *testing.T) {
	for in, want := range map[string]bool{
		"Created task fix-1 on `api`, with new branch fix/1":       true,
		"FrIDE inbox #3 · update\n• → agent: Fix GitHub issue #12": true,
		"Created task fix-1\nThe tests fail on main.":              false,
		"PR merged": false,
	} {
		if got := FrideBookkeeping(in); got != want {
			t.Errorf("FrideBookkeeping(%q) = %v", in, got)
		}
	}
	for in, want := range map[string]bool{
		"Which empty state?": true,
		"FrIDE inbox #2 · needs your reply\nThe agent is still working; I'd resend it": false,
		"Please confirm the plan.": true,
		"Should I force-push":      true,
	} {
		if got := FrideAsks(in); got != want {
			t.Errorf("FrideAsks(%q) = %v", in, got)
		}
	}
}

func TestFrideStalled(t *testing.T) {
	a := "FrIDE inbox #4 · needs your reply\nAgent fix-12 has not moved for 2h. Should I nudge it?"
	b := "FrIDE inbox #4 · needs your reply\nAgent fix-12 has not moved for 3h. Should I nudge it?"
	if !FrideStalled(a) || !FrideStalled("The agent hasn't moved since noon") || FrideStalled("Which fix?") {
		t.Error("FrideStalled")
	}
	if FrideStallKey(a) != FrideStallKey(b) || FrideStallKey(a) == FrideStallKey("Agent review has not moved for 2h") {
		t.Error("FrideStallKey")
	}
}

func TestFrideFanoutMessage(t *testing.T) {
	got := FrideFanoutMessage("mini", "update", []int64{9, 10, 15}, "FrIDE inbox #9 · update\nAll merged.")
	if want := "🛠️ *FrIDE on mini* (#9, #10, #15):\nAll merged."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
