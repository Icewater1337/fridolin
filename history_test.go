package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// countingReader counts what is read of the history.
type countingReader struct {
	r    *bytes.Reader
	read int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.read += int64(n)
	return n, err
}

// The backward reader hands each line over once, newest first, however the
// blocks cut the file, and keeps going past a line too long to take.
func TestScanHistoryBackward(t *testing.T) {
	long := strings.Repeat("x", maxHistoryLine+1)
	block := strings.Repeat("y", historyBlock-3) // with "\n", it ends a byte short of a block
	// several is a line across four blocks whose parts read differently,
	// so that parts joined out of order show.
	var several strings.Builder
	for i := 0; several.Len() < 3*historyBlock+100; i++ {
		fmt.Fprintf(&several, "%d,", i)
	}
	cases := []struct {
		name    string
		file    string
		budget  int64
		stopAt  string // visit returns false for this line
		want    []string
		skipped int
	}{
		{"lines, newest first", "a\nb\nc\n", 0, "", []string{"c", "b", "a"}, 0},
		{"no newline at the end", "a\nb", 0, "", []string{"b", "a"}, 0},
		{"blank lines and spaces", "\n a \n\n\r\nb\r\n", 0, "", []string{"b", "a"}, 0},
		{"empty", "", 0, "", nil, 0},
		{"stopped by visit", "a\nb\nc\n", 0, "b", []string{"c", "b"}, 0},
		{"a line across blocks", "first\n" + block + "zz\nlast\n", 0, "", []string{"last", block + "zz", "first"}, 0},
		{"a line across several blocks", "first\n" + several.String() + "\nlast\n", 0, "", []string{"last", several.String(), "first"}, 0},
		{"the first line across several blocks", several.String() + "\nlast", 0, "", []string{"last", several.String()}, 0},
		{"a long line is skipped", "old\n" + long + "\nnew\n", 0, "", []string{"new", "old"}, 1},
		{"a long first line", long + "\nnew\n", 0, "", []string{"new"}, 1},
		{"a long last line", "old\n" + long, 0, "", []string{"old"}, 1},
		{"the budget ends the reading", "old\n" + block + "\nnew\n", historyBlock, "", []string{"new"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			r := &countingReader{r: bytes.NewReader([]byte(c.file))}
			skipped, err := scanHistoryBackward(r, int64(len(c.file)), c.budget, func(line []byte) bool {
				got = append(got, string(line))
				return string(line) != c.stopAt
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") || skipped != c.skipped {
				short := func(ss []string) []string {
					var out []string
					for _, s := range ss {
						out = append(out, truncate(s, 20))
					}
					return out
				}
				t.Errorf("lines %q, skipped %d; want %q, %d", short(got), skipped, short(c.want), c.skipped)
			}
			if c.budget > 0 && r.read > c.budget {
				t.Errorf("read %d bytes, over the budget of %d", r.read, c.budget)
			}
		})
	}
}

// The prompt's quick look takes the lines that hold one of the chat's
// JIDs, and costs no allocation for each of the many lines it looks at.
func TestMentionsChat(t *testing.T) {
	// A JID longer than the 32 bytes Go converts on the stack.
	long := strings.Repeat("1", 40) + "@lid"
	want := mentionsChat([]string{testDMChat, long})
	cases := []struct {
		line string
		want bool
	}{
		{`{"chat":"` + testDMChat + `","text":"hi"}`, true},
		{`{"chat":"` + long + `","text":"hi"}`, true},
		{`{"chat":"` + testGroup + `","text":"hi"}`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := want([]byte(c.line)); got != c.want {
			t.Errorf("mentionsChat(%q) = %v, want %v", c.line, got, c.want)
		}
	}
	line := []byte(`{"chat":"` + testGroup + `","text":"` + strings.Repeat("chatter ", 50) + `"}`)
	if n := testing.AllocsPerRun(100, func() { want(line) }); n != 0 {
		t.Errorf("%v allocations a line, want none", n)
	}
}

// A line of megabytes is copied once or twice, not again for each block it
// crosses: that cost about 136 MB for a line of 4 MB, on every reading
// that went past it.
func TestScanHistoryBackwardCopiesALongLineOnce(t *testing.T) {
	line := strings.Repeat("x", maxHistoryLine-historyBlock)
	file := []byte("old\n" + line + "\nnew\n")
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var got []int
	if _, err := scanHistoryBackward(bytes.NewReader(file), int64(len(file)), 0, func(l []byte) bool {
		got = append(got, len(l))
		return true
	}); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if !slices.Equal(got, []int{3, len(line), 3}) {
		t.Fatalf("line lengths %v, want 3, %d and 3", got, len(line))
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > uint64(4*len(line)) {
		t.Errorf("reading a line of %d KiB allocated %d KiB, want at most four times the line", len(line)>>10, n>>10)
	}
}

// A run's prompt reads the history back from its end, not the whole file:
// the chat's last messages are found behind megabytes of another chat's,
// a line too long to take does not hide what came after it, and what lies
// further back than the budget is not read at all.
func TestPromptHistoryIsReadFromTheEnd(t *testing.T) {
	prevBudget, prevLine := promptHistoryBudget, maxHistoryLine
	promptHistoryBudget, maxHistoryLine = 4<<20, 256<<10
	t.Cleanup(func() { promptHistoryBudget, maxHistoryLine = prevBudget, prevLine })
	filler := strings.Repeat("chatter ", 128)
	other := func(br *Brain, bytes int) {
		for range bytes / len(filler) {
			br.appendHistory(testGroup, "15559876543", filler, historyIn)
		}
	}
	cases := []struct {
		name  string
		setup func(br *Brain)
		want  string
	}{
		{"behind another chat's megabytes", func(br *Brain) {
			br.appendHistory(testDMChat, "15551234567", "one", historyIn)
			other(br, 3<<20)
		}, "one"},
		{"after a line too long to take", func(br *Brain) {
			br.appendHistory(testDMChat, "15551234567", "old", historyIn)
			// JSON writes each < as \u003c, six bytes.
			br.appendHistory(testDMChat, "15551234567", strings.Repeat("<", maxHistoryLine/2), historyIn)
			br.appendHistory(testDMChat, "15551234567", "newest", historyIn)
		}, "old newest"},
		{"after a line that is no JSON", func(br *Brain) {
			br.appendHistory(testDMChat, "15551234567", "old", historyIn)
			f, err := os.OpenFile(filepath.Join(br.Dir(), historyFileName), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(f, `{"chat":"`+testDMChat+`", broken`)
			f.Close()
			br.appendHistory(testDMChat, "15551234567", "newest", historyIn)
		}, "old newest"},
		{"further back than the budget", func(br *Brain) {
			br.appendHistory(testDMChat, "15551234567", "ancient", historyIn)
			other(br, 5<<20)
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bareBrain(t)
			logged := captureLog(t)
			c.setup(br)
			before, since := br.historyBefore(br.logInbound(dm("now")), dmHistoryWindow)
			if entryTexts(before) != c.want || len(since) != 0 {
				t.Errorf("historyBefore = %q, %q; want %q", entryTexts(before), entryTexts(since), c.want)
			}
			switch {
			case strings.Contains(c.name, "too long") && !logContains(logged, "brain a: skipped 1 history lines longer than 256 KiB"):
				t.Errorf("log = %q, want the long line noted", logged())
			case strings.Contains(c.name, "no JSON") && !logContains(logged, "brain a: skipped 1 history lines that are not JSON"):
				t.Errorf("log = %q, want the broken line noted", logged())
			}
		})
	}
}

// The dashboard's feed is seeded with the history's last lines.
func TestActivitySeedReadsTheLastLines(t *testing.T) {
	br := bareBrain(t)
	for i := range 60 {
		br.appendHistory(testDMChat, "15551234567", fmt.Sprintf("message %d", i), historyIn)
	}
	feed := NewActivity(100)
	feed.Seed(br)
	got := feed.Recent(100, "")
	if len(got) != 50 || got[0].Text != "message 59" || got[49].Text != "message 10" {
		var texts []string
		for _, e := range got {
			texts = append(texts, e.Text)
		}
		t.Errorf("feed = %q, want messages 59 down to 10", texts)
	}
}

// entryTexts is the texts of es, joined by spaces.
func entryTexts(es []historyEntry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Text)
	}
	return strings.Join(out, " ")
}
