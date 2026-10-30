package outbound

import (
	"fmt"
	"strings"
)

// FrideNews is FrIDE's news about a thread (its webhook, relayed by the
// node), as FrideNewsMessage says it in the chat that started the work.
type FrideNews struct {
	// Thread is fridolin's number of the thread, which !move and the core
	// brain's tools take; Node is the name of the node FrIDE runs on.
	Thread int64
	Node   string
	// Kind is started, update, question, done or failed.
	Kind string
	// Text is FrIDE's own summary, ready for a person.
	Text                    string
	Task, Branch, Rationale string
}

// frideBody is FrIDE's summary without its first line when that only
// names the inbox thread ("FrIDE inbox #7 · needs your reply"): this chat
// knows the thread by fridolin's number.
func frideBody(text string) string {
	text = strings.TrimSpace(text)
	first, rest, _ := strings.Cut(text, "\n")
	if strings.HasPrefix(first, "FrIDE inbox #") {
		return strings.TrimSpace(rest)
	}
	return text
}

// How much of FrIDE's text a chat gets; the dashboard's FrIDE page keeps
// every text whole. A question is the owner's to answer, so it comes
// whole: only a runaway text reaches its bound. A failure keeps its
// reason. Other news is a few lines.
const (
	frideMaxLines         = 5
	frideMaxRunes         = 600
	frideMaxFailedRunes   = 1500
	frideMaxQuestionRunes = 4000
)

// FrideMore points from a shortened FrIDE text to where it is whole.
const FrideMore = "… more in the dashboard (FrIDE page)"

// frideShort is news cut to frideMaxLines lines and frideMaxRunes
// characters, with FrideMore when it was cut.
func frideShort(text string) string { return frideCut(text, frideMaxLines, frideMaxRunes) }

// frideFor is FrIDE's text for news of kind: a question nearly whole, a
// failure at length, other news short.
func frideFor(kind, text string) string {
	switch kind {
	case "question":
		return frideCut(text, 0, frideMaxQuestionRunes)
	case "failed":
		return frideCut(text, 0, frideMaxFailedRunes)
	}
	return frideShort(text)
}

// frideCut is text without empty lines, cut to maxLines lines (0: any)
// and maxRunes characters, at the end of a sentence or word, never inside
// one, with FrideMore when it was cut.
func frideCut(text string, maxLines, maxRunes int) string {
	var lines []string
	cut := false
	for l := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if maxLines > 0 && len(lines) == maxLines {
			cut = true
			break
		}
		lines = append(lines, strings.TrimRight(l, " \t"))
	}
	out := strings.Join(lines, "\n")
	if r := []rune(out); len(r) > maxRunes {
		out, cut = cutAtBoundary(string(r[:maxRunes])), true
	}
	if cut {
		out += "\n" + FrideMore
	}
	return out
}

// cutAtBoundary is s up to its last sentence end (., ?, ! or a line break)
// in its second half, else up to its last space, else s: the reader gets
// whole words.
func cutAtBoundary(s string) string {
	half := len(s) / 2
	if i := strings.LastIndexAny(s, ".?!\n"); i >= half {
		return strings.TrimSpace(s[:i+1])
	}
	if i := strings.LastIndexAny(s, " \t"); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// FrideBookkeeping reports whether text is only FrIDE's bookkeeping: the
// task it made ("Created task …") or the brief it passed to an agent
// ("• → agent: …"), which the chat that wrote the brief knows.
func FrideBookkeeping(text string) bool {
	body := frideBody(text)
	if body == "" {
		return true
	}
	for l := range strings.SplitSeq(body, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
		case strings.HasPrefix(l, "Created task "), strings.HasPrefix(l, "• →"), strings.HasPrefix(l, "→ "):
		default:
			return false
		}
	}
	return true
}

// frideAskPhrases mark a question that has no question mark.
var frideAskPhrases = []string{"should i", "shall i", "do you want", "would you like", "please confirm", "please decide", "please choose", "which one", "let me know", "your call", "need your", "needs your decision"}

// FrideAsks reports whether text asks something. FrIDE marks progress as
// needing a reply too (fride#16): a text with no question mark and none
// of frideAskPhrases is news, not a question.
func FrideAsks(text string) bool {
	body := strings.ToLower(frideBody(text))
	if strings.Contains(body, "?") {
		return true
	}
	for _, p := range frideAskPhrases {
		if strings.Contains(body, p) {
			return true
		}
	}
	return false
}

// FrideMerged reports whether text says work was merged: news a quiet
// chat gets, whatever FrIDE calls it.
func FrideMerged(text string) bool {
	return strings.Contains(strings.ToLower(frideBody(text)), "merged")
}

// FrideStalled reports whether text is FrIDE's notice that an agent has
// not moved for a while ("agent … has not moved for 2h"): progress, even
// when FrIDE marks it as needing a reply (#306).
func FrideStalled(text string) bool {
	body := strings.ToLower(frideBody(text))
	return strings.Contains(body, "has not moved") || strings.Contains(body, "hasn't moved")
}

// FrideStallKey is a stall notice as two compare: the same but for its
// numbers, which count the hours.
func FrideStallKey(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, frideBody(text))
}

// FrideBodyKey is text as two pieces of news compare: the same text sent
// for several threads is one piece of news.
func FrideBodyKey(text string) string { return frideBody(text) }

// FrideAgentNews is FrIDE's news about work a scheduled agent handed it,
// as a run of that agent reads it: the thread, what it was asked to do
// (brief, shortened), and FrIDE's text whole.
func FrideAgentNews(n FrideNews, brief string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FrIDE #%d on %s: %s", n.Thread, n.Node, n.Kind)
	if n.Task != "" {
		fmt.Fprintf(&b, " (task %s", n.Task)
		if n.Branch != "" {
			fmt.Fprintf(&b, ", branch %s", n.Branch)
		}
		b.WriteString(")")
	}
	if n.Kind == "question" {
		fmt.Fprintf(&b, ". Answer it with fride_reply, thread %d", n.Thread)
	}
	b.WriteString(".\n")
	if brief = strings.TrimSpace(brief); brief != "" {
		r := []rune(brief)
		if len(r) > 400 {
			brief = string(r[:400]) + "…"
		}
		b.WriteString("The work you handed it: " + brief + "\n")
	}
	b.WriteString("FrIDE says:\n" + frideBody(n.Text))
	return b.String()
}
