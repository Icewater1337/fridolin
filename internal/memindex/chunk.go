package memindex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
)

// Provenance is where a chunk's text came from, as the indexer tells it
// from the file and the line, never from what the text says.
type Provenance string

// The provenance classes. Only Owner and Agent are ever recalled into a
// prompt unasked.
const (
	// Owner is a message an allowed number typed in a direct chat with the
	// bridge, on WhatsApp, or a note whose source anchors all point at such
	// messages.
	Owner Provenance = "owner"
	// Agent is a note the brain wrote in memories/.
	Agent Provenance = "agent"
	// Untrusted is everything someone else wrote or sent: group members,
	// files and messages handed in, transcripts, and notes the brain marked
	// as taken from the web or another such source.
	Untrusted Provenance = "untrusted"
	// System is what went out to a chat, the brain's own replies
	// included, and what the bridge's schedules hand the brain: machine
	// output, which may repeat anything the brain read, a web page
	// included, so it is never recalled unasked.
	System Provenance = "system"
)

// Injectable reports whether a chunk of provenance p may go into a
// prompt without the brain asking for it.
func (p Provenance) Injectable() bool { return p == Owner || p == Agent }

// ValidProvenance reports whether p is one of the four classes.
func ValidProvenance(p Provenance) bool {
	switch p {
	case Owner, Agent, Untrusted, System:
		return true
	}
	return false
}

// chunk is one piece of a source file, as the index holds it.
type chunk struct {
	path, anchor, text string
	prov               Provenance
	importance         int
	triggers           []string
	ts                 time.Time
	who, chat          string
	// sources are the source: values of the chunk's notes comment, which
	// classify a memory note (see classifyNote).
	sources []string
}

// Where the sources are in a brain's folder.
const (
	MemoriesDir    = "memories"
	TranscriptsDir = "transcripts"
	HistoryFile    = history.FileName
)

// maxChunk is the most text one chunk holds; a longer section is split at
// paragraphs, a longer paragraph at maxChunk.
const maxChunk = 2000

// metaRE is a notes comment, <!-- triggers: a, b; importance: 3; source: … -->,
// which the brain writes at the end of a note: what should call it to mind,
// how much it matters, and where it came from.
var metaRE = regexp.MustCompile(`<!--\s*((?:triggers|importance|source)\s*:[^>]*?)\s*-->`)

// noteMeta is what a notes comment says.
type noteMeta struct {
	triggers   []string
	importance int
	sources    []string
	set        bool // importance was given
}

// parseMeta reads the notes comments in text and returns it without them.
func parseMeta(text string) (string, noteMeta) {
	var m noteMeta
	out := metaRE.ReplaceAllStringFunc(text, func(c string) string {
		body := metaRE.FindStringSubmatch(c)[1]
		for part := range strings.SplitSeq(body, ";") {
			key, val, ok := strings.Cut(part, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "triggers":
				for t := range strings.SplitSeq(val, ",") {
					if t = strings.TrimSpace(t); t != "" && len(m.triggers) < 20 {
						m.triggers = append(m.triggers, truncate(t, 100))
					}
				}
			case "importance":
				if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					m.importance, m.set = min(max(n, 0), 5), true
				}
			case "source":
				for s := range strings.SplitSeq(val, ",") {
					if s = strings.TrimSpace(s); s != "" && len(m.sources) < 20 {
						m.sources = append(m.sources, s)
					}
				}
			}
		}
		return ""
	})
	return out, m
}

// merge gives m what def says and m does not.
func (m noteMeta) merge(def noteMeta) noteMeta {
	m.triggers = append(m.triggers, def.triggers...)
	m.sources = append(m.sources, def.sources...)
	if !m.set {
		m.importance, m.set = def.importance, def.set
	}
	return m
}

// headingRE is an ATX heading.
var headingRE = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t#]*$`)

// dailyNoteRE is a daily note's file name, memories/…/YYYY-MM-DD.md.
var dailyNoteRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.md$`)

// section is the text under one heading.
type section struct {
	anchor string
	lines  []string
}

// chunkMarkdown splits a markdown note at its headings, each section one
// chunk (or more, for a long one) anchored by its heading's slug, as
// GitHub makes them. A notes comment applies to its section; one before
// the first heading applies to the whole file. A daily note is dated by
// its name, any other note by when it changed last.
func chunkMarkdown(rel string, data []byte, mtime time.Time) []chunk {
	ts := mtime
	if m := dailyNoteRE.FindStringSubmatch(path.Base(rel)); m != nil {
		if d, err := time.ParseInLocation("2006-01-02", m[1], mtime.Location()); err == nil {
			ts = d
		}
	}
	var (
		sections []section
		cur      = section{anchor: ""}
		fence    string
		slugs    = map[string]int{}
	)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), len(data)+1)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			cur.lines = append(cur.lines, line)
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			cur.lines = append(cur.lines, line)
			continue
		}
		if m := headingRE.FindStringSubmatch(line); m != nil {
			sections = append(sections, cur)
			cur = section{anchor: uniqueSlug(slugs, m[2]), lines: []string{line}}
			continue
		}
		cur.lines = append(cur.lines, line)
	}
	sections = append(sections, cur)

	var (
		out []chunk
		def noteMeta
	)
	for i, s := range sections {
		text, meta := parseMeta(strings.Join(s.lines, "\n"))
		if i == 0 {
			def = meta
		} else {
			meta = meta.merge(def)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		anchor := s.anchor
		if anchor == "" {
			anchor = "top"
		}
		for _, part := range splitText(text, maxChunk) {
			out = append(out, chunk{path: rel, anchor: anchor, text: part, prov: Agent, importance: meta.importance, triggers: meta.triggers, ts: ts, sources: meta.sources})
		}
	}
	return out
}

// uniqueSlug is heading's anchor, the way GitHub makes it: lower case,
// letters, digits, - and _ kept, spaces made -, the rest dropped, and -1,
// -2 … added to one that came before in the file.
func uniqueSlug(seen map[string]int, heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	slug := b.String()
	if slug == "" {
		slug = "section"
	}
	n := seen[slug]
	seen[slug] = n + 1
	if n > 0 {
		return slug + "-" + strconv.Itoa(n)
	}
	return slug
}

// splitText cuts text into pieces of at most n bytes, at blank lines where
// it can, else at line ends, else anywhere on a rune boundary.
func splitText(text string, n int) []string {
	if len(text) <= n {
		return []string{text}
	}
	var out []string
	for len(text) > n {
		cut := strings.LastIndex(text[:n], "\n\n")
		if cut < n/4 {
			cut = strings.LastIndex(text[:n], "\n")
		}
		if cut < n/4 {
			cut = n
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
		}
		if p := strings.TrimSpace(text[:cut]); p != "" {
			out = append(out, p)
		}
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

// chunkTranscript splits a transcript into paragraphs, anchored p1, p2 …
// in order. Whoever spoke, it is untrusted: the brain did not write it,
// and the recording may hold anyone's voice.
func chunkTranscript(rel string, data []byte, mtime time.Time) []chunk {
	var out []chunk
	for i, part := range splitText(strings.TrimSpace(string(data)), maxChunk/2) {
		out = append(out, chunk{path: rel, anchor: "p" + strconv.Itoa(i+1), text: part, prov: Untrusted, ts: mtime})
	}
	return out
}

// maxHistoryChunk bounds one history line's text in the index.
const maxHistoryChunk = 4000

// HandOffSenders are the senders the bridge itself names on the messages
// it hands a brain from its schedules (see the intent package): their
// lines are system, not untrusted.
var HandOffSenders = map[string]bool{"intent": true, "schedule": true}

// historyChunk is the chunk of one history line, number n (from 1), or
// false for a line the index leaves out (a reset, or one
// without text).
func historyChunk(owners map[string]bool, n int, line []byte) (chunk, bool) {
	var e history.Entry
	if err := json.Unmarshal(line, &e); err != nil || e.Type == history.Reset || strings.TrimSpace(e.Text) == "" {
		return chunk{}, false
	}
	anchor := e.ID
	if anchor == "" {
		anchor = "L" + strconv.Itoa(n)
	}
	ts, _ := time.Parse(time.RFC3339, e.TS)
	return chunk{
		path: HistoryFile, anchor: anchor, text: truncate(e.Text, maxHistoryChunk),
		prov: historyProvenance(owners, e), ts: ts, who: e.From, chat: e.Chat,
	}, true
}

// historyProvenance classifies a history line. Owner takes all of: a
// message to the brain (in), with a WhatsApp message id (which /ingest,
// /send and the bridge's hand-offs never write), in a direct chat (not a
// group, nor a task another brain delegated), from an allowed number.
// What went out to a chat (the brain's
// replies, which may repeat what it read anywhere, and what scripts and
// other brains sent) and what the bridge's schedules hand the brain is
// system; the rest, other people's messages and votes included, untrusted.
func historyProvenance(owners map[string]bool, e history.Entry) Provenance {
	switch e.Type {
	case history.In:
		switch {
		case e.ID != "" && !chat.IsGroupJID(e.Chat) && !chat.IsDelegateChat(e.Chat) && owners[chat.IdentityKey(e.From)]:
			return Owner
		case e.ID == "" && HandOffSenders[e.From]:
			return System
		}
		return Untrusted
	case history.Out:
		return System
	}
	return Untrusted
}

// truncate cuts s to at most n bytes, on a rune boundary, marking the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// LineAnchor reports whether a history chunk's anchor is its line number
// (L12), given to a line without a WhatsApp message id.
func LineAnchor(anchor string) bool {
	n, ok := strings.CutPrefix(anchor, "L")
	if !ok || n == "" {
		return false
	}
	_, err := strconv.Atoi(n)
	return err == nil
}
