package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TriggerMatch says where in a group message a trigger keyword must appear.
type TriggerMatch string

const (
	// MatchAnywhere fires on the keyword anywhere in the message. Good for
	// distinctive words ("@claude", a bot's name).
	MatchAnywhere TriggerMatch = "anywhere"
	// MatchStart fires only when the message begins with the keyword. Use it
	// for short shorthands: "@c" at the start addresses the bot, "@carla" does not.
	MatchStart TriggerMatch = "start"
	// MatchWord fires on the keyword as a whole word anywhere in the message.
	MatchWord TriggerMatch = "word"
)

// validTriggerMatches are the values a trigger's match takes; one left
// out is MatchAnywhere.
var validTriggerMatches = map[TriggerMatch]bool{MatchAnywhere: true, MatchStart: true, MatchWord: true}

// Trigger is one keyword that makes the bridge answer in a group.
type Trigger struct {
	Text  string       `json:"text"`
	Match TriggerMatch `json:"match,omitempty"`
}

// TriggerList is the configured triggers. In JSON each entry is either a plain
// string (matched anywhere) or {"text": "…", "match": "start"|"word"|"anywhere"}.
type TriggerList []Trigger

// UnmarshalJSON accepts both the string and the object form for each entry.
func (tl *TriggerList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("triggers must be a list: %w", err)
	}
	out := make(TriggerList, 0, len(raw))
	for _, r := range raw {
		var s string
		if err := json.Unmarshal(r, &s); err == nil {
			out = append(out, Trigger{Text: s, Match: MatchAnywhere})
			continue
		}
		var t Trigger
		if err := json.Unmarshal(r, &t); err != nil {
			return fmt.Errorf("trigger %s: want a string or {\"text\",\"match\"}", string(r))
		}
		if t.Match == "" {
			t.Match = MatchAnywhere
		}
		if !validTriggerMatches[t.Match] {
			return fmt.Errorf("trigger %q: match must be anywhere, start or word, not %q", t.Text, t.Match)
		}
		out = append(out, t)
	}
	*tl = out
	return nil
}

// MarshalJSON writes "anywhere" triggers as plain strings, the others as objects.
func (tl TriggerList) MarshalJSON() ([]byte, error) {
	out := make([]any, 0, len(tl))
	for _, t := range tl {
		if t.Match == "" || t.Match == MatchAnywhere {
			out = append(out, t.Text)
		} else {
			out = append(out, t)
		}
	}
	return json.Marshal(out)
}

// TriggersFromStrings builds a list of match-anywhere triggers.
func TriggersFromStrings(ss []string) TriggerList {
	out := make(TriggerList, 0, len(ss))
	for _, s := range ss {
		out = append(out, Trigger{Text: s, Match: MatchAnywhere})
	}
	return out
}

// First returns the first trigger's text, for help messages.
func (tl TriggerList) First() string {
	for _, t := range tl {
		if t.Text != "" {
			return t.Text
		}
	}
	return ""
}

// Matches reports whether text addresses the bridge through any trigger.
func (tl TriggerList) Matches(text string) bool {
	for _, t := range tl {
		if _, _, ok := t.find(text); ok {
			return true
		}
	}
	return false
}

// Strip removes trigger keywords from text and trims the result. A start
// trigger is removed only from the beginning; the others everywhere they match.
func (tl TriggerList) Strip(text string) string {
	for _, t := range tl {
		if t.Text == "" {
			continue
		}
		if t.Match == MatchStart {
			if i, j, ok := t.find(text); ok {
				text = text[:i] + text[j:]
			}
			continue
		}
		// Bounded: each pass removes one occurrence, and there are at most
		// len(text) of them.
		for n := len(text); n >= 0; n-- {
			i, j, ok := t.find(text)
			if !ok {
				break
			}
			text = text[:i] + text[j:]
		}
	}
	return strings.TrimSpace(text)
}

// find locates the trigger in text and returns its byte span in text itself
// (not in a lowercased copy, whose byte offsets can differ).
func (t Trigger) find(text string) (int, int, bool) {
	if t.Text == "" || text == "" {
		return 0, 0, false
	}
	switch t.Match {
	case MatchStart:
		lead := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
		end, ok := hasFoldPrefix(text[lead:], t.Text)
		if !ok {
			return 0, 0, false
		}
		end += lead
		if needsBoundary(t.Text) && !boundaryAt(text, end) {
			return 0, 0, false
		}
		return lead, end, true
	default:
		for i := 0; i < len(text); {
			if end, ok := hasFoldPrefix(text[i:], t.Text); ok {
				end += i
				if t.Match != MatchWord || ((i == 0 || boundaryBefore(text, i)) && boundaryAt(text, end)) {
					return i, end, true
				}
			}
			_, size := utf8.DecodeRuneInString(text[i:])
			i += size
		}
		return 0, 0, false
	}
}

// hasFoldPrefix reports whether s starts with prefix under Unicode case folding
// and returns how many bytes of s the prefix covered.
func hasFoldPrefix(s, prefix string) (int, bool) {
	i := 0
	for _, pr := range prefix {
		if i >= len(s) {
			return 0, false
		}
		sr, size := utf8.DecodeRuneInString(s[i:])
		if !strings.EqualFold(string(sr), string(pr)) {
			return 0, false
		}
		i += size
	}
	return i, true
}

// needsBoundary: a keyword ending in a letter or digit must not run into the
// next word ("@c" must not match "@carla"); one ending in a symbol ("©") may.
func needsBoundary(word string) bool {
	r, _ := utf8.DecodeLastRuneInString(word)
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func boundaryAt(text string, i int) bool {
	if i >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[i:])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func boundaryBefore(text string, i int) bool {
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}
