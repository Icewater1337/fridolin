package main

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestChunkMessage(t *testing.T) {
	// At limit 100 a chunk of fewer than ten has 94 characters for text.
	para := strings.Repeat("a", 46) + "." // 47 characters
	url := "https://example.com/" + strings.Repeat("x", 130)
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }

	// Two paragraphs of 60 and 55 characters, each of two lines: either fits
	// a chunk, the first line of the second would fit after the first.
	report := "Backup report for last night:\nevery brain folder was copied."
	calendar := "Calendar sync:\nit still waits a few seconds for Google."
	// One paragraph of four lines; two lines fit a chunk, three do not.
	list := []string{
		"Pack the charger and the spare battery",
		"Print the tickets for the train ride",
		"Water the plants on the balcony",
		"Leave the key with the neighbours",
	}
	// One line of three sentences of about 60 characters each.
	sentences := []string{
		"Did the nightly backup finish before the disk was unplugged?",
		"It did, and the copy on the disk matches the brain folder!",
		"So we can wipe the old laptop and give it back on Monday.",
	}
	// A paragraph of 93 characters: with its "\n\n" it is 95, but the
	// whitespace at a cut does not count, so it still fits.
	snug := "The backup ran at night and copied every brain folder to the\nexternal disk without one error."

	cases := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{"short text is sent as is", "  hello\n", 100, []string{"  hello\n"}},
		{"empty text gives no chunks", "", 100, nil},
		{"blank text gives no chunks", " \n\t ", 100, nil},
		{"text that fits once trimmed is not numbered",
			strings.Repeat("a", 100) + "\n\n\n", 100,
			[]string{strings.Repeat("a", 100)}},
		{"invalid UTF-8 is replaced", "ab\xffcd", 100, []string{"ab\uFFFDcd"}},
		{"zero means the default limit", strings.Repeat("a", 3900), 0, []string{strings.Repeat("a", 3900)}},
		{"numbering does not cut off the end of a chunk",
			para + "\n\n" + para + "\n\n" + para, 100,
			[]string{"[1/3] " + para, "[2/3] " + para, "[3/3] " + para}},
		{"emoji count as one character each",
			strings.Repeat("😀", 150), 100,
			[]string{"[1/2] " + strings.Repeat("😀", 94), "[2/2] " + strings.Repeat("😀", 56)}},
		{"a word with no separators is cut between runes",
			strings.Repeat("ж", 250), 100,
			[]string{"[1/3] " + strings.Repeat("ж", 94), "[2/3] " + strings.Repeat("ж", 94), "[3/3] " + strings.Repeat("ж", 62)}},
		{"one long line is split on its own",
			"Intro paragraph.\n\n" + url + "\n\nShort one. Short two.", 100,
			[]string{"[1/3] Intro paragraph.", "[2/3] " + url[:94], "[3/3] " + url[94:] + "\n\nShort one. Short two."}},
		{"paragraphs that fit are not cut into lines",
			report + "\n\n" + calendar, 100,
			[]string{"[1/2] " + report, "[2/2] " + calendar}},
		{"a long paragraph is cut at line ends, not between words",
			strings.Join(list, "\n"), 100,
			[]string{"[1/2] " + list[0] + "\n" + list[1], "[2/2] " + list[2] + "\n" + list[3]}},
		{"a long line is cut at sentence ends of every kind",
			strings.Join(sentences, " "), 100,
			[]string{"[1/3] " + sentences[0], "[2/3] " + sentences[1], "[3/3] " + sentences[2]}},
		{"a paragraph that fits only without its separator is kept whole",
			"Short intro.\n\n" + snug + "\n\nClosing words.", 100,
			[]string{"[1/3] Short intro.", "[2/3] " + snug, "[3/3] Closing words."}},
		{"a space a chunk would end with does not count",
			words(50), 100,
			[]string{"[1/3] " + words(19), "[2/3] " + words(19), "[3/3] " + words(12)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChunkMessage(tc.text, tc.limit)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ChunkMessage(%q, %d)\n got %q\nwant %q", tc.text, tc.limit, got, tc.want)
			}
			checkChunks(t, tc.text, tc.limit, got)
		})
	}
}

func TestChunkMessageKeepsCharactersWhole(t *testing.T) {
	// A word longer than a chunk is cut between runes. Where a character
	// is made of several runes, the cut moves back to its start, so no
	// message begins with a stray accent or half an emoji. A limit of 101
	// leaves 95 runes per body, which lands inside a character of two,
	// three or seven runes.
	accent := "e\u0301"            // e + combining acute accent
	vowel := "\u0915\u093e"        // Devanagari KA + the spacing vowel sign AA
	thumb := "👍\U0001F3FD"         // thumbs up + skin tone
	keycap := "1\ufe0f\u20e3"      // digit + emoji variation selector + enclosing keycap
	flag := "\U0001F1E9\U0001F1EA" // two regional indicators
	// A black flag + the tags "gbsct" + a cancel tag: the flag of Scotland.
	scotland := "\U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F"
	family := "👨\u200d👩\u200d👧" // five runes joined by zero-width joiners

	cases := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{"accents stay on their letter", strings.Repeat(accent, 100), 101, []string{
			"[1/3] " + strings.Repeat(accent, 47), "[2/3] " + strings.Repeat(accent, 47), "[3/3] " + strings.Repeat(accent, 6)}},
		{"vowel signs stay on their consonant", strings.Repeat(vowel, 100), 101, []string{
			"[1/3] " + strings.Repeat(vowel, 47), "[2/3] " + strings.Repeat(vowel, 47), "[3/3] " + strings.Repeat(vowel, 6)}},
		{"skin tones stay on their emoji", strings.Repeat(thumb, 100), 101, []string{
			"[1/3] " + strings.Repeat(thumb, 47), "[2/3] " + strings.Repeat(thumb, 47), "[3/3] " + strings.Repeat(thumb, 6)}},
		// 95 runes end after the variation selector of the 32nd keycap.
		{"keycaps stay whole", strings.Repeat(keycap, 70), 101, []string{
			"[1/3] " + strings.Repeat(keycap, 31), "[2/3] " + strings.Repeat(keycap, 31), "[3/3] " + strings.Repeat(keycap, 8)}},
		{"flags stay in pairs", strings.Repeat(flag, 100), 101, []string{
			"[1/3] " + strings.Repeat(flag, 47), "[2/3] " + strings.Repeat(flag, 47), "[3/3] " + strings.Repeat(flag, 6)}},
		// 95 runes end in the middle of the tags of the 14th flag.
		{"tag flags stay whole", strings.Repeat(scotland, 30), 101, []string{
			"[1/3] " + strings.Repeat(scotland, 13), "[2/3] " + strings.Repeat(scotland, 13), "[3/3] " + strings.Repeat(scotland, 4)}},
		{"joined emoji stay joined", strings.Repeat(family, 40), 100, []string{
			"[1/3] " + strings.Repeat(family, 18), "[2/3] " + strings.Repeat(family, 18), "[3/3] " + strings.Repeat(family, 4)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChunkMessage(tc.text, tc.limit)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ChunkMessage(%q, %d)\n got %q\nwant %q", tc.text, tc.limit, got, tc.want)
			}
			checkChunks(t, tc.text, tc.limit, got)
		})
	}

	t.Run("a run of marks too long to keep whole is cut anyway", func(t *testing.T) {
		text := "a" + strings.Repeat("\u0301", 300)
		got := ChunkMessage(text, 100)
		if len(got) != 4 {
			t.Errorf("got %d chunks, want 4", len(got))
		}
		checkChunks(t, text, 100, got)
	})
}

func TestChunkMessageWidensNumberingForTenOrMoreChunks(t *testing.T) {
	// Twelve chunks need "[10/12] ", two characters more than the first
	// round reserved; the bodies must shrink to make room, not get cut.
	text := strings.Repeat("word ", 200)
	got := ChunkMessage(text, 100)
	if len(got) != 12 || !strings.HasPrefix(got[11], "[12/12] ") {
		t.Fatalf("got %d chunks, last %q", len(got), got[len(got)-1])
	}
	checkChunks(t, text, 100, got)
}

func TestChunkMessageRaisesTinyLimits(t *testing.T) {
	// A limit smaller than the numbering used to panic on a negative slice
	// bound, crashing the bridge from the brain worker.
	text := strings.Repeat("abc ", 100)
	for _, limit := range []int{1, 5, 6, 99} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			got := ChunkMessage(text, limit)
			if len(got) < 2 {
				t.Fatalf("expected the text to be split, got %q", got)
			}
			checkChunks(t, text, limit, got)
		})
	}
}

func TestChunkMessageProperties(t *testing.T) {
	// Random replies built from awkward tokens: multi-byte and combined
	// characters, runs of separators, words longer than a chunk.
	tokens := []string{
		"word", "Wort", "слово", "日本語", "\u00e9", "e\u0301", "😀", "👍\U0001F3FD", "👨\u200d👩\u200d👧", "\U0001F1E9\U0001F1EA",
		" ", "  ", "\t", "\u00a0", "\n", "\r\n", "\n\n", "\n\n\n", ". ", "! ", "? ", ".",
		"https://example.com/" + strings.Repeat("x", 120), strings.Repeat("y", 300),
	}
	rng := rand.New(rand.NewPCG(1, 2))
	limits := []int{0, 1, 99, 100, 101, 150, 500, 3900}
	for i := range 400 {
		var b strings.Builder
		for range rng.IntN(3000) {
			b.WriteString(tokens[rng.IntN(len(tokens))])
		}
		limit := limits[i%len(limits)]
		if i%3 == 0 {
			limit = 100 + rng.IntN(500)
		}
		text := b.String()
		checkChunks(t, text, limit, ChunkMessage(text, limit))
		if t.Failed() {
			t.Fatalf("failed for limit %d and text %q", limit, text)
		}
	}
}

// checkChunks asserts what every result of ChunkMessage must satisfy: each
// chunk is valid UTF-8, within the limit and not blank; several chunks are
// numbered "[i/N] " in order; the chunks, without their numbering, are the
// input with only whitespace dropped between them; and a paragraph short
// enough for a chunk of its own is never cut.
func checkChunks(t *testing.T, text string, maxSize int, chunks []string) {
	t.Helper()
	limit := maxSize
	if limit <= 0 {
		limit = defaultChunkSize
	}
	limit = max(limit, minChunkSize)
	input := strings.ToValidUTF8(text, "\uFFFD")
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		if len(chunks) != 0 {
			t.Errorf("blank input gave %d chunks", len(chunks))
		}
		return
	}
	if fits := utf8.RuneCountInString(trimmed) <= limit; fits != (len(chunks) == 1) {
		t.Errorf("input of %d characters at limit %d gave %d chunks", utf8.RuneCountInString(trimmed), limit, len(chunks))
	}
	if len(chunks) == 1 && chunks[0] != input && chunks[0] != trimmed {
		t.Errorf("a single chunk must be the input, got %q", chunks[0])
	}

	bodies := make([]string, len(chunks))
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8: %q", i, c)
		}
		if n := utf8.RuneCountInString(c); n > limit {
			t.Errorf("chunk %d has %d characters, over the limit of %d", i, n, limit)
		}
		body := c
		if len(chunks) > 1 {
			prefix := fmt.Sprintf("[%d/%d] ", i+1, len(chunks))
			if !strings.HasPrefix(c, prefix) {
				t.Errorf("chunk %d does not start with %q: %q", i, prefix, c)
				return
			}
			body = c[len(prefix):]
			if body == "" || body != strings.TrimSpace(body) {
				t.Errorf("chunk %d is blank or keeps whitespace from a cut: %q", i, body)
			}
		}
		bodies[i] = strings.TrimSpace(body)
	}

	// Where each body lies in the input, as byte offsets.
	spans := make([][2]int, len(bodies))
	rest := input
	for i, b := range bodies {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if !strings.HasPrefix(rest, b) {
			t.Errorf("chunk %d is not the next part of the input:\nchunk %q\ninput %q", i, b, truncate(rest, len(b)+20))
			return
		}
		start := len(input) - len(rest)
		spans[i] = [2]int{start, start + len(b)}
		rest = rest[len(b):]
	}
	if strings.TrimSpace(rest) != "" {
		t.Errorf("text after the last chunk was dropped: %q", rest)
	}

	// A paragraph that fits in the room left by the widest "[N/N] " prefix,
	// allowing one more digit in case the count shrank on the last round,
	// must lie inside a single chunk.
	if len(chunks) < 2 {
		return
	}
	digits := len(strconv.Itoa(len(chunks))) + 1
	budget := limit - len("[/] ") - 2*digits
	off := 0
	for _, p := range strings.Split(input, "\n\n") {
		start := off + len(p) - len(strings.TrimLeftFunc(p, unicode.IsSpace))
		core := strings.TrimSpace(p)
		end := start + len(core)
		off += len(p) + len("\n\n")
		if core == "" || utf8.RuneCountInString(core) > budget {
			continue
		}
		if !slices.ContainsFunc(spans, func(s [2]int) bool { return s[0] <= start && end <= s[1] }) {
			t.Errorf("a paragraph of %d characters fits a chunk but was cut: %q", utf8.RuneCountInString(core), truncate(core, 80))
		}
	}
}
