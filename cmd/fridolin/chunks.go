package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// defaultChunkSize is the longest outgoing message when max_chunk_size is
// unset. minChunkSize is the smallest limit ChunkMessage works with: below
// it the "[i/N] " numbering would leave too little room for the text.
const (
	defaultChunkSize = 3900
	minChunkSize     = 100
)

// chunkSeparators are the places a long reply may be cut, coarsest first:
// paragraphs, lines, sentence ends, words. A finer level is only used for a
// segment that is still too long after the coarser ones, so one long line
// does not break up the rest of the reply.
var chunkSeparators = [][]string{
	{"\n\n"},
	{"\n"},
	{". ", "! ", "? "},
	{" "},
}

// ChunkMessage splits text into messages of at most maxSize characters
// (runes, not bytes), the "[i/N] " numbering included. Text that fits is
// returned as is. Longer text is cut at the coarsest separator that makes
// each piece fit, and only whitespace is dropped: a split reply is trimmed
// at both ends, and no chunk starts or ends with whitespace, indentation
// included. The chunks hold every other character of text, in order.
// maxSize 0 or less means the default; a limit below minChunkSize is
// raised to it.
// Blank text gives no chunks, and invalid UTF-8 is replaced so every chunk
// is valid.
func ChunkMessage(text string, maxSize int) []string {
	if maxSize <= 0 {
		maxSize = defaultChunkSize
	}
	maxSize = max(maxSize, minChunkSize)
	text = strings.ToValidUTF8(text, "\uFFFD")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if utf8.RuneCountInString(text) <= maxSize {
		return []string{text}
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= maxSize {
		return []string{text}
	}

	// Room for the prefix is reserved before splitting, so no chunk has to
	// be cut afterwards. Its width depends on the number of chunks: start
	// with one digit and split again with a wider reservation until the
	// count fits it. The width only grows, so this ends after a few rounds.
	var bodies []string
	for digits := 1; ; {
		budget := maxSize - prefixWidth(digits)
		bodies = packChunks(splitPieces(text, 0, budget), budget)
		n := len(strconv.Itoa(len(bodies)))
		if n <= digits {
			break
		}
		digits = n
	}

	out := make([]string, len(bodies))
	for i, b := range bodies {
		out[i] = fmt.Sprintf("[%d/%d] %s", i+1, len(bodies), b)
	}
	return out
}

// prefixWidth is the length of the widest "[i/N] " prefix when N has the
// given number of digits.
func prefixWidth(digits int) int {
	return len("[/] ") + 2*digits
}

// splitPieces cuts text into pieces that join back to exactly text and
// each fit in budget runes once the whitespace at their ends is dropped, the
// same measure packChunks uses. A piece that fits is kept whole; only one
// that is still too long is cut again at the next, finer separator, and a
// run of text with no separator at all is cut between runes.
func splitPieces(text string, level, budget int) []string {
	if utf8.RuneCountInString(strings.TrimSpace(text)) <= budget {
		return []string{text}
	}
	if level == len(chunkSeparators) {
		return hardSplit(text, budget)
	}
	var pieces []string
	for _, part := range splitAfterAny(text, chunkSeparators[level]) {
		pieces = append(pieces, splitPieces(part, level+1, budget)...)
	}
	return pieces
}

// splitAfterAny cuts s after every occurrence of any of seps. Each separator
// stays at the end of the part before it, so the parts join back to s.
func splitAfterAny(s string, seps []string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		for _, sep := range seps {
			if strings.HasPrefix(s[i:], sep) {
				parts = append(parts, s[start:i+len(sep)])
				start = i + len(sep)
				i = start - 1
				break
			}
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

// hardSplit cuts text into pieces of at most budget runes, for a word too
// long to fit in any chunk. It cuts between runes, never inside one, and
// where it can between whole characters too (see wholeCharacterCut).
func hardSplit(text string, budget int) []string {
	var pieces []string
	for {
		cut, more := runeOffset(text, budget)
		if !more {
			return append(pieces, text)
		}
		cut = wholeCharacterCut(text, cut)
		pieces = append(pieces, text[:cut])
		text = text[cut:]
	}
}

// maxCutBackoff bounds how many runes wholeCharacterCut steps back. A longer
// run of combining marks is cut where it is.
const maxCutBackoff = 16

// wholeCharacterCut moves a cut at byte offset cut back to the start of the
// character it falls in, so that an accent, a skin tone or the rest of a
// joined emoji is not sent as a message of its own.
func wholeCharacterCut(text string, cut int) int {
	c := cut
	for range maxCutBackoff {
		if !joinsPrevious(text[:c], text[c:]) {
			return c
		}
		_, w := utf8.DecodeLastRuneInString(text[:c])
		if c -= w; c == 0 {
			break
		}
	}
	return cut
}

// joinsPrevious reports whether the first rune of after belongs to the same
// visible character as the last rune of before.
func joinsPrevious(before, after string) bool {
	const zwj = '\u200d' // joins emoji such as a family into one
	prev, _ := utf8.DecodeLastRuneInString(before)
	next, _ := utf8.DecodeRuneInString(after)
	switch {
	case prev == zwj || next == zwj:
		return true
	case unicode.In(next, unicode.Mn, unicode.Me, unicode.Mc):
		// Marks: accents, vowel signs, the keycap. Every variation
		// selector, such as the one asking for an emoji, is an Mn mark.
		return true
	case next >= 0x1F3FB && next <= 0x1F3FF: // skin tones
		return true
	case next >= 0xE0020 && next <= 0xE007F: // tags, as in the flag of Scotland
		return true
	case isRegionalIndicator(prev) && isRegionalIndicator(next):
		// A flag is a pair of indicators: the cut splits one when an odd
		// number of them come before it.
		n := 0
		for s := before; ; n++ {
			r, w := utf8.DecodeLastRuneInString(s)
			if !isRegionalIndicator(r) {
				break
			}
			s = s[:len(s)-w]
		}
		return n%2 == 1
	}
	return false
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// runeOffset returns the byte offset just after the first n runes of s, and
// whether anything follows it.
func runeOffset(s string, n int) (int, bool) {
	i := 0
	for ; n > 0 && i < len(s); n-- {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return i, i < len(s)
}

// packChunks joins consecutive pieces greedily into chunks of at most budget
// runes. Each chunk starts and ends at a cut, where whitespace is dropped;
// whitespace a chunk would end with does not count against the budget.
// Every piece must fit within budget on its own.
func packChunks(pieces []string, budget int) []string {
	var chunks []string
	var cur strings.Builder
	curLen := 0
	for _, p := range pieces {
		core := strings.TrimRightFunc(p, unicode.IsSpace)
		if cur.Len() > 0 && curLen+utf8.RuneCountInString(core) > budget {
			chunks = append(chunks, strings.TrimRightFunc(cur.String(), unicode.IsSpace))
			cur.Reset()
			curLen = 0
		}
		if cur.Len() == 0 {
			p = strings.TrimLeftFunc(p, unicode.IsSpace)
		}
		cur.WriteString(p)
		curLen += utf8.RuneCountInString(p)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, strings.TrimRightFunc(cur.String(), unicode.IsSpace))
	}
	return chunks
}
