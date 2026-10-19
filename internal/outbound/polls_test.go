package outbound

import (
	"fmt"

	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"unicode/utf8"
)

func TestExtractPolls(t *testing.T) {
	text := `Sure, let's vote.

[POLL]
Question: When should we meet?
- Monday 2pm
- Tuesday 3pm
[/POLL]

Let me know.`

	polls, remaining := ExtractPolls(text)
	if len(polls) != 1 {
		t.Fatalf("got %d polls, want 1", len(polls))
	}
	p := polls[0]
	if p.Question != "When should we meet?" {
		t.Errorf("question = %q", p.Question)
	}
	if len(p.Options) != 2 || p.Options[0] != "Monday 2pm" {
		t.Errorf("options = %v", p.Options)
	}
	if p.MaxSelectable != 1 {
		t.Errorf("MaxSelectable = %d, want 1", p.MaxSelectable)
	}
	if strings.Contains(remaining, "POLL") {
		t.Errorf("poll block left in remaining text: %q", remaining)
	}
	if !strings.Contains(remaining, "Let me know.") {
		t.Errorf("surrounding text lost: %q", remaining)
	}
}

// The end of a block is found in any letter case, like its start: a
// "[Poll]…[/poll]" block is a poll, not markup in the chat.
func TestExtractPollsAnyCase(t *testing.T) {
	polls, remaining := ExtractPolls("Vote:\n[Poll max=2]\nQuestion: Which?\n- A\n- B\n[/poll] thanks")
	if len(polls) != 1 || polls[0].Question != "Which?" || polls[0].MaxSelectable != 2 {
		t.Errorf("got %+v", polls)
	}
	if remaining != "Vote:\n thanks" {
		t.Errorf("remaining = %q", remaining)
	}
}

func TestExtractPollsMaxSelectable(t *testing.T) {
	polls, _ := ExtractPolls("[POLL max=2]\nQuestion: Which?\n- A\n- B\n- C\n[/POLL]")
	if len(polls) != 1 || polls[0].MaxSelectable != 2 {
		t.Fatalf("got %+v", polls)
	}
}

func TestExtractPollsClampsMaxToOptionCount(t *testing.T) {
	// WhatsApp rejects a poll whose max exceeds the number of options.
	polls, _ := ExtractPolls("[POLL max=9]\nQuestion: Which?\n- A\n- B\n[/POLL]")
	if len(polls) != 1 {
		t.Fatalf("got %d polls, want 1", len(polls))
	}
	if polls[0].MaxSelectable != 2 {
		t.Errorf("MaxSelectable = %d, want 2", polls[0].MaxSelectable)
	}
}

func TestExtractPollsTruncatesToWhatsAppLimit(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[POLL]\nQuestion: Pick\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, "- option %d\n", i+1)
	}
	sb.WriteString("[/POLL]")

	polls, _ := ExtractPolls(sb.String())
	if len(polls) != 1 {
		t.Fatalf("got %d polls, want 1", len(polls))
	}
	if len(polls[0].Options) != MaxPollOptions {
		t.Errorf("options = %d, want %d", len(polls[0].Options), MaxPollOptions)
	}
}

// One option is not a poll; neither is a block with no question, nor one
// that never ends. Each stays in the text as the brain wrote it, so what
// it said still reaches the chat.
func TestExtractPollsRejectsIncomplete(t *testing.T) {
	for _, text := range []string{
		"Here is my idea:\n[POLL]\nQuestion: Only one?\n- A\n[/POLL]\nafter",
		"[POLL]\n- A\n- B\n[/POLL] what do you think?",
		"[POLL]\nQuestion: unterminated\n- A\n- B",
		"[POLL]\nQuestion: Same twice?\n- A\n- A\n[/POLL]",
	} {
		polls, remaining := ExtractPolls(text)
		if len(polls) != 0 {
			t.Errorf("ExtractPolls(%q) returned %d polls, want 0", text, len(polls))
		}
		if remaining != text {
			t.Errorf("ExtractPolls(%q) left %q, want the text as it was", text, remaining)
		}
	}
}

// A block left open ends nowhere: its header is text, and the next block
// is a poll of its own, with the words between them kept.
func TestExtractPollsUnclosedBlockDoesNotSwallowTheNext(t *testing.T) {
	text := "[POLL]\nQuestion: first?\n- A\n- B\nbetween\n[POLL]\nQuestion: second?\n- C\n- D\n[/POLL]\nend"
	polls, remaining := ExtractPolls(text)
	want := []Poll{{Question: "second?", Options: []string{"C", "D"}, MaxSelectable: 1}}
	if !reflect.DeepEqual(polls, want) {
		t.Errorf("polls = %+v, want %+v", polls, want)
	}
	if wantText := "[POLL]\nQuestion: first?\n- A\n- B\nbetween\n\nend"; remaining != wantText {
		t.Errorf("remaining = %q, want %q", remaining, wantText)
	}
}

// An option given twice counts once, since votes are matched by the hash
// of its text; options that differ in case are two.
func TestExtractPollsDropsRepeatedOptions(t *testing.T) {
	polls, _ := ExtractPolls("[POLL max=4]\nQuestion: Which?\n- A\n- A\n- a\n- B\n[/POLL]")
	want := []Poll{{Question: "Which?", Options: []string{"A", "a", "B"}, MaxSelectable: 3}}
	if !reflect.DeepEqual(polls, want) {
		t.Errorf("polls = %+v, want %+v", polls, want)
	}
}

func TestExtractPollsNoPoll(t *testing.T) {
	polls, remaining := ExtractPolls("just a normal reply")
	if len(polls) != 0 {
		t.Errorf("got %d polls, want 0", len(polls))
	}
	if remaining != "just a normal reply" {
		t.Errorf("remaining = %q", remaining)
	}
}

// anyPollCloseRe finds the end of a poll block in any letter case, the way
// pollHeaderRe finds its start.
var anyPollCloseRe = regexp.MustCompile(`(?i)\[/POLL\]`)

// FuzzExtractPolls feeds ExtractPolls model output. Whatever the model
// writes, each poll that comes out has a question, 2 to 12 distinct
// trimmed options and a max within them, and text without a poll in it
// reaches the chat as it was (see checkPolls). A well-formed block built
// from the fuzzed question and options, with nothing around it that looks
// like one, comes back as exactly that poll, and the text around it as it
// was; a block that is no poll stays in the text.
func FuzzExtractPolls(f *testing.F) {
	seeds := []struct {
		before, question, options, after string
		maxSel                           uint8
	}{
		{"Sure, let's vote.\n\n", "When should we meet?", "Monday 2pm\nTuesday 3pm", "\n\nLet me know.", 0},
		{"", "Which?", "A\nB\nC", "", 2},
		{"", "Which?", "A\nB", "", 9},
		{"", "Pick", strings.Repeat("option\n", 20), "", 0},
		{"", "Only one?", "A", "", 0},
		{"", "", "A\nB", "", 0},
		{"[POLL]\nQuestion: unterminated\n- A\n- B", "Q", "a\nb", "", 0},
		{"[POLL max=3]\nQuestion: Q\n- a\n- b\n[/POLL] and ", "Q", "a\nb", " [POLL]", 1},
		{"[PO[POLL]", "Q", "a\nb", "LL]", 0},
		{"İİ ", "Fråga?", "é\n\U0001F600", " \xff", 3},
	}
	for _, s := range seeds {
		f.Add(s.before, s.question, s.options, s.after, s.maxSel)
	}
	f.Fuzz(func(t *testing.T, before, question, options, after string, maxSel uint8) {
		checkPolls(t, before)

		q := strings.TrimSpace(strings.ReplaceAll(question, "\n", " "))
		var opts []string
		for _, o := range strings.Split(options, "\n") {
			if o = strings.TrimSpace(o); o != "" {
				opts = append(opts, o)
			}
		}
		var block strings.Builder
		if maxSel > 0 {
			fmt.Fprintf(&block, "[POLL max=%d]\n", maxSel)
		} else {
			block.WriteString("[POLL]\n")
		}
		block.WriteString("Question: " + q + "\n")
		for _, o := range opts {
			block.WriteString("- " + o + "\n")
		}
		block.WriteString("[/POLL]")
		text := before + block.String() + after
		polls, remaining := checkPolls(t, text)

		// Another start or end in the text, the fuzzed parts' own included,
		// makes a different block.
		if len(pollHeaderRe.FindAllString(text, -1)) != 1 || len(anyPollCloseRe.FindAllString(text, -1)) != 1 {
			return
		}
		var distinct []string
		for _, o := range opts {
			if !slices.Contains(distinct, o) {
				distinct = append(distinct, o)
			}
		}
		if q == "" || len(distinct) < 2 {
			if len(polls) != 0 {
				t.Errorf("got %+v from a block without a question or two options", polls)
			}
			if want := strings.TrimSpace(text); remaining != want {
				t.Errorf("remaining text = %q, want the block left in: %q", remaining, want)
			}
			return
		}
		if want := strings.TrimSpace(before + after); remaining != want {
			t.Errorf("remaining text = %q, want %q", remaining, want)
		}
		want := Poll{Question: q, Options: distinct[:min(len(distinct), MaxPollOptions)], MaxSelectable: 1}
		if maxSel > 0 {
			want.MaxSelectable = int(maxSel)
		}
		want.MaxSelectable = min(want.MaxSelectable, len(want.Options))
		if len(polls) != 1 || !reflect.DeepEqual(polls[0], want) {
			t.Errorf("got %+v, want [%+v]", polls, want)
		}
	})
}

// checkPolls runs ExtractPolls on text and checks what holds for any text:
// each poll has a question and 2 to 12 distinct options, each a trimmed
// line of its own; there are no more polls than closed blocks; and the
// remaining text, which goes to the chat, is the input when no poll came
// out of it and shorter otherwise, and still valid UTF-8 if the input was.
func checkPolls(t *testing.T, text string) ([]Poll, string) {
	t.Helper()
	polls, remaining := ExtractPolls(text)
	line := func(s string) bool {
		return s != "" && s == strings.TrimSpace(s) && !strings.Contains(s, "\n")
	}
	for _, p := range polls {
		if !line(p.Question) {
			t.Errorf("question %q is not a trimmed line", p.Question)
		}
		if n := len(p.Options); n < 2 || n > MaxPollOptions {
			t.Errorf("poll %q has %d options, want 2 to %d", p.Question, n, MaxPollOptions)
		}
		for i, o := range p.Options {
			if !line(o) {
				t.Errorf("option %q is not a trimmed line", o)
			}
			if slices.Contains(p.Options[:i], o) {
				t.Errorf("poll %q has the option %q twice", p.Question, o)
			}
		}
		if p.MaxSelectable < 0 || p.MaxSelectable > len(p.Options) {
			t.Errorf("poll %q: max %d for %d options", p.Question, p.MaxSelectable, len(p.Options))
		}
		if utf8.ValidString(text) && !utf8.ValidString(p.Question+strings.Join(p.Options, "")) {
			t.Errorf("poll %+v is not valid UTF-8", p)
		}
	}
	if closed := len(anyPollCloseRe.FindAllString(text, -1)); len(polls) > closed {
		t.Errorf("%d polls from %d closed blocks", len(polls), closed)
	}
	// Nothing is dropped but the polls themselves.
	if len(polls) == 0 && remaining != strings.TrimSpace(text) {
		t.Errorf("text without a poll came back as %q", remaining)
	}
	if len(polls) > 0 && len(remaining) >= len(text) {
		t.Errorf("remaining text %q is no shorter than the input, though %d polls left it", remaining, len(polls))
	}
	if utf8.ValidString(text) && !utf8.ValidString(remaining) {
		t.Errorf("remaining text %q is not valid UTF-8", remaining)
	}
	return polls, remaining
}
