package outbound

import (
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Poll represents a parsed [POLL]…[/POLL] block.
type Poll struct {
	Question      string
	Options       []string
	MaxSelectable int // 1 = single choice, 0 = unlimited
}

// MaxPollOptions is WhatsApp's own limit on options per poll.
const MaxPollOptions = 12

// pollHeaderRe matches [POLL] or [POLL max=N], and pollCloseRe the [/POLL]
// that ends the block, both in any letter case.
var (
	pollHeaderRe = regexp.MustCompile(`(?i)\[POLL(?:\s+max=(\d+))?\]`)
	pollCloseRe  = regexp.MustCompile(`(?i)\[/POLL\]`)
)

// ExtractPolls finds the [POLL]…[/POLL] blocks in text and returns the
// polls they hold and the text without them. It reads the text from the
// start: a block ends at the first [/POLL] after its header, and a header
// that another header follows before that is plain text, so a block the
// model left open does not swallow the next one and the words between
// them. A block that is no poll (no question, or fewer than two options)
// stays in the text as written: nothing the brain said goes missing.
func ExtractPolls(text string) ([]Poll, string) {
	var polls []Poll
	var out strings.Builder
	rest := text
	for {
		h := pollHeaderRe.FindStringSubmatchIndex(rest)
		if h == nil {
			break
		}
		body := rest[h[1]:]
		c := pollCloseRe.FindStringIndex(body)
		if c == nil {
			break // unclosed: the rest is text
		}
		if next := pollHeaderRe.FindStringIndex(body[:c[0]]); next != nil {
			out.WriteString(rest[:h[1]+next[0]])
			rest = rest[h[1]+next[0]:]
			continue
		}
		maxSelectable := 1
		if h[2] >= 0 {
			if n, err := strconv.Atoi(rest[h[2]:h[3]]); err == nil {
				maxSelectable = n
			}
		}
		if p, ok := parsePollBlock(body[:c[0]], maxSelectable); ok {
			out.WriteString(rest[:h[0]])
			polls = append(polls, p)
		} else {
			out.WriteString(rest[:h[1]+c[1]])
		}
		rest = body[c[1]:]
	}
	out.WriteString(rest)
	return polls, strings.TrimSpace(out.String())
}

// parsePollBlock reads the lines between a poll's header and its end. An
// option given twice counts once: WhatsApp reports a vote as the hash of
// the option's text, so two equal options could not be told apart, and
// options differing only in case are kept, since they hash differently.
func parsePollBlock(block string, maxSelectable int) (Poll, bool) {
	var question string
	var options []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(strings.ToLower(line), "question:"):
			question = strings.TrimSpace(line[len("question:"):])
		case strings.HasPrefix(line, "- "):
			if opt := strings.TrimSpace(line[2:]); opt != "" && !slices.Contains(options, opt) {
				options = append(options, opt)
			}
		}
	}
	if question == "" || len(options) < 2 {
		return Poll{}, false
	}
	if len(options) > MaxPollOptions {
		log.Printf("poll %q had %d options; truncating to WhatsApp's limit of %d", question, len(options), MaxPollOptions)
		options = options[:MaxPollOptions]
	}
	// WhatsApp rejects a poll whose max exceeds the number of options.
	maxSelectable = min(maxSelectable, len(options))
	return Poll{Question: question, Options: options, MaxSelectable: maxSelectable}, true
}
