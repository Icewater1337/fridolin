package toolcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/history"
)

const historyTestChat = "120363000000000000@g.us"

// writeHistory writes rows as a brain's history file in dir.
func writeHistory(t *testing.T, dir string, rows ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, r := range rows {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, history.FileName)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type historyResult struct {
	Hits      []map[string]any `json:"hits"`
	Count     int              `json:"count"`
	Truncated bool             `json:"truncated"`
	File      string           `json:"file"`
	Error     string           `json:"error"`
}

func (r historyResult) texts() []string {
	var out []string
	for _, h := range r.Hits {
		out = append(out, h["text"].(string))
	}
	return out
}

func TestHistorySearch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	plus2 := time.FixedZone("+02", 2*3600)
	rows := []map[string]any{
		{"ts": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339), "type": "in", "from": "15551234567", "chat": historyTestChat, "text": "old Invoice"},
		{"ts": now.Add(-2 * time.Hour).In(plus2).Format(time.RFC3339), "type": "out", "from": "main", "chat": historyTestChat, "text": "the invoice is paid"},
		{"ts": now.Add(-time.Hour).Format(time.RFC3339), "type": "in", "from": "15551234567", "chat": "15551234567@s.whatsapp.net", "text": "invoices for march?"},
		{"ts": "yesterday", "type": "in", "chat": historyTestChat, "text": "an invoice with a bad time"},
		{"ts": now.Format(time.RFC3339), "type": "in", "chat": historyTestChat, "text": ""},
		{"ts": now.Format(time.RFC3339), "type": "in", "chat": historyTestChat, "text": "nothing to see"},
	}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"substring, newest first, any case", []string{"invoice"}, []string{"an invoice with a bad time", "invoices for march?", "the invoice is paid", "old Invoice"}},
		{"case-sensitive", []string{"--case-sensitive", "Invoice"}, []string{"old Invoice"}},
		{"regex", []string{"--regex", `invoices?\b`, "--case-sensitive"}, []string{"an invoice with a bad time", "invoices for march?", "the invoice is paid"}},
		{"a substring is not a regex", []string{"march?"}, []string{"invoices for march?"}},
		{"since an age, with offsets and Z", []string{"--since", "7d", "invoice"}, []string{"invoices for march?", "the invoice is paid"}},
		{"since a time", []string{"--since", "2026-01-20T10:30:00Z", "invoice"}, []string{"invoices for march?"}},
		{"in one chat", []string{"--chat", historyTestChat, "invoice"}, []string{"an invoice with a bad time", "the invoice is paid", "old Invoice"}},
		{"only received", []string{"--from-only", "invoice"}, []string{"an invoice with a bad time", "invoices for march?", "old Invoice"}},
		{"oldest first", []string{"--reverse", "--limit", "2", "invoice"}, []string{"old Invoice", "the invoice is paid"}},
		{"flags after the query", []string{"invoice", "--limit", "1"}, []string{"an invoice with a bad time"}},
		{"a query that looks like a flag, after --", []string{"--", "--from-only"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := writeHistory(t, dir, rows...)
			code, out, errs := runTool(t, History, dir, nil, now, tc.args...)
			var r historyResult
			if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
			}
			if got := r.texts(); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("hits = %q, want %q", got, tc.want)
			}
			if r.Count != len(tc.want) || r.File != path {
				t.Errorf("count %d, file %q; want %d, %q", r.Count, r.File, len(tc.want), path)
			}
		})
	}
}

func TestHistoryHitsKeepTheLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHistory(t, dir,
		map[string]any{"ts": "2026-01-15T10:00:00Z", "type": "in", "from": "15551234567", "chat": historyTestChat, "text": "first"},
		map[string]any{"ts": "2026-01-15T11:00:00Z", "type": "in", "from": "15551234567", "chat": historyTestChat, "text": "second", "media": 2},
	)
	// A line far longer than a bufio.Scanner's default buffer, and junk.
	f, err := os.OpenFile(filepath.Join(dir, history.FileName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	long, _ := json.Marshal(map[string]any{"ts": "2026-01-15T12:00:00Z", "type": "out", "text": "long " + strings.Repeat("x", 2<<20)})
	f.WriteString("not json\n[1,2]\n\n" + string(long) + "\n")
	f.Close()

	code, out, _ := runTool(t, History, dir, nil, time.Now(), "--limit", "0", "--reverse", "")
	var r historyResult
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 {
		t.Fatalf("exit %d: %.300s", code, out)
	}
	if len(r.Hits) != 3 || r.Truncated {
		t.Fatalf("%d hits (truncated %v), want 3", len(r.Hits), r.Truncated)
	}
	second := r.Hits[1]
	if second["line"] != float64(2) || second["media"] != float64(2) || second["from"] != "15551234567" {
		t.Errorf("hit = %v, want line 2 with every field of the line", second)
	}
	if r.Hits[2]["line"] != float64(6) {
		t.Errorf("long line numbered %v, want 6", r.Hits[2]["line"])
	}
}

func TestHistoryFindsTheFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	brain := filepath.Join(home, "brains", "main")
	if err := os.MkdirAll(brain, 0o700); err != nil {
		t.Fatal(err)
	}
	writeHistory(t, brain, map[string]any{"ts": "2026-01-15T10:00:00Z", "type": "in", "text": "dentist on monday"})
	env := map[string]string{"HOME": home}
	cases := []struct {
		name, dir string
		args      []string
	}{
		{"the folder it runs in", brain, nil},
		{"--brain-dir", home, []string{"--brain-dir", "~/brains/main"}},
		{"--file", home, []string{"--file", "~/brains/main/" + history.FileName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := runTool(t, History, tc.dir, env, time.Now(), append(tc.args, "dentist")...)
			var r historyResult
			_ = json.Unmarshal([]byte(out), &r)
			if code != 0 || r.File != filepath.Join(brain, history.FileName) || r.Count != 1 {
				t.Errorf("exit %d, %+v", code, r)
			}
		})
	}
}

func TestHistoryTextOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHistory(t, dir, map[string]any{"ts": "2026-01-15T10:00:00Z", "type": "in", "from": "15551234567", "text": "two\nlines " + strings.Repeat("é", 500)})
	code, out, errs := runTool(t, History, dir, nil, time.Now(), "--text", "lines")
	want := "2026-01-15T10:00:00Z [in 15551234567] two ⏎ lines " + strings.Repeat("é", 400-len([]rune("two ⏎ lines "))) + "…\n"
	if code != 0 || out != want {
		t.Errorf("exit %d, stdout:\n%q\nwant\n%q", code, out, want)
	}
	if !strings.Contains(errs, "1 hit(s)") {
		t.Errorf("stderr %q, want the count", errs)
	}
}

// TestHistoryErrors: every mistake is one JSON error on stdout and exit
// status 1, where the assistant looks for the result.
func TestHistoryErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no history file", []string{"x"}, "history file not found"},
		{"no query", nil, "give one QUERY"},
		{"two queries", []string{"a", "b"}, "give one QUERY"},
		{"bad since", []string{"--since", "last week", "x"}, "use 30m, 24h, 7d, 2w"},
		{"since in days, spelled out", []string{"--since", "7days", "x"}, "use 30m, 24h, 7d, 2w"},
		{"bad regex", []string{"--regex", "("}, "invalid regular expression"},
		{"unknown flag", []string{"--nope", "x"}, "flag provided but not defined"},
		{"file and brain dir", []string{"--file", "a", "--brain-dir", "b", "x"}, "give one"},
		{"negative limit", []string{"--limit", "-1", "x"}, "--limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, out, _ := runTool(t, History, t.TempDir(), nil, time.Now(), tc.args...)
			var r historyResult
			if err := json.Unmarshal([]byte(out), &r); err != nil || code != 1 || !strings.Contains(r.Error, tc.want) {
				t.Errorf("exit %d, stdout %q; want exit 1 and an error with %q", code, out, tc.want)
			}
		})
	}
}

func TestParseSince(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	cases := []struct {
		in   string
		want time.Time
		err  string // instead of want
	}{
		{"30m", now.Add(-30 * time.Minute), ""},
		{"24h", now.Add(-24 * time.Hour), ""},
		{"7d", now.Add(-7 * 24 * time.Hour), ""},
		{"2w", now.Add(-2 * week), ""},
		{" 90 s ", now.Add(-90 * time.Second), ""},
		{"2026-01-15T10:00:00Z", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), ""},
		{"2026-01-15T11:00:00+01:00", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), ""},
		{"2026-01-15T10:00", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), ""}, // in now's zone
		{"2026-01-15", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), ""},
		// The longest age a Duration holds, and one week more.
		{"15250w", now.Add(-15250 * week), ""},
		{"15251w", time.Time{}, `"15251w" is too large`},
		{"99999999999w", time.Time{}, `"99999999999w" is too large`},
		{"9223372037s", time.Time{}, `"9223372037s" is too large`},
		{"99999999999999999999s", time.Time{}, `"99999999999999999999s" is too large`},
		{"yesterday", time.Time{}, "use 30m, 24h, 7d, 2w or a time"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSince(tc.in, now)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Errorf("parseSince(%q) = %v, %v; want an error with %q", tc.in, got, err, tc.err)
				}
				return
			}
			if err != nil || !got.Equal(tc.want) {
				t.Errorf("parseSince(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
			}
		})
	}
}
