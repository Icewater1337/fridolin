package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// `fridolin history` searches a brain's conversation-history.jsonl. A run
// gets only the last messages of its chat in the prompt; this is how a
// brain looks further back. It runs in the brain's folder, inside its
// sandbox, and reads the brain's own file.

const historyUsage = `Usage: fridolin history [flags] QUERY

Searches a brain's conversation-history.jsonl for QUERY, a substring (or a
regular expression with --regex), newest hits first. In a brain's run it
reads the brain's own history, in the folder it runs in.

Prints JSON: {"hits": [{"line", "ts", "type", "from", "chat", "text"}, ...],
"count", "truncated", "file"}. --text prints one line per hit instead.

Flags:
`

// historyCmd is `fridolin history`.
func historyCmd(args []string, t toolIO) int {
	flags := flag.NewFlagSet("fridolin history", flag.ContinueOnError)
	file := flags.String("file", "", "the history file to search")
	brainDir := flags.String("brain-dir", "", "a brain's folder, to search its history")
	since := flags.String("since", "", "only messages since then: 30m, 24h, 7d, 2w or a time such as 2026-01-05T09:00")
	limit := flags.Int("limit", 50, "the most hits to show (0: all)")
	regex := flags.Bool("regex", false, "QUERY is a regular expression (RE2 syntax)")
	caseSensitive := flags.Bool("case-sensitive", false, "match upper and lower case exactly")
	text := flags.Bool("text", false, "one line per hit instead of JSON")
	asJSON := flags.Bool("json", false, "JSON (the default; for scripts written for history_search.py)")
	chat := flags.String("chat", "", "only messages in this chat JID")
	fromOnly := flags.Bool("from-only", false, "only messages the brain received")
	reverse := flags.Bool("reverse", false, "oldest hits first")
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), historyUsage)
		flags.PrintDefaults()
	}
	positional, err := t.parseTool(flags, args)
	if err != nil {
		return t.exitFor("fridolin history", err)
	}
	switch {
	case len(positional) != 1:
		return t.fail("fridolin history: give one QUERY (quote it when it has spaces), got %d", len(positional))
	case *file != "" && *brainDir != "":
		return t.fail("fridolin history: --file and --brain-dir name the same thing; give one")
	case *text && *asJSON:
		return t.fail("fridolin history: --text and --json are two outputs; give one")
	case *limit < 0:
		return t.fail("fridolin history: --limit must be 0 (all) or more, got %d", *limit)
	}

	q := historyQuery{chat: *chat, fromOnly: *fromOnly}
	pattern := positional[0]
	if !*regex {
		pattern = regexp.QuoteMeta(pattern)
	}
	if !*caseSensitive {
		pattern = "(?i)" + pattern
	}
	if q.match, err = regexp.Compile(pattern); err != nil {
		return t.fail("fridolin history: invalid regular expression: %v", err)
	}
	if *since != "" {
		if q.since, err = parseSince(*since, t.now); err != nil {
			return t.fail("fridolin history: --since: %v", err)
		}
	}

	path := filepath.Join(t.dir, historyFileName)
	switch home := t.getenv("HOME"); {
	case *file != "":
		path = expandHome(*file, home)
	case *brainDir != "":
		path = filepath.Join(expandHome(*brainDir, home), historyFileName)
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return t.fail("history file not found: %s. Pass --file or --brain-dir, or run it in a brain's folder", path)
	}
	if err != nil {
		return t.fail("reading the history: %v", err)
	}
	defer f.Close()
	hits, err := searchHistory(f, q)
	if err != nil {
		return t.fail("reading %s: %v", path, err)
	}

	if !*reverse {
		slices.Reverse(hits)
	}
	truncated := *limit > 0 && len(hits) > *limit
	if truncated {
		hits = hits[:*limit]
	}
	if !*text {
		return t.emit(map[string]any{"hits": hits, "count": len(hits), "truncated": truncated, "file": path})
	}
	for _, h := range hits {
		fmt.Fprintf(t.stdout, "%s [%s %s] %s\n", h.field("ts"), h.field("type"), h.field("from"), oneLine(h.field("text"), 400))
	}
	fmt.Fprintf(t.stderr, "\n— %d hit(s) — file: %s\n", len(hits), path)
	return 0
}

// historyQuery is what a history line must match to be a hit.
type historyQuery struct {
	match    *regexp.Regexp
	since    time.Time // zero: any time
	chat     string    // "": any chat
	fromOnly bool      // only historyIn lines
}

// historyHit is one matching line: its own fields, whatever the bridge
// wrote, plus "line", its line number in the file.
type historyHit map[string]any

// field is the hit's field name as text, "?" when it has none.
func (h historyHit) field(name string) string {
	if s, ok := h[name].(string); ok {
		return s
	}
	return "?"
}

// searchHistory returns the lines of r that match q, in file order. A
// line that is not a JSON object, has no text, or (with a since) has no
// time that parses is skipped; a long line is no problem.
func searchHistory(r io.Reader, q historyQuery) ([]historyHit, error) {
	br := bufio.NewReader(r)
	var hits []historyHit
	for lineNo := 1; ; lineNo++ {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if h := matchHistoryLine(line, q); h != nil {
				h["line"] = lineNo
				hits = append(hits, h)
			}
		}
		if errors.Is(err, io.EOF) {
			return hits, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// matchHistoryLine decodes one line and returns it when it matches q.
func matchHistoryLine(line []byte, q historyQuery) historyHit {
	var h historyHit
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber() // keep any number the bridge adds one day as written
	if dec.Decode(&h) != nil || h == nil {
		return nil
	}
	text, _ := h["text"].(string)
	if text == "" {
		return nil
	}
	if q.fromOnly && h["type"] != historyIn {
		return nil
	}
	if q.chat != "" && h["chat"] != q.chat {
		return nil
	}
	if !q.since.IsZero() {
		ts, _ := h["ts"].(string)
		at, ok := parseHistoryTime(ts, time.Local)
		if !ok || at.Before(q.since) {
			return nil
		}
	}
	if !q.match.MatchString(text) {
		return nil
	}
	return h
}

var sinceAgo = regexp.MustCompile(`^(\d+)\s*([smhdw])$`)

// parseSince reads --since: an age (30m, 24h, 7d, 2w) back from now, or a
// point in time.
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if m := sinceAgo.FindStringSubmatch(s); m != nil {
		unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
		// A Duration holds about 292 years. A larger age would overflow
		// it into a nonsense cutoff, one in the future say.
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n > math.MaxInt64/int64(unit) {
			return time.Time{}, fmt.Errorf("%q is too large", s)
		}
		return now.Add(-time.Duration(n) * unit), nil
	}
	if at, ok := parseHistoryTime(s, now.Location()); ok {
		return at, nil
	}
	return time.Time{}, fmt.Errorf("use 30m, 24h, 7d, 2w or a time such as 2026-01-05T09:00 (got %q)", s)
}

// historyTimeLayouts are the times parseHistoryTime reads: RFC 3339, as
// the bridge writes them (with Z on a host in UTC), the same without
// seconds, and the forms people type, which have no offset.
var historyTimeLayouts = []struct {
	layout string
	local  bool // no offset: a time in loc
}{
	{time.RFC3339Nano, false},
	{"2006-01-02T15:04Z07:00", false},
	{"2006-01-02T15:04:05.999999999", true},
	{"2006-01-02T15:04", true},
	{"2006-01-02 15:04:05", true},
	{"2006-01-02 15:04", true},
	{"2006-01-02", true},
}

// parseHistoryTime parses a timestamp, taking one without an offset as a
// time in loc.
func parseHistoryTime(s string, loc *time.Location) (time.Time, bool) {
	for _, l := range historyTimeLayouts {
		var at time.Time
		var err error
		if l.local {
			at, err = time.ParseInLocation(l.layout, s, loc)
		} else {
			at, err = time.Parse(l.layout, s)
		}
		if err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// oneLine puts s on one line, ⏎ marking its line breaks, and cuts it to
// limit characters.
func oneLine(s string, limit int) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}
