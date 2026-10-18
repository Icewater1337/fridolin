package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Polls a brain sends. A reply marks one as a block:
//
//	[POLL max=2]
//	Question: When should we meet?
//	- Monday 2pm
//	- Tuesday 3pm
//	[/POLL]
//
// The bridge sends each block as a real WhatsApp poll (Outbox.Reply), keeps
// the poll in bridge.db (Store.SavePoll) and matches the votes WhatsApp
// reports back to its options (matchVote).

// pollData represents a parsed [POLL]…[/POLL] block.
type pollData struct {
	Question      string
	Options       []string
	MaxSelectable int // 1 = single choice, 0 = unlimited
}

// maxPollOptions is WhatsApp's own limit on options per poll.
const maxPollOptions = 12

// pollHeaderRe matches [POLL] or [POLL max=N], and pollCloseRe the [/POLL]
// that ends the block, both in any letter case.
var (
	pollHeaderRe = regexp.MustCompile(`(?i)\[POLL(?:\s+max=(\d+))?\]`)
	pollCloseRe  = regexp.MustCompile(`(?i)\[/POLL\]`)
)

// extractPolls finds the [POLL]…[/POLL] blocks in text and returns the
// polls they hold and the text without them. It reads the text from the
// start: a block ends at the first [/POLL] after its header, and a header
// that another header follows before that is plain text, so a block the
// model left open does not swallow the next one and the words between
// them. A block that is no poll (no question, or fewer than two options)
// stays in the text as written: nothing the brain said goes missing.
func extractPolls(text string) ([]pollData, string) {
	var polls []pollData
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
func parsePollBlock(block string, maxSelectable int) (pollData, bool) {
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
		return pollData{}, false
	}
	if len(options) > maxPollOptions {
		log.Printf("poll %q had %d options; truncating to WhatsApp's limit of %d", question, len(options), maxPollOptions)
		options = options[:maxPollOptions]
	}
	// WhatsApp rejects a poll whose max exceeds the number of options.
	maxSelectable = min(maxSelectable, len(options))
	return pollData{Question: question, Options: options, MaxSelectable: maxSelectable}, true
}

// matchVote returns the options a vote chose. WhatsApp sends the SHA-256
// hashes of the chosen options' text, not the text; a hash that matches
// none is dropped.
func matchVote(hashes [][]byte, options []string) []string {
	var selected []string
	for _, hash := range hashes {
		for _, opt := range options {
			h := sha256.Sum256([]byte(opt))
			if bytes.Equal(hash, h[:]) {
				selected = append(selected, opt)
				break
			}
		}
	}
	return selected
}

// pollVote is a vote the bridge recorded in one of its polls.
type pollVote struct {
	Poll     store.SentPoll
	Voter    types.JID // as canonicalUser has them
	Selected []string  // empty: the vote was taken back
}

// recordVote writes a vote into the history of the brain that sent the
// poll, as an event: the brain's next run in that chat sees who voted
// for what, without anyone asking it to look. Polls from before the
// bridge kept their brain go to the brain that owns their chat.
func (b *Bridge) recordVote(v pollVote) {
	br := b.brainMgr.Get(v.Poll.Brain)
	if br == nil {
		br = b.brainMgr.BrainFor(v.Poll.Chat, IsGroupJID(v.Poll.Chat))
	}
	if br == nil {
		log.Printf("poll vote in %s: no brain to tell", v.Poll.Chat)
		return
	}
	br.appendHistory(v.Poll.Chat, v.Voter.User, voteText(v.Poll.Question, v.Selected), history.Event)
}

// voteText is how a vote reads in a brain's history, after the voter's
// name: `voted "Monday 2pm" in the poll "When should we meet?"`.
func voteText(question string, selected []string) string {
	if len(selected) == 0 {
		return fmt.Sprintf("took back their vote in the poll %q", question)
	}
	quoted := make([]string, len(selected))
	for i, s := range selected {
		quoted[i] = strconv.Quote(s)
	}
	return fmt.Sprintf("voted %s in the poll %q", strings.Join(quoted, ", "), question)
}
